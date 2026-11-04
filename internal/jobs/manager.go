package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Manager starts and tracks jobs.
type Manager struct {
	cfg   *config.Config
	clock clock.Clock
	bus   *events.Bus
	log   *slog.Logger
	guard *resource.Guard
	// deadlines is the per-kind wall-clock policy (deadlines.go). It is a
	// field so a test can replace it with sub-second bounds; production
	// always takes defaultDeadlinePolicy.
	deadlines deadlinePolicy
	mu        sync.Mutex
	jobs      map[string]*Job
	order     []string
	seq       int
	// group is the one shutdown join of the started jobs: Track admits a job,
	// Done retires it, and Shutdown's Close waits for the rest.
	group join.Group
}

// Option configures the manager without changing every caller.
type Option func(*Manager)

// WithGuard makes the resource guard the one admission and pacing owner of
// every job this manager starts.
func WithGuard(g *resource.Guard) Option {
	return func(m *Manager) { m.guard = g }
}

// NewManager returns a manager; bus and clk may be nil (no events; wall clock).
func NewManager(cfg *config.Config, bus *events.Bus, clk clock.Clock, opts ...Option) *Manager {
	m := &Manager{cfg: cfg, clock: clock.Or(clk), bus: bus, log: logx.For("jobs"), jobs: map[string]*Job{},
		deadlines: defaultDeadlinePolicy}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Start runs fn in the background under every resource ceiling. The guard is
// checked before a job exists: a host above a ceiling refuses the start with
// a typed error, so the caller and the UI can say why nothing ran. A running
// job is paced by its own progress reports instead.
//
// The job is also bounded by the deadline of its kind (deadlines.go): a phase
// that never reports progress cannot hold its slot forever.
func (m *Manager) Start(kind, name string, fn Func) (*Job, error) {
	return m.start(resource.AdmitAll, m.deadlineFor(kind), kind, name, nil, fn)
}

// StartEssential runs a job whose work must keep going while the host is
// busy: mail fetching is the app's core promise, so a CPU or memory spike
// must not stop new mail from arriving. The disk ceiling still applies,
// because that is the one a write can exhaust. Heavy analysis uses Start.
//
// An essential job has no deadline: how long new mail takes is the mailbox's
// answer, not the manager's, and stopping a sync mid-folder drops its cursor
// advance. It is still cancellation- and shutdown-bounded.
func (m *Manager) StartEssential(kind, name string, fn Func) (*Job, error) {
	return m.start(resource.AdmitWrites, noDeadline, kind, name, nil, fn)
}

// deadlineFor resolves a kind's bound through the manager's policy. A nil
// policy (a Manager built as a literal in a test) falls back to the shipped
// table rather than leaving every job unbounded.
func (m *Manager) deadlineFor(kind string) time.Duration {
	if m.deadlines == nil {
		return defaultDeadlinePolicy(kind)
	}
	return m.deadlines(kind)
}

// startClaimed is the entry point of StartClaimed: the caller already holds
// the Single slot, so finalize releases it exactly once through release, even
// when the deadline finalizes the record while the body is still blocked.
func (m *Manager) startClaimed(s *Single, kind, name string, run func(context.Context, func(Progress)) error) (*Job, error) {
	return m.start(resource.AdmitAll, m.deadlineFor(kind), kind, name, s.End, run)
}

func (m *Manager) start(admission resource.Admission, deadline time.Duration, kind, name string, release func(), fn Func) (*Job, error) {
	if fn == nil || kind == "" {
		return nil, errors.New("jobs: kind and func are required")
	}
	if m.guard != nil {
		paused, err := m.guard.CheckAdmission(context.Background(), admission)
		switch {
		case err != nil:
			m.log.Debug("resource guard readings unavailable; starting the job", "err", err)
		case paused != nil:
			m.log.Info("job refused by the resource guard", "kind", kind, "resource", string(paused.Kind),
				"percent", paused.Percent, "limit", paused.Limit)
			return nil, paused
		}
	}
	m.mu.Lock()
	if !m.group.Track() {
		m.mu.Unlock()
		return nil, ErrShutdown
	}
	m.seq++
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{cancel: cancel, ctx: ctx, deadline: deadline, release: release, done: make(chan struct{}), admission: admission,
		info: Info{ID: "job-" + strconv.Itoa(m.seq), Kind: kind, Name: name, Status: runstate.Running, StartedAt: m.clock.Now().UTC()}}
	m.jobs[j.info.ID] = j
	m.order = append(m.order, j.info.ID)
	m.mu.Unlock()

	m.log.Info("job started", "job", j.info.ID, "kind", kind, "name", name)
	m.publish(j.Info())
	go m.run(ctx, j, fn, deadline)
	return j, nil
}

// run drives one job body. Two paths can end it: the body returning, or the
// deadline expiring while the body is still blocked. The deadline finalizes
// the record itself, so a body that ignores its context can no longer hold
// its Single slot, its terminal record or the shutdown join forever; the
// body's own later return finds finalize already spent.
func (m *Manager) run(ctx context.Context, j *Job, fn Func, deadline time.Duration) {
	defer m.group.Done()
	// The deadline is wall time by definition, so it is a real timer, not
	// the clock seam that paces work: a fake clock's Sleep advances time
	// instantly and would expire every job at once. Tests shorten the bound
	// through Manager.deadlines instead.
	if deadline > 0 {
		timer := time.AfterFunc(deadline, func() { m.expire(j) })
		defer timer.Stop()
	}
	err := m.call(ctx, j, fn)
	// One lock decides who owns the record: a body that returned first keeps
	// its own outcome, and a body that returned after the expiry yields to
	// the expiry. Without this, the state would depend on goroutine timing.
	j.mu.Lock()
	j.bodyReturned = true
	expired := j.expiring
	j.mu.Unlock()
	if expired {
		m.finalize(j, nil, true)
		return
	}
	m.finalize(j, err, false)
}

// expire is the deadline path: it claims the job under the lock (so a body
// that already returned keeps its own outcome), cancels the context so a
// cooperative body stops at its next check, then finalizes so a body that
// ignores its context still ends as a bounded, truthful record instead of
// holding its slot.
func (m *Manager) expire(j *Job) {
	j.mu.Lock()
	if j.bodyReturned {
		j.mu.Unlock()
		return
	}
	j.expiring = true
	j.cancel()
	j.mu.Unlock()
	m.finalize(j, nil, true)
}

// finalize writes the terminal record exactly once, in the order consumers
// rely on: one log line, one publish, slot release, then Done. It is the
// record's single writer: the body returning and the deadline expiring both
// call it, and finalizeOnce decides which one owns the outcome.
//
// The body's goroutine is what Shutdown joins, so a body that ignores its
// context still delays the join; it can no longer, however, hold the job's
// record or its Single slot, and its later reports are dropped.
func (m *Manager) finalize(j *Job, err error, expired bool) {
	j.finalizeOnce.Do(func() {
		j.mu.Lock()
		if expired {
			// The manager stopped this job, so the honest record is its own
			// deadline, not whatever the body happened to return after it.
			err = &DeadlineError{Kind: j.info.Kind, Deadline: j.deadline,
				Elapsed: m.clock.Now().UTC().Sub(j.info.StartedAt)}
			// Classify against the cancelled context, so the terminal state
			// is cancelled: the manager stopped the work, it did not fail on
			// its own. runstate is the one owner of that rule.
			j.cancel()
		}
		j.info.FinishedAt = m.clock.Now().UTC()
		j.info.Status = runstate.Of(j.ctx, err)
		j.info.Paused = nil
		j.err = err
		if err != nil {
			// A job error can quote a provider body or a mail value. The
			// job record is published on the API and SSE, so it keeps the
			// redacted text, not the raw error.
			j.info.Error = logx.Redact(err.Error())
			// A connector's stable cause is the one fact a bilingual screen
			// can render without parsing the wrapper chain. The interface is
			// structural, so jobs does not import the source package.
			var caused interface{ Cause() (string, string) }
			if errors.As(err, &caused) {
				if code, _ := caused.Cause(); code != "" {
					j.info.Cause = code
				}
			}
		}
		j.finished = true
		info := j.info
		// Publish under the same lock a report holds: the finished flag and
		// the terminal record are one atomic transition for consumers, so a
		// report can never be published after it.
		m.publish(info)
		j.mu.Unlock()

		// One line per job, as before. An expiry adds the kind, the bound
		// and the elapsed time on that same line at warn level.
		level := slog.LevelInfo
		attrs := []any{"job", info.ID, "status", info.Status, "err", info.Error,
			"duration", info.FinishedAt.Sub(info.StartedAt)}
		switch {
		case expired:
			level = slog.LevelWarn
			attrs = append(attrs, "kind", info.Kind, "deadline", j.deadline,
				"elapsed", info.FinishedAt.Sub(info.StartedAt))
		case info.Status == runstate.Failed:
			level = slog.LevelError
		}
		m.log.Log(context.Background(), level, "job finished", attrs...)
		m.prune()

		// The slot is released after the terminal record: a queued run may
		// start now, and it must see this one finished.
		if j.release != nil {
			j.release()
		}
		j.cancel()
		close(j.done)
	})
}

// call runs fn, turning a panic into a failure (logged with its stack). It
// owns the ETA: every report is measured against the job's own average rate,
// and a stage that changes the denominator starts a new rate, so one phase
// never measures another (the classifier's questions then its messages).
func (m *Manager) call(ctx context.Context, j *Job, fn Func) error {
	baseAt, baseDone, baseTotal := m.clock.Now(), int64(0), int64(0)
	err := Safe(func() error {
		return fn(ctx, func(p Progress) {
			now := m.clock.Now()
			if p.Total != baseTotal {
				baseAt, baseDone, baseTotal = now, p.Done, p.Total
			}
			if p.ETA == nil {
				p.ETA = etaFor(baseAt, now, p.Done-baseDone, p.Total-p.Done)
			}
			j.mu.Lock()
			if j.finished {
				// The deadline already finalized this job; a report that
				// arrives afterwards must not rewrite or republish its
				// record. The publish happens under mu, so the terminal
				// record can never be followed by a stale "running" one.
				j.mu.Unlock()
				return
			}
			j.info.Progress = p
			m.publish(j.info)
			j.mu.Unlock()
			// Pacing belongs after the report, before the producer starts
			// its next unit: the UI sees the progress that was just made,
			// then the job waits for the host to fall under the ceiling.
			m.pace(ctx, j)
		})
	})
	var p *PanicError
	if errors.As(err, &p) {
		// A panic value can carry mail content. The type and stack identify
		// the failure; the value itself never reaches the log ring.
		m.log.Error("job panicked", "job", j.info.ID, "panic_type", fmt.Sprintf("%T", p.Value), "stack", p.Stack)
		return fmt.Errorf("jobs: %w", err)
	}
	return err
}

// pace blocks the job while the host is above a resource ceiling. The guard
// caches its reading for resource.sample_interval, so a job that reports per
// message does not sample the host per message. Sleep is the clock seam, so
// tests advance deterministically instead of waiting on the wall clock.
func (m *Manager) pace(ctx context.Context, j *Job) {
	if m.guard == nil {
		return
	}
	m.guard.WaitAdmission(ctx, j.admission, func(paused *resource.LimitError) {
		m.setPaused(j, paused)
	})
}

// setPaused publishes a pause transition once, not on every report while the
// same ceiling holds. Like a report, the publish happens under mu so the
// terminal record is never followed by a stale pause.
func (m *Manager) setPaused(j *Job, paused *resource.LimitError) {
	j.mu.Lock()
	if j.finished {
		// A deadline finalized the record; a late pace transition must not
		// republish it or reinstate a pause.
		j.mu.Unlock()
		return
	}
	before := j.info.Paused
	if samePause(before, paused) {
		j.mu.Unlock()
		return
	}
	j.info.Paused = paused
	info := j.info
	m.publish(info)
	j.mu.Unlock()
	switch {
	case paused != nil:
		m.log.Info("job paused by the resource guard", "job", info.ID, "resource", string(paused.Kind),
			"percent", paused.Percent, "limit", paused.Limit)
	case before != nil:
		m.log.Info("job resumed: the host is under the resource limit", "job", info.ID, "resource", string(before.Kind))
	}
}

func samePause(a, b *resource.LimitError) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Kind == b.Kind && a.Limit == b.Limit
}

// etaFor is the time left when gained items took the elapsed time and
// remaining are left (nil: no rate yet, or nothing left).
func etaFor(base, now time.Time, gained, remaining int64) *time.Duration {
	if gained <= 0 || remaining <= 0 {
		return nil
	}
	took := now.Sub(base)
	if took <= 0 {
		return nil
	}
	left := time.Duration(float64(took) / float64(gained) * float64(remaining)).Round(time.Second)
	if left < time.Second {
		left = time.Second
	}
	return &left
}

func (m *Manager) publish(info Info) {
	if m.bus != nil {
		events.Publish(m.bus, TopicJob, info)
	}
}

// prune drops the oldest finished jobs beyond jobs.history_size.
func (m *Manager) prune() {
	limit := m.cfg.Int("jobs.history_size")
	m.mu.Lock()
	defer m.mu.Unlock()
	var finished []string
	for _, id := range m.order {
		if m.jobs[id].Info().Status != runstate.Running {
			finished = append(finished, id)
		}
	}
	drop := map[string]bool{}
	for _, id := range finished[:max(0, len(finished)-limit)] {
		delete(m.jobs, id)
		drop[id] = true
	}
	m.order = slices.DeleteFunc(m.order, func(id string) bool { return drop[id] })
}

// Get returns a job by id.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns snapshots of all tracked jobs, oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.jobs[id].Info())
	}
	return out
}

// CancelAll refuses new jobs and cancels every running one without waiting.
// It is the part of shutdown that lets a handler waiting on a job return
// before the API join deadline.
func (m *Manager) CancelAll() {
	m.group.Refuse()
	m.mu.Lock()
	for _, j := range m.jobs {
		j.Cancel()
	}
	m.mu.Unlock()
}

// Shutdown refuses new jobs, cancels running ones and waits for them (or ctx).
// A job that outlives ctx is reported as ErrUnjoined while keeping the
// context error, so callers can tell an unjoined job from a joined one.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.CancelAll()
	if !m.group.Close(ctx) {
		return fmt.Errorf("%w: %w", ErrUnjoined, ctx.Err())
	}
	return nil
}
