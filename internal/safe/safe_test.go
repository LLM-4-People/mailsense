package safe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestValueReturnsTheValueAndError(t *testing.T) {
	v, err := Value(func() (int, error) { return 7, nil })
	if err != nil || v != 7 {
		t.Fatalf("value %d err %v", v, err)
	}
	want := errors.New("no")
	if _, err := Value(func() (int, error) { return 0, want }); !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func TestValueTurnsAPanicIntoAnError(t *testing.T) {
	v, err := Value(func() (int, error) { panic("boom") })
	if err == nil || v != 0 {
		t.Fatalf("value %d err %v, want a panic error", v, err)
	}
	var p *PanicError
	if !errors.As(err, &p) || p.Stack == "" {
		t.Fatalf("panic error %#v, want the value and stack", err)
	}
}

// TestDetachedBuildOutlivesTheParentContext pins the one reason a build uses
// Detached: the caller's request ends, the build does not. Cancelling the
// parent while the build runs must leave the build's context live.
func TestDetachedBuildOutlivesTheParentContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	started := make(chan struct{})
	release := make(chan struct{})
	buildErr := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Detached(parent, nil, time.Minute, func(ctx context.Context) (int, error) {
			if ctx == parent {
				return 0, errors.New("the build runs on the parent's context")
			}
			close(started)
			<-release
			buildErr <- ctx.Err()
			return 1, nil
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the detached build never started")
	}
	cancelParent()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("detached build: %v", err)
	}
	if err := <-buildErr; err != nil {
		t.Fatalf("the parent's cancellation reached the build context: %v", err)
	}
}

// TestDetachedBuildIsBoundedByTheTimeout pins the bound: a build that ignores
// its caller still ends at the timeout, with the deadline error in hand.
func TestDetachedBuildIsBoundedByTheTimeout(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	start := time.Now()
	buildErr := make(chan error, 1)
	_, err := Detached(context.Background(), base, 20*time.Millisecond, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		buildErr <- ctx.Err()
		return 0, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("build error = %v, want the timeout's DeadlineExceeded", err)
	}
	if got := <-buildErr; !errors.Is(got, context.DeadlineExceeded) {
		t.Fatalf("build context error = %v, want DeadlineExceeded", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the timeout did not bound the build: %v", elapsed)
	}
}

// TestDetachedBuildEndsWithTheBaseContext pins the shutdown path: ending the
// service's base context ends the build even though its parent is still live.
func TestDetachedBuildEndsWithTheBaseContext(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := Detached(context.Background(), base, time.Minute, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		})
		errs <- err
	}()
	cancelBase()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("build error = %v, want the base cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the base cancellation did not end the build")
	}
}

// TestDetachedBuildTurnsAPanicIntoAnError pins the panic path: the caller gets
// an ordinary error carrying the panic and its stack, so it keeps its own
// message text.
func TestDetachedBuildTurnsAPanicIntoAnError(t *testing.T) {
	v, err := Detached(context.Background(), context.Background(), time.Minute, func(context.Context) (int, error) {
		panic("boom")
	})
	if err == nil || v != 0 {
		t.Fatalf("value %d err %v, want a panic error", v, err)
	}
	var p *PanicError
	if !errors.As(err, &p) || p.Stack == "" || p.Value != "boom" {
		t.Fatalf("panic error %#v, want the value and stack kept for the caller's mapping", err)
	}
}
