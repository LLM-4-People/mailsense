package triage

import (
	"context"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Triage page bounds. They are part of the read contract, not settings:
// the UI asks for a page and the server refuses a page it would not
// render.
const (
	// TriagePageDefault is the page size of a query without one.
	TriagePageDefault = 50
	// TriagePageMax is the most results one page returns.
	TriagePageMax = 200
)

// PageQuery asks for one page of the triage, newest message first.
type PageQuery struct {
	Limit  int `json:"limit,omitempty"`  // 0: TriagePageDefault
	Offset int `json:"offset,omitempty"` // 0: the first page
}

// Page is one deterministic page of the triage. Total is the number of
// decided results in the population, not the page size; the UI pages
// until Offset+len(Results) reaches it. Results is a map because the UI
// addresses rows by id; IDs carries the displayed order (newest message
// first), including only the rows actually present in Results.
type Page struct {
	Key store.TriageKey `json:"key"`
	// Profile is the profile the page was built under: the server needs it
	// to decode answers, but the wire does not. The screen reads the one
	// question catalogue (/api/questions); shipping the whole profile on
	// every page duplicated ~16 KB per request.
	Profile *taxonomy.Profile `json:"-"`
	Results map[int64]PageRow `json:"results"`
	IDs     []int64           `json:"ids"`
	Routed  bool              `json:"routed"`
	// Preview: the explicit learning.test_mode preview is on (the per-
	// message Result.Preview marks the messages carrying a preview answer).
	Preview bool `json:"preview"`
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
}

// PageRow is one Recent-list row: exactly the fields the list renders. The
// full Result (every answer value, the subject hierarchy and the uncertainty
// list) stays on /api/triage/{id}; shipping it in every page put ~60-70 KB of
// unread JSON on each 50-row read. Answered counts only usable values: a
// present refusal or abstention increments Abstained and never takes the
// first-value slot. Missing stays the absent questions. FirstQuestion is the
// first certified answered question in the profile's document order, or the
// first automatic rule value when no question is certified.
type PageRow struct {
	MessageID int64 `json:"message_id"`
	Answered  int   `json:"answered"`
	Abstained int   `json:"abstained"`
	// Automatic counts the route abstentions a deterministic rule filled.
	// It is its own count, beside answered and abstained: the value is shown,
	// but the route did not certify it and it must never inflate the answered
	// total or hide the remaining plain abstentions.
	Automatic     int            `json:"automatic,omitempty"`
	FirstQuestion string         `json:"first_question,omitempty"`
	FirstValue    taxonomy.Value `json:"first_value"`
	Missing       int            `json:"missing"`
	Stale         bool           `json:"stale"`
	DecidedAt     string         `json:"decided_at,omitempty"`
	Escalate      bool           `json:"escalate"`
	Failure       string         `json:"failure,omitempty"`
	Preview       bool           `json:"preview"`
}

// pageRowOf projects one full result onto the list row. Only the profile's
// questions count: a stored value outside it is ignored, and a present value
// that is refused or abstained is counted apart from the usable answers.
func pageRowOf(res Result, profile *taxonomy.Profile) PageRow {
	row := PageRow{MessageID: res.MessageID, Missing: len(res.Missing),
		Stale: res.Stale, DecidedAt: res.DecidedAt, Escalate: res.Escalate, Failure: res.Failure, Preview: res.Preview}
	if profile != nil {
		for _, q := range profile.Questions {
			value, ok := res.Values[q.Name]
			if !ok {
				continue
			}
			switch {
			case value.Rule != "":
				// The route abstained; the rule supplied a display value.
				row.Automatic++
			case !value.Answered():
				row.Abstained++
				continue
			default:
				row.Answered++
			}
			if row.FirstQuestion == "" || (row.FirstValue.Rule != "" && value.Rule == "") {
				row.FirstQuestion, row.FirstValue = q.Name, value
			}
		}
	}
	return row
}

// Summary is the triage header: the coverage counts and the page
// population. It never carries message content or a per-question
// distribution: the Recent screen reads coverage, total and preview only,
// and the distribution duplicated a full-population scan on every rebuild.
// The preview flag only says the test preview is on: the counts stay the
// stored route's (preview answers are display data, never counted).
type Summary struct {
	Key      store.TriageKey `json:"key"`
	Routed   bool            `json:"routed"`
	Preview  bool            `json:"preview"`
	Coverage Coverage        `json:"coverage"`
	Total    int             `json:"total"` // decided results (the page population)
}

// Page returns one page of the triage, newest message first. It reads the
// page's route rows and message rows only: never the whole message table,
// the whole answers payload, or the causal core. The routed page is served
// from the bounded read-model cache (internal/viewcache): a fresh entry
// answers at once, an expired one answers while one background rebuild runs,
// and concurrent first callers share a single build. Without a route (tests
// only) the paid cache is read fresh on every call.
func (t *Triager) Page(ctx context.Context, q PageQuery) (*Page, error) {
	limit := q.Limit
	if limit == 0 {
		limit = TriagePageDefault
	}
	if limit < 1 || limit > TriagePageMax {
		return nil, fmt.Errorf("triage: page limit must be between 1 and %d, got %d", TriagePageMax, q.Limit)
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("triage: page offset must be 0 or more, got %d", q.Offset)
	}
	// The gate is checked before any snapshot: a closed gate never serves
	// a cached gated view.
	if err := t.gate.Allow(ctx); err != nil {
		return nil, err
	}
	if t.route == nil {
		s, err := t.prepare(ctx)
		if err != nil {
			return nil, err
		}
		out := &Page{Key: s.key, Profile: s.profile, Results: map[int64]PageRow{}, IDs: []int64{}, Preview: t.previewing(ctx), Limit: limit, Offset: q.Offset}
		return t.pageDecider(ctx, q, out)
	}
	id, err := t.identity()
	if err != nil {
		return nil, err
	}
	key := t.cacheKey(ctx, id, limit, q.Offset)
	return t.pages.Get(ctx, key, func(ctx context.Context) (*Page, error) {
		return t.buildPage(ctx, id.routeKey, limit, q.Offset)
	})
}

// cacheKey is the one cache identity of a routed triage view: the mail and
// route generations, the preview flag, the decider identity and the page
// window (zero for a summary). A field added in one path and not the other
// would mis-cache a page against its summary.
func (t *Triager) cacheKey(ctx context.Context, id *viewIdentity, limit, offset int) viewKey {
	return viewKey{
		gen: t.st.MailGeneration(), routeGen: t.st.RouteGeneration(), preview: t.previewing(ctx),
		version: id.key, fingerprint: id.fingerprint, routeKey: id.routeKey, decider: id.decider,
		limit: limit, offset: offset,
	}
}

// pageResult builds one page row without the causal core. The route answers
// are the certified values, so the page has no prior blend, no subject and
// no coverage counters; decideValues and assembleResult are the full read's
// own passes, so a page row cannot drift from the detail result.
func pageResult(p *taxonomy.Profile, r *store.MessageRow, a messageAnswers) (Result, bool) {
	if len(a.Answers) == 0 {
		return assembleResult(p, r, a, nil), a.Failure != ""
	}
	values := make(map[string]taxonomy.Value, len(a.Answers))
	for name, answer := range a.Answers {
		values[name] = taxonomy.FromAnswer(answer)
	}
	decideValues(p, values)
	return assembleResult(p, r, a, values), true
}

// eachEligible streams the page window's rows under the triaged roles and
// owner scope. The page row carries identity and revision only: it reads no
// subject, sender, body, headers, recipients or items, because the list
// renders the stored answers and the revision-based stale flag; message
// content belongs to the detail read. The route page already selected the
// window, so the picked store filter is nil, exactly as the full read's row
// read (only=nil).
func (s *viewSession) eachEligible(ctx context.Context, st *store.Store, rows []int64, fn func(r *store.MessageRow) error) error {
	return eachTriagedFields(ctx, st, s.roles, nil, rows, store.MessageFieldsOf(), fn)
}

// buildPage renders one routed page from the store. It is the build behind
// the cache and must not touch it; the route is wired (Page caches only a
// routed triager). It uses the page session and the page-only result
// builder: the causal core (threads, behaviour, evidence, priors, labels,
// history and composer) is never loaded for a list row.
func (t *Triager) buildPage(ctx context.Context, routeKey string, limit, offset int) (*Page, error) {
	s, err := t.prepareView(ctx)
	if err != nil {
		return nil, err
	}
	out := &Page{Key: s.key, Profile: s.profile, Results: map[int64]PageRow{}, IDs: []int64{}, Routed: true,
		Preview: t.previewing(ctx), Limit: limit, Offset: offset}
	window, total, err := t.st.RoutePage(ctx, routeKey, triagedFilter(s.roles), limit, offset)
	if err != nil {
		return nil, err
	}
	out.Total = total
	if len(window) == 0 {
		return out, nil
	}
	ids := make([]int64, len(window))
	for i, ref := range window {
		ids[i] = ref.MessageID
	}
	// The window row may carry a failed later attempt; the page shows that
	// failure exactly as the full read does, so the list chip and coverage
	// cannot disagree about the same message.
	records, previews, failures, err := t.loadRouteAnswers(ctx, s.key, routeKey, ids)
	if err != nil {
		return nil, err
	}
	rules, err := t.loadRuleAnswers(ctx, ids)
	if err != nil {
		return nil, err
	}
	err = s.eachEligible(ctx, t.st, ids, func(r *store.MessageRow) error {
		a, err := routeMessageAnswers(s.profile, r, records, previews, rules)
		if err != nil {
			return err
		}
		a.Failure = failures[r.RowID].Error
		res, shown := pageResult(s.profile, r, a)
		if shown {
			out.Results[r.RowID] = pageRowOf(res, s.profile)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	// The row read streams in row-id order; the displayed order is the route
	// window's instant order. IDs keeps that order and leaves out a window row
	// pageResult hid (no answers and no failure), so the wire never names an
	// id that Results does not carry.
	out.IDs = make([]int64, 0, len(out.Results))
	for _, ref := range window {
		if _, ok := out.Results[ref.MessageID]; ok {
			out.IDs = append(out.IDs, ref.MessageID)
		}
	}
	return out, nil
}

// pageDecider is the page of a triager without a route (tests only): the
// paid cache is the one source, exactly as the full read serves it.
func (t *Triager) pageDecider(ctx context.Context, q PageQuery, out *Page) (*Page, error) {
	v, err := t.read(ctx, nil)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(v.Results))
	for id := range v.Results {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		if a > b {
			return -1
		}
		if a < b {
			return 1
		}
		return 0
	})
	out.Total = len(ids)
	if q.Offset >= out.Total {
		return out, nil
	}
	end := min(q.Offset+out.Limit, out.Total)
	displayed := ids[q.Offset:end]
	out.IDs = slices.Clone(displayed)
	for _, id := range displayed {
		out.Results[id] = pageRowOf(v.Results[id], v.Profile)
	}
	return out, nil
}

// Summary counts the stored answers over the eligible population, with
// the same coverage rules the per-message result uses. Like Page it is
// served from the bounded read-model cache when the route is wired; the
// gate is checked before any snapshot.
func (t *Triager) Summary(ctx context.Context) (*Summary, error) {
	if err := t.gate.Allow(ctx); err != nil {
		return nil, err
	}
	if t.route == nil {
		s, err := t.prepare(ctx)
		if err != nil {
			return nil, err
		}
		out := &Summary{Key: s.key, Preview: t.previewing(ctx)}
		return t.summaryDecider(ctx, s, out)
	}
	id, err := t.identity()
	if err != nil {
		return nil, err
	}
	key := t.cacheKey(ctx, id, 0, 0)
	return t.summaries.Get(ctx, key, func(ctx context.Context) (*Summary, error) {
		return t.buildSummary(ctx, id.routeKey)
	})
}

// buildSummary renders the routed summary from the store. It is the build
// behind the cache and must not touch it; the route is wired (Summary caches
// only a routed triager). The store's one aggregate is the whole read: no
// message is materialized and no answer is decoded here.
func (t *Triager) buildSummary(ctx context.Context, routeKey string) (*Summary, error) {
	s, err := t.prepareView(ctx)
	if err != nil {
		return nil, err
	}
	questions := questionKeys(s.profile)
	c, err := t.coverage(ctx, routeKey, s.key.Fingerprint, s.key.Decider, triagedFilter(s.roles), questions)
	if err != nil {
		return nil, err
	}
	return &Summary{
		Key: s.key, Routed: true, Preview: t.previewing(ctx),
		Coverage: Coverage{Eligible: c.Eligible, Fresh: c.Fresh, Partial: c.Partial, Stale: c.Stale,
			Failed: c.Failed, Unjudged: c.Eligible - c.Total},
		Total: c.Total,
	}, nil
}

// summaryDecider is the summary of a triager without a route (tests only).
func (t *Triager) summaryDecider(ctx context.Context, s *session, out *Summary) (*Summary, error) {
	v, err := t.read(ctx, nil)
	if err != nil {
		return nil, err
	}
	out.Coverage = v.Coverage
	out.Total = len(v.Results)
	return out, nil
}
