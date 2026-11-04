package join

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A tracked task keeps Close waiting until its Done; an untracked group closes
// at once.
func TestCloseWaitsForTrackedTasks(t *testing.T) {
	g := New()
	if !g.Track() {
		t.Fatal("a fresh group refused a task")
	}
	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- g.Close(ctx)
	}()
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) before the task finished", joined)
	case <-time.After(50 * time.Millisecond):
	}
	if !g.Closed() {
		t.Fatal("Close did not report the group closed while waiting")
	}
	g.Done()
	if joined := <-closed; !joined {
		t.Fatal("Close did not join a finished task")
	}
}

// After Close, Track refuses the task: no caller may spawn work the join no
// longer covers.
func TestTrackRefusesAfterClose(t *testing.T) {
	g := New()
	if !g.Close(t.Context()) {
		t.Fatal("an empty group did not close at once")
	}
	if g.Track() {
		t.Fatal("a closed group admitted a task")
	}
	g.Done() // harmless on a closed, empty group
}

// Close is bounded by the caller's context and reports the miss; a later
// Close observes the join. Concurrent callers wait on the same drain.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	g := New()
	if !g.Track() {
		t.Fatal("a fresh group refused a task")
	}
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if g.Close(expired) {
		t.Fatal("Close joined with an expired context")
	}

	var wg sync.WaitGroup
	out := make([]bool, 3)
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out[i] = g.Close(ctx)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	g.Done()
	wg.Wait()
	for i, joined := range out {
		if !joined {
			t.Fatalf("concurrent Close %d did not join after the task finished", i)
		}
	}
	if !g.Close(t.Context()) {
		t.Fatal("a repeat Close of a drained group reported a miss")
	}
}

// Done on an empty group never underflows, so a mistaken extra call cannot
// make Close report a join while a task still runs.
func TestDoneNeverUnderflows(t *testing.T) {
	g := New()
	g.Done()
	if !g.Track() {
		t.Fatal("a fresh group refused a task")
	}
	g.Done()
	g.Done()
	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- g.Close(ctx)
	}()
	if !<-closed {
		t.Fatal("an empty group did not close")
	}
}

// Refuse is the first half of Close: it closes the gate without waiting, so an
// owner can stop admitting work, drain its producer, and join afterwards. The
// count it leaves is what a bounded join reports as unjoined.
func TestRefuseClosesTheGateWithoutWaiting(t *testing.T) {
	g := New()
	if !g.Track() {
		t.Fatal("a fresh group refused a task")
	}
	if got := g.Active(); got != 1 {
		t.Fatalf("Active = %d, want 1", got)
	}
	released := make(chan struct{})
	go func() {
		<-released
		g.Done()
	}()

	g.Refuse()
	if !g.Closed() {
		t.Fatal("Refuse did not close the gate")
	}
	if g.Track() {
		t.Fatal("Track admitted a task after Refuse")
	}
	// Refuse returned while the task is still running: it closes, it does not
	// wait.
	if got := g.Active(); got != 1 {
		t.Fatalf("Active after Refuse = %d, want the task still counted", got)
	}
	close(released)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !g.Close(ctx) {
		t.Fatal("Close after Refuse did not join the released task")
	}
	if got := g.Active(); got != 0 {
		t.Fatalf("Active after Close = %d, want 0", got)
	}
}
