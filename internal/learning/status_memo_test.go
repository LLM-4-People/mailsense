package learning

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// A build memo answers every stage once: repeated reads within one build
// (the machine and the stages nested under it) return the first answer
// without asking the stage again.
func TestStatusOfMemoizesWithinOneBuild(t *testing.T) {
	a := &fakeStage{id: "counting", st: StageStatus{State: Done}}
	ctx := withStatusMemo(t.Context())
	for range 3 {
		st, err := StatusOf(ctx, a)
		if err != nil || st.State != Done {
			t.Fatalf("StatusOf: %+v, %v", st, err)
		}
	}
	if got := a.callCount(); got != 1 {
		t.Fatalf("3 reads within one build asked the stage %d times, want 1", got)
	}
	// Outside the memo every read executes: there is no cross-build cache.
	for range 2 {
		if _, err := StatusOf(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.callCount(); got != 3 {
		t.Fatalf("2 reads outside the memo asked the stage %d more times, want 2 (total 3)", got)
	}
	// A second build is a new memo: it asks again.
	StatusOf(withStatusMemo(t.Context()), a)
	if got := a.callCount(); got != 4 {
		t.Fatalf("a new build reused the last build's answer (calls %d, want 4)", got)
	}
}

// A stage that fails is asked once per build and every read of that build
// returns the same status and the same error.
func TestStatusOfPreservesAFailedStageWithinTheBuild(t *testing.T) {
	want := errors.New("stage failed")
	a := &fakeStage{id: "failing", st: StageStatus{State: Running, Progress: Progress{Done: 4, Total: 9}}}
	a.err = want
	ctx := withStatusMemo(t.Context())
	for range 3 {
		st, err := StatusOf(ctx, a)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
		if st.State != Running || st.Progress.Done != 4 || st.Progress.Total != 9 {
			t.Fatalf("status = %+v, want the stage's own status", st)
		}
	}
	if got := a.callCount(); got != 1 {
		t.Fatalf("3 reads of a failing stage asked it %d times, want 1", got)
	}
}

// Distinct stages never share an answer, even inside one build.
func TestStatusOfKeepsStagesApart(t *testing.T) {
	ctx := withStatusMemo(t.Context())
	a := &fakeStage{id: "a", st: StageStatus{State: Done}}
	b := &fakeStage{id: "b", st: StageStatus{State: Blocked}}
	if st, _ := StatusOf(ctx, a); st.State != Done {
		t.Fatalf("a = %+v", st)
	}
	if st, _ := StatusOf(ctx, b); st.State != Blocked {
		t.Fatalf("b = %+v", st)
	}
	if a.callCount() != 1 || b.callCount() != 1 {
		t.Fatalf("calls a=%d b=%d, want 1 each", a.callCount(), b.callCount())
	}
}

// A stage whose dynamic type is not comparable (it holds a slice) must not
// panic: the memo keys on the stage type and id, never on the value.
type sliceStage struct {
	id    string
	parts []string
	calls *int
}

func (s sliceStage) ID() string { return s.id }

func (s sliceStage) Status(context.Context) (StageStatus, error) {
	(*s.calls)++
	return StageStatus{State: Done}, nil
}

func TestStatusOfHandlesANonComparableStage(t *testing.T) {
	calls := 0
	s := sliceStage{id: "slice", parts: []string{"a", "b"}, calls: &calls}
	ctx := withStatusMemo(t.Context())
	for range 2 {
		if st, err := StatusOf(ctx, s); err != nil || st.State != Done {
			t.Fatalf("non-comparable stage: %+v, %v", st, err)
		}
	}
	if calls != 1 {
		t.Fatalf("non-comparable stage asked %d times within one build, want 1", calls)
	}
	if _, err := StatusOf(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("non-comparable stage outside the memo: %d calls, want 2", calls)
	}
}

// A fact is read once per build and shared by every caller of the same
// (type, name); another name or another type is another fact.
func TestMemoAnswersEachFactOnceWithinOneBuild(t *testing.T) {
	ctx := withStatusMemo(t.Context())
	calls := 0
	build := func(context.Context) (string, error) {
		calls++
		return "first", nil
	}
	for range 3 {
		got, err := Memo(ctx, "layer1.fact", build)
		if err != nil || got != "first" {
			t.Fatalf("Memo: %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("3 reads of one fact ran the build %d times, want 1", calls)
	}
	// A fact with another name is another fact.
	if got, _ := Memo(ctx, "layer1.other", build); got != "first" {
		t.Fatalf("another name: %q", got)
	}
	if calls != 2 {
		t.Fatalf("a fact of another name did not build: %d calls, want 2", calls)
	}
	// The same name under another type is another fact too.
	var typed int
	if got, err := Memo(ctx, "layer1.fact", func(context.Context) (int, error) {
		typed++
		return 42, nil
	}); err != nil || got != 42 {
		t.Fatalf("same name, other type: %d, %v", got, err)
	}
	if typed != 1 || calls != 2 {
		t.Fatalf("a fact of another type did not build (typed %d, string %d)", typed, calls)
	}
}

// Concurrent readers of one fact share the one build answer.
func TestMemoAnswersConcurrentReadersOnce(t *testing.T) {
	ctx := withStatusMemo(t.Context())
	var calls, wrong atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := Memo(ctx, "layer1.concurrent", func(context.Context) (int, error) {
				calls.Add(1)
				return 7, nil
			})
			if err != nil || got != 7 {
				wrong.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("8 concurrent readers ran the build %d times, want 1", calls.Load())
	}
	if wrong.Load() != 0 {
		t.Fatalf("%d concurrent readers got another value", wrong.Load())
	}
}

// Memo outside a status build is exactly build(ctx): it never caches.
func TestMemoWithoutABuildIsDirect(t *testing.T) {
	calls := 0
	build := func(context.Context) (int, error) {
		calls++
		return calls, nil
	}
	for want := 1; want <= 2; want++ {
		got, err := Memo(t.Context(), "layer1.fact", build)
		if err != nil || got != want {
			t.Fatalf("read %d: %d, %v", want, got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("2 reads outside a build ran the build %d times, want 2", calls)
	}
}

// A failing fact is read once per build and every caller of that build
// sees the same error.
func TestMemoPreservesAFailedFactWithinTheBuild(t *testing.T) {
	want := errors.New("fact failed")
	ctx := withStatusMemo(t.Context())
	calls := 0
	for range 2 {
		if _, err := Memo(ctx, "layer1.failing", func(context.Context) (int, error) {
			calls++
			return 0, want
		}); !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	}
	if calls != 1 {
		t.Fatalf("a failing fact ran %d times within one build, want 1", calls)
	}
}
