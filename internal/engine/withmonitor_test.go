package engine

import "testing"

// TestWithMonitorInstallsOneMonitor: every adapter factory must route usage
// through the same monitor. A caller-supplied monitor is kept; a missing one
// is created over the bus, sink and clock, and the sink is the monitor so no
// usage is recorded twice or lost.
func TestWithMonitorInstallsOneMonitor(t *testing.T) {
	base := Deps{}
	got := WithMonitor(base)
	if got.Monitor == nil {
		t.Fatal("no monitor was installed")
	}
	if got.Sink != got.Monitor {
		t.Fatalf("the usage sink is not the monitor: %T vs %T", got.Sink, got.Monitor)
	}
	if base.Monitor != nil || base.Sink != nil {
		t.Fatal("WithMonitor mutated the caller's Deps value")
	}

	supplied := NewMonitor(nil, nil, nil)
	kept := WithMonitor(Deps{Monitor: supplied})
	if kept.Monitor != supplied {
		t.Fatal("a supplied monitor was replaced")
	}
	if kept.Sink != supplied {
		t.Fatal("the supplied monitor is not the usage sink")
	}
}
