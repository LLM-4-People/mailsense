package syncer

import (
	"context"
	"log/slog"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Scheduler is the one owner of automatic fetching: when sync.auto is on it
// starts the picked sources' syncs immediately and again every
// sync.auto.interval. It owns cadence only; the Syncer owns the run, the
// source claim (single-flight) and the status a read side trusts.
//
// Polling is the bounded design: Outlook has no reliable cross-process push
// subscription, and a local poll of one cheap pass per interval is cheaper
// and more robust than a COM event sink that dies with the session. Failure
// pacing stays with the source breaker, not here: a closed Outlook fails
// fast and the next interval retries, so opening it needs no manual action.
type Scheduler struct {
	sync       *Syncer
	cfg        *config.Config
	jobs       *jobs.Manager
	connectors func() []source.Connector
	log        *slog.Logger
	now        func() time.Time
	full       bool
}

// NewScheduler builds the automatic sync loop. connectors is the source
// registry's current list (read on every tick, so a newly wired source is
// picked up without a restart).
func NewScheduler(s *Syncer, cfg *config.Config, m *jobs.Manager, connectors func() []source.Connector) *Scheduler {
	return &Scheduler{sync: s, cfg: cfg, jobs: m, connectors: connectors, log: logx.For("syncer"), now: time.Now, full: true}
}

// Run fetches immediately, waits for every started run to finish, then rests
// for one interval before the next check. Waiting for completion is what keeps
// a long run from being followed by an immediate restart on a fixed grid: the
// effective period is run duration + rest, never two overlapping passes.
// Shutdown joins the loop without starting another run; a run already started
// belongs to the jobs manager and is cancelled by it.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		started := s.attempt(ctx)
		if !waitJobs(ctx, started) {
			return
		}
		if !s.wait(ctx, s.cfg.Duration(SettingAutoInterval)) {
			return
		}
	}
}

// waitJobs blocks until every started job finishes or shutdown is requested.
func waitJobs(ctx context.Context, started []*jobs.Job) bool {
	for _, j := range started {
		select {
		case <-ctx.Done():
			return false
		case <-j.Done():
		}
	}
	return true
}

func (s *Scheduler) wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// attempt starts one auto-sync when enabled and no source is already
// syncing. The single-flight check is a courtesy log only: StartPicked itself
// maps ErrSourceBusy to a benign skip, so a run that starts between the check
// and the call is still safe.
func (s *Scheduler) attempt(ctx context.Context) []*jobs.Job {
	if err := ctx.Err(); err != nil {
		return nil
	}
	if !s.cfg.Bool(SettingAuto) {
		return nil
	}
	if s.sync.Busy() {
		s.log.Debug("automatic sync skipped: a sync is already running")
		return nil
	}
	var started []*jobs.Job
	var err error
	if s.full {
		// The first pass after a restart also refreshes the world model; the
		// steady state must not re-read calendars and address books every
		// interval, so every later pass is mail-only. It has no snapshot in
		// hand, so StartPicked reads the selection itself (nil).
		started, err = s.sync.StartPicked(ctx, s.jobs, s.connectors(), nil)
	} else {
		started, err = s.sync.StartPickedMail(ctx, s.jobs, s.connectors())
	}
	if err != nil {
		s.log.Warn("automatic sync did not start", "err", err)
		return nil
	}
	if len(started) == 0 {
		return nil
	}
	s.full = false
	s.log.Debug("automatic sync triggered")
	return started
}
