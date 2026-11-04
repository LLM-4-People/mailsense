package main

import (
	"context"
	"errors"
	"time"

	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// startStoreMaintenance launches the one store housekeeping worker after
// the warm is ready: it prunes old run history, bounds the WAL sidecar and
// refreshes the planner statistics. It never blocks API readiness; the
// returned stop cancels it and waits, so a closed store is never touched.
func startStoreMaintenance(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		storeMaintain(ctx, a.cfg.Duration(store.SettingMaintainEvery), a.st, a.cfg.Int(store.SettingRetainRuns))
	})
}

// storeMaintenanceTimeout bounds one maintenance pass. Each prune batch is
// short, but a checkpoint can still wait on another writer; a timed-out pass
// is best-effort, and the next interval retries it. It never cancels the
// worker's own lifetime context.
const storeMaintenanceTimeout = 2 * time.Minute

// storeMaintain runs one pass immediately (a restart may be the first
// chance to reclaim a WAL left by the previous process), then on the
// configured interval.
func storeMaintain(ctx context.Context, interval time.Duration, st *store.Store, keepRuns int) {
	log := logx.For("store")
	run := func() bool {
		passCtx, cancel := context.WithTimeout(ctx, storeMaintenanceTimeout)
		rep, err := st.Maintain(passCtx, keepRuns)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn("store maintenance timed out; the next interval retries", "timeout", storeMaintenanceTimeout, "err", err)
				return true
			}
			log.Warn("store maintenance failed", "err", err)
			return true
		}
		if rep.Checkpoint || rep.RunsDeleted > 0 {
			log.Info("store maintenance", "wal_bytes", rep.WALBytes, "checkpoint", rep.Checkpoint, "runs_deleted", rep.RunsDeleted)
		}
		return true
	}
	if !run() {
		return
	}
	repeatEvery(ctx, interval, run)
}
