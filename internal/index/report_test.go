package index

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Committed is the trigger guard: a run that stopped before any successful
// vector write committed nothing, and a succeeded run that selected
// nothing did too; a stored or confirmed vector commits.
func TestReportCommitted(t *testing.T) {
	cases := []struct {
		name string
		rep  Report
		want bool
	}{
		{"zero", Report{}, false},
		{"failed unavailable before any write", Report{State: runstate.Failed, StoppedBy: StoppedUnavailable, Failed: 8, NotAttempted: 33834}, false},
		{"embedded 1", Report{State: runstate.Succeeded, Embedded: 1}, true},
		{"unchanged 1", Report{State: runstate.Succeeded, Unchanged: 1}, true},
		{"both", Report{State: runstate.Succeeded, Embedded: 2, Unchanged: 3}, true},
		{"stopped after embedding a batch", Report{State: runstate.Failed, StoppedBy: StoppedUnavailable, Embedded: 3}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rep.Committed(); got != c.want {
				t.Fatalf("Committed() = %v, want %v for %+v", got, c.want, c.rep)
			}
		})
	}
}
