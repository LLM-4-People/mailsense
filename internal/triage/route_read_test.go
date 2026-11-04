package triage

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// fakeRoute serves stored route records (the store in the app) under the
// fingerprint it was asked for, and the optional preview records.
type fakeRoute struct {
	fingerprint  string
	records      map[int64]store.RouteRecord
	previews     map[int64]store.PreviewRecord
	previewCalls int
}

func (f *fakeRoute) LoadRouteRecords(_ context.Context, fingerprint string, ids []int64) (map[int64]store.RouteRecord, error) {
	f.fingerprint = fingerprint
	out := map[int64]store.RouteRecord{}
	for _, id := range ids {
		if r, ok := f.records[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func (f *fakeRoute) LoadPreviewRecords(_ context.Context, fingerprint string, ids []int64) (map[int64]store.PreviewRecord, error) {
	f.fingerprint = fingerprint
	f.previewCalls++
	out := map[int64]store.PreviewRecord{}
	for _, id := range ids {
		if r, ok := f.previews[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

// previewingGate is a learning gate whose explicit test preview is on.
type previewingGate struct{}

func (previewingGate) Allow(context.Context) error         { return nil }
func (previewingGate) PreviewAnswers(context.Context) bool { return true }

// revision is the message's current revision (the route row's staleness
// reference).
func (w *world) revision(t testing.TB, id int64) int64 {
	t.Helper()
	var rev int64
	if err := w.st.EachMessage(t.Context(), store.MessageFilter{RowIDs: []int64{id}}, func(r *store.MessageRow) error {
		rev = r.Revision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rev
}

func confidence(v float64) *float64 { return &v }

// The app's answers are the route's only: a paid decision for the same
// message and for a question the route does not answer never shows, while
// the paid cache stays planning data.
func TestPaidAndRouteAnswersNeverMix(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	w.run(Options{})
	paid, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := paid.Results[id].Values["category"].Choice; got == "" {
		t.Fatalf("the paid answer is missing: %+v", paid.Results[id])
	}
	if paid.Routed {
		t.Fatal("the paid path claims to be routed")
	}
	fr := &fakeRoute{records: map[int64]store.RouteRecord{id: {
		MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Choice: "work", Confidence: confidence(0.9)}}}}}}
	w.useRoute(t, fr)
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Routed || fr.fingerprint != testRouteKey {
		t.Fatalf("routed %v, route key %q want %q", v.Routed, fr.fingerprint, testRouteKey)
	}
	if got := v.Results[id].Values["category"].Choice; got != "work" {
		t.Fatalf("the view mixed the paid answer: %q", got)
	}
	// The paid cache has reply_needed; the route record does not: it is
	// missing, never filled from the paid key.
	if _, ok := v.Results[id].Values["reply_needed"]; ok {
		t.Fatal("a paid answer filled a question the route did not answer")
	}
	if !slices.Contains(v.Results[id].Missing, "reply_needed") {
		t.Fatalf("missing %v", v.Results[id].Missing)
	}
	// Plan reads and counts the paid cache only (the route is read-only).
	plan, err := w.tr.Plan(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Fresh != 1 || plan.Unseen != 0 {
		t.Fatalf("the paid cache is still the planning input: %+v", plan)
	}
}

// An abstained route answer is present and carries no value. A
// model-level abstention (below_target) is uncertain and escalates; a
// route-level one (not_certified) is a deliberate non-answer.
func TestTriageReadServesAnAbstainedRouteAnswer(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	fr := &fakeRoute{records: map[int64]store.RouteRecord{id: {
		MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]route.Answer{
			"category":     {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}},
			"reply_needed": {Answer: engine.Answer{Type: engine.Noul, Abstain: engine.AbstainBelowTarget}},
		}}}}
	w.useRoute(t, fr)
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := v.Results[id]
	cat, ok := r.Values["category"]
	if !ok {
		t.Fatalf("the abstained answer is not present: %+v", r)
	}
	if cat.Choice != "" || cat.Number != 0 || cat.Confidence != nil || cat.Abstain != route.AbstainNotCertified {
		t.Fatalf("an abstained answer carries a value: %+v", cat)
	}
	if len(r.Uncertain) != 1 || r.Uncertain[0] != "reply_needed" || !r.Escalate {
		t.Fatalf("only the model-level abstention is uncertain: %+v", r)
	}
	if v.Coverage.Partial != 1 || v.Coverage.Unjudged != 0 || v.Coverage.Stale != 0 {
		t.Fatalf("coverage %+v", v.Coverage)
	}
}

// A message whose only certified escalation answer is confident is not
// escalated by the other roles' route-level non-answers. This is the live
// shape with one certified question (suspicious): the un-certified roles
// must not flag every message for the Generator.
func TestTriageReadDoesNotEscalateRouteLevelNonAnswers(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	fr := &fakeRoute{records: map[int64]store.RouteRecord{id: {
		MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]route.Answer{
			"category":     {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}},
			"reply_needed": {Answer: engine.Answer{Type: engine.Noul, Abstain: route.AbstainBelowCoverage}},
			"urgency":      {Answer: engine.Answer{Type: engine.Score, Abstain: route.AbstainNotCertified}},
			"suspicious":   {Answer: engine.Answer{Type: engine.Noul, Value: 0.1}},
		}}}}
	w.useRoute(t, fr)
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := v.Results[id]
	if len(r.Uncertain) != 0 || r.Escalate {
		t.Fatalf("route-level non-answers escalated: %+v", r)
	}
}

// Staleness is the route row's revision against the message's.
func TestRouteAnswerIsStaleByItsRevision(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	rev := w.revision(t, id)
	rec := store.RouteRecord{MessageID: id, Revision: rev, DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Choice: "work", Confidence: confidence(0.9)}}}}
	fr := &fakeRoute{records: map[int64]store.RouteRecord{id: rec}}
	w.useRoute(t, fr)
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v.Results[id].Stale || v.Coverage.Stale != 0 || v.Coverage.Partial != 1 {
		t.Fatalf("a current row is stale: %+v %+v", v.Results[id], v.Coverage)
	}
	rec.Revision = rev - 1
	fr.records[id] = rec
	v, err = w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Results[id].Stale || v.Coverage.Stale != 1 {
		t.Fatalf("an older row is fresh: %+v %+v", v.Results[id], v.Coverage)
	}
	if v.Results[id].DecidedAt != "2026-10-25T10:00:00Z" {
		t.Fatalf("decided at %q", v.Results[id].DecidedAt)
	}
}

// The store is the production route reader: the read serves the rows the
// layer-1 commit wrote under the profile fingerprint, and a message
// edited after the row went stale.
func TestReadServesTheStoredRoute(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginLayer1Run(t.Context(), "inputs")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Choice: "work", Confidence: confidence(0.9)}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), RunID: run, Answers: string(body)}
	err = w.st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: p.Fingerprint, Decider: "layer1"},
		RouteKey:  testRouteKey,
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes:    func(fn func(store.RouteAnswer) error) error { return fn(rec) }})
	if err != nil {
		t.Fatal(err)
	}
	w.useRoute(t, w.st)
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Routed || v.Results[id].Values["category"].Choice != "work" || v.Results[id].Stale || v.Results[id].DecidedAt == "" {
		t.Fatalf("the stored route is not served: %+v", v.Results[id])
	}
	m, err := w.st.MessageByRow(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	m.Body += " (edited)"
	w.write(m)
	v, err = w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Results[id].Stale || v.Coverage.Stale != 1 {
		t.Fatalf("an edited message is not stale: %+v %+v", v.Results[id], v.Coverage)
	}
}

// A prior is evidence about a real answer, never a substitute for one: an
// abstained answer is not annotated (the zero value must not become a
// model answer).
func TestPriorsDoNotAnnotateAnAbstainedAnswer(t *testing.T) {
	w := newWorld(t, nil)
	for i, conv := range []string{"A", "B", "C", "D"} {
		w.mail("a@client.com", conv, "Budget", time.Duration(i)*time.Hour, 10*time.Minute)
	}
	target := w.mail("a@client.com", "T", "Budget", 9*time.Hour, 0)
	c, err := w.tr.core(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.st.MessageByRow(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	values := c.priors.annotate(c.profile, map[string]engine.Answer{
		"reply_needed": {Type: engine.Noul, Abstain: engine.AbstainBelowTarget},
	}, target, &m)
	v := values["reply_needed"]
	if v.Abstain == "" || v.Prior != nil || v.Number != 0 || v.Model != 0 {
		t.Fatalf("an abstained answer was blended: %+v", v)
	}
	for _, answer := range []engine.Answer{
		{Type: engine.Noul, Abstain: route.AbstainNotCertified},
		{Type: engine.Noul, Abstain: route.AbstainBelowCoverage},
		{Type: engine.Noul, Abstain: route.AbstainNoAnswer},
	} {
		values := c.priors.annotate(c.profile, map[string]engine.Answer{"reply_needed": answer}, target, &m)
		v := values["reply_needed"]
		if v.Abstain != answer.Abstain || v.Prior != nil || v.Number != 0 || v.Model != 0 {
			t.Fatalf("route non-answer %q was blended: %+v", answer.Abstain, v)
		}
	}
}

// givenChoice is the threshold-free shape of a choice answer (the store's
// preview payload) for a profile question's option.
func givenChoice(q *engine.Question, pick string) engine.Answer {
	probs := make([]float64, len(q.Options))
	fill := (1 - 0.9) / float64(len(q.Options)-1)
	for i := range probs {
		probs[i] = fill
	}
	for i := range q.Options {
		if q.Options[i].Name == pick {
			probs[i] = 0.9
		}
	}
	return selective.Given(q, probs, 0.5)
}

// The explicit test preview shows the layer-1 best guess only where the
// route abstained because it is uncertified; a below-target abstention is
// never overridden.
func TestPreviewAnswersReplaceOnlyUncertifiedRouteAnswers(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	rq, _ := p.Question("reply_needed")
	uq, _ := p.Question("urgency")
	fr := &fakeRoute{
		records: map[int64]store.RouteRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{
				"category":     {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}},
				"reply_needed": {Answer: engine.Answer{Type: engine.Noul, Abstain: route.AbstainNotCertified}},
				"urgency":      {Answer: engine.Answer{Type: engine.Score, Abstain: engine.AbstainBelowTarget}},
			}}},
		previews: map[int64]store.PreviewRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]engine.Answer{
				"category":     givenChoice(cq, "work"),
				"reply_needed": selective.Given(rq, []float64{0.1, 0.9}, 0.5),
				"urgency":      selective.Given(uq, []float64{0.05, 0.1, 0.15, 0.4, 0.3}, 0.5),
			}}},
	}
	w.useRoute(t, fr)

	off, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if off.Preview || fr.previewCalls != 0 {
		t.Fatalf("preview off read previews: view=%v calls=%d", off.Preview, fr.previewCalls)
	}
	if got := off.Results[id]; got.Preview || got.Values["category"].Abstain != route.AbstainNotCertified ||
		got.Values["reply_needed"].Abstain != route.AbstainNotCertified {
		t.Fatalf("preview off does not show the route abstentions: %+v", got)
	}

	w.tr.gate = previewingGate{}
	on, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !on.Preview || fr.previewCalls == 0 {
		t.Fatalf("preview on is not reported or not read: view=%v calls=%d", on.Preview, fr.previewCalls)
	}
	got := on.Results[id]
	if !got.Preview {
		t.Fatal("the message does not report a substituted answer")
	}
	if a := got.Values["category"]; a.Abstain != "" || a.Choice != "work" || a.Confidence == nil {
		t.Fatalf("the category preview is not shown: %+v", a)
	}
	if a := got.Values["reply_needed"]; a.Abstain != "" || a.Number != 0.9 {
		t.Fatalf("the reply_needed preview is not shown: %+v", a)
	}
	if a := got.Values["urgency"]; a.Abstain != engine.AbstainBelowTarget || a.Choice != "" {
		t.Fatalf("a below-target abstention was overridden: %+v", a)
	}
	if !reflect.DeepEqual(off.Coverage, on.Coverage) {
		t.Fatalf("the preview changed the coverage: %+v -> %+v", off.Coverage, on.Coverage)
	}
	if !reflect.DeepEqual(off.Results[id].Missing, on.Results[id].Missing) {
		t.Fatalf("the preview changed the missing questions: %v -> %v", off.Results[id].Missing, on.Results[id].Missing)
	}
}

// A message with no preview row keeps its abstention, and a certified
// route answer is never replaced by the preview answer.
func TestPreviewNeverFillsOrOverridesWithoutAnUncertifiedAbstention(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	missing := w.mail("a@client.com", "A", "Budget", 0, 0)
	certified := w.mail("a@client.com", "B", "Budget", time.Hour, 0)
	abstained := engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}
	fr := &fakeRoute{
		records: map[int64]store.RouteRecord{
			missing: {MessageID: missing, Revision: w.revision(t, missing), DecidedAt: "2026-10-25T10:00:00Z",
				Answers: map[string]route.Answer{"category": {Answer: abstained}}},
			certified: {MessageID: certified, Revision: w.revision(t, certified), DecidedAt: "2026-10-25T10:00:00Z",
				Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Choice: "finance",
					Confidence: confidence(0.9), Probabilities: map[string]float64{"finance": 0.9, "work": 0.1}}}}},
		},
		previews: map[int64]store.PreviewRecord{certified: {
			MessageID: certified, Revision: w.revision(t, certified), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]engine.Answer{"category": givenChoice(cq, "work")}}},
	}
	w.useRoute(t, fr)
	w.tr.gate = previewingGate{}
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !v.Preview {
		t.Fatal("the view does not report the preview setting")
	}
	if r := v.Results[missing]; r.Preview || r.Values["category"].Abstain != route.AbstainNotCertified {
		t.Fatalf("a message without a preview row lost its abstention: %+v", r)
	}
	if r := v.Results[certified]; r.Preview || r.Values["category"].Choice != "finance" {
		t.Fatalf("the preview overrode a certified route answer: %+v", r)
	}
}

// The page serves the same preview answers without changing the route's
// ordering or coverage counters.
func TestPreviewPageKeepsOrderingAndCoverage(t *testing.T) {
	w := newWorld(t, nil)
	older := w.mail("a@client.com", "A", "Budget", 0, 0)
	newer := w.mail("b@client.com", "B", "Budget", time.Hour, 0)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	answer := engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}
	routeBody, err := json.Marshal(map[string]route.Answer{"category": {Answer: answer}})
	if err != nil {
		t.Fatal(err)
	}
	previewBody, err := json.Marshal(map[string]engine.Answer{"category": givenChoice(cq, "work")})
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginLayer1Run(t.Context(), "inputs")
	if err != nil {
		t.Fatal(err)
	}
	var routes []store.RouteAnswer
	var previews []store.PreviewAnswer
	for _, id := range []int64{older, newer} {
		routes = append(routes, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), RunID: run, Answers: string(routeBody)})
		previews = append(previews, store.PreviewAnswer{MessageID: id, Revision: w.revision(t, id), RunID: run, Answers: string(previewBody)})
	}
	err = w.st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: p.Fingerprint, Decider: "layer1"},
		RouteKey:  testRouteKey,
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes: func(fn func(store.RouteAnswer) error) error {
			for _, r := range routes {
				if err := fn(r); err != nil {
					return err
				}
			}
			return nil
		},
		Preview: func(fn func(store.PreviewAnswer) error) error {
			for _, r := range previews {
				if err := fn(r); err != nil {
					return err
				}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	w.useRoute(t, w.st)
	firstOff, err := w.tr.Page(t.Context(), PageQuery{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	secondOff, err := w.tr.Page(t.Context(), PageQuery{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	summaryOff, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The list row is a projection now: the full answer map is the detail
	// read, and the row must carry exactly the detail's first value.
	detailOff, err := w.tr.Result(t.Context(), newer)
	if err != nil {
		t.Fatal(err)
	}
	if detailOff.Values["category"].Abstain != route.AbstainNotCertified && firstOff.Total != 2 {
		t.Fatalf("preview off page %+v", firstOff)
	}
	w.tr.gate = previewingGate{}
	firstOn, err := w.tr.Page(t.Context(), PageQuery{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	secondOn, err := w.tr.Page(t.Context(), PageQuery{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	summaryOn, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !firstOn.Preview || firstOn.Total != firstOff.Total || secondOn.Total != secondOff.Total {
		t.Fatalf("the preview changed the page size: %+v %+v", firstOn, firstOff)
	}
	if summaryOn.Coverage != summaryOff.Coverage || summaryOn.Total != summaryOff.Total || !summaryOn.Preview {
		t.Fatalf("the preview changed the coverage counters: %+v -> %+v", summaryOff, summaryOn)
	}
	if _, ok := firstOn.Results[newer]; !ok {
		t.Fatalf("the newest message is no longer first: %+v", firstOn.Results)
	}
	if _, ok := secondOn.Results[older]; !ok {
		t.Fatalf("the oldest message is no longer second: %+v", secondOn.Results)
	}
	for _, page := range []*Page{firstOn, secondOn} {
		for id, res := range page.Results {
			detail, err := w.tr.Result(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Preview || res.FirstQuestion == "" || !reflect.DeepEqual(res.FirstValue, detail.Values[res.FirstQuestion]) {
				t.Fatalf("message %d did not show the preview answer: %+v", id, res)
			}
		}
	}
	for _, page := range []*Page{firstOff, secondOff} {
		for id, res := range page.Results {
			// The page was built with the preview off: the route abstained,
			// so the row counts the present non-answer and has no answered
			// first value (the gate has already switched to preview by this
			// point, so the detail read shows the preview answer).
			if res.Preview || res.Abstained != 1 || res.Answered != 0 || res.FirstQuestion != "" {
				t.Fatalf("message %d preview off: %+v", id, res)
			}
		}
	}
}

// The preview trust boundary uses the same answer-state predicate: only a
// given answer is accepted; a refusal and every abstention are rejected
// before their other fields are read.
func TestPreviewAnswerRequiresAGivenAnswer(t *testing.T) {
	w := newWorld(t, nil)
	q := mustQuestion(t, w, "category")
	cases := []struct {
		name   string
		answer engine.Answer
		ok     bool
	}{
		{"answered", givenChoice(q, "work"), true},
		{"refused", engine.Answer{Type: engine.Choice, Refused: true}, false},
		{"below target", engine.Answer{Type: engine.Choice, Abstain: engine.AbstainBelowTarget}, false},
		{"not certified", engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}, false},
		{"below coverage", engine.Answer{Type: engine.Choice, Abstain: route.AbstainBelowCoverage}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPreviewAnswer(q, tc.answer)
			if (err == nil) != tc.ok {
				t.Fatalf("checkPreviewAnswer(%+v) = %v, want ok %v", tc.answer, err, tc.ok)
			}
			if !tc.ok && !strings.Contains(err.Error(), "not a given answer") {
				t.Fatalf("rejected for another reason: %v", err)
			}
		})
	}
}

// A malformed preview row fails the read rather than silently becoming an
// answer.
func TestMalformedPreviewFailsTheRead(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	fr := &fakeRoute{
		records: map[int64]store.RouteRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}}},
		}},
		previews: map[int64]store.PreviewRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]engine.Answer{"category": {Type: engine.Noul, Value: 0.5}}}},
	}
	w.useRoute(t, fr)
	w.tr.gate = previewingGate{}
	if _, err := w.tr.Read(t.Context()); err == nil {
		t.Fatal("a preview answer of the wrong type must fail the read")
	}
	fr.previews[id] = store.PreviewRecord{MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]engine.Answer{"category": givenChoice(mustQuestion(t, w, "category"), "work")}}
	if _, err := w.tr.Read(t.Context()); err != nil {
		t.Fatalf("a valid preview answer failed: %v", err)
	}
	fr.previews[id] = store.PreviewRecord{MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
		Answers: map[string]engine.Answer{"category": {Type: engine.Choice, Choice: "work", Confidence: confidence(0.9),
			Probabilities: map[string]float64{"finance": 0.5, "work": 0.5}}}}
	if _, err := w.tr.Read(t.Context()); err == nil {
		t.Fatal("an incomplete distribution must fail the read")
	}
}

func mustQuestion(t *testing.T, w *world, name string) *engine.Question {
	t.Helper()
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	q, ok := p.Question(name)
	if !ok {
		t.Fatalf("question %s missing", name)
	}
	return q
}

// Display must render the class selective.Decide chose at the profile
// cutoff, not re-threshold the model value at 0.5 or round an expected
// level. The raw value stays on the wire for the probability display.
func TestDecidedValueUsesTheProfileCutoff(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.Thresholds[taxonomy.ThresholdReply] = 0.6
	noul := taxonomy.Value{Type: engine.Noul, Number: 0.55, Model: 0.55}
	if got := decidedValue(p, "reply_needed", noul); got.Decided != engine.NoulNo {
		t.Fatalf("0.55 at cutoff 0.6 = %q, want %q", got.Decided, engine.NoulNo)
	}
	score := taxonomy.Value{Type: engine.Score, Number: 2.4, Model: 2.4,
		Probabilities: map[string]float64{"0": 0.05, "1": 0.10, "2": 0.20, "3": 0.30, "4": 0.35}}
	if got := decidedValue(p, "urgency", score); got.Decided != "4" {
		t.Fatalf("score argmax = %q, want 4", got.Decided)
	}
}
