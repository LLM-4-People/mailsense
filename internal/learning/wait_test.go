package learning

import (
	"context"
	"errors"
	"testing"
)

// waitStage is a prerequisite with a fixed answer (the package's fakeStage
// carries channels and counters for the machine tests).
type waitStage struct {
	id     string
	status StageStatus
	err    error
}

func (w waitStage) ID() string { return w.id }
func (w waitStage) Status(context.Context) (StageStatus, error) {
	return w.status, w.err
}

// A stage waiting on its prerequisites gets the same pending answer from one
// owner, with the name of the stage it waits for.
func TestWaitForReportsTheBlockingStage(t *testing.T) {
	done := waitStage{id: "index", status: StageStatus{State: Done}}
	busy := waitStage{id: "evidence", status: StageStatus{State: Running}}
	if _, ready, err := WaitFor(t.Context(), done); err != nil || !ready {
		t.Fatalf("a done prerequisite: ready=%v err=%v", ready, err)
	}
	wait, ready, err := WaitFor(t.Context(), done, busy)
	if err != nil || ready {
		t.Fatalf("a running prerequisite: ready=%v err=%v", ready, err)
	}
	if wait.State != Pending || wait.Note != "waits for the evidence stage" {
		t.Fatalf("the wait answer: %+v", wait)
	}
	boom := errors.New("boom")
	if _, _, err := WaitFor(t.Context(), waitStage{id: "sync", err: boom}); !errors.Is(err, boom) {
		t.Fatalf("a failing prerequisite: %v", err)
	}
}
