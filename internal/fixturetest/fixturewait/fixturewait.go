// Package fixturewait provides bounded polling for tests. It is a leaf
// package (standard library only) so a package that fixturetest already
// depends on through syncer can use the wait helper without an import cycle.
package fixturewait

import (
	"context"
	"testing"
	"time"
)

// PollInterval is the pause between two condition checks.
const PollInterval = time.Millisecond

// WaitUntil polls cond until it returns true, the timeout expires, or t's
// context is cancelled. It fails t with the condition description and a
// bounded wait, never spinning without a deadline or ignoring cancellation.
func WaitUntil(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	ctx := t.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(min(PollInterval, remaining))
	}
}
