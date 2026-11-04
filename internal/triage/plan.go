package triage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// ErrBudget is returned when a run would exceed triage.budget.*.
var ErrBudget = errors.New("triage: budget exceeded")

// Options select what a run decides.
type Options struct {
	// Limit caps the messages decided (newest first); 0 = every pending one.
	Limit int `json:"limit"`
	// MessageIDs restricts the run to these message rows (nil = all).
	MessageIDs []int64 `json:"message_ids,omitempty"`
	// Stores restricts the run to these store keys (each must be picked:
	// source.SettingSelected; nil = every picked store).
	Stores []string `json:"stores,omitempty"`
	// Force re-decides messages whose cached answers are current (the
	// repeat-agreement probe: decision models may vary between calls).
	Force bool `json:"force,omitempty"`
	// Redecide decides the stale decisions again (Plan.Stale, at the cost
	// Plan.StaleEstimate shows): the user's explicit action. Never implied.
	Redecide bool `json:"redecide,omitempty"`
}

// Budget is the per-run limit (triage.budget.*): the engine's shared
// spending rule (calls, USD and unpriced providers).
type Budget = engine.Budget

// Plan is what a run would do and cost, before any call.
type Plan struct {
	Key      store.TriageKey `json:"key"`
	Eligible int             `json:"eligible"`
	// Fresh counts complete decisions this plan does not re-decide. The full
	// Plan composes every state, so there it means "current state". The
	// pending plan (Run) deliberately does not compose states for decisions
	// that are not candidates: that compose is the whole-mailbox read it
	// exists to avoid. A decision whose derived state moved therefore counts
	// as Fresh in a run report and as Stale in Plan/Read; the run report
	// means "not pending now", and Plan/Read is the surface that discloses
	// derived-data staleness.
	Fresh   int `json:"fresh"`
	Partial int `json:"incomplete"` // current state, some questions unanswered: only those are asked
	// Invalidated counts cached decisions whose MESSAGE changed since (a
	// new revision): they are decided again. A profile or decider change is
	// another key: every message is unseen there.
	Invalidated int `json:"invalidated"`
	// Stale counts paid decisions whose state changed while their message
	// did not (the cleaning, evidence or thread views changed): they stay
	// valid and are never decided again on their own; StaleEstimate is what
	// deciding them again would cost (Options.Redecide).
	Stale         int             `json:"stale"`
	StaleEstimate engine.Estimate `json:"stale_estimate"`
	Unseen        int             `json:"unseen"` // never decided
	// Quarantined counts candidates held back by repeated permanent
	// per-message rejections at the current inputs. They stay failed and
	// unjudged in the coverage; they are neither pending nor fresh.
	Quarantined int             `json:"quarantined"`
	Pending     int             `json:"pending"`
	Selected    int             `json:"selected"`
	Unsendable  int             `json:"unsendable"` // over the provider's context budget: fail without a call
	Estimate    engine.Estimate `json:"estimate"`
	Budget      Budget          `json:"budget"`
	Refusal     string          `json:"refusal,omitempty"`
	items       []item
}

// item is one pending message. Its state is not kept (a full-archive plan
// would hold every state in memory): only its estimate, the hash the
// state had when planned, and the answers already paid for when only
// some questions are missing. Dispatch composes the state again.
type item struct {
	rowID    int64
	revision int64 // the message's revision when planned
	hash     string
	received string
	est      engine.Estimate
	have     map[string]engine.Answer // answers kept for the same state (missing ones are asked)
}

// Plan composes every eligible state, compares it with the cache and
// estimates the calls and cost of deciding what is pending. It is the
// disclosure plan: it visits the whole eligible population so Stale and
// StaleEstimate include decisions whose derived data changed without a
// message revision. Runs use planPending instead.
func (t *Triager) Plan(ctx context.Context, o Options) (*Plan, error) {
	p, _, err := t.planFull(ctx, o)
	return p, err
}

// startScope prepares the session a plan runs under: the session itself,
// the decision scope it asks and the eligible population of that scope. The
// store refusal comes before any decision read, and both plans enter the
// store through it, so the full and the pending plan can never disagree
// about which stores a run may read or which scope it counts.
func (t *Triager) startScope(ctx context.Context, o Options) (*session, store.DecisionScope, int, error) {
	s, err := t.prepare(ctx)
	if err != nil {
		return nil, store.DecisionScope{}, 0, err
	}
	if err := s.checkStores(o.Stores); err != nil {
		return nil, store.DecisionScope{}, 0, err
	}
	sc := t.decisionScope(s, o)
	eligible, err := t.st.DecisionCount(ctx, sc)
	if err != nil {
		return nil, store.DecisionScope{}, 0, err
	}
	return s, sc, eligible, nil
}

// planFull is the exact plan of the whole eligible population.
func (t *Triager) planFull(ctx context.Context, o Options) (*Plan, *session, error) {
	s, sc, eligible, err := t.startScope(ctx, o)
	if err != nil {
		return nil, nil, err
	}
	// Only the eligible population's decisions are needed: a cached row of
	// another store, role or key can never serve this plan.
	cached, err := t.st.LoadScopeDecisions(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	var failures map[int64]store.Failure
	if !o.Force && !o.Redecide {
		if failures, err = t.st.LoadScopeFailures(ctx, sc); err != nil {
			return nil, nil, err
		}
	}
	p := t.planOf(s)
	p.Eligible = eligible
	err = s.each(ctx, t.st, o.Stores, o.MessageIDs, func(r *store.MessageRow, state string) error {
		it := item{rowID: r.RowID, revision: r.Revision, hash: StateHash(state), received: r.Message.ReceivedAt}
		t.classifyItem(p, s, &it, state, cached, failures, o)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	selectAndEstimate(p, o)
	return p, s, nil
}

// planPending is the batch plan of a run. It visits only the messages the
// store's candidate predicate selects (never decided, decided at another
// revision, or missing an answer), so an idle or already-decided mailbox
// loads no answers and composes no state. A candidate that turns out
// current is counted fresh and selects nothing.
//
// A decision at the message's revision with every question answered is
// never pending under these options: its derived-data staleness is
// disclosed by Plan/Read and re-decided only by Redecide (which widens the
// candidates to the whole population). Force does the same for fresh
// decisions. Pending, ordering, the batch limit, the estimates and the
// refusal therefore stay exactly the run's, while the mailbox's unchanged
// decisions are never read or composed.
func (t *Triager) planPending(ctx context.Context, o Options) (*Plan, *session, error) {
	s, sc, eligible, err := t.startScope(ctx, o)
	if err != nil {
		return nil, nil, err
	}
	rows, err := t.candidates(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	p := t.planOf(s)
	p.Eligible = eligible
	if len(rows) == 0 {
		if p.Quarantined, err = t.st.DecisionQuarantinedCount(ctx, sc); err != nil {
			return nil, nil, err
		}
		p.Fresh = eligible - p.Quarantined
		selectAndEstimate(p, o)
		return p, s, nil
	}
	cached, err := t.loadDecisions(ctx, s.key, rows)
	if err != nil {
		return nil, nil, err
	}
	err = s.each(ctx, t.st, o.Stores, rows, func(r *store.MessageRow, state string) error {
		it := item{rowID: r.RowID, revision: r.Revision, hash: StateHash(state), received: r.Message.ReceivedAt}
		t.classifyItem(p, s, &it, state, cached, nil, o)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// Every eligible message the candidates did not name is either a decision
	// at its revision with all answers present (counted Fresh: not pending
	// now, though its derived state may have moved) or a quarantined
	// failure. No default run re-decides either, and composing every state to
	// split the two is the whole-mailbox read this plan avoids.
	if len(rows) < eligible {
		if p.Quarantined, err = t.st.DecisionQuarantinedCount(ctx, sc); err != nil {
			return nil, nil, err
		}
	}
	if extra := eligible - len(rows) - p.Quarantined; extra > 0 {
		p.Fresh += extra
	}
	selectAndEstimate(p, o)
	return p, s, nil
}

// classifyItem counts one message against the cache and appends it when the
// run must decide it. It is the one per-message planning rule of both plans;
// failures is the scope's stored failure map for the full plan (nil for the
// batch plan, whose candidates already exclude quarantined rows).
func (t *Triager) classifyItem(p *Plan, s *session, it *item, state string, cached map[int64]store.Decision, failures map[int64]store.Failure, o Options) {
	var failure *store.Failure
	if f, ok := failures[it.rowID]; ok {
		failure = &f
	}
	need, stale := s.classify(p, it, cached, failure, o)
	if stale {
		est, _ := s.decider.Plan(state, s.missing(nil))
		p.StaleEstimate.Add(est)
	}
	if !need {
		return
	}
	est, perr := s.decider.Plan(state, s.missing(it.have))
	if perr != nil {
		p.Unsendable++
	}
	it.est = est
	p.items = append(p.items, *it)
}

// planOf is an empty plan of a session: its key and the budget settings.
func (t *Triager) planOf(s *session) *Plan {
	p := &Plan{Key: s.key, Budget: Budget{MaxCalls: t.cfg.Int("triage.budget.max_calls"), MaxUSD: t.cfg.Float("triage.budget.max_usd"),
		AllowUnpriced: t.cfg.Bool("triage.budget.allow_unpriced")}}
	p.StaleEstimate.PriceKnown = true
	return p
}

// decisionScope is the store query a plan asks: the session's decision key,
// the triaged population, the picked stores and the profile's questions.
func (t *Triager) decisionScope(s *session, o Options) store.DecisionScope {
	return decisionScopeOf(s.key, s.profile, s.roles, s.answerContext, o)
}

// decisionScopeOf is the one decision scope of a plan and the trigger's
// pending probe: the answer identity, the triaged population, the picked
// stores and the profile's questions. The probe computes the same identity
// from the profile/roles alone, so it asks exactly the predicate the plan
// will use.
func decisionScopeOf(key store.TriageKey, profile *taxonomy.Profile, roles []mail.FolderRole, answerContext string, o Options) store.DecisionScope {
	filter := triagedFilter(roles)
	filter.RowIDs = o.MessageIDs
	return store.DecisionScope{
		Key:       key,
		Filter:    filter,
		Stores:    o.Stores,
		Questions: questionKeys(profile),
		Force:     o.Force,
		Redecide:  o.Redecide,
		Context:   answerContext,
	}
}

// questionKeys is the profile's question set as the store's data: the name
// and type a stored decision must carry to be current.
func questionKeys(p *taxonomy.Profile) []store.QuestionKey {
	out := make([]store.QuestionKey, len(p.Questions))
	for i := range p.Questions {
		out[i] = store.QuestionKey{Name: p.Questions[i].Name, Type: string(p.Questions[i].Type)}
	}
	return out
}

// hasPending is the trigger's cheap gate: it checks the requested stores
// (the same refusal Plan gives), then asks the store for one eligible
// message without a current decision. It needs only the profile, identity
// and roles, never the causal core: one EXISTS ... LIMIT 1 answers it, so
// the no-work path loads no decision row and composes no state.
func (t *Triager) hasPending(ctx context.Context, o Options) (bool, error) {
	s, err := t.prepareProbe(ctx)
	if err != nil {
		return false, err
	}
	if err := checkStores(s.stores, o.Stores); err != nil {
		return false, err
	}
	return t.pending(ctx, decisionScopeOf(s.key, s.profile, s.roles, s.answer, o))
}

// classify counts an item against the cache: need when it is decided in
// this run, stale when it holds a paid decision for an unchanged message
// whose state changed. The one policy of paid decisions (lead decision
// 2026-10-15): a decision stays valid for its message until the message's
// own revision changes — or the question set or decider changes (another
// key). A change of derived data (cleaning, evidence, thread views) only
// makes it stale: disclosed with its re-decision cost, decided again only
// on the user's action (Options.Redecide).
//
// A would-be candidate held by a permanent per-message rejection at this
// revision and answer context is counted quarantined instead: it is not
// pending (the same input will be rejected again) and not fresh. Force and
// Redecide are explicit user actions and ignore the quarantine; a new
// revision, key, route or preview changes the failure's own input match.
func (s *session) classify(p *Plan, it *item, cached map[int64]store.Decision, failure *store.Failure, o Options) (need, stale bool) {
	hold := failure != nil && !o.Force && !o.Redecide && failure.Quarantined(it.revision, s.answerContext)
	d, hit := cached[it.rowID]
	if !hit {
		if hold {
			p.Quarantined++
			return false, false
		}
		p.Unseen++
		return true, false
	}
	current := d.StateHash == it.hash
	if !current && d.Revision != it.revision {
		if hold {
			p.Quarantined++
			return false, false
		}
		p.Invalidated++ // the message itself changed
		return true, false
	}
	have, missing := decodeAnswers(s.profile, d.Answers)
	switch {
	case len(missing) > 0 && len(have) > 0 && !o.Force:
		if hold {
			p.Quarantined++
			return false, false
		}
		p.Partial++ // only the questions never paid for are asked
		it.have = have
		return true, false
	case len(missing) > 0:
		if hold {
			p.Quarantined++
			return false, false
		}
		p.Invalidated++ // unreadable: decided again
		return true, false
	case !current:
		p.Stale++
		return o.Redecide || o.Force, true
	}
	p.Fresh++
	return o.Force, false
}

func (s *session) checkStores(only []string) error { return checkStores(s.stores, only) }

// checkStores refuses a run or probe that names a store the user did not
// pick, before any query or call.
func checkStores(stores map[string]bool, only []string) error {
	for _, k := range only {
		if !stores[k] {
			return fmt.Errorf("triage: store %q is not picked (%s)", k, source.SettingSelected)
		}
	}
	return nil
}

// selectAndEstimate keeps the newest pending items up to the limit and
// checks the budget.
func selectAndEstimate(p *Plan, o Options) {
	p.Pending = len(p.items)
	slices.SortStableFunc(p.items, func(a, b item) int {
		return cmp.Or(strings.Compare(b.received, a.received), cmp.Compare(b.rowID, a.rowID))
	})
	if o.Limit > 0 && len(p.items) > o.Limit {
		p.items = p.items[:o.Limit]
	}
	p.Selected = len(p.items)
	p.Estimate.PriceKnown = true
	for _, it := range p.items {
		p.Estimate.Add(it.est)
	}
	p.Refusal = p.Budget.Refuse(p.Estimate)
}
