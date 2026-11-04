package sidecar

import (
	"errors"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
)

// latch is one start's outcome: closed once the runtime is ready (ep) or
// has given up (err). Each start after a stop gets a new latch.
type latch struct {
	ch  chan struct{}
	ep  engine.Endpoint
	err error
}

func newLatch() *latch { return &latch{ch: make(chan struct{})} }

// instance is one managed runtime serving one model.
type instance struct {
	m       *Manager
	runtime string
	model   string
	mu      sync.Mutex
	status  Status
	cur     *latch
	running bool   // a supervisor is active
	gen     uint64 // the active supervisor's generation (see ensure)
	// lastUse is when Locate last admitted a caller: the idle policy's
	// only use signal. touch and the supervisor read it under mu.
	lastUse time.Time
	// pokes wake the supervisor's watch at once: false = check health now
	// (a caller could not reach it), true = restart (a re-probe).
	pokes chan bool
}

func newInstance(m *Manager, runtime, model string) *instance {
	return &instance{m: m, runtime: runtime, model: model, cur: newLatch(), pokes: make(chan bool, 1),
		lastUse: m.clock.Now(), status: Status{Runtime: runtime, Model: model, State: StateIdle, Since: m.clock.Now().UTC()}}
}

// stopChild stops a child with the one stop grace the manager configures. The
// probe, canary, tune and restart paths all stop a child the same way; the
// launch path reads the same setting once for the child's WaitDelay, which is a
// different use of the value.
func (in *instance) stopChild(c *child) {
	c.stop(in.m.cfg.Duration("sidecar.stop_timeout"), in.m.clock)
}

// touch records the last real use. Locate is the one entry point that
// hands out the endpoint, so every embed call touches the instance before
// ensure; the supervisor's idle policy reads it under the same lock.
func (in *instance) touch() {
	in.mu.Lock()
	in.lastUse = in.m.clock.Now()
	in.mu.Unlock()
}

// ensure starts a supervisor unless one runs, and returns the latch to
// wait on. After Stop closed the start gate, the latch is resolved with
// ErrStopped in place: resolve would take in.mu again.
func (in *instance) ensure() *latch {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.running {
		select {
		case <-in.cur.ch: // the previous supervisor gave up: try again
			in.cur = newLatch()
		default:
		}
		in.running = true
		in.gen++
		gen := in.gen
		if !in.m.startWorker(func() { in.supervise(gen) }) {
			in.running = false
			select {
			case <-in.cur.ch:
			default:
				in.cur.err = ErrStopped
				close(in.cur.ch)
			}
		}
	}
	return in.cur
}

// resolve closes the current latch (ready or given up).
func (in *instance) resolve(ep engine.Endpoint, err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	select {
	case <-in.cur.ch:
	default:
		in.cur.ep, in.cur.err = ep, err
		close(in.cur.ch)
	}
}

// rearm gives waiters a new latch while the runtime restarts.
func (in *instance) rearm() {
	in.mu.Lock()
	defer in.mu.Unlock()
	select {
	case <-in.cur.ch:
		in.cur = newLatch()
	default:
	}
}

// failed: a caller could not reach ep. If ep is what the latch hands out,
// waiters get a new latch (resolved again by the supervisor once it has
// checked or restarted the runtime) and the supervisor checks it now.
func (in *instance) failed(ep engine.Endpoint) {
	in.mu.Lock()
	stale := false
	select {
	case <-in.cur.ch:
		stale = in.cur.err == nil && in.cur.ep == ep
	default:
	}
	if stale {
		in.cur = newLatch()
	}
	in.mu.Unlock()
	if stale {
		in.m.log.Warn("runtime reported unreachable: checking it now", "runtime", in.runtime)
		in.poke(false)
	}
}

// poke wakes the supervisor (restart or just check), never blocking.
func (in *instance) poke(restart bool) {
	select {
	case in.pokes <- restart:
	default:
	}
}

func (in *instance) snapshot() Status {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.status
}

// set changes the status (fn edits a copy), publishes and logs it.
func (in *instance) set(state State, detail string, fn func(*Status)) {
	in.mu.Lock()
	st := in.status
	st.State, st.Detail, st.Since = state, detail, in.m.clock.Now().UTC()
	if fn != nil {
		fn(&st)
	}
	in.status = st
	in.mu.Unlock()
	in.publish(st)
}

// publish logs and publishes one status snapshot.
func (in *instance) publish(st Status) {
	log := in.m.log.With("runtime", st.Runtime, "model", st.Model, "variant", st.Variant, "state", st.State)
	switch st.State {
	case StateFailed, StateRestarting:
		log.Warn("runtime "+string(st.State), "detail", st.Detail, "restarts", st.Restarts)
	default:
		log.Info("runtime "+string(st.State), "detail", st.Detail, "pid", st.PID, "threads", st.Threads, "fallback", st.Fallback)
	}
	if in.m.bus != nil {
		events.Publish(in.m.bus, TopicState, st)
	}
}

// stopIdle ends this serve cleanly at the idle policy's request: it resolves
// the current latch with ErrStopped and clears the supervisor flag in one
// critical section, so the next Locate starts a new supervisor and a caller
// already waiting learns that this runtime is gone. It is not a failure: no
// restart count, backoff or broken-build mark is touched. The status is
// published while the lock is held so the stop cannot be overtaken by a
// restart's starting event.
func (in *instance) stopIdle(gen uint64, detail string) {
	in.mu.Lock()
	select {
	case <-in.cur.ch:
	default:
		in.cur.err = ErrStopped
		close(in.cur.ch)
	}
	if in.gen == gen {
		in.running = false
	}
	st := in.status
	st.State, st.Detail, st.Since = StateStopped, detail, in.m.clock.Now().UTC()
	st.PID = 0
	in.status = st
	in.publish(st)
	in.mu.Unlock()
}

// permanent errors end the supervisor at once: retrying cannot help until
// a setting, the pins or the files change.
func permanent(err error) bool {
	return errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrNoVulkan) ||
		errors.Is(err, ErrHash) || errors.Is(err, ErrStopped)
}
