package resource

import (
	"context"
	"testing"
)

// TestWaitAdmissionReportsEveryTransition: the one pacing loop hands the current
// ceiling to onChange on each poll and nil when the host recovers, so a job can
// publish exactly its pause and resume.
func TestWaitAdmissionReportsEveryTransition(t *testing.T) {
	g, sampler, clk := cpuOnly(t, 80)
	sampler.seq = []Usage{
		{CPU: reading(90), RAM: reading(10), Disk: reading(10)},
		{CPU: reading(90), RAM: reading(10), Disk: reading(10)},
		{CPU: reading(50), RAM: reading(10), Disk: reading(10)},
	}
	var seen []*LimitError
	paused := g.WaitAdmission(t.Context(), AdmitAll, func(p *LimitError) { seen = append(seen, p) })
	if paused != nil {
		t.Fatalf("WaitAdmission returned %+v, want a recovery", paused)
	}
	if len(seen) != 3 || seen[0] == nil || seen[1] == nil || seen[2] != nil {
		t.Fatalf("transitions = %+v, want paused, paused, resumed", seen)
	}
	if len(clk.Sleeps()) != 2 {
		t.Fatalf("sleeps = %v, want two intervals", clk.Sleeps())
	}
}

// TestWaitAdmissionRespectsTheAdmissionClass: a write-only admission is not
// stopped by a CPU ceiling, and still hears the resumed state.
func TestWaitAdmissionRespectsTheAdmissionClass(t *testing.T) {
	g, sampler, _ := cpuOnly(t, 80)
	sampler.set(Usage{CPU: reading(100), RAM: reading(10), Disk: reading(10)})
	calls := 0
	if paused := g.WaitAdmission(context.Background(), AdmitWrites, func(*LimitError) { calls++ }); paused != nil {
		t.Fatalf("AdmitWrites paused on CPU: %+v", paused)
	}
	if calls != 1 {
		t.Fatalf("onChange called %d times, want the one resumed report", calls)
	}
}
