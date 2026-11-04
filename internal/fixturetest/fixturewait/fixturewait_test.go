package fixturewait

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTB captures a failure without failing the real test, so the
// timeout and cancellation paths can be asserted.
type recordingTB struct {
	testing.TB
	ctx context.Context

	mu     sync.Mutex
	failed bool
	msg    string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Context() context.Context { return r.ctx }

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *recordingTB) failure() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failed, r.msg
}

// runWaitUntil runs the helper in its own goroutine because a failure calls
// runtime.Goexit. done closes however the helper exits.
func runWaitUntil(tb *recordingTB, timeout time.Duration, what string, cond func() bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		WaitUntil(tb, timeout, what, cond)
	}()
	return done
}

func TestWaitUntil(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		timeout time.Duration
		cond    func() bool
		ctx     func(context.Context) context.Context
		failed  bool
		message string
	}{
		{
			name:    "an already true condition returns at once",
			timeout: time.Second,
			cond:    func() bool { return true },
		},
		{
			name:    "a condition that becomes true is polled until it holds",
			timeout: time.Second,
			cond: func() func() bool {
				polls := 0
				return func() bool {
					polls++
					return polls >= 3
				}
			}(),
		},
		{
			name:    "a false condition fails with the timeout and description",
			timeout: 20 * time.Millisecond,
			cond:    func() bool { return false },
			failed:  true,
			message: "timed out after 20ms waiting for the impossible condition",
		},
		{
			name:    "a cancelled context fails without waiting for the timeout",
			timeout: time.Hour,
			cond:    func() bool { return false },
			ctx: func(parent context.Context) context.Context {
				cancelled, cancel := context.WithCancel(parent)
				cancel()
				return cancelled
			},
			failed:  true,
			message: "waiting for the impossible condition: context canceled",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			if tc.ctx != nil {
				ctx = tc.ctx(ctx)
			}
			tb := &recordingTB{TB: t, ctx: ctx}
			start := time.Now()
			done := runWaitUntil(tb, tc.timeout, "the impossible condition", tc.cond)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("WaitUntil did not return: the wait is unbounded")
			}
			elapsed := time.Since(start)
			failed, msg := tb.failure()
			if failed != tc.failed {
				t.Fatalf("failed = %v, want %v (message %q)", failed, tc.failed, msg)
			}
			if tc.message != "" && !strings.Contains(msg, tc.message) {
				t.Fatalf("failure message %q does not contain %q", msg, tc.message)
			}
			if tc.failed && elapsed > tc.timeout+time.Second {
				t.Fatalf("failure took %s for a %s timeout", elapsed, tc.timeout)
			}
		})
	}
}

// TestWaitUntilDoesNotLeakAGoroutine finishes a wait and proves the helper's
// goroutine exited; the helper itself must not spawn any.
func TestWaitUntilDoesNotLeakAGoroutine(t *testing.T) {
	t.Parallel()
	before := runtime.NumGoroutine()
	released := make(chan struct{})
	cond := func() bool {
		select {
		case <-released:
			return true
		default:
			return false
		}
	}
	tb := &recordingTB{TB: t, ctx: t.Context()}
	done := runWaitUntil(tb, time.Second, "the release", cond)
	close(released)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUntil did not return after its condition held")
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines grew from %d to %d", before, runtime.NumGoroutine())
		}
		time.Sleep(time.Millisecond)
	}
}
