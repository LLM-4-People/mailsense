package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// Review L0 (bugs #7, rules #5): a request made while a run is in progress
// is not lost — it starts one more run when the current one ends, and
// requests made meanwhile coalesce into that one run.
func TestSingleNeverLosesARequest(t *testing.T) {
	var s Single
	runs := 0
	start := func() { runs++ } // a run that is still in progress until End
	s.Want(start)
	if runs != 1 || !s.Busy() || s.Begin() {
		t.Fatalf("the first request starts at once and holds the slot: runs %d busy %v", runs, s.Busy())
	}
	s.Want(start)
	s.Want(start)
	if runs != 1 {
		t.Fatalf("requests during a run wait for it: %d runs", runs)
	}
	s.End()
	if runs != 2 || !s.Busy() {
		t.Fatalf("the pending request runs once when the run ends: %d runs, busy %v", runs, s.Busy())
	}
	s.End()
	if runs != 2 || s.Busy() {
		t.Fatalf("nothing more was wanted: %d runs, busy %v", runs, s.Busy())
	}
	// A synchronous run (Begin/End) also passes a request on.
	if !s.Begin() {
		t.Fatal("a free slot can be claimed")
	}
	s.Want(start)
	s.End()
	if runs != 3 {
		t.Fatalf("a request during a synchronous run starts after it: %d", runs)
	}
	s.End()
}

func TestSingleUnderConcurrency(t *testing.T) {
	var s Single
	var mu sync.Mutex
	active, maxActive, runs := 0, 0, 0
	var wg sync.WaitGroup
	var start func()
	start = func() {
		mu.Lock()
		active++
		runs++
		maxActive = max(maxActive, active)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			active--
			mu.Unlock()
			s.End()
		}()
	}
	var callers sync.WaitGroup
	for range 200 {
		callers.Add(1)
		go func() { defer callers.Done(); s.Want(start) }()
	}
	callers.Wait()
	wg.Wait()
	if maxActive != 1 || runs == 0 || s.Busy() {
		t.Fatalf("one run at a time, none left running: max %d runs %d busy %v", maxActive, runs, s.Busy())
	}
}

// StartClaimed releases the slot when the manager refuses the job: a
// refused start must not wedge the one-at-a-time guard.
func TestStartClaimedReleasesTheSlotOnRefusal(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	var s Single
	if !s.Begin() {
		t.Fatal("a free slot can be claimed")
	}
	_, err := StartClaimed(&s, m, "k", "n", func(context.Context, func(Progress)) error {
		t.Fatal("the refused job ran")
		return nil
	})
	if !errors.Is(err, ErrShutdown) {
		t.Fatalf("a shut-down manager refused with %v", err)
	}
	if !s.Begin() {
		t.Fatal("the refused job kept the slot")
	}
	s.End()
}

// A job that starts releases the slot when it ends, not before.
func TestStartClaimedReleasesTheSlotWhenTheJobEnds(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	defer m.Shutdown(context.Background())
	var s Single
	if !s.Begin() {
		t.Fatal("a free slot can be claimed")
	}
	started, release := make(chan struct{}), make(chan struct{})
	j, err := StartClaimed(&s, m, "k", "n", func(context.Context, func(Progress)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if s.Begin() {
		t.Fatal("the slot was free while the job was running")
	}
	close(release)
	<-j.Done()
	if !s.Begin() {
		t.Fatal("the job ended without releasing the slot")
	}
	s.End()
}
