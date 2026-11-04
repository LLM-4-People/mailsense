package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// supervise starts the runtime, watches it and restarts it with backoff
// until the manager stops, the idle policy stops an unused runtime, or it
// gives up (permanent error, or sidecar.health.failures consecutive failed
// starts). A GPU build that
// keeps dying soon after its start (sidecar.health.failures serves
// shorter than sidecar.restart.max_delay) is marked broken: the next
// start uses cpu when the platform has one, else it reports the broken
// build (re-probe to try again). There is no fallback without a cpu
// build.
func (in *instance) supervise(gen uint64) {
	defer func() {
		in.mu.Lock()
		if in.gen == gen { // an idle stop may already have admitted the next supervisor
			in.running = false
		}
		in.mu.Unlock()
	}()
	ctx := in.m.ctx
	failures, short := 0, 0
	for {
		r, err := in.start(ctx)
		if err != nil {
			failures++
			if ctx.Err() != nil || permanent(err) || failures >= in.m.cfg.Int("sidecar.health.failures") {
				in.giveUp(ctx, err)
				return
			}
			in.set(StateRestarting, err.Error(), func(s *Status) { s.Restarts++ })
		} else {
			healthy, reason, idle := in.serve(ctx, r)
			if idle {
				in.stopIdle(gen, reason)
				return
			}
			if ctx.Err() != nil {
				in.set(StateStopped, "", func(s *Status) { s.PID = 0 })
				in.resolve(engine.Endpoint{}, ErrStopped)
				return
			}
			failures, short = in.afterServe(r, healthy, reason, failures, short)
		}
		if in.m.clock.Sleep(ctx, mathx.Backoff(in.m.cfg.Duration("sidecar.restart.base_delay"), in.m.cfg.Duration("sidecar.restart.max_delay"), failures-1)) != nil {
			in.giveUp(ctx, ErrStopped)
			return
		}
	}
}

// afterServe counts a serve: a long healthy one resets the counts; a
// short one counts towards the backoff and, for a GPU build, towards
// marking it broken.
func (in *instance) afterServe(r *running, healthy time.Duration, reason string, failures, short int) (int, int) {
	if healthy >= in.m.cfg.Duration("sidecar.restart.max_delay") {
		return 1, 0
	}
	if r.build.Spec.GPU {
		short++
		if short >= in.m.cfg.Int("sidecar.health.failures") {
			in.markBroken(r.build, r.requested, fmt.Sprintf("it stopped %d times within %s of its start (last: %s)", short, in.m.cfg.Duration("sidecar.restart.max_delay"), reason))
			return 1, 0
		}
	}
	return failures + 1, short
}

func (in *instance) giveUp(ctx context.Context, err error) {
	if ctx.Err() != nil {
		err = ErrStopped
		in.set(StateStopped, "", nil)
	} else {
		in.set(StateFailed, err.Error(), nil)
	}
	in.resolve(engine.Endpoint{}, fmt.Errorf("%w: %s: %w", ErrFailed, in.runtime, err))
}

// serve publishes the ready runtime, watches it until it must restart or
// the idle policy ends it, stops it, and reports how long it was healthy,
// why it ended and whether the idle policy (a clean stop, not a failure)
// ended it.
func (in *instance) serve(ctx context.Context, r *running) (time.Duration, string, bool) {
	in.set(StateReady, "", func(s *Status) {
		s.Variant, s.PID, s.Threads, s.Fallback, s.Requested, s.Canary = r.build.Variant, r.child.pid, r.threads, r.fallback, r.requested, r.canary
	})
	in.resolve(endpointOf(r.child), nil)
	since := in.m.clock.Now()
	reason, idle := in.watch(ctx, r)
	healthy := in.m.clock.Now().Sub(since)
	in.rearm()
	in.stopChild(r.child)
	if idle {
		return healthy, reason, true
	}
	if ctx.Err() == nil {
		in.set(StateRestarting, reason, func(s *Status) { s.Restarts++; s.PID = 0 })
	}
	return healthy, reason, false
}

// watch checks /health every sidecar.health.interval, or at once when a
// caller reports the runtime unreachable, and returns why it must
// restart: it exited, failed sidecar.health.failures checks in a row,
// sidecar.variant changed, or a re-probe was asked for. It also returns
// true when sidecar.idle_timeout ends the serve: a clean stop the
// supervisor must not count or restart. A healthy check hands the
// endpoint out again to callers waiting after a report.
func (in *instance) watch(ctx context.Context, r *running) (string, bool) {
	misses := 0
	for {
		poked, restart := in.wait(ctx, in.m.cfg.Duration("sidecar.health.interval"))
		switch {
		case ctx.Err() != nil:
			return "stopped", false
		case restart:
			return "a re-probe was asked for", false
		case r.child.exited():
			return fmt.Sprintf("the process exited (%v): %s", r.child.err, r.child.out.last(3)), false
		case in.m.cfg.String("sidecar.variant") != r.requested:
			return "sidecar.variant changed to " + in.m.cfg.String("sidecar.variant"), false
		}
		if reason, idle := in.idleDue(); idle {
			return reason, true
		}
		if err := in.m.healthy(ctx, r.child, r.build); err != nil {
			if poked {
				return fmt.Sprintf("a caller could not reach it and the health check failed: %v", err), false
			}
			if misses++; misses >= in.m.cfg.Int("sidecar.health.failures") {
				return fmt.Sprintf("%d health checks failed: %v", misses, err), false
			}
			continue
		}
		misses = 0
		in.resolve(endpointOf(r.child), nil)
	}
}

// idleDue reports whether sidecar.idle_timeout ends this serve, with a
// detail naming the policy and how long the runtime has been unused. A
// positive timeout is a floor, not the whole window: one embeddings call
// may hold the endpoint for up to http.timeout.total after the Locate that
// admitted it (the embed adapter's client timeout, with no override), so
// the stop waits at least that long after the last touch. The latch swap
// is in the same critical section as touch and resolve, so a caller that
// arrives after the decision waits for the stop instead of receiving an
// endpoint that is about to end.
func (in *instance) idleDue() (string, bool) {
	timeout := in.m.cfg.Duration("sidecar.idle_timeout")
	if timeout <= 0 {
		return "", false
	}
	window := max(timeout, in.m.cfg.Duration("http.timeout.total"))
	in.mu.Lock()
	defer in.mu.Unlock()
	idle := in.m.clock.Now().Sub(in.lastUse)
	if idle < window {
		return "", false
	}
	select {
	case <-in.cur.ch:
		in.cur = newLatch()
	default:
	}
	return fmt.Sprintf("idle for %s (sidecar.idle_timeout %s)", idle.Round(time.Millisecond), timeout), true
}

// wait sleeps d or until a poke; it reports whether it was poked and
// whether the poke asks for a restart. A poke that arrives while the
// sleep ends is never lost.
func (in *instance) wait(ctx context.Context, d time.Duration) (poked, restart bool) {
	sctx, cancel := context.WithCancel(ctx)
	got := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case p := <-in.pokes:
			got <- p
			cancel()
		case <-sctx.Done():
		}
	}()
	_ = in.m.clock.Sleep(sctx, d)
	cancel()
	<-done
	select {
	case p := <-got:
		return true, p
	default:
		return false, false
	}
}

// healthy is one /health check (one attempt, bounded by the interval).
func (m *Manager) healthy(ctx context.Context, c *child, b Build) error {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Duration("sidecar.health.interval"))
	defer cancel()
	ctx = httpx.WithPolicy(ctx, func(p *httpx.Policy) { p.MaxAttempts = 1 })
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+b.RuntimeSpec.HealthPath, nil)
	if err != nil {
		return err
	}
	httpx.SetBearer(req, c.key)
	resp, err := m.health.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health answered HTTP %d", resp.StatusCode)
	}
	return nil
}
