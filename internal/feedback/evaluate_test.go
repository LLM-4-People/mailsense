package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// An abstained or refused value is a non-answer, not a zero prediction: it
// must not enter the confusion matrix as "no" and turn a correct label into
// a false negative.
func TestEvaluateSkipsAbstainedAndRefusedValues(t *testing.T) {
	p := profile(t)
	judged := Judged{
		1: {"reply_needed": {Type: engine.Noul, Number: 0.9, Model: 0.9}},            // answered
		2: {"reply_needed": {Type: engine.Noul, Abstain: engine.AbstainBelowTarget}}, // abstained
		3: {"reply_needed": {Type: engine.Noul, Abstain: route.AbstainNotCertified}}, // abstained
		4: {"reply_needed": {Type: engine.Noul, Refused: true}},                      // refused
	}
	var labels []Label
	for id := int64(1); id <= 4; id++ {
		labels = append(labels, label(id, "reply_needed", engine.Noul, taxonomy.Label{Number: 1}, "", "", ""))
	}
	ev := Evaluate(judged, labels, nil, p)
	if len(ev.Manual) != 1 {
		t.Fatalf("one question: %+v", ev.Manual)
	}
	got := ev.Manual[0]
	if got.Count != 1 || got.Metrics["accuracy"] != 1 || got.Metrics["precision"] != 1 ||
		got.Metrics["recall"] != 1 || got.Metrics["f1"] != 1 {
		t.Fatalf("only the answered value is a prediction: %+v", got)
	}
	if got.Confusion == nil || got.Confusion.Rows[1].Counts[0] != 0 || got.Confusion.Rows[1].Counts[1] != 1 {
		t.Fatalf("no abstention is a false negative: %+v", got.Confusion)
	}
	if ev.Sources[store.LabelManual] != 4 {
		t.Fatalf("the loaded labels are still counted: %v", ev.Sources)
	}
}

// The weak reply evaluation ranks and calibrates the model's own probability:
// an abstained or refused value has none and must not be scored as P(no).
func TestWeakReplySkipsAbstainedValues(t *testing.T) {
	p := profile(t)
	judged := Judged{
		1: {"reply_needed": {Type: engine.Noul, Model: 0.9}},                         // answered, replied
		2: {"reply_needed": {Type: engine.Noul, Model: 0.1}},                         // answered, no reply
		3: {"reply_needed": {Type: engine.Noul, Abstain: engine.AbstainBelowTarget}}, // abstained
		4: {"reply_needed": {Type: engine.Noul, Abstain: route.AbstainNotCertified}}, // abstained
	}
	weak := map[int64]bool{1: true, 2: false, 3: true, 4: true}
	got := Evaluate(judged, nil, weak, p).WeakReply
	if got == nil || got.Count != 2 || got.Metrics["accuracy"] != 1 || got.Metrics["auc"] != 1 || got.Metrics["brier"] != 0.01 {
		t.Fatalf("only the answered values are ranked and calibrated: %+v", got)
	}
}

// The trainer resolves one evaluation label per (message, question) by
// source precedence; the feedback reference must agree, or one message is
// counted twice and the judge can outvote the owner.
func TestReferenceResolvesOneLabelPerPair(t *testing.T) {
	p := profile(t)
	manual := at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "", "", ""), 1)
	judge := at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "", "", ""), 2)
	judge.Source = store.LabelJudge
	judged := Judged{1: {"category": {Type: engine.Choice, Choice: "work"}}}

	ev := Evaluate(judged, []Label{judge, manual}, nil, p)
	if len(ev.Manual) != 1 || ev.Manual[0].Count != 1 || ev.Manual[0].Metrics["accuracy"] != 1 {
		t.Fatalf("one pair, scored once, on the manual value: %+v", ev.Manual)
	}
	if ev.Sources[store.LabelManual] != 1 || ev.Sources[store.LabelJudge] != 1 {
		t.Fatalf("every loaded source is still reported: %v", ev.Sources)
	}
	for _, ls := range [][]Label{{judge, manual}, {manual, judge}} {
		if ref := Reference(ls); len(ref) != 1 || ref[0].Source != store.LabelManual || ref[0].Value.Choice != "work" {
			t.Fatalf("the manual label outranks the judge's in either load order: %+v", ref)
		}
	}
}

// Different messages and questions are distinct pairs: resolution keeps
// every label in input order.
func TestReferenceKeepsDistinctPairsAndOrder(t *testing.T) {
	first := at(label(2, "category", engine.Choice, taxonomy.Label{Choice: "spam"}, "", "", ""), 1)
	second := at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"}, "", "", ""), 2)
	third := at(label(1, "urgency", engine.Score, taxonomy.Label{Number: 2}, "", "", ""), 3)
	third.Source = store.LabelJudge
	got := Reference([]Label{first, second, third})
	want := []struct {
		id     int64
		q      string
		source string
		choice string
		number float64
	}{
		{2, "category", store.LabelManual, "spam", 0},
		{1, "category", store.LabelManual, "work", 0},
		{1, "urgency", store.LabelJudge, "", 2},
	}
	if len(got) != len(want) {
		t.Fatalf("distinct pairs are kept: %+v", got)
	}
	for i, w := range want {
		if got[i].MessageID != w.id || got[i].Question != w.q || got[i].Source != w.source ||
			got[i].Value.Choice != w.choice || got[i].Value.Number != w.number {
			t.Fatalf("label %d out of order or changed: %+v, want %+v", i, got[i], w)
		}
	}
}
