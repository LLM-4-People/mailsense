package triage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// fakeRules serves deterministic rule answers by row, exactly as the
// evidence engine does in the app.
type fakeRules struct {
	answers map[int64]map[string]engine.Answer
	calls   int
}

func (f *fakeRules) RuleAnswers(_ context.Context, ids []int64) (map[int64]map[string]engine.Answer, error) {
	f.calls++
	out := map[int64]map[string]engine.Answer{}
	for _, id := range ids {
		if a, ok := f.answers[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func automaticChoice(q *engine.Question, option, rule string) engine.Answer {
	probs := map[string]float64{}
	for _, o := range q.Options {
		probs[o.Name] = 0
	}
	probs[option] = 1
	return engine.Answer{Type: q.Type, Choice: option, Probabilities: probs, AutoRule: rule}
}

// A rule fills only the uncertified route abstention. A below-coverage
// abstention and a certified answer keep the route's word, and the rules
// never touch coverage or the missing list.
func TestRuleAnswersFillOnlyUncertifiedAbstentions(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	uncertified := w.mail("a@client.com", "A", "Budget", 0, 0)
	covered := w.mail("b@client.com", "B", "Budget", time.Hour, 0)
	certified := w.mail("c@client.com", "C", "Budget", 2*time.Hour, 0)
	noRecord := w.mail("d@client.com", "D", "Budget", 3*time.Hour, 0)

	fr := &fakeRoute{records: map[int64]store.RouteRecord{
		uncertified: {MessageID: uncertified, Revision: w.revision(t, uncertified), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}}}},
		covered: {MessageID: covered, Revision: w.revision(t, covered), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainBelowCoverage}}}},
		certified: {MessageID: certified, Revision: w.revision(t, certified), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Choice: "finance",
				Confidence: confidence(0.9), Probabilities: map[string]float64{"finance": 0.9, "work": 0.1}}}}},
	}}
	w.useRoute(t, fr)
	rules := &fakeRules{answers: map[int64]map[string]engine.Answer{}}
	for _, id := range []int64{uncertified, covered, certified, noRecord} {
		rules.answers[id] = map[string]engine.Answer{"category": automaticChoice(cq, "work", "cat_work")}
	}
	w.tr.UseRuleAnswers(rules)

	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rules.calls == 0 {
		t.Fatal("the rule reader was never asked")
	}
	got := v.Results[uncertified]
	if value := got.Values["category"]; value.Choice != "work" || value.Rule != "cat_work" || value.Abstain != "" {
		t.Fatalf("uncertified answer = %+v, want the rule's work answer with provenance", value)
	}
	if value := got.Values["category"]; value.Number != 0 || value.Confidence != nil {
		t.Fatalf("the rule answer was blended or given a model confidence: %+v", value)
	}
	if value := v.Results[covered].Values["category"]; value.Abstain != route.AbstainBelowCoverage || value.Rule != "" {
		t.Fatalf("below-coverage answer was replaced: %+v", value)
	}
	if value := v.Results[certified].Values["category"]; value.Choice != "finance" || value.Rule != "" {
		t.Fatalf("certified answer was replaced: %+v", value)
	}
	if value, ok := v.Results[noRecord].Values["category"]; ok && value.Rule != "" {
		t.Fatalf("a message without a route record gained a rule answer: %+v", value)
	}
}

// The explicit test preview wins over the rule fallback: a preview answer
// is a model best guess the owner asked to see, and the rule must not
// replace it.
func TestPreviewWinsOverRuleAnswers(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	fr := &fakeRoute{
		records: map[int64]store.RouteRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]route.Answer{"category": {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}}},
		}},
		previews: map[int64]store.PreviewRecord{id: {
			MessageID: id, Revision: w.revision(t, id), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]engine.Answer{"category": givenChoice(cq, "finance")}}},
	}
	w.useRoute(t, fr)
	w.tr.UseRuleAnswers(&fakeRules{answers: map[int64]map[string]engine.Answer{
		id: {"category": automaticChoice(cq, "work", "cat_work")},
	}})
	w.tr.gate = previewingGate{}
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if value := v.Results[id].Values["category"]; value.Choice != "finance" || value.Rule != "" {
		t.Fatalf("the rule replaced the preview: %+v", value)
	}
}

// The page keeps the route's certified counts: a rule answer is displayed
// but counted apart, so it cannot inflate answered or coverage.
func TestRuleAnswersPageCountsSeparately(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	body, err := json.Marshal(map[string]route.Answer{
		"category": {Answer: engine.Answer{Type: engine.Choice, Abstain: route.AbstainNotCertified}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginLayer1Run(t.Context(), "inputs")
	if err != nil {
		t.Fatal(err)
	}
	err = w.st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: p.Fingerprint, Decider: "layer1"},
		RouteKey:  testRouteKey,
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes: func(fn func(store.RouteAnswer) error) error {
			return fn(store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), RunID: run, Answers: string(body)})
		}})
	if err != nil {
		t.Fatal(err)
	}
	w.useRoute(t, w.st)
	// The rule reader is wired before the first read, exactly as main does.
	w.tr.UseRuleAnswers(&fakeRules{answers: map[int64]map[string]engine.Answer{
		id: {"category": automaticChoice(cq, "work", "cat_work")},
	}})
	page, err := w.tr.Page(t.Context(), PageQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	row := page.Results[id]
	if row.Answered != 0 || row.Abstained != 0 || row.Automatic != 1 || row.FirstValue.Rule != "cat_work" {
		t.Fatalf("automatic page row = %+v", row)
	}
	// The route record carries one question, so the row stays partial; the
	// rule answer changes neither the eligible nor the partial count.
	if summary.Coverage.Eligible != 1 || summary.Coverage.Partial != 1 || summary.Total != 1 {
		t.Fatalf("the rule answers changed the route coverage: %+v", summary.Coverage)
	}
}
