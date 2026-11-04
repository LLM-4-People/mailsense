package triage

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// View is the triage as the app reads it: every judged (or failed)
// message with priors blended now, the coverage, and the inputs insights need.
type View struct {
	Key       store.TriageKey   `json:"key"`
	Profile   *taxonomy.Profile `json:"profile"`
	Results   map[int64]Result  `json:"results"`
	Coverage  Coverage          `json:"coverage"`
	Analysis  *threads.Analysis `json:"-"`
	Behaviour *threads.Profile  `json:"-"`
	// Routed: the answers are the certified route's, not the paid
	// decider's cache (the app always serves the route).
	Routed bool `json:"routed"`
	// Preview: the explicit learning.test_mode preview is on; answers that
	// the route abstained on because it is uncertified may be the local
	// model's threshold-free best guess (Result.Preview marks each such
	// message). This grants nothing.
	Preview bool `json:"preview"`
	labels  []feedback.Label
}

// Judged returns the values of every judged message (stale ones too: they
// are the latest the owner has).
func (v *View) Judged() feedback.Judged {
	out := feedback.Judged{}
	for id, r := range v.Results {
		if r.Values != nil {
			out[id] = r.Values
		}
	}
	return out
}

// Read builds the view. With the route wired it serves the route's stored
// answers under the profile fingerprint only; without one (tests only) the
// paid decider's key. Results of another source are never mixed in; a
// message whose last paid attempt failed keeps its earlier paid decision
// (shown stale) and carries the failure; a never-decided failed message is
// listed with its failure only.
func (t *Triager) Read(ctx context.Context) (*View, error) {
	return t.read(ctx, nil)
}

// ErrNotTriaged is returned for a row outside the eligible triaged scope:
// the message is not owner mail of a picked store and triaged folder role.
var ErrNotTriaged = errors.New("triage: message is not in the triaged scope")

// Result reads one message's triage (the per-message query of the UI).
func (t *Triager) Result(ctx context.Context, rowID int64) (Result, error) {
	v, err := t.read(ctx, []int64{rowID})
	if err != nil {
		return Result{}, err
	}
	r, ok := v.Results[rowID]
	if !ok {
		if v.Coverage.Eligible == 0 {
			return Result{}, fmt.Errorf("%w: message %d", ErrNotTriaged, rowID)
		}
		return Result{MessageID: rowID}, nil // eligible, not decided yet
	}
	return r, nil
}

// Results reads the triage of several messages at once (search filters);
// a message absent from the map is not triaged or not decided yet.
func (t *Triager) Results(ctx context.Context, rows []int64) (map[int64]Result, error) {
	if len(rows) == 0 {
		return map[int64]Result{}, nil
	}
	v, err := t.read(ctx, rows)
	if err != nil {
		return nil, err
	}
	return v.Results, nil
}

// read is the one path of every user-facing triage answer (Read, Result,
// Results, Analytics): nothing is answered until the picked mailboxes
// are fully learned (the learning gate). Runs (teacher labels) are not
// answers and are not gated. With a route wired the answers are the
// route's only; without one (tests only) the paid decider's cache is
// served, exactly as before the route existed.
func (t *Triager) read(ctx context.Context, rows []int64) (*View, error) {
	if err := t.gate.Allow(ctx); err != nil {
		return nil, err
	}
	s, err := t.prepareForRead(ctx)
	if err != nil {
		return nil, err
	}
	v := &View{Key: s.key, Profile: s.profile, Results: map[int64]Result{}, Analysis: s.analysis, Behaviour: s.behaviour,
		Preview: t.previewing(ctx), labels: s.labels}
	if t.route != nil {
		routeKey, err := t.configuredRouteKey()
		if err != nil {
			return nil, err
		}
		return t.readRoute(ctx, s, routeKey, rows, v)
	}
	cached, err := t.st.LoadDecisions(ctx, s.key)
	if err != nil {
		return nil, err
	}
	failures, err := t.st.LoadFailures(ctx, s.key)
	if err != nil {
		return nil, err
	}
	err = s.each(ctx, t.st, nil, rows, func(r *store.MessageRow, state string) error {
		a := messageAnswers{Failure: failures[r.RowID].Error}
		if d, ok := cached[r.RowID]; ok {
			a.Answers, a.Missing = decodeAnswers(s.profile, d.Answers)
			a.At, a.Stale = d.DecidedAt, d.StateHash != StateHash(state)
		}
		res, ok := s.result(r, a, &v.Coverage)
		if ok {
			v.Results[r.RowID] = res
		}
		return nil
	})
	return v, err
}

// loadRouteAnswers reads the route answers of one id set: the route records,
// the preview rows (when the route reader serves them) and the stored
// failures of the triage key. The routed read and the routed page both load
// their messages' answers through it, so the failure projection and the
// error wrapping of the two surfaces cannot drift. The order is part of the
// contract: records, previews, then failures.
func (t *Triager) loadRouteAnswers(ctx context.Context, key store.TriageKey, routeKey string, ids []int64) (map[int64]store.RouteRecord, map[int64]store.PreviewRecord, map[int64]store.Failure, error) {
	records, err := t.route.LoadRouteRecords(ctx, routeKey, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	previews, err := t.loadPreviews(ctx, routeKey, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	failures, err := t.st.LoadFailures(ctx, key)
	if err != nil {
		return nil, nil, nil, err
	}
	return records, previews, failures, nil
}

// readRoute is the view of the certified route: the route table under the
// route key is the only answer source (never mixed with the paid cache, not
// even per message). Staleness is the route row's
// revision against the message's. With the explicit test preview on, an
// answer the uncertified route abstained on is shown as the layer-1
// model's threshold-free best guess from the separate preview table;
// nothing else is replaced.
func (t *Triager) readRoute(ctx context.Context, s *session, routeKey string, rows []int64, v *View) (*View, error) {
	filter := triagedFilter(s.roles)
	filter.RowIDs = rows
	ids, err := t.st.MessageIDs(ctx, filter)
	if err != nil {
		return nil, err
	}
	// A message whose last attempt failed may have no route row at all. The
	// failures are the stored truth the summary counts, so the routed read
	// loads them too: coverage and the per-message failure chip then agree
	// with Summary instead of reporting zero failed.
	records, previews, failures, err := t.loadRouteAnswers(ctx, s.key, routeKey, ids)
	if err != nil {
		return nil, err
	}
	v.Routed = true
	err = s.eachEligible(ctx, t.st, nil, rows, func(r *store.MessageRow) error {
		a, err := routeMessageAnswers(s.profile, r, records, previews)
		if err != nil {
			return err
		}
		a.Failure = failures[r.RowID].Error
		res, shown := s.result(r, a, &v.Coverage)
		if shown {
			v.Results[r.RowID] = res
		}
		return nil
	})
	return v, err
}

// loadPreviews reads the preview rows of the given messages when the
// explicit test preview is on and the route reader serves them; a reader
// without previews (tests) shows the route abstentions.
func (t *Triager) loadPreviews(ctx context.Context, routeKey string, ids []int64) (map[int64]store.PreviewRecord, error) {
	if !t.previewing(ctx) {
		return nil, nil
	}
	pr, ok := t.route.(PreviewReader)
	if !ok {
		return nil, nil
	}
	return pr.LoadPreviewRecords(ctx, routeKey, ids)
}

// ApplyPreview is previewAnswers for another read surface (priority): the
// one preview substitution rule and trust boundary of the app. A caller
// passes the profile's answers and the message's preview answers; the
// returned map is have with only the uncertified abstentions replaced.
func ApplyPreview(p *taxonomy.Profile, have map[string]engine.Answer, preview map[string]engine.Answer) (bool, error) {
	return previewAnswers(p, have, preview)
}

// previewAnswers replaces a question's route answer only when the route
// abstained because it is uncertified: a certified route answer, a
// below-target or no-answer abstention, a refused answer and a question
// absent from the route record are never overridden. A question without a
// preview keeps its abstention. It reports whether any question was
// replaced and fails on a malformed preview (never a silent answer).
func previewAnswers(p *taxonomy.Profile, have map[string]engine.Answer, preview map[string]engine.Answer) (bool, error) {
	replaced := false
	for i := range p.Questions {
		q := &p.Questions[i]
		cur, ok := have[q.Name]
		if !ok || cur.Abstain != route.AbstainNotCertified {
			continue
		}
		a, ok := preview[q.Name]
		if !ok {
			continue
		}
		if err := checkPreviewAnswer(q, a); err != nil {
			return false, err
		}
		have[q.Name] = a
		replaced = true
	}
	return replaced, nil
}

// checkPreviewAnswer validates one stored preview answer at the trust
// boundary: the question's type, a usable Given value (never refused or
// abstained), finite numbers and the question's own distribution keys.
func checkPreviewAnswer(q *engine.Question, a engine.Answer) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("triage: preview answer of %s: "+format, append([]any{q.Name}, args...)...)
	}
	if a.Type != q.Type {
		return bad("type %q, want %q", a.Type, q.Type)
	}
	if !a.Answered() {
		return bad("not a given answer")
	}
	if math.IsNaN(a.Value) || math.IsInf(a.Value, 0) {
		return bad("non-finite value %v", a.Value)
	}
	if a.Confidence != nil && (math.IsNaN(*a.Confidence) || math.IsInf(*a.Confidence, 0) || *a.Confidence < 0 || *a.Confidence > 1) {
		return bad("invalid confidence %v", *a.Confidence)
	}
	sum := 0.0
	for key, p := range a.Probabilities {
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return bad("invalid probability of %q", key)
		}
		sum += p
	}
	switch q.Type {
	case engine.Noul:
		if a.Value < 0 || a.Value > 1 {
			return bad("value outside [0, 1]")
		}
		if len(a.Probabilities) != 0 {
			return bad("yes/no answers carry no distribution")
		}
	case engine.Choice:
		if !q.HasOption(a.Choice) {
			return bad("choice %q is not an option", a.Choice)
		}
		if a.Confidence == nil {
			return bad("missing confidence")
		}
		if len(a.Probabilities) != len(q.Options) {
			return bad("distribution of %d options, want %d", len(a.Probabilities), len(q.Options))
		}
		for _, o := range q.Options {
			if _, ok := a.Probabilities[o.Name]; !ok {
				return bad("distribution lacks option %q", o.Name)
			}
		}
		if math.Abs(sum-1) > 1e-5 {
			return bad("distribution sums to %v", sum)
		}
	case engine.Score:
		if a.Value < 0 || a.Value > float64(q.Top()) {
			return bad("value outside [0, %d]", q.Top())
		}
		if a.Confidence == nil {
			return bad("missing confidence")
		}
		if len(a.Probabilities) != len(q.Levels) {
			return bad("distribution of %d levels, want %d", len(a.Probabilities), len(q.Levels))
		}
		for k := range q.Levels {
			if _, ok := a.Probabilities[engine.LevelKey(k)]; !ok {
				return bad("distribution lacks level %q", engine.LevelKey(k))
			}
		}
		if math.Abs(sum-1) > 1e-5 {
			return bad("distribution sums to %v", sum)
		}
	}
	return nil
}

// messageAnswers is one message's stored answers as a reader serves them:
// the profile's questions present (an abstention included: it is an
// answer with no value), the names missing, when they were decided and
// whether the message moved on since.
type messageAnswers struct {
	Answers map[string]engine.Answer
	Missing []string
	At      string
	Stale   bool
	Failure string
	Preview bool // at least one answer was replaced by the layer-1 preview
}

// routeAnswers maps one route record's answers to the profile's
// questions: a question absent from the record, or with another type, is
// missing. An abstained route answer stays present (no value): a
// model-level abstention is uncertain and escalates, while a route-level
// one is a deliberate non-answer.
func routeAnswers(p *taxonomy.Profile, stored map[string]route.Answer) (map[string]engine.Answer, []string) {
	have := map[string]engine.Answer{}
	var missing []string
	for i := range p.Questions {
		q := &p.Questions[i]
		a, ok := stored[q.Name]
		if !ok || a.Type != q.Type {
			missing = append(missing, q.Name)
			continue
		}
		have[q.Name] = a.Answer
	}
	return have, missing
}

// routeMessageAnswers is the one route-record read behind a served message:
// the record's answers and staleness when it has one, then the preview
// substitution of the answers the uncertified route abstained on. The record
// and preview maps are already loaded; it adds no read and keeps the preview
// gate exactly where the caller put it.
func routeMessageAnswers(p *taxonomy.Profile, r *store.MessageRow,
	records map[int64]store.RouteRecord, previews map[int64]store.PreviewRecord) (messageAnswers, error) {
	a := messageAnswers{}
	if rec, ok := records[r.RowID]; ok {
		a.Answers, a.Missing = routeAnswers(p, rec.Answers)
		a.At, a.Stale = rec.DecidedAt, rec.Revision != r.Revision
	}
	if prec, ok := previews[r.RowID]; ok {
		changed, err := previewAnswers(p, a.Answers, prec.Answers)
		if err != nil {
			return messageAnswers{}, err
		}
		a.Preview = changed
	}
	return a, nil
}

// decidedValue records the certified class of a noul or score answer. The
// class is selective.Decide's at the profile cutoff; Number stays the
// model's own value, so display and the certified decision cannot disagree.
func decidedValue(p *taxonomy.Profile, name string, v taxonomy.Value) taxonomy.Value {
	q, ok := p.Question(name)
	if !ok || q == nil || !v.Answered() {
		return v
	}
	class, ok := selective.Decided(q, v.Answer(), p.Cutoff(name))
	if !ok {
		return v
	}
	switch q.Type {
	case engine.Noul:
		v.Decided = []string{engine.NoulNo, engine.NoulYes}[class]
	case engine.Score:
		v.Decided = engine.LevelKey(class)
	}
	return v
}

// decideValues records the certified class of every answer value at the
// profile cutoff. It is the one value pass of the full read and the page.
func decideValues(p *taxonomy.Profile, values map[string]taxonomy.Value) {
	for name, value := range values {
		values[name] = decidedValue(p, name, value)
	}
}

func (s *session) decideValues(values map[string]taxonomy.Value) {
	decideValues(s.profile, values)
}

// assembleResult is the one projection from one message's answers and
// assembled values to a Result. The full read and the page share it so
// their fields cannot drift. values nil means the message was not judged:
// the result keeps its identity, failure and preview only (the read's
// unjudged row). The caller still owns what only it has: priors before the
// call, the subject after it, and the read's coverage counters.
func assembleResult(p *taxonomy.Profile, r *store.MessageRow, a messageAnswers, values map[string]taxonomy.Value) Result {
	res := Result{MessageID: r.RowID, Values: values, Failure: a.Failure, Preview: a.Preview}
	if values == nil {
		return res
	}
	res.Missing, res.DecidedAt, res.Stale = a.Missing, a.At, a.Stale
	res.Uncertain = p.Uncertain(values)
	res.Escalate = len(res.Uncertain) > 0
	return res
}

// result builds one message's result and counts it in the coverage.
func (s *session) result(r *store.MessageRow, a messageAnswers, c *Coverage) (Result, bool) {
	c.Eligible++
	failed := a.Failure != ""
	if failed {
		c.Failed++
	}
	if len(a.Answers) == 0 {
		c.Unjudged++
		return assembleResult(s.profile, r, a, nil), failed
	}
	values := s.priors.annotate(s.profile, a.Answers, r.RowID, &r.Message)
	s.decideValues(values)
	res := assembleResult(s.profile, r, a, values)
	// The route answers are the certified classification; the blended values
	// are the display smoothing. The subject is derived from the same raw
	// answers the priority ranking reads, so the detail screen and the Brief
	// cannot disagree about a message's path.
	res.Subject = subject.FromAnswers(s.profile, a.Answers)
	switch {
	case res.Stale:
		c.Stale++
	case len(a.Missing) > 0:
		c.Partial++
	default:
		c.Fresh++
	}
	return res, true
}
