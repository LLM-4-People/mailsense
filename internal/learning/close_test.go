package learning

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

// statusClock is the fixed clock of the close tests.
func statusClock() *clocktest.Fake {
	return clocktest.New(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
}

// Close joins the detached status compute: a stage read held open keeps Close
// waiting, and the release joins it. The status installed before Close stays
// readable, and a second Close is idempotent.
func TestCloseJoinsTheDetachedStatusCompute(t *testing.T) {
	clk := statusClock()
	a := &fakeStage{id: "a", st: StageStatus{State: Running, Progress: Progress{Done: 1, Total: 100}}}
	m, err := New(func() string { return "scope" }, &memLatch{}, nil, clk, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}

	release, entered := make(chan struct{}), make(chan struct{}, 1)
	a.setBlocker(release, entered)
	m.Invalidate()
	// The stale read starts the detached compute and returns immediately.
	served, err := m.Status(t.Context())
	if err != nil || served.State != Learning {
		t.Fatalf("stale status %+v %v", served, err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the detached compute never started")
	}

	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- m.Close(ctx)
	}()
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) with the status compute still running", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the status compute")
	}
	if !m.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// A compute that finishes after Close never installs as fresh: the revision
// Close moved makes the late result unusable, so a later read recomputes
// instead of trusting it.
func TestALateComputeNeverInstallsAsFresh(t *testing.T) {
	a := &fakeStage{id: "a", st: StageStatus{State: Running, Progress: Progress{Done: 1, Total: 100}}}
	m, err := New(func() string { return "scope" }, &memLatch{}, nil, statusClock(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	a.setBlocker(release, entered)
	m.Invalidate()
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the detached compute never started")
	}

	// The compute is blocked in the stage while Close runs and returns; its
	// join is bounded, so the compute outlives it here on purpose: the test
	// wants the "finished after Close" case, not the normal one.
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if m.Close(expired) {
		t.Fatal("Close joined a compute still inside the stage")
	}
	before := m.last

	// The stage now reports a changed status, and the released compute
	// returns it under the pre-Close revision.
	a.set(StageStatus{State: Done, Progress: Progress{Done: 100, Total: 100}})
	a.setBlocker(nil, nil)
	close(release)
	waitUntil(t, "the late compute to finish", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.refreshing
	})

	m.mu.Lock()
	if m.fresh {
		m.mu.Unlock()
		t.Fatal("a compute that finished after Close installed itself as fresh")
	}
	if changed(m.last, before) {
		m.mu.Unlock()
		t.Fatalf("the late compute replaced the status: %+v -> %+v", before, m.last)
	}
	m.mu.Unlock()

	// After Close, a read serves the retained status and starts no detached
	// compute: shutdown owns no goroutine any more. The synchronous path
	// (the caller's own work) still recomputes.
	served, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if served.State != Learning {
		t.Fatalf("a post-Close read recomputed: %+v", served)
	}
	computed, err := m.computeStatus(t.Context(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	if computed.State != Learned {
		t.Fatalf("the synchronous path did not recompute: %+v", computed)
	}
	if !m.Close(t.Context()) {
		t.Fatal("the later Close did not observe the join")
	}
}

// waitUntil polls a condition on the scheduler.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		runtime.Gosched()
	}
}
