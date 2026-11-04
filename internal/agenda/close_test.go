package agenda

import (
	"context"
	"testing"
	"time"
)

// Close joins a build the cache is running and reports the conjunction;
// afterwards a read of the same horizon serves the installed result without
// reading the world again, and a second Close reports the same outcome.
func TestCloseJoinsTheRefreshAndIsIdempotent(t *testing.T) {
	svc, world, _, clk := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	world.started = make(chan struct{}, 1)
	world.release = make(chan struct{})

	clk.Advance(2 * 24 * time.Hour) // past the cache TTL
	stale, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil || stale.Obligations != 1 {
		t.Fatalf("an expired agenda did not serve its snapshot: %+v %v", stale, err)
	}
	<-world.started

	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- svc.Close(ctx)
	}()
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) with the refresh still reading the world", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(world.release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the refresh")
	}

	calls := world.calls
	after, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil || after.Obligations != 1 {
		t.Fatalf("post-Close read: %+v %v", after, err)
	}
	if world.calls != calls {
		t.Fatal("a post-Close read read the world again")
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// A Close whose context has already expired reports the miss and does not
// hang; a later Close observes the join.
func TestCloseIsBoundedByTheCallerContext(t *testing.T) {
	svc, world, _, clk := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	world.started = make(chan struct{}, 1)
	world.release = make(chan struct{})
	clk.Advance(2 * 24 * time.Hour)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	<-world.started

	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if svc.Close(expired) {
		t.Fatal("Close joined with an expired context")
	}
	close(world.release)
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !svc.Close(ctx) {
		t.Fatal("the repeated Close did not observe the join")
	}
}

// A store read that ignores cancellation is not faked into a join: Close
// reports the miss under its bound.
func TestCloseReportsANonCooperativeBuild(t *testing.T) {
	svc, world, _, clk := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	world.started = make(chan struct{}, 1)
	world.release = make(chan struct{}) // ignores the build context
	clk.Advance(2 * 24 * time.Hour)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	<-world.started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if svc.Close(ctx) {
		t.Fatal("Close reported a join while the world read was still running")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("Close waited %s, want the caller's bound", elapsed)
	}
	close(world.release)
	if !svc.Close(eventually(t)) {
		t.Fatal("the later Close did not observe the join")
	}
}

// eventually returns a context that a slow-but-eventual join still satisfies.
func eventually(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
