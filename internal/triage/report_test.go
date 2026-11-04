package triage

import "testing"

// Drained is the gate between the teacher backlog and a layer-1 retrain: a
// batch smaller than the backlog is not a reason to start a five-minute
// training pass.
func TestReportDrained(t *testing.T) {
	cases := []struct {
		name string
		rep  Report
		want bool
	}{
		{"no plan", Report{}, true},
		{"backlog remains", Report{Plan: &Plan{Pending: 5000, Selected: 500}, Decided: 500}, false},
		{"last batch", Report{Plan: &Plan{Pending: 500, Selected: 500}, Decided: 480, Partial: 20}, true},
		{"a failure leaves work", Report{Plan: &Plan{Pending: 500, Selected: 500}, Decided: 480, Partial: 10}, false},
	}
	for _, c := range cases {
		if got := c.rep.Drained(); got != c.want {
			t.Errorf("%s: Drained() %v, want %v", c.name, got, c.want)
		}
	}
}
