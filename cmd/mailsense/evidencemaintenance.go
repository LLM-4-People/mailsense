package main

import (
	"context"
	"errors"
	"time"

	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// evidenceReconcileInterval is how often the startup worker re-checks the
// evidence inputs when no event arrives. It is low frequency on purpose: the
// check is one fingerprint and one query, and it only exists to heal a
// restart or a run that could not start.
const evidenceReconcileInterval = 5 * time.Minute

// startEvidenceReconcile launches the one background worker that starts an
// evidence run a restart owes. It waits for the warm (ready) like the other
// maintenance workers and never blocks API readiness; the returned stop
// cancels it and waits.
func startEvidenceReconcile(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		maintainEvidence(ctx, evidenceReconcileInterval, a.evidence.Ensure, a.jobs)
	})
}

// maintainEvidence is the worker behind startEvidenceReconcile: one reconcile
// at the start, then a low-frequency one. A busy engine is not a failure (a
// run is already reading the same inputs); any other error is reported once
// per distinct message, and the loop keeps the app self-healing.
func maintainEvidence(ctx context.Context, interval time.Duration,
	ensure func(context.Context, *jobs.Manager) (*jobs.Job, error), m *jobs.Manager) {
	log := logx.For("evidence")
	if interval <= 0 {
		interval = evidenceReconcileInterval
	}
	warned := ""
	reconcile := func() bool {
		j, err := ensure(ctx, m)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if errors.Is(err, evidence.ErrBusy) {
				return true
			}
			if err.Error() != warned {
				log.Warn("evidence catch-up failed", "err", logx.RedactedError(err))
				warned = err.Error()
			}
			return true
		}
		warned = ""
		if j != nil {
			log.Info("evidence catch-up started", "job", j.Info().ID)
		}
		return true
	}
	if !reconcile() {
		return
	}
	repeatEvery(ctx, interval, reconcile)
}
