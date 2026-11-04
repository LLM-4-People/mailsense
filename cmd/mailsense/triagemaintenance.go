package main

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

// startTriageMaintenance launches the one automatic triage catch-up worker
// after the warm is ready. It never blocks API readiness, and the returned
// stop cancels it and waits, so shutdown never leaves a run reading a closed
// store.
func startTriageMaintenance(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		maintainTriage(ctx, triage.AutoInterval(a.cfg), a.triage, a.jobs, triage.AutoBatch(a.cfg))
	})
}

// maintainTriage runs one catch-up immediately, then on a low-frequency
// ticker. StartIfPending probes the store first and lets the admitted run
// plan, so an idle mailbox creates no job and the worker stays cheap.
// Repeated provider-unavailable warnings are throttled until a run succeeds
// or the failure changes.
func maintainTriage(ctx context.Context, interval time.Duration, t *triage.Triager, m *jobs.Manager, batch int) {
	log := logx.For("triage")
	warned := ""
	wait := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interval):
			return true
		}
	}
	for ctx.Err() == nil {
		j, err := t.StartIfPending(ctx, m, triage.Options{Limit: batch})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err.Error() != warned {
				log.Warn("automatic triage catch-up failed", "err", logx.RedactedError(err))
				warned = err.Error()
			}
			if !wait() {
				return
			}
			continue
		}
		warned = ""
		if j == nil {
			// Nothing pending: the ticker is the fallback for new mail.
			if !wait() {
				return
			}
			continue
		}
		log.Info("automatic triage catch-up started", "job", j.Info().ID, "limit", batch)
		// A batch that stored decisions chains the next one at once: a
		// backlog drains at the model's pace, not at one batch per interval.
		// The run's outcome decides whether that can make progress.
		select {
		case <-ctx.Done():
			return
		case <-j.Done():
		}
		info := j.Info()
		// Chain only after a run that stored work. A failed run may have
		// stored nothing (provider down) or only failures (a rejected
		// message); re-planning at once would select the same rows and bill
		// them again. The existing catch-up interval is the pacing owner.
		if info.Status == runstate.Failed || info.Status == runstate.Cancelled || info.Progress.Done == 0 {
			if !wait() {
				return
			}
		}
	}
}
