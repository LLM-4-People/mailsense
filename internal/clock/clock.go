// Package clock is the one time source of production code, so tests can
// replace real time with clocktest.Fake instead of sleeping.
package clock

import (
	"context"
	"time"
)

// Clock tells time and waits.
type Clock interface {
	Now() time.Time
	// Sleep waits d or until ctx is done (then it returns ctx.Err()).
	Sleep(ctx context.Context, d time.Duration) error
}

// Real is the wall clock.
func Real() Clock { return real{} }

// Or returns c, or the wall clock when c is nil.
func Or(c Clock) Clock {
	if c == nil {
		return Real()
	}
	return c
}

type real struct{}

func (real) Now() time.Time { return time.Now() }

func (real) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
