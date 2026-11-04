package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// An abstained judged value is a non-answer: it is neither in flaggedShare's
// denominator nor flagged. Before the fix, !v.Refused let it in and its
// unknown confidence counted it as flagged, diluting the share.
func TestFlaggedShareIgnoresAbstainedValues(t *testing.T) {
	p := profile(t)
	q, ok := p.RoleQuestion(taxonomy.RoleCategory)
	if !ok {
		t.Fatal("no category question")
	}
	conf := func(c float64) *float64 { return &c }
	judged := Judged{
		1: {"category": {Type: engine.Choice, Choice: "work", Confidence: conf(0.9)}},
		2: {"category": {Type: engine.Choice, Choice: "work", Confidence: conf(0.2)}},
		3: {"category": {Type: engine.Choice, Abstain: engine.AbstainBelowTarget}},
		4: {"category": {Type: engine.Choice, Abstain: "not_certified"}},
	}
	if got := flaggedShare(judged, p, q, 0.6); got != 0.5 {
		t.Fatalf("one of two answered values is flagged; abstentions are not: %v", got)
	}
	// An answered low-confidence value is counted; an answered confident one
	// stays below the threshold.
	judged[5] = map[string]taxonomy.Value{"category": {Type: engine.Choice, Choice: "work", Confidence: conf(0.1)}}
	if got := flaggedShare(judged, p, q, 0.6); got != 0.667 {
		t.Fatalf("two of three answered values are flagged: %v", got)
	}
}
