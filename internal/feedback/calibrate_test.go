package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// An abstained judged value is a non-answer, not a "no" at Number 0: it is
// not a sweep pair. Before the fix, !v.Refused admitted it to Labels and a
// below_target/not_certified value changed what the labels were said to
// support.
func TestCalibrateIgnoresAbstainedValues(t *testing.T) {
	p := profile(t)
	ls := []Label{
		label(1, "reply_needed", engine.Noul, taxonomy.Label{Number: 1}, "", "", ""),
		label(2, "reply_needed", engine.Noul, taxonomy.Label{Number: 1}, "", "", ""),
		label(3, "reply_needed", engine.Noul, taxonomy.Label{Number: 0}, "", "", ""),
		label(4, "reply_needed", engine.Noul, taxonomy.Label{Number: 0}, "", "", ""),
	}
	judged := func(extra *taxonomy.Value) Judged {
		j := Judged{
			1: {"reply_needed": {Type: engine.Noul, Number: 0.9}},
			2: {"reply_needed": {Type: engine.Noul, Number: 0.7}},
			3: {"reply_needed": {Type: engine.Noul, Number: 0.6}},
		}
		if extra != nil {
			j[4] = map[string]taxonomy.Value{"reply_needed": *extra}
		}
		return j
	}
	ref, ok := suggestion(Calibrate(judged(nil), ls, p, 0.05, 3), "reply_needed")
	if !ok || ref.Suggested != 0.65 || ref.Labels != 3 {
		t.Fatalf("baseline: %+v (found %v)", ref, ok)
	}
	for _, reason := range []string{engine.AbstainBelowTarget, "not_certified"} {
		got, ok := suggestion(Calibrate(judged(&taxonomy.Value{Type: engine.Noul, Number: 0, Abstain: reason}), ls, p, 0.05, 3), "reply_needed")
		if !ok || got.Suggested != ref.Suggested || got.Labels != ref.Labels {
			t.Fatalf("%q abstention moved the sweep: %+v, want %+v", reason, got, ref)
		}
	}
	got, ok := suggestion(Calibrate(judged(&taxonomy.Value{Type: engine.Noul, Number: 0}), ls, p, 0.05, 3), "reply_needed")
	if !ok || got.Labels != 4 {
		t.Fatalf("an answered value is a sweep pair: %+v (found %v)", got, ok)
	}
}
