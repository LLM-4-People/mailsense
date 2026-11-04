package search

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// blockingCoverage counts whole-mailbox eligible scans and holds the first one
// open so a second caller can be observed waiting for it.
type blockingCoverage struct {
	scans   atomic.Int32
	block   chan struct{}
	started chan struct{}
}

func (c *blockingCoverage) Eligible(context.Context) ([]int64, error) {
	c.scans.Add(1)
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.block
	return nil, nil
}

// TestCoverageRebuildIsSingleFlight: after one mail write, two searches that
// both find the eligible set stale must not scan the mailbox twice. The second
// waits for the first install and reads the set it produced.
func TestCoverageRebuildIsSingleFlight(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	cov := &blockingCoverage{block: make(chan struct{}), started: make(chan struct{}, 1)}
	w.s.cov = cov
	w.s.mu.Lock()
	w.s.cache = coverageCache{}
	w.s.mu.Unlock()

	done := make(chan struct{}, 2)
	go func() { _, _ = w.s.coverage(context.Background()); done <- struct{}{} }()
	select {
	case <-cov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first eligible scan never started")
	}
	go func() { _, _ = w.s.coverage(context.Background()); done <- struct{}{} }()
	select {
	case <-done:
		t.Fatal("a second search returned before the first scan finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(cov.block)
	for range 2 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a coverage read never returned")
		}
	}
	if got := cov.scans.Load(); got != 1 {
		t.Fatalf("the eligible set was scanned %d times, want 1", got)
	}
}
