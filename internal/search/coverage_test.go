package search

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
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

// TestCoverageCountIsCountedOncePerSequence pins the derived coverage count:
// repeated searches over an unmoved vector copy share one scan of the copy
// (the counter), and a vector write moves the copy's sequence so the next
// search recounts without rebuilding the eligible set (the mail generation
// did not move).
func TestCoverageCountIsCountedOncePerSequence(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("only", mail.RoleInbox, "Invoice", "Please pay the invoice.")
	// No index yet: the message is eligible and the copy is empty.
	res := w.search(Query{Text: "invoice"})
	if res.Semantic.Eligible != 1 || res.Semantic.Covered != 0 {
		t.Fatalf("cold coverage %+v", res.Semantic)
	}
	scans := w.s.coverageScans.Load()
	if scans != 1 {
		t.Fatalf("the first search counted the copy %d times, want 1", scans)
	}
	w.search(Query{Text: "invoice"})
	if got := w.s.coverageScans.Load(); got != scans {
		t.Fatalf("a repeated search at the same sequence recounted the copy (%d → %d)", scans, got)
	}

	// One vector row is written without touching the mail generation: the
	// copy's sequence moves and the count is recomputed, not served stale.
	w.index()
	res = w.search(Query{Text: "invoice"})
	if !res.Semantic.Used || res.Semantic.Covered != 1 || res.Semantic.Eligible != 1 {
		t.Fatalf("the vector write was not counted: %+v", res.Semantic)
	}
	if got := w.s.coverageScans.Load(); got != scans+1 {
		t.Fatalf("the sequence move did not recount the copy: %d → %d", scans, got)
	}
	w.search(Query{Text: "invoice"})
	if got := w.s.coverageScans.Load(); got != scans+1 {
		t.Fatalf("the recount was not reused: want %d, got %d", scans+1, got)
	}
}

// TestCoverageCountMatchesBruteForce pins the memoized count against an
// independent count of the eligible ids the copy holds: full coverage, then
// the stale window after a mail write (the new message is eligible without a
// vector), then the recovery when the indexer writes it.
func TestCoverageCountMatchesBruteForce(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	brute := func() (int, int) {
		ids, err := w.s.cov.Eligible(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		covered := 0
		for _, id := range ids {
			if w.s.vectors.Has(id) {
				covered++
			}
		}
		return covered, len(ids)
	}
	for i := range 2 {
		res := w.search(Query{Text: "invoice"})
		covered, eligible := brute()
		if res.Semantic.Covered != covered || res.Semantic.Eligible != eligible {
			t.Fatalf("search %d: coverage %d/%d, brute force %d/%d",
				i, res.Semantic.Covered, res.Semantic.Eligible, covered, eligible)
		}
	}

	w.mail("fourth", mail.RoleInbox, "Fourth invoice", "Another invoice.")
	res := w.search(Query{Text: "invoice"})
	covered, eligible := brute()
	if res.Semantic.Covered != covered || res.Semantic.Eligible != eligible || eligible != 4 || covered != 3 {
		t.Fatalf("a mail write: coverage %d/%d, brute force %d/%d, want 3/4",
			res.Semantic.Covered, res.Semantic.Eligible, covered, eligible)
	}
	w.index()
	res = w.search(Query{Text: "invoice"})
	covered, eligible = brute()
	if res.Semantic.Covered != covered || res.Semantic.Eligible != eligible || eligible != 4 || covered != 4 {
		t.Fatalf("after indexing: coverage %d/%d, brute force %d/%d, want 4/4",
			res.Semantic.Covered, res.Semantic.Eligible, covered, eligible)
	}
}

// TestCoverageCountIsSharedByConcurrentSearches: a burst of searches at one
// sequence counting the same eligible set must count the copy once, through
// the same single-flight the rebuild uses.
func TestCoverageCountIsSharedByConcurrentSearches(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.search(Query{Text: "invoice"}) // load the copy before resetting the cache
	w.s.mu.Lock()
	w.s.cache = coverageCache{}
	w.s.mu.Unlock()
	before := w.s.coverageScans.Load()

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if covered, eligible := w.s.coverage(context.Background()); covered != 3 || eligible != 3 {
				t.Errorf("concurrent coverage %d/%d, want 3/3", covered, eligible)
			}
		}()
	}
	wg.Wait()
	if got := w.s.coverageScans.Load() - before; got != 1 {
		t.Fatalf("8 concurrent searches counted the copy %d times, want 1", got)
	}
}
