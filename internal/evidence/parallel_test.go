package evidence

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// TestMapReduceMergesEveryItemOnce: every item reaches the pool exactly once
// and its result is merged at that item's index, whatever the worker count.
func TestMapReduceMergesEveryItemOnce(t *testing.T) {
	const n = 500
	for _, workers := range []int{1, 2, 8} {
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		out := make([]int, n)
		var calls atomic.Int64
		err := mapReduce(t.Context(), workers, items, func(i int) string { return fmt.Sprintf("item %d", i) },
			func(i int) (int, error) {
				calls.Add(1)
				return i * i, nil
			}, func(index int, square int) {
				out[index] = square
			})
		if err != nil {
			t.Fatalf("%d workers: %v", workers, err)
		}
		if got := calls.Load(); got != n {
			t.Fatalf("%d workers: %d calls, want %d", workers, got, n)
		}
		for i := range out {
			if out[i] != i*i {
				t.Fatalf("%d workers: index %d holds %d, want %d", workers, i, out[i], i*i)
			}
		}
	}
	// An empty set is not an error and starts no work.
	if err := mapReduce(t.Context(), 4, nil, func(int) string { return "" },
		func(int) (int, error) { t.Fatal("called for an empty set"); return 0, nil },
		func(int, int) { t.Fatal("merged for an empty set") }); err != nil {
		t.Fatal(err)
	}
}

// TestMapReduceTurnsAPanicIntoThatItemsError: a panic inside one item becomes
// that item's error (with the panic value and stack preserved) and never kills
// the process or the other items. The reported failure is the lowest index, so
// two broken messages always report the same one.
func TestMapReduceTurnsAPanicIntoThatItemsError(t *testing.T) {
	const n = 40
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	merged := make([]bool, n)
	var calls atomic.Int64
	err := mapReduce(t.Context(), 4, items, func(i int) string { return fmt.Sprintf("item %d", i) },
		func(i int) (int, error) {
			calls.Add(1)
			if i == 7 || i == 9 {
				panic("synthetic evidence worker crash")
			}
			return i, nil
		}, func(index int, _ int) {
			merged[index] = true
		})
	var panicErr *jobs.PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("panic not reported as a jobs.PanicError: %v", err)
	}
	if !strings.Contains(err.Error(), "item 7") || !strings.Contains(err.Error(), "synthetic evidence worker crash") {
		t.Fatalf("error does not name the item and its panic: %v", err)
	}
	// Two broken items report the lower one, whatever order the workers ran.
	if strings.Contains(err.Error(), "item 9") {
		t.Fatalf("error names a later item instead of the first: %v", err)
	}
	if got := calls.Load(); got != n {
		t.Fatalf("%d items ran, want %d: a panicking item must not stop the pool", got, n)
	}
	for _, i := range []int{7, 9} {
		if merged[i] {
			t.Fatalf("panicked item %d's zero result was merged", i)
		}
	}
	for i := range merged {
		if i != 7 && i != 9 && !merged[i] {
			t.Fatalf("item %d was dropped", i)
		}
	}
}

// TestMapReduceStopsTheProducerOnCancellation: cancelling inside an item
// abandons the rest of a large set at once instead of draining it. The pool
// reports no item error (the caller reads the cause) and returns promptly.
func TestMapReduceStopsTheProducerOnCancellation(t *testing.T) {
	const n = 100_000
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var started atomic.Int64
	start := time.Now()
	err := mapReduce(ctx, 1, make([]int, n), func(i int) string { return fmt.Sprintf("item %d", i) },
		func(i int) (int, error) {
			started.Add(1)
			cancel()
			return i, nil
		}, func(int, int) {})
	if err != nil {
		t.Fatalf("a cancelled pool reported an item error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the pool took %s to stop on cancellation", elapsed)
	}
	// One worker cancelled on its first item: no later item may start.
	if got := started.Load(); got != 1 {
		t.Fatalf("%d items started after the first cancelled; want 1", got)
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatalf("cause %v", context.Cause(ctx))
	}
}

// TestMapReduceLeavesNoWorkerBehind: the call returns only after every worker
// exited, so a shutdown join never races a pool goroutine touching the store.
func TestMapReduceLeavesNoWorkerBehind(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 5 {
		items := make([]int, 2000)
		for i := range items {
			items[i] = i
		}
		if err := mapReduce(t.Context(), 8, items, func(int) string { return "item" },
			func(i int) (int, error) { return i, nil }, func(int, int) {}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("%d goroutines after the pool, %d before", after, before)
	}
}

// TestBuildTemplatesStopsOnCancellation: the template pass is part of the
// shared context build, which used to ignore cancellation entirely. A stopped
// context abandons the mailbox and reports its cause instead of counting every
// sender first.
func TestBuildTemplatesStopsOnCancellation(t *testing.T) {
	items := make([]templateItem, 0, 100)
	vecs := make(map[int64]unitVector, 100)
	for i := range 100 {
		items = append(items, templateItem{row: int64(i), sender: fmt.Sprintf("s%d@example.org", i),
			at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		vecs[int64(i)] = unitVector{values: []int8{1, 0, 0, 0, 0, 0, 0, 0}, norm: 1}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	got, err := buildTemplates(ctx, items, vecs, 0.9, 20, 4)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want the cancellation cause", err)
	}
	if got != nil {
		t.Fatalf("a cancelled pass returned templates: %+v", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the cancelled pass took %s", elapsed)
	}
}
