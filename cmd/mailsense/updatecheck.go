package main

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/update"
)

// startUpdateCheck launches the release-detection worker after the warm is
// ready. The first pass runs the persisted interval gate: a first install
// checks at once, while a restart inside update.check.interval makes no
// request. Later passes wake on a shorter cadence than the interval, so the
// jitter cannot delay a due check by a whole interval. The returned stop
// cancels the worker and joins it before the store and the API close.
func startUpdateCheck(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		run := func() bool {
			if _, err := a.update.MaybeCheck(ctx); err != nil && ctx.Err() == nil {
				logx.For("update").Warn("update check state was not recorded", "err", logx.RedactedError(err))
			}
			return true
		}
		if !run() {
			return
		}
		repeatEvery(ctx, updateCheckTick(a.cfg.Duration(update.SettingCheckInterval)), run)
	})
}

// updateCheckTick is the worker's wake-up cadence, not the check cadence:
// update.check.interval owns how often a check runs. One quarter of the
// interval, clamped to [1m, 1h], keeps a wake-up cheap while a jitter of up
// to 10% of the interval cannot push the next check a whole tick late.
func updateCheckTick(interval time.Duration) time.Duration {
	return min(max(interval/4, time.Minute), time.Hour)
}
