package main

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// startAutoSync launches the one automatic-fetch owner after the startup
// warm has finished. It starts the picked sources immediately, then on
// sync.auto.interval; a disabled setting keeps the loop alive so turning it
// on in Settings takes effect on the next check without a restart. The
// returned stop cancels the loop and waits before the jobs manager and the
// store shut down.
func startAutoSync(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		syncer.NewScheduler(a.syncer, a.cfg, a.jobs, a.registry.List).Run(ctx)
	})
}
