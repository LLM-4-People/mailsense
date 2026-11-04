package layer1

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// routeKey is the configuration's route storage key; a pass writes its
// route and preview rows under it, never under the legacy question-spec
// fingerprint.
func (w *world) routeKey(t *testing.T) string {
	t.Helper()
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return plan.RouteKey()
}

// routeAnswersByID reads the current configuration's stored route answers.
func (w *world) routeAnswersByID(t *testing.T) map[int64]map[string]route.Answer {
	t.Helper()
	return w.routeAnswersAt(t, w.routeKey(t))
}

// routeAnswersAt reads the route rows stored under one explicit key: a
// configuration change moves the current key before the next commit, so a
// test that checks the previous rows must name the key they were written
// under.
func (w *world) routeAnswersAt(t *testing.T, key string) map[int64]map[string]route.Answer {
	t.Helper()
	out, err := w.st.LoadRouteAnswers(t.Context(), key, w.ids)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// rawRouteAnswers is the answers column of every stored row, byte for
// byte.
func (w *world) rawRouteAnswers(t *testing.T) map[int64]string {
	t.Helper()
	out := map[int64]string{}
	if err := w.st.EachRouteAnswer(t.Context(), w.routeKey(t), func(a store.RouteAnswer) error {
		out[a.MessageID] = a.Answers
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// permissiveRequirements lets a route certify on the few labels the world
// has, so the test sees given answers as well as abstentions.
func permissiveRequirements(t *testing.T, w *world) {
	t.Helper()
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 4, "target_accuracy": 0.6, "min_coverage": 0.3, "z": 1.2, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
}

// Every message of the layer-1 population gets one route answer for every
// question (an abstention included, so the app escalates), an uncertified
// question abstains not_certified, and two passes over the same inputs
// write byte-identical route answers and equal route reports.
func TestRouteAnswersAreWrittenAndDeterministic(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	rep := w.run()
	if rep.Routed != len(w.ids) || rep.Routed != rep.Answered {
		t.Fatalf("routed %d, answered %d of %d (the same population)", rep.Routed, rep.Answered, len(w.ids))
	}
	if rep.Given == 0 {
		t.Fatalf("no route gave an answer: %+v", rep.Routes)
	}
	given := 0
	for _, r := range rep.Routes {
		for _, n := range r.Given {
			given += n
		}
	}
	if given != rep.Given {
		t.Fatalf("the per-question counts sum to %d, the report says %d", given, rep.Given)
	}
	var category *RouteReport
	for i := range rep.Routes {
		if rep.Routes[i].Question == "category" {
			category = &rep.Routes[i]
		}
	}
	if category == nil || !category.Certification.Certified || category.Certification.Eligible == 0 || category.Given[route.Layer1]+category.Given[route.LayerSubQ]+category.Given[route.LayerKey] == 0 {
		t.Fatalf("category %+v", category)
	}
	firstRaw := w.rawRouteAnswers(t)
	first := w.routeAnswersByID(t)
	if len(first) != len(w.ids) {
		t.Fatalf("%d route rows for %d messages", len(first), len(w.ids))
	}
	for id, answers := range first {
		if len(answers) != len(w.p.Questions) {
			t.Fatalf("message %d has %d route answers, want one per question (%d)", id, len(answers), len(w.p.Questions))
		}
		for _, q := range w.p.Questions {
			a, ok := answers[q.Name]
			if !ok {
				t.Fatalf("message %d has no answer to %s", id, q.Name)
			}
			if a.Type != q.Type {
				t.Fatalf("message %d %s: answer type %s", id, q.Name, a.Type)
			}
			if a.Abstain != "" && (a.Choice != "" || a.Value != 0 || a.Confidence != nil || a.Layer != "") {
				t.Fatalf("message %d %s: an abstention carries a value: %+v", id, q.Name, a)
			}
			if q.Name == "urgency" && a.Abstain != route.AbstainNotCertified {
				t.Fatalf("message %d urgency has no labels: %+v", id, a)
			}
		}
	}
	again := w.run()
	if !reflect.DeepEqual(again.Routes, rep.Routes) {
		t.Fatalf("the same inputs changed the route report:\n%+v\n%+v", rep.Routes, again.Routes)
	}
	if second := w.routeAnswersByID(t); !reflect.DeepEqual(first, second) {
		t.Fatalf("the same inputs changed the route answers:\n%+v\n%+v", first, second)
	}
	if second := w.rawRouteAnswers(t); !reflect.DeepEqual(firstRaw, second) {
		t.Fatalf("the same inputs changed the stored route answer bytes:\n%+v\n%+v", firstRaw, second)
	}
}

// The certification part is tested against the requirement's
// certify_sources only: a route whose certification labels come from
// another evaluation source has no eligible item and cannot certify.
func TestRouteCertifiesOnlyAgainstTheConfiguredSources(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 4, "target_accuracy": 0.6, "min_coverage": 0.3, "z": 1.2, "family_wise": false,
		"certify_sources": ["manual"]}}`); err != nil {
		t.Fatal(err)
	}
	rep := w.run()
	for _, r := range rep.Routes {
		if r.Question != "category" {
			continue
		}
		if r.Certification.Certified || r.Certification.Eligible != 0 {
			t.Fatalf("judge labels certified a route that only accepts manual labels: %+v", r.Certification)
		}
		return
	}
	t.Fatal("category has no route report")
}

// A stored SubQ answer that refused, abstained or did not survive
// validation offers nothing; a valid one offers the stored distribution
// bit for bit (the read's float32 form, never re-decided), and a pass
// spends no paid call.
func TestRouteOffersUseTheStoredAnswersOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	ctx := t.Context()
	if err := w.st.AddUsage(ctx, engine.UsageRecord{Provider: "subq", Model: "snap", Usage: engine.Usage{Calls: 1, EstimatedUSD: 0.5}}); err != nil {
		t.Fatal(err)
	}
	before, err := w.st.ListUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.run()
	after, err := w.st.ListUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a layer-1 pass spent a paid call: before %+v, after %+v", before, after)
	}

	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := store.TriageKey{Fingerprint: w.p.Fingerprint, Decider: plan.DeciderKey}
	// one refused, one abstained and one invalid answer for category
	bad := []map[string]engine.Answer{
		{"category": {Type: engine.Choice, Refused: true}},
		{"category": {Type: engine.Choice, Abstain: engine.AbstainBelowTarget, Probabilities: map[string]float64{"finance": 0.9, "work": 0.1}}},
		{"category": {Type: engine.Choice, Probabilities: map[string]float64{"no-such-option": 1}}},
	}
	for i, answers := range bad {
		raw, err := json.Marshal(answers)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.st.SaveDecision(ctx, key, store.Decision{MessageID: w.ids[i], Answers: string(raw), RunID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := Train(ctx, plan, &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := w.p.Question("category")
	s := &settled{q: cq, model: tr.Models["category"]}
	for i := range bad {
		for _, o := range routeOffers(plan, tr, s, w.ids[i]) {
			if o.Layer == route.LayerSubQ {
				t.Fatalf("message %d: a refused/abstained/invalid stored answer offered %v", w.ids[i], o.Probs)
			}
		}
	}
	id := w.ids[len(bad)]
	offers := routeOffers(plan, tr, s, id)
	var subq *route.Offer
	for i := range offers {
		if offers[i].Layer == route.LayerSubQ {
			subq = &offers[i]
		}
	}
	if subq == nil {
		t.Fatal("a valid stored answer gave no SubQ offer")
	}
	stored, err := w.st.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var answers map[string]engine.Answer
	if err := json.Unmarshal([]byte(stored[id].Answers), &answers); err != nil {
		t.Fatal(err)
	}
	want := answers["category"].Probabilities
	if len(subq.Probs) != len(cq.Options) {
		t.Fatalf("%d probabilities for %d options", len(subq.Probs), len(cq.Options))
	}
	for k, opt := range cq.Options {
		if subq.Probs[k] != float64(float32(want[opt.Name])) {
			t.Fatalf("%s: offer %v, stored %v (bit for bit)", opt.Name, subq.Probs[k], want[opt.Name])
		}
	}
}

// countingInputs counts the decider reads a pass makes.
type countingInputs struct {
	*StoreInputs
	calls map[string]int
}

func (c *countingInputs) Decisions(ctx context.Context, key store.TriageKey, fn func(int64, map[string]engine.Answer) error) error {
	c.calls[key.Decider]++
	return c.StoreInputs.Decisions(ctx, key, fn)
}

// The route's SubQ read is the configured decider's, and a teacher source
// with the same decider key reuses it: one read, one validation.
func TestTheRouteDeciderIsReadOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 20, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	in := &countingInputs{StoreInputs: &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}},
		calls: map[string]int{}}
	if _, err := Train(t.Context(), plan, in, nil); err != nil {
		t.Fatal(err)
	}
	if in.calls[plan.DeciderKey] != 1 {
		t.Fatalf("the configured decider was read %d times, want one (the route and its teacher source)", in.calls[plan.DeciderKey])
	}
}

// Measuring the rules is not a teacher source: a plan without one has the
// same teacher targets, and the rules are measured anyway (the route's
// key participant needs them).
func TestRuleMeasurementLeavesTeacherTargetsUnchanged(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	ctx := t.Context()
	base := &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}
	withRules, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Train(ctx, withRules, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The question-level targets: message → target and weight.
	targets := func(tr *Trained) map[string]map[int64]string {
		out := map[string]map[int64]string{}
		for name, qs := range tr.sets {
			out[name] = map[int64]string{}
			for _, e := range qs.train {
				raw, _ := json.Marshal(e.Target)
				out[name][tr.Design.IDs[e.Row]] = string(raw) + "\x00" + strconv.FormatFloat(e.Weight, 'g', -1, 64)
			}
		}
		return out
	}
	if err := writeTeachers(w.cfg, `{"sources": {"rules": null}}`); err != nil {
		t.Fatal(err)
	}
	withoutRules, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Train(ctx, withoutRules, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(targets(a), targets(b)) {
		t.Fatal("removing the rules teacher source changed the teacher targets")
	}
	if len(b.g.rules) == 0 || len(b.g.fires) == 0 {
		t.Fatalf("the rules were not measured without a rules teacher: %d rules, %d firing messages", len(b.g.rules), len(b.g.fires))
	}
}

// A cancelled pass promotes nothing and rewrites no route answer: the
// previous rows stay.
func TestACanceledRunWritesNoRoutes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 40, nil)
	w.run()
	// The grid is a route-key input: changing it moves the key at once, so
	// the previous rows are read under the key they were written with.
	key := w.routeKey(t)
	before := w.routeAnswersAt(t, key)
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil { // new inputs: a real run
		t.Fatal(err)
	}
	if next := w.routeKey(t); next == key {
		t.Fatal("the grid change did not move the route key")
	}
	ctx, cancel := context.WithCancel(t.Context())
	rep, err := w.r.Run(ctx, func(jobs.Progress) { cancel() })
	if err == nil || rep.State != runstate.Cancelled {
		t.Fatalf("%+v %v", rep, err)
	}
	if after := w.routeAnswersAt(t, key); !reflect.DeepEqual(before, after) {
		t.Fatal("a cancelled run changed the route answers of the previous key")
	}
	if after := w.routeAnswersAt(t, w.routeKey(t)); len(after) != 0 {
		t.Fatalf("a cancelled run wrote %d route answers under the new key", len(after))
	}
}

// The route's population is the layer-1 key's: a message with no model row
// (or not a design row) stays pending and gets no route answer.
func TestRouteAnswererLeavesPendingMessagesPending(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 20, nil)
	ctx := t.Context()
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Train(ctx, plan, &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := w.p.Question("category")
	o := &outcome{questions: []*settled{{q: cq, model: tr.Models["category"]}}}
	if o.routes, err = routeStates(ctx, plan, tr, o.questions); err != nil {
		t.Fatal(err)
	}
	rep := Report{Routes: routeReports(plan, o.routes)}
	answer := func(s *settled, id int64) ([]float64, bool) { return tr.predictWith(s.model, id) }
	offers := func(s *settled, id int64, _ engine.Answer, _ bool) []route.Offer { return routeOffers(plan, tr, s, id) }
	routeAnswer := routeAnswerer(o, offers, &rep)
	if raw, err := routeAnswer(999999, nil); err != nil || raw != "" {
		t.Fatalf("a message with no row: %q %v", raw, err)
	}
	probs, ok := answer(o.questions[0], w.ids[0])
	if !ok {
		t.Fatal("the fixture's first message has no answer")
	}
	raw, err := routeAnswer(w.ids[0], map[string]engine.Answer{
		"category": selective.Answer(cq, probs, plan.Profile.Cutoff("category"), tr.Models["category"].Threshold),
	})
	if err != nil || raw == "" {
		t.Fatalf("a message with a model row: %q %v", raw, err)
	}
	var answers map[string]route.Answer
	if err := json.Unmarshal([]byte(raw), &answers); err != nil || answers["category"].Type != cq.Type {
		t.Fatalf("route answer: %v %v", answers, err)
	}
	// No settled model: the message stays pending even if it has a row.
	pending := &outcome{questions: []*settled{{q: cq}}}
	if pending.routes, err = routeStates(ctx, plan, tr, pending.questions); err != nil {
		t.Fatal(err)
	}
	pendingReport := Report{Routes: routeReports(plan, pending.routes)}
	if raw, err := routeAnswerer(pending, offers, &pendingReport)(w.ids[0], nil); err != nil || raw != "" {
		t.Fatalf("a message no model answers: %q %v", raw, err)
	}
}
