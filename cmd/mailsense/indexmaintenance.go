package main

import (
	"context"
	"errors"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// indexReconcileInterval is how often the startup worker re-checks the
// semantic index when no sync event arrives. It is low frequency on purpose:
// the first full index of a large mailbox takes a long time, and a stalled or
// failed run must heal without an event, but the reconcile must never spin.
const indexReconcileInterval = 2 * time.Minute

// startSemanticMaintenance launches the one background worker that keeps the
// semantic side warm after the warm has finished (ready closes): one
// reconcile through Indexer.Ensure, one query warm-up, then a low-frequency
// reconcile loop. It never blocks API readiness or the warm; the returned
// stop cancels it and waits, so the runtime is never stopped under a warm-up
// that is still reading it.
func startSemanticMaintenance(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		maintainSemanticIndex(ctx, indexReconcileInterval, a.index.Ensure, a.search.Warm, a.jobs)
	})
}

// maintainSemanticIndex is the startup/reconcile worker behind
// startSemanticMaintenance. The first Ensure runs immediately; the warm-up
// follows it once so the first user query does not pay the runtime load. The
// loop then re-runs Ensure every interval, because a restart, a provider-space
// change or a failed/stalled run leaves work that no sync event announces.
// Repeated embedder-unavailable warnings are throttled until a successful
// reconcile (or until the failure changes).
func maintainSemanticIndex(ctx context.Context, interval time.Duration,
	ensure func(context.Context, *jobs.Manager) (index.Coverage, error),
	warm func(context.Context) error, m *jobs.Manager) {
	log := logx.For("index")
	if interval <= 0 {
		interval = indexReconcileInterval
	}
	unavailableWarned := false
	var last index.Coverage
	haveCoverage := false
	reconcile := func() bool {
		cov, err := ensure(ctx, m)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			unavailable := errors.Is(err, engine.ErrUnavailable)
			if !unavailable || !unavailableWarned {
				log.Warn("semantic index catch-up failed", "err", err)
			} else {
				log.Debug("semantic index catch-up still failing", "err", err)
			}
			unavailableWarned = unavailable
			return true
		}
		unavailableWarned = false
		if !haveCoverage || cov.Counts != last.Counts {
			log.Info("semantic index coverage", "indexed", cov.Indexed, "eligible", cov.Eligible,
				"pending", cov.Pending, "stale", cov.Stale, "quarantined", cov.Quarantined, "space", cov.Space)
		} else {
			log.Debug("semantic index coverage unchanged", "indexed", cov.Indexed, "eligible", cov.Eligible,
				"pending", cov.Pending, "stale", cov.Stale)
		}
		last, haveCoverage = cov, true
		return true
	}
	if !reconcile() {
		return
	}
	if warm != nil {
		if err := warm(ctx); err != nil && ctx.Err() == nil {
			if errors.Is(err, engine.ErrUnavailable) {
				// The learning status already reports the missing embedder;
				// a startup warm must not repeat that warning.
				log.Debug("semantic search warm-up skipped: the embedder is unavailable", "err", err)
			} else {
				log.Warn("semantic search warm-up failed", "err", err)
			}
		}
	}
	repeatEvery(ctx, interval, reconcile)
}
