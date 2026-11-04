package main

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// startWhenReady launches one background worker after the startup warm has
// finished (ready closes) or returns on shutdown. Every maintenance worker
// shares this ready-wait, done-channel and bounded join; the returned stop
// cancels the worker and waits at most shutdownJoinTimeout, so a cooperative
// worker is always joined and a non-cooperative one is reported instead of
// hanging shutdown before the API and the store can close.
func startWhenReady(shutdown *shutdownRequest, ready <-chan struct{}, run func(context.Context)) func() {
	ctx, cancel := shutdown.requestContext()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		run(ctx)
	}()
	return func() {
		cancel()
		stopWorker(shutdown, done, shutdownJoinTimeout)
	}
}

// stopWorker waits for one worker to finish after its context was canceled.
// A worker that outlives the bound marks the run unjoined: the store and the
// instance lock then stay open, because closing either under a live worker is
// worse than leaving the process to exit with its files intact. It reports
// whether the worker joined.
func stopWorker(shutdown *shutdownRequest, done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		shutdown.MarkUnjoined()
		logx.For("shutdown").Warn("background worker did not join before shutdown", "timeout", timeout)
		return false
	}
}

// repeatEvery calls step on every tick until ctx is done or step reports
// false. The caller runs its first pass before calling this, so a worker can
// do setup (a warm-up) between that pass and the loop. It is the one ticker
// loop of the maintenance workers.
func repeatEvery(ctx context.Context, interval time.Duration, step func() bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !step() {
				return
			}
		}
	}
}
