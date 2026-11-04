// Package clocktest provides a deterministic clock for tests.
package clocktest

import (
	"context"
	"sync"
	"time"
)

// Barrier is a Fake whose first n Now calls wait for one another and are
// released together. A cold-cache concurrency test wraps the service clock so
// every caller is inside the service before the first build runs; calls after
// the barrier opens read the fake clock directly. n at or below one is an
// open barrier.
type Barrier struct {
	*Fake
	n       int
	mu      sync.Mutex
	arrived int
	open    chan struct{}
}

// NewBarrier returns a barrier clock over f for n callers.
func NewBarrier(f *Fake, n int) *Barrier {
	b := &Barrier{Fake: f, n: n, open: make(chan struct{})}
	if n <= 1 {
		close(b.open)
	}
	return b
}

// Now blocks until n callers arrived, then returns the fake time.
func (b *Barrier) Now() time.Time {
	b.mu.Lock()
	if b.arrived < b.n {
		b.arrived++
		if b.arrived == b.n {
			close(b.open)
		}
	}
	b.mu.Unlock()
	<-b.open
	return b.Fake.Now()
}

// Fake is a manual clock: Sleep records the duration and advances time
// instantly (or runs OnSleep when set).
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  []time.Duration
	OnSleep func(ctx context.Context, d time.Duration) error
}

// New returns a fake clock at start.
func New(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// Sleep records d; without OnSleep it advances the clock and returns ctx.Err().
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.sleeps = append(f.sleeps, d)
	hook := f.OnSleep
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, d)
	}
	f.Advance(d)
	return ctx.Err()
}

// Sleeps returns the recorded sleep durations.
func (f *Fake) Sleeps() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}
