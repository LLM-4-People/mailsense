package learning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Overall states of the app's learning.
const (
	Learning = "learning" // stages still running or pending
	Stuck    = "blocked"  // at least one stage needs action
	Learned  = "learned"  // every stage done: everything is allowed
)

// Status is the whole learning status: the one query of the learning
// screen, published on TopicStatus whenever it changes.
type Status struct {
	State  string        `json:"state"`
	Stages []StageStatus `json:"stages"`
	// Scope names the selection the status is for (sorted store keys):
	// once it is learned it is latched; a later selection that adds a
	// store closes the gate until that is learned too.
	Scope string    `json:"scope"`
	At    time.Time `json:"at"`
}

// TopicStatus carries every changed learning status.
var TopicStatus = events.NewTopic[Status]("learning.status")

// ErrLocked is returned (wrapped in *LockedError) by Allow while the
// picked mailboxes are not fully learned.
var ErrLocked = errors.New("learning: not available until the picked mailboxes are fully learned")

// LockedError carries the status that keeps the gate closed.
type LockedError struct{ Status Status }

func (e *LockedError) Error() string { return fmt.Sprintf("%v (state %s)", ErrLocked, e.Status.State) }
func (e *LockedError) Unwrap() error { return ErrLocked }

// Gate is what the user-facing features ask before answering.
type Gate interface {
	Allow(ctx context.Context) error
}

// Previewer is the one preview rule: the explicit test setting is on and the
// real gate is closed. Every screen that labels or substitutes an
// uncertified preview answer asks this rule; the setting alone must never
// relabel certified content as preview.
type Previewer interface {
	PreviewAnswers(ctx context.Context) bool
}

// Latch persists the scope that was fully learned once, so the app stays
// open while learning continues incrementally on new mail (store meta).
type Latch interface {
	LearnedScope(ctx context.Context) (string, error)
	SetLearnedScope(ctx context.Context, scope string) error
}

// Machine combines the stages into the learning status and is the Gate.
type Machine struct {
	stages []Stage
	scope  func() string
	latch  Latch
	bus    *events.Bus
	clock  clock.Clock
	log    *slog.Logger

	mu         sync.Mutex
	computeMu  sync.Mutex
	last       Status
	fresh      bool // last is current: no event since it was computed (Invalidate)
	refreshing bool
	// again is set when an invalidation lands while a rebuild is in
	// flight: the rebuild must run once more instead of leaving the cache
	// stale (the final index.finished must not be lost behind a progress
	// rebuild).
	again bool
	// revision moves on every Invalidate. A background compute started
	// under an older revision never installs its result as fresh.
	revision uint64
	rates    map[string]sample
	preview  func() bool

	// base is canceled by Close, so a cooperative stage read stops at once
	// instead of running out statusRefreshTimeout under a closing store.
	base context.Context
	// cancel is also the statusRefreshTimeout bound's parent; a nil cancel
	// means the machine has no shutdown contract (a zero-value Machine).
	cancel context.CancelFunc
	// jobs tracks the detached build of a stale status. The build reads the
	// stages after the request that started it returned, so only the machine
	// can join it before its store closes; a synchronous Status call is the
	// caller's own work and is never tracked.
	jobs *join.Group
}

var _ Previewer = (*Machine)(nil)

// statusRefreshTimeout bounds one background status rebuild.
const statusRefreshTimeout = 2 * time.Minute

// sample is a stage's first progress observation while running.
type sample struct {
	at   time.Time
	done int64
}

// New builds the machine. scope names the current learning scope (the
// picked mailboxes); bus and clk may be nil.
func New(scope func() string, latch Latch, bus *events.Bus, clk clock.Clock, stages ...Stage) (*Machine, error) {
	if scope == nil || latch == nil || len(stages) == 0 {
		return nil, errors.New("learning: scope, latch and stages are required")
	}
	seen := map[string]bool{}
	for _, s := range stages {
		if s == nil || s.ID() == "" || seen[s.ID()] {
			return nil, fmt.Errorf("learning: stages need unique ids (%v)", s)
		}
		seen[s.ID()] = true
	}
	base, cancel := context.WithCancel(context.Background())
	return &Machine{stages: stages, scope: scope, latch: latch, bus: bus, clock: clock.Or(clk), log: logx.For("learning"),
		rates: map[string]sample{}, base: base, cancel: cancel, jobs: join.New()}, nil
}

// Status returns the current status (every stage asked on a build), adds
// ETAs, latches a fully learned scope and publishes a change. A fresh
// status is returned directly; a stale status for the same scope is
// returned immediately while one background rebuild runs. Only the first
// build, or a scope change, blocks.
func (m *Machine) Status(ctx context.Context) (Status, error) {
	scope := m.scope()
	m.mu.Lock()
	last, fresh := m.last, m.fresh
	m.mu.Unlock()
	if !last.At.IsZero() && last.Scope == scope {
		if fresh {
			return last, nil
		}
		m.mu.Lock()
		if !m.refreshing && m.jobs.Track() {
			m.refreshing = true
			m.mu.Unlock()
			go m.refreshStatus(scope)
		} else {
			m.mu.Unlock()
		}
		return last, nil
	}
	return m.computeStatus(ctx, scope)
}

// computeStatus builds one status without touching the SWR decision. The
// caller must not hold mu.
func (m *Machine) computeStatus(ctx context.Context, scope string) (Status, error) {
	m.computeMu.Lock()
	defer m.computeMu.Unlock()

	m.mu.Lock()
	if m.fresh && m.last.Scope == scope && !m.last.At.IsZero() {
		st := m.last
		m.mu.Unlock()
		return st, nil
	}
	rev := m.revision
	m.mu.Unlock()

	now := m.clock.Now()
	st := Status{State: Learned, Scope: scope, At: now.UTC()}
	// One memo for this build: the stage fan-out reads the same stage
	// through several paths (the index directly, as layer 1's "after"
	// stage, and through the route's layer-1 stage), so each stage is
	// asked once. The memo never outlives the build.
	ctx = withStatusMemo(ctx)
	for _, s := range m.stages {
		ss, err := StatusOf(ctx, s)
		if err != nil {
			return Status{}, fmt.Errorf("learning: stage %s: %w", s.ID(), err)
		}
		ss.ID = s.ID()
		st.Stages = append(st.Stages, ss)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range st.Stages {
		m.eta(&st.Stages[i], now)
		switch st.Stages[i].State {
		case Blocked:
			st.State = Stuck
		case Done:
		default:
			if st.State == Learned {
				st.State = Learning
			}
		}
	}
	if st.State == Learned && st.Scope != "" {
		if err := m.latch.SetLearnedScope(ctx, st.Scope); err != nil {
			return Status{}, err
		}
	}
	if m.revision != rev {
		// An event arrived while the stages were being read. Return the
		// computed status but do not mark it fresh or publish it.
		return st, nil
	}
	if changed(m.last, st) {
		m.log.Info("learning status", "state", st.State, "stages", summary(st))
		if m.bus != nil {
			events.Publish(m.bus, TopicStatus, st)
		}
	}
	m.last, m.fresh = st, true
	return st, nil
}

// refreshStatus rebuilds one stale status off the request path. The build runs
// under the machine's base context — canceled by Close — bounded by
// statusRefreshTimeout, so shutdown never waits out a stage read. A result
// computed under an older revision is discarded by computeStatus; when an
// invalidation arrived meanwhile (again), it runs once more so the latest
// event is never lost behind an in-flight rebuild.
func (m *Machine) refreshStatus(scope string) {
	defer m.jobs.Done()
	ctx, cancel := context.WithTimeout(m.base, statusRefreshTimeout)
	defer cancel()
	for {
		_, err := m.computeStatus(ctx, scope)
		m.mu.Lock()
		again := m.again
		m.again = false
		if !again {
			m.refreshing = false
		}
		m.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("learning status refresh failed; serving the cached status", "err", err)
			}
			return
		}
		if !again {
			return
		}
		if ctx.Err() != nil {
			m.mu.Lock()
			m.refreshing = false
			m.mu.Unlock()
			return
		}
	}
}

// Close refuses new background rebuilds, cancels the running one and joins
// it, bounded by ctx. It reports whether it joined: false means a stage read
// outlived ctx, so the store owner should keep the store open. It is
// idempotent.
//
// A rebuild that started before Close and finishes after it never installs as
// fresh: Close moves the revision under the same lock a compute checks it
// under, exactly as any invalidation does, so the late result is returned to
// nobody. The status already installed stays readable, and a Status call
// after Close serves it without starting another detached rebuild.
func (m *Machine) Close(ctx context.Context) bool {
	if m == nil {
		// A partially wired app has no machine: there is nothing to join.
		return true
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	m.revision++
	m.fresh = false
	m.again = false
	m.mu.Unlock()
	if m.jobs == nil {
		return true
	}
	return m.jobs.Close(ctx)
}

// Invalidate marks the cached status stale (RefreshOn calls it for every
// event that can change a stage: a sync, a discovery, an index run, a
// settings change); the next read serves the old status and refreshes it.
// An invalidation during a rebuild schedules one more rebuild.
func (m *Machine) Invalidate() {
	m.mu.Lock()
	m.fresh = false
	m.revision++
	if m.refreshing {
		m.again = true
	}
	m.mu.Unlock()
}

// SetPreview sets the explicit, reversible test preview: while the
// callback returns true, Allow lets the automatic answer screens through
// even though the real gate is closed. It never changes Open, Status, the
// latch or the stages; a nil callback and false both disable it.
func (m *Machine) SetPreview(on func() bool) {
	m.mu.Lock()
	m.preview = on
	m.mu.Unlock()
}

// previewing reports the test preview without holding the lock while the
// callback reads configuration.
func (m *Machine) previewing() bool {
	m.mu.Lock()
	on := m.preview
	m.mu.Unlock()
	return on != nil && on()
}

// Previewing reports the explicit test preview setting. It is never the
// real gate (Open is) and grants nothing by itself.
func (m *Machine) Previewing() bool { return m.previewing() }

// PreviewAnswers reports whether preview values may substitute answers: the
// explicit setting is on and the real gate is closed. Once the gate is open
// the certified route wins even while the setting stays on; the setting alone
// must never relabel a certified answer as an uncertified best guess, and a
// broken rule is not a closed gate either. It is GateState's Preview, so the
// gate route and every in-process caller answer from one reading.
func (m *Machine) PreviewAnswers(ctx context.Context) bool { return m.GateState(ctx).Preview }

// Allow is what the user-facing features ask before answering. With the
// explicit test preview on it always succeeds (the screens show that the
// answers are uncertified); otherwise it is the real gate decision.
func (m *Machine) Allow(ctx context.Context) error {
	// The shortcut is deliberate: while the test preview is on, the answer is
	// allowed without reading the rule at all, so a broken latch cannot block
	// a screen the preview is meant to unblock.
	if m.previewing() {
		return nil
	}
	return m.GateState(ctx).Allow
}

// Open reports the real gate answer, ignoring the test preview entirely.
// A locked gate is (false, nil); only a real failure is an error.
func (m *Machine) Open(ctx context.Context) (bool, error) {
	gate := m.GateState(ctx)
	return gate.Open, gate.Err
}

// GateState is one consistent reading of the gate. Asking PreviewAnswers, Open
// and Allow separately evaluates the rule once per answer, so a learning event
// landing between them can produce a view that never existed: a preview from
// one state, an open flag from another, the allowance from a third.
type GateState struct {
	// Preview is what PreviewAnswers answers: the test setting is on and the
	// rule answered a lock. A broken rule is not a closed gate, so it does
	// not claim the preview.
	Preview bool
	// Open is what Open answers: the real gate with no failure.
	Open bool
	// Allow is what Allow answers: nil when answering is allowed (the real
	// gate, or the test preview), the typed lock when it is not, and any
	// other failure as-is.
	Allow error
	// Err is the rule failure Open reports: nil when the rule answered, open
	// or locked, and the underlying failure otherwise. It survives the test
	// preview, which hides the lock from Allow but never hides a broken
	// latch or status read.
	Err error
}

// GateState reads the rule once and derives the three answers a caller needs,
// so the gate route asks one question instead of three.
func (m *Machine) GateState(ctx context.Context) GateState {
	previewing := m.previewing()
	err := m.gate(ctx)
	open := err == nil
	allow := err
	raw := err
	if err != nil {
		var locked *LockedError
		if errors.As(err, &locked) {
			raw = nil
		}
	}
	preview := false
	if previewing {
		// A broken rule is neither open nor known-closed: the preview may
		// still let answers through (Allow), but it must not claim the gate is
		// closed, or a failed latch would relabel answers as uncertified.
		preview = !open && raw == nil
		allow = nil
	}
	return GateState{Preview: preview, Open: open, Allow: allow, Err: raw}
}

// gate is the real gate (the rule, user decision 2026-10-15): nil while
// the current selection is a non-empty subset of the last fully learned
// one (the latch) — learning goes on incrementally on new mail, and
// unpicking a store keeps the rest learned; any store added to the
// selection closes the gate until the new selection is learned as a
// whole. Otherwise a *LockedError with the status (cached while no event
// changed it).
func (m *Machine) gate(ctx context.Context) error {
	scope := m.scope()
	learned, err := m.latch.LearnedScope(ctx)
	if err != nil {
		return err
	}
	if subset(scope, learned) {
		return nil
	}
	st, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if st.State == Learned && st.Scope != "" {
		return nil
	}
	return &LockedError{Status: st}
}

// subset: every key of scope (comma-separated store keys) is in learned,
// and scope names at least one.
func subset(scope, learned string) bool {
	if scope == "" {
		return false
	}
	have := map[string]bool{}
	for _, k := range strings.Split(learned, ",") {
		have[k] = true
	}
	for _, k := range strings.Split(scope, ",") {
		if !have[k] {
			return false
		}
	}
	return true
}

// eta fills a running stage's ETA from its rate since it was first seen
// running; other states forget the sample.
func (m *Machine) eta(ss *StageStatus, now time.Time) {
	if ss.State != Running || ss.Progress.Total <= 0 {
		delete(m.rates, ss.ID)
		return
	}
	first, ok := m.rates[ss.ID]
	if !ok || ss.Progress.Done < first.done {
		m.rates[ss.ID] = sample{at: now, done: ss.Progress.Done}
		return
	}
	gained, took := ss.Progress.Done-first.done, now.Sub(first.at)
	if gained <= 0 || took <= 0 {
		return
	}
	left := time.Duration(float64(ss.Progress.Total-ss.Progress.Done) / float64(gained) * float64(took))
	left = left.Round(time.Second)
	ss.ETA = &left
}

// changed ignores the timestamp and the ETA (both move on every call).
func changed(a, b Status) bool {
	strip := func(s Status) Status {
		s.At = time.Time{}
		out := append([]StageStatus(nil), s.Stages...)
		for i := range out {
			out[i].ETA = nil
		}
		s.Stages = out
		return s
	}
	return !reflect.DeepEqual(strip(a), strip(b))
}

func summary(st Status) string {
	out := ""
	for _, s := range st.Stages {
		out += fmt.Sprintf("%s=%s(%d/%d) ", s.ID, s.State, s.Progress.Done, s.Progress.Total)
	}
	return out
}
