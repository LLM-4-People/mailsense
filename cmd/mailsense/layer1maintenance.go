package main

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// layer1ReconcileInterval is how often the startup worker re-asks for the
// run the inputs need when no event arrives. The check is one fingerprint
// and one last-run read; the trigger itself decides whether anything runs.
const layer1ReconcileInterval = 5 * time.Minute

// startLayer1Reconcile launches the one background worker that starts the
// layer-1 run a restart owes. It waits for the warm like the other
// maintenance workers and never blocks API readiness; the returned stop
// cancels it and waits.
func startLayer1Reconcile(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		maintainLayer1(ctx, layer1ReconcileInterval, a.layer1.Ensure, a.jobs)
	})
}

// maintainLayer1 is the worker behind startLayer1Reconcile: one reconcile at
// the start, then a low-frequency one. It has no error path of its own: the
// trigger gate logs a run it cannot start and the next reconcile retries.
func maintainLayer1(ctx context.Context, interval time.Duration, ensure func(*jobs.Manager), m *jobs.Manager) {
	if interval <= 0 {
		interval = layer1ReconcileInterval
	}
	ensure(m)
	repeatEvery(ctx, interval, func() bool { ensure(m); return true })
}
