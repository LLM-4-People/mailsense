package learning

import (
	"errors"
	"sync/atomic"
	"testing"
)

// A stable answer is reused while the key is unchanged; another key asks
// the stage again, and the old entry is replaced (one entry only).
func TestStageCacheReusesUntilTheKeyChanges(t *testing.T) {
	var c StageCache
	var calls int
	build := func() (StageStatus, error) {
		calls++
		return StageStatus{State: Done, Progress: Progress{Done: int64(calls), Total: 3}}, nil
	}
	for range 3 {
		st, err := c.Status("a", build)
		if err != nil || st.Progress.Done != 1 {
			t.Fatalf("same key: %+v %v", st, err)
		}
	}
	if calls != 1 {
		t.Fatalf("3 reads of one key ran the build %d times, want 1", calls)
	}
	if st, _ := c.Status("b", build); st.Progress.Done != 2 {
		t.Fatalf("a new key did not rebuild: %+v", st)
	}
	if st, _ := c.Status("a", build); st.Progress.Done != 3 {
		t.Fatalf("the one entry did not replace the old key: %+v", st)
	}
}

// A running stage is never cached: its progress comes from the live run on
// every read, not from the last frame.
func TestStageCacheNeverCachesRunning(t *testing.T) {
	var c StageCache
	var calls int
	build := func() (StageStatus, error) {
		calls++
		return StageStatus{State: Running, Progress: Progress{Done: int64(calls), Total: 10}}, nil
	}
	for want := 1; want <= 2; want++ {
		st, err := c.Status("run", build)
		if err != nil || st.State != Running || st.Progress.Done != int64(want) {
			t.Fatalf("read %d: %+v %v", want, st, err)
		}
	}
}

// A failed read is not stored: the next read retries instead of pinning a
// transient store or provider failure.
func TestStageCacheNeverCachesAnError(t *testing.T) {
	var c StageCache
	want := errors.New("store busy")
	var calls int
	build := func() (StageStatus, error) {
		calls++
		if calls == 1 {
			return StageStatus{State: Blocked}, want
		}
		return StageStatus{State: Done}, nil
	}
	if _, err := c.Status("a", build); !errors.Is(err, want) {
		t.Fatalf("first read: %v", err)
	}
	st, err := c.Status("a", build)
	if err != nil || st.State != Done || calls != 2 {
		t.Fatalf("a failed read was cached: %+v %v (calls %d)", st, err, calls)
	}
}

// Concurrent readers of one key share one build; a reader of another key
// does not see the first key's answer.
func TestStageCacheConcurrentReaders(t *testing.T) {
	var c StageCache
	var calls atomic.Int32
	start := make(chan struct{})
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			<-start
			st, err := c.Status("a", func() (StageStatus, error) {
				calls.Add(1)
				return StageStatus{State: Done, Note: "a"}, nil
			})
			if err != nil || st.Note != "a" {
				t.Errorf("concurrent read: %+v %v", st, err)
			}
		}()
	}
	close(start)
	for range 8 {
		<-done
	}
	if calls.Load() == 0 {
		t.Fatal("no build ran")
	}
	// The cache, not the number of racers, must hold one stable answer.
	st, err := c.Status("a", func() (StageStatus, error) {
		t.Fatal("a cached key rebuilt")
		return StageStatus{}, nil
	})
	if err != nil || st.Note != "a" {
		t.Fatalf("after the racers: %+v %v", st, err)
	}
}

// Without a key change, a fact-looking status is returned as the same value
// (blockers and notes included), not rebuilt synthetically.
func TestStageCachePreservesTheAnswer(t *testing.T) {
	var c StageCache
	want := StageStatus{
		State:    Blocked,
		Progress: Progress{Done: 2, Total: 7, Unit: UnitMessages, Stage: "fit", Detail: "q1", StepDone: 1, StepTotal: 3},
		Blockers: []Blocker{{Code: "x", Subject: "s", Actions: []Action{Do("act")}, Detail: "d",
			Measure: &Measure{Have: 1, Need: 2, Unit: "labels"}}},
		Note: "n",
	}
	got, err := c.Status("k", func() (StageStatus, error) { return want, nil })
	if err != nil || !sameStatus(got, want) {
		t.Fatalf("first read: %+v %v", got, err)
	}
	again, err := c.Status("k", func() (StageStatus, error) { return StageStatus{}, nil })
	if err != nil || !sameStatus(again, want) {
		t.Fatalf("cached read: %+v %v", again, err)
	}
}

func sameStatus(a, b StageStatus) bool {
	if a.State != b.State || a.Note != b.Note || a.Progress != b.Progress || len(a.Blockers) != len(b.Blockers) {
		return false
	}
	for i := range a.Blockers {
		x, y := a.Blockers[i], b.Blockers[i]
		if x.Code != y.Code || x.Subject != y.Subject || x.Detail != y.Detail ||
			(x.Measure == nil) != (y.Measure == nil) || len(x.Actions) != len(y.Actions) {
			return false
		}
		if x.Measure != nil && *x.Measure != *y.Measure {
			return false
		}
	}
	return true
}
