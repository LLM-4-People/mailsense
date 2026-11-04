package triage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// Settings the package owns. They are read from the live configuration on
// every event, so turning triage.auto off stops the next run without a
// restart.
const (
	SettingAuto     = "triage.auto"
	SettingBatch    = "triage.auto.batch"
	SettingInterval = "triage.auto.interval"
	// DeciderSetting names the provider/model/fingerprint the decider runs.
	DeciderSetting = "triage.decider"
	// SettingLeaseTTL is how long a run's lease is held before another
	// process may take the store over.
	SettingLeaseTTL = "triage.lease_ttl"
)

// probeTimeout bounds the automatic trigger's existence probe. A probe is one
// indexed EXISTS over the candidate predicate; a store that cannot answer
// within this bound leaves the request pending for the next report (and the
// periodic catch-up) instead of holding the trigger's generation open.
const probeTimeout = 30 * time.Second

// AfterSync starts a bounded triage run after every sync run that advanced
// the stored mail (syncer.Report.Advanced) while triage.auto is on. The
// events feed one coalescing worker keyed by the store's mail generation: an
// advanced report only records a request, and three reports for the same
// generation share one cheap pending probe. A report that arrives while a
// probe or a run is in flight leaves exactly one pending request, served
// when that work ends. The full core and plan are built by the admitted run,
// never on the event path. stop unsubscribes, cancels the worker and joins
// it.
func (t *Triager) AfterSync(bus *events.Bus, m *jobs.Manager) (stop func(), err error) {
	if bus == nil || m == nil {
		return nil, errors.New("triage: automatic triage needs an event bus and a job manager")
	}
	return newSyncTrigger(t, m).watch(bus)
}

// StartIfPending starts one bounded run only when the store's cheap pending
// probe finds work. The probe reads one EXISTS ... LIMIT 1 row and composes
// no state; the plan and coverage are built once, by the admitted run
// itself. It is the periodic catch-up's entry point; the AfterSync worker
// probes once per mail generation and admits the run directly.
func (t *Triager) StartIfPending(ctx context.Context, m *jobs.Manager, o Options) (*jobs.Job, error) {
	pending, err := t.hasPending(ctx, o)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, nil
	}
	return t.Start(m, o)
}

// syncTrigger is the one worker behind AfterSync. Its probe and start fields
// are the seams tests use to observe a scan or a refused admission;
// production always runs Triager.hasPending and Triager.Start. The worker
// owns every mutable field except pending (guarded by mu) and requested
// (atomic).
type syncTrigger struct {
	t *Triager
	m *jobs.Manager

	probe        func(context.Context, Options) (bool, error)
	start        func(*jobs.Manager, Options) (*jobs.Job, error)
	probeTimeout time.Duration

	// wake coalesces requests: its one slot means a burst is one wake, and
	// pending records whether the worker still has work to serve. requested
	// counts accepted advanced reports (including coalesced ones) for tests.
	wake      chan struct{}
	mu        sync.Mutex
	pending   bool
	requested atomic.Uint64

	// served is the mail generation an admitted run (or a no-work probe)
	// covers. A generation already served is skipped without a scan.
	served     uint64
	haveServed bool
	// haveProbed holds the one probe result awaiting admission: the
	// generation it was taken at found pending work, but the run was refused
	// (busy or guard-paused). A later report for the same generation retries
	// the admission without scanning again.
	haveProbed bool
	probedGen  uint64

	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

func newSyncTrigger(t *Triager, m *jobs.Manager) *syncTrigger {
	return &syncTrigger{t: t, m: m, probe: t.hasPending, start: t.Start,
		probeTimeout: probeTimeout, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// watch subscribes to finished syncs and starts the worker. The returned stop
// is idempotent: it unsubscribes first (so no new request can arrive), then
// cancels and joins the worker.
func (tr *syncTrigger) watch(bus *events.Bus) (stop func(), err error) {
	onStop, err := events.On(bus, syncer.TopicFinished, tr.t.cfg.EventBuffer(), func(rep syncer.Report) {
		if rep.Advanced() && tr.t.cfg.Bool(SettingAuto) {
			tr.request()
		}
	})
	if err != nil {
		return nil, err
	}
	tr.ctx, tr.cancel = context.WithCancel(context.Background())
	go tr.loop()
	var once sync.Once
	return func() {
		once.Do(func() {
			onStop()
			tr.cancel()
			<-tr.done
		})
	}, nil
}

// request records one pending pass. The handler does no store work: it only
// marks the request and wakes the worker, which reads the current mail
// generation when it runs.
func (tr *syncTrigger) request() {
	tr.requested.Add(1)
	tr.mu.Lock()
	tr.pending = true
	tr.mu.Unlock()
	select {
	case tr.wake <- struct{}{}:
	default:
	}
}

// take consumes the pending request (there is one worker, so it owns it).
func (tr *syncTrigger) take() bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	waiting := tr.pending
	tr.pending = false
	return waiting
}

// requeue leaves a failed request for the next report without a wake: the
// worker must not retry on its own, or a paused admission would spin.
func (tr *syncTrigger) requeue() {
	tr.mu.Lock()
	tr.pending = true
	tr.mu.Unlock()
}

func (tr *syncTrigger) loop() {
	defer close(tr.done)
	for {
		select {
		case <-tr.ctx.Done():
			return
		case <-tr.wake:
		}
		if !tr.handle() {
			return
		}
	}
}

// handle serves one coalesced request. It runs on the worker only.
func (tr *syncTrigger) handle() bool {
	if !tr.take() {
		return true
	}
	o, ok := tr.options()
	if !ok {
		return true // triage.auto is off: the request is dropped
	}
	gen := tr.t.st.MailGeneration()
	if tr.haveServed && gen == tr.served {
		return true // nothing was written since the probe that served this generation
	}
	if !(tr.haveProbed && tr.probedGen == gen) {
		pending, err := tr.scan(o)
		if err != nil {
			if tr.ctx.Err() != nil {
				return false
			}
			tr.t.log.Warn("triage pending probe failed", "err", err)
			tr.requeue()
			return true
		}
		if !pending {
			tr.served, tr.haveServed = gen, true
			tr.haveProbed = false
			return true
		}
		tr.haveProbed, tr.probedGen = true, gen
	}
	j, err := tr.start(tr.m, o)
	if err != nil {
		if tr.ctx.Err() == nil && !errors.Is(err, ErrTriageBusy) {
			tr.t.log.Warn("triage run not started", "after", "sync", "err", err)
		}
		// The probe result stays valid for this generation: the next report
		// retries the admission without another scan.
		tr.requeue()
		return true
	}
	// The admitted run plans and serves this generation. A report that
	// arrives while it runs stays pending and becomes one follow-up.
	tr.served, tr.haveServed = gen, true
	tr.haveProbed = false
	tr.t.log.Info("triage started automatically", "after", "sync", "job", j.Info().ID, "limit", o.Limit)
	select {
	case <-tr.ctx.Done():
		return false
	case <-j.Done():
		return true
	}
}

// options reads the live automatic settings; false means automatic triage is
// off now.
func (tr *syncTrigger) options() (Options, bool) {
	if !tr.t.cfg.Bool(SettingAuto) {
		return Options{}, false
	}
	return Options{Limit: AutoBatch(tr.t.cfg)}, true
}

// AutoBatch resolves the batch size of one automatic run from the live
// configuration. The schema owns the default (500) and the minimum (1); this
// accessor restates neither, so a change to the default changes the automatic
// batch with no second number to keep in step.
func AutoBatch(cfg *config.Config) int { return cfg.Int(SettingBatch) }

// AutoInterval resolves the catch-up interval from the live configuration. The
// schema owns the default (5m) and the minimum (30s).
func AutoInterval(cfg *config.Config) time.Duration { return cfg.Duration(SettingInterval) }

// scan runs the existence probe under a bounded context, never the event
// path's background context.
func (tr *syncTrigger) scan(o Options) (bool, error) {
	ctx, cancel := context.WithTimeout(tr.ctx, tr.probeTimeout)
	defer cancel()
	return tr.probe(ctx, o)
}
