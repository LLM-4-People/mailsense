// Package join is the one bounded join of detached background work.
//
// A cache or a service that rebuilds off the request path starts a goroutine
// the API's handler join cannot see: the request it was started from has
// already returned, so only its owner knows the goroutine exists. A Group is
// that owner's record: Track admits one task, Done retires it, and Close
// refuses new tasks and waits for the tracked ones, bounded by the caller's
// context. The group is the single source of truth for the closed/drained
// state; callers keep their own build locks and never hold one across a build.
package join

import (
	"context"
	"sync"
)

// Group tracks the detached tasks of one owner for a bounded shutdown join.
// The zero value is ready to use.
type Group struct {
	mu      sync.Mutex
	closed  bool
	active  int
	drained chan struct{}
	// drainedNow records that drained was closed: close is not idempotent
	// on a channel, so the flag, not a channel send, is the state.
	drainedNow bool
}

// New returns an empty group.
func New() *Group { return &Group{} }

// Track admits one task. It reports false when the group is closed: the
// caller must not spawn the task, because Close may already have returned and
// the resource the task reads may be gone. A task admitted here is counted
// until its Done, so Close always waits for it.
func (g *Group) Track() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.active++
	return true
}

// Done retires one tracked task. It is safe on a group with no active task,
// so a deferred Done never underflows.
func (g *Group) Done() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.drainLocked()
	g.mu.Unlock()
}

// Active is how many tracked tasks have not reported Done yet. It is the
// read side of the join for a shutdown log that names what it abandoned; it
// is not a scheduling signal.
func (g *Group) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// Closed reports whether Close was called. It is the read side of the one
// closed flag: a reader that must serve instead of building asks here.
func (g *Group) Closed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.closed
}

// Refuse closes the admission gate without waiting for the tracked tasks. It
// is the first half of Close, for an owner that must stop admitting work
// before it drains its producer: an HTTP server refuses handlers, shuts its
// listener down, and only then joins what was already running. It is
// idempotent; a later Track reports false.
func (g *Group) Refuse() {
	g.mu.Lock()
	g.closed = true
	g.drainLocked()
	g.mu.Unlock()
}

// Close refuses new tasks and waits for every tracked one, bounded by ctx. It
// is idempotent and safe for concurrent callers: each waits on the same
// drain, so a later Close still reports the join a shorter one missed. It
// reports false only when ctx ended before the group drained; the tasks are
// then still running, which is why the caller keeps their resources open.
func (g *Group) Close(ctx context.Context) bool {
	g.Refuse()
	g.mu.Lock()
	drained, done := g.drainedNow, g.channelLocked()
	g.mu.Unlock()
	if drained {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return joined(done)
	}
}

// joined reports whether the drain closed before the caller's context ended.
// Close wakes on whichever became ready first; this second look makes the
// answer depend on the drain, not on the select's random choice, so a join that
// finished in the same instant is not reported as unjoined.
func joined(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// channelLocked returns the drain channel, creating it on first use so a
// waiter and the final Done always share one channel. The caller holds mu.
func (g *Group) channelLocked() chan struct{} {
	if g.drained == nil {
		g.drained = make(chan struct{})
	}
	return g.drained
}

// drainLocked closes the drain channel once the group is closed and no task
// is left. The caller holds mu.
func (g *Group) drainLocked() {
	if !g.closed || g.active != 0 || g.drainedNow {
		return
	}
	g.drainedNow = true
	close(g.channelLocked())
}
