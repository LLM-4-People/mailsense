package learning_test

import (
	"context"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

// countStage is a learning stage that counts how often Status runs.
type countStage struct {
	id string
	mu sync.Mutex
	st learning.StageStatus
	n  int
}

func (c *countStage) ID() string { return c.id }

func (c *countStage) Status(context.Context) (learning.StageStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.st, nil
}

func (c *countStage) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

type openLatch struct{}

func (openLatch) LearnedScope(context.Context) (string, error)  { return "", nil }
func (openLatch) SetLearnedScope(context.Context, string) error { return nil }

// The production wiring asks the embeddings stage directly and through the
// layer-1 stage and the route's layer-1 stage (three paths). One Status
// build must plan the index coverage once: every stage answers once.
//
// Shape: the real layer1.Stage/RouteStage/LabelsStage constructors wrap a
// counting fake index stage. The fake reports Pending, so each real stage
// stops at its "after" check before reading its (empty) runner; the test
// exercises the learning machine's stage fan-out, not training.
func TestComputeStatusAsksEachStageOnce(t *testing.T) {
	idx := &countStage{id: learning.StageEmbeddings, st: learning.StageStatus{State: learning.Pending}}
	sync := &countStage{id: learning.StageSync, st: learning.StageStatus{State: learning.Pending}}
	// A value runner, not a nil pointer: the stage reads the runner's live
	// progress before its "after" check (a running run is the truth even
	// when its inputs are pending), and a zero-value runner has none.
	runner := &layer1.Runner{}
	build := func() *learning.Machine {
		m, err := learning.New(func() string { return "store-1" }, openLatch{}, nil, nil,
			idx,
			runner.LabelsStage(sync),
			runner.Stage(idx),
			runner.RouteStage(runner.Stage(idx)),
		)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	st, err := build().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Stages) != 4 {
		t.Fatalf("status has %d stages, want 4: %+v", len(st.Stages), st)
	}
	if got := idx.calls(); got != 1 {
		t.Fatalf("one status build asked the index stage %d times, want 1", got)
	}
	// No cross-build cache: a second machine (a new build) asks again.
	if _, err := build().Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := idx.calls(); got != 2 {
		t.Fatalf("a second build left the index stage at %d calls, want 2", got)
	}
}
