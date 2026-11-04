package installprofile

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

const (
	// minLearnInterval is the floor between two learning passes. A sync
	// arrives every minute while mail flows; a whole-mailbox scan on every
	// one of them is the hidden cost this trigger removes.
	minLearnInterval = 10 * time.Minute
	// learnSettle is the quiet window in which a burst of sync events
	// coalesces into one pass, so one mailbox refresh does not become a
	// pass plus a follow-up.
	learnSettle = time.Second
	// learnTimeout bounds one pass. A mailbox too large to measure within
	// it keeps the previously learned values.
	learnTimeout = 5 * time.Minute
	// admitRetry is the bounded wait after a paused admission. It keeps the
	// request pending without spinning on the guard.
	admitRetry = 30 * time.Second
)

// AfterSync runs the learners after a sync run that advanced the stored mail
// (syncer.Report.Advanced), so the install profile follows the mail. The
// events feed one coalescing worker: a run at a time, at most one per
// minLearnInterval, and a sync that arrives during a run becomes one pending
// request instead of a queued event.
//
// stop unsubscribes and cancels without waiting, and done closes when the
// worker has actually exited. The caller owns the shutdown bound: a pass
// reads the whole mailbox, so the app joins it through the same bounded
// stop helper as its other workers and keeps the store open when the join
// misses. Splitting the two keeps one owner of the timeout.
func (l *Learner) AfterSync(bus *events.Bus) (stop func(), done <-chan struct{}, err error) {
	return newTrigger(l).watch(bus)
}

// trigger is the one worker behind AfterSync. Its run field is the seam
// tests use to hold or time a pass; production always runs Learner.Run.
type trigger struct {
	l     *Learner
	run   func(context.Context) (Report, error)
	clock clock.Clock
	log   *slog.Logger

	// requests holds the one pending request. A second sync while one is
	// pending is already represented by that request and is dropped.
	requests chan struct{}
	// requested counts the advanced syncs the trigger accepted, including
	// those coalesced into a pending request. Tests use it to wait for a
	// burst to reach the worker.
	requested atomic.Uint64
	done      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc

	interval time.Duration
	settle   time.Duration
	timeout  time.Duration
	retry    time.Duration
}

func newTrigger(l *Learner) *trigger {
	return &trigger{
		l: l, run: l.Run, clock: l.clock, log: l.log,
		requests: make(chan struct{}, 1), done: make(chan struct{}),
		interval: minLearnInterval, settle: learnSettle,
		timeout: learnTimeout, retry: admitRetry,
	}
}

// watch subscribes to finished syncs and starts the worker. The returned stop
// is idempotent and non-blocking: it unsubscribes first (so no new request can
// arrive) and cancels the worker's context. done closes when the worker has
// exited, so the caller joins it under its own bounded shutdown wait.
func (t *trigger) watch(bus *events.Bus) (stop func(), done <-chan struct{}, err error) {
	if bus == nil {
		return nil, nil, errors.New("installprofile: AfterSync needs an event bus")
	}
	onStop, err := events.On(bus, syncer.TopicFinished, t.l.cfg.EventBuffer(), func(rep syncer.Report) {
		if rep.Advanced() {
			t.request()
		}
	})
	if err != nil {
		return nil, nil, err
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	go t.loop()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			onStop()
			t.cancel()
		})
	}
	return stop, t.done, nil
}

// request records one pending pass. The channel is the coalescing point: a
// request that finds it full is already covered by the pending one.
func (t *trigger) request() {
	t.requested.Add(1)
	select {
	case t.requests <- struct{}{}:
	default:
	}
}

// drain consumes every request made before the pass that is about to run;
// they are all served by that one pass. A request that arrives after the
// drain remains for the follow-up pass.
func (t *trigger) drain() {
	for {
		select {
		case <-t.requests:
		default:
			return
		}
	}
}

// loop is the worker: it owns the one run at a time and the throttle.
func (t *trigger) loop() {
	defer close(t.done)
	pending := false
	var last time.Time
	ran := false
	for {
		if !pending {
			select {
			case <-t.ctx.Done():
				return
			case <-t.requests:
				pending = true
			}
			// Let a burst settle before the first pass so ten sync events
			// from one refresh produce one run, not one and a follow-up.
			if err := t.clock.Sleep(t.ctx, t.settle); err != nil {
				return
			}
		}
		if ran {
			// A pending request waits out the interval's remainder; it is
			// not dropped and it does not start a pass beside the last one.
			if wait := t.interval - t.clock.Now().Sub(last); wait > 0 {
				if err := t.clock.Sleep(t.ctx, wait); err != nil {
					return
				}
			}
		}
		if err := t.l.admit(t.ctx); err != nil {
			if t.ctx.Err() != nil {
				return
			}
			t.log.Info("install profile learning paused", "reason", err, "retry_in", t.retry)
			if err := t.clock.Sleep(t.ctx, t.retry); err != nil {
				return
			}
			continue // the request stays pending
		}
		// Everything that arrived before this point is served by this pass.
		t.drain()
		pending = false
		start := time.Now()
		_, err := t.runPass()
		elapsed := time.Since(start)
		if err != nil {
			if t.ctx.Err() != nil {
				return // stop cancelled the pass
			}
			if errors.Is(err, context.DeadlineExceeded) {
				t.log.Warn("install profile learning timed out", "duration", elapsed, "timeout", t.timeout, "err", err)
			} else {
				t.log.Warn("install profile not learned after sync", "duration", elapsed, "err", err)
			}
		}
		// The successful pass logs its own measured line in Learner.Run,
		// with the learned values' fields and the same duration basis.
		ran = true
		last = t.clock.Now()
	}
}

// runPass bounds one learning pass. A timeout cancels the store scans; the
// learned values are written only after both measurements succeed, so the
// previous profile stays intact.
func (t *trigger) runPass() (Report, error) {
	ctx, cancel := context.WithTimeout(t.ctx, t.timeout)
	defer cancel()
	return t.run(ctx)
}
