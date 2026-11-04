package feedback

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// mixedTeachingLabels is one sender's manual label, judge reference label
// and judge production answer. The last two must never reach a prompt, a
// prior or the evaluation count.
func mixedTeachingLabels() []Label {
	manual := at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "a@x.com", "Budget 2026", ""), 1)
	reference := at(label(2, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "a@x.com", "Budget 2026", ""), 2)
	reference.Source = store.LabelJudge
	answer := at(label(3, "category", engine.Choice, taxonomy.Label{Choice: "action"}, "a@x.com", "Budget 2026", ""), 3)
	answer.Source = store.LabelJudgeAnswer
	return []Label{manual, reference, answer}
}

func TestSourcePolicyKeepsIndependentLabels(t *testing.T) {
	ls := mixedTeachingLabels()
	for name, filter := range map[string]func([]Label) []Label{"Teaching": Teaching, "Reference": Reference} {
		got := filter(ls)
		if len(got) != 2 || got[0].Source != store.LabelManual || got[1].Source != store.LabelJudge {
			t.Fatalf("%s must keep manual and judge, never the production answer: %+v", name, got)
		}
		if got[0].Value.Choice != "work" || got[1].Value.Choice != "spam" {
			t.Fatalf("%s changed the kept labels: %+v", name, got)
		}
	}
	counts := SourceCounts(ls)
	if len(counts) != 3 || counts[store.LabelManual] != 1 || counts[store.LabelJudge] != 1 || counts[store.LabelJudgeAnswer] != 1 {
		t.Fatalf("per-source counts %v", counts)
	}
	if got := SourceCounts(nil); len(got) != 3 || got[store.LabelManual] != 0 || got[store.LabelJudge] != 0 || got[store.LabelJudgeAnswer] != 0 {
		t.Fatalf("an empty deployment reports the three sources at zero: %v", got)
	}
}

// A judge answer is the model's own production answer. Before the source
// policy it was selected as an example and counted as a prior, so the model
// taught itself. The judge's independent reference labels stay usable.
func TestJudgeAnswerLabelsNeverBecomeExamplesOrPriors(t *testing.T) {
	ls := mixedTeachingLabels()
	// The per-message selectors take the boundary-filtered set (the triage
	// core calls Teaching once per load); the policy itself is pinned above.
	got := SelectExamples(Teaching(ls), 9, "a@x.com", "Budget 2026", day.Add(10*time.Hour), 10, mail.NewDomains(nil))
	if len(got) != 2 {
		t.Fatalf("examples must be manual and judge, never the production answer: %+v", got)
	}
	sources := map[string]bool{}
	for _, l := range got {
		sources[l.Source] = true
	}
	if !sources[store.LabelManual] || !sources[store.LabelJudge] || sources[store.LabelJudgeAnswer] {
		t.Fatalf("example sources %v", sources)
	}

	p := profile(t)
	prior := []Label{
		at(label(1, "suspicious", engine.Noul, taxonomy.Label{Number: 1}, "a@evil.com", "", ""), 1),
		at(label(2, "suspicious", engine.Noul, taxonomy.Label{Number: 1}, "a@evil.com", "", ""), 2),
		at(label(3, "suspicious", engine.Noul, taxonomy.Label{Number: 0}, "a@evil.com", "", ""), 3),
	}
	prior[1].Source = store.LabelJudge
	prior[2].Source = store.LabelJudgeAnswer
	pr, ok := SuspiciousPrior(Teaching(prior), p, "a@evil.com", 99, day.Add(10*time.Hour), 1, 2, mail.NewDomains(nil))
	if !ok || pr.Samples != 2 || pr.Rate != 1 {
		t.Fatalf("the prior must count the manual and judge labels: ok=%v %+v", ok, pr)
	}
}

func TestEvaluateCountsIndependentLabels(t *testing.T) {
	p := profile(t)
	ls := mixedTeachingLabels()
	judged := Judged{
		1: {"category": {Type: engine.Choice, Choice: "work"}}, // the manual label, right
		2: {"category": {Type: engine.Choice, Choice: "work"}}, // judge reference, wrong
		3: {"category": {Type: engine.Choice, Choice: "spam"}}, // judge answer, wrong
	}
	ev := Evaluate(judged, ls, nil, p)
	if len(ev.Manual) != 1 || ev.Manual[0].Question != "category" || ev.Manual[0].Count != 2 || ev.Manual[0].Metrics["accuracy"] != 0.5 {
		t.Fatalf("the evaluation must score manual and judge, never the production answer: %+v", ev.Manual)
	}
	if len(ev.Sources) != 3 || ev.Sources[store.LabelManual] != 1 || ev.Sources[store.LabelJudge] != 1 || ev.Sources[store.LabelJudgeAnswer] != 1 {
		t.Fatalf("per-source counts %v", ev.Sources)
	}
}

func TestCalibrateIgnoresJudgeAnswer(t *testing.T) {
	p := profile(t)
	judged := Judged{}
	var manual []Label
	for i, c := range []struct {
		v   float64
		yes bool
	}{{0.9, true}, {0.7, true}, {0.6, false}, {0.3, false}} {
		id := int64(i + 1)
		judged[id] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Number: c.v}}
		manual = append(manual, label(id, "reply_needed", engine.Noul, taxonomy.Label{Number: map[bool]float64{true: 1}[c.yes]}, "", "", ""))
	}
	// The production answer agrees with none of the owner's answers and
	// pushes the sweep to a threshold the manual labels do not support.
	mixed := append([]Label{}, manual...)
	for i, c := range []struct {
		v   float64
		yes bool
	}{{0.99, false}, {0.55, true}} {
		id := int64(10 + i)
		judged[id] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Number: c.v}}
		l := label(id, "reply_needed", engine.Noul, taxonomy.Label{Number: map[bool]float64{true: 1}[c.yes]}, "", "", "")
		l.Source = store.LabelJudgeAnswer
		mixed = append(mixed, l)
	}
	s, ok := suggestion(Calibrate(judged, mixed, p, 0.05, 3), "reply_needed")
	if !ok || s.Suggested != 0.65 || s.Labels != 4 || s.F1 != 1 {
		t.Fatalf("a judge answer moved the threshold: %+v (found %v)", s, ok)
	}

	// A manual label still calibrates: a "no" at 0.65 pushes the cut to 0.7.
	judged[20] = map[string]taxonomy.Value{"reply_needed": {Type: engine.Noul, Number: 0.65}}
	manual = append(manual, label(20, "reply_needed", engine.Noul, taxonomy.Label{Number: 0}, "", "", ""))
	s, ok = suggestion(Calibrate(judged, manual, p, 0.05, 3), "reply_needed")
	if !ok || s.Suggested != 0.7 || s.Labels != 5 || s.F1 != 1 {
		t.Fatalf("a manual label must still calibrate: %+v (found %v)", s, ok)
	}
}

func suggestion(list []Suggestion, question string) (Suggestion, bool) {
	for _, s := range list {
		if s.Question == question {
			return s, true
		}
	}
	return Suggestion{}, false
}
