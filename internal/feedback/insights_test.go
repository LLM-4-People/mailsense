package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// An abstained suspicious value is a non-answer: it is never a suspect, even
// when it carries a number above the cutoff. Before the fix, !v.Refused let
// such a value name a known domain as spoofed or compromised.
func TestImpersonationIgnoresAbstainedSuspicion(t *testing.T) {
	p := profile(t)
	a := analysis(t)
	known := map[string]bool{"client.com": true}
	judged := func(v taxonomy.Value) Judged {
		j := Judged{}
		for i := range a.Messages {
			j[a.Messages[i].RowID] = map[string]taxonomy.Value{"suspicious": v}
		}
		return j
	}
	for _, reason := range []string{engine.AbstainBelowTarget, "not_certified"} {
		if got := impersonation(a, judged(taxonomy.Value{Type: engine.Noul, Number: 1, Abstain: reason}), p, known); len(got) != 0 {
			t.Fatalf("%q abstention is not a suspect: %+v", reason, got)
		}
	}
	got := impersonation(a, judged(taxonomy.Value{Type: engine.Noul, Number: 1}), p, known)
	if len(got) != 6 || got[0].Domain != "client.com" || got[0].Value != 1 {
		t.Fatalf("an answered suspicious value from a known domain is a suspect: %+v", got)
	}
	if got := impersonation(a, judged(taxonomy.Value{Type: engine.Noul, Number: 1}), p, nil); len(got) != 0 {
		t.Fatalf("an unknown domain is not a suspect: %+v", got)
	}
}
