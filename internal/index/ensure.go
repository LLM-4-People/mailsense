package index

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// Ensure reconciles the semantic index with the mail it is meant to cover:
// it builds the configured embedder, plans the work and returns the current
// coverage. When actionable mail is pending or stale it starts exactly one
// background job through the same single-run guard as Start, so a restart, a
// provider-space change or a failed run heals without waiting for the next
// sync. A run already in progress is not duplicated: Ensure returns that
// run's starting coverage without planning, and the current run and the next
// reconcile own the remaining work. An unbuildable embedder is returned
// explicitly (the caller logs and throttles it); Ensure never calls a
// provider itself.
//
// After a run stopped because the embedder was unavailable and wrote
// nothing, Ensure returns that run's starting coverage without planning or
// starting anything until index.retry_unavailable ends. The wait is
// process-local: any effective settings change, a healthy embedding
// provider, a ready managed runtime, a new mail generation, a new space or
// text version, a changed role scope or quarantine threshold ends it. The
// latest unavailable run stays in index_runs, so the stage keeps reporting
// its blocker; nothing is hidden and no work is dropped.
func (ix *Indexer) Ensure(ctx context.Context, m *jobs.Manager) (Coverage, error) {
	e, err := ix.src.Embedder()
	if err != nil {
		return Coverage{}, err
	}
	key := ix.retryKeyFor(e.Space(), ix.textVersion())
	if cov, waiting := ix.retryActive(key, ix.clock.Now()); waiting {
		return cov, nil
	}
	// The run slot is the one owner of the work: while a run holds it, a
	// second plan would only rescan the writes that run is making and could
	// not start anything. Stage.Status keeps reporting the live progress of
	// that run; the next reconcile plans again.
	if ix.single.Busy() {
		return ix.runningCoverage(e.Space()), nil
	}
	work, cov, err := ix.plan(ctx, e.Space(), Options{})
	if err != nil {
		return cov, err
	}
	// Quarantined and partial messages are counted as pending or stale but
	// are not actionable: a run would select nothing. Starting one for them
	// on every reconcile would only add empty jobs.
	if len(work) == 0 {
		ix.clearRetry()
		return cov, nil
	}
	if m == nil {
		return cov, fmt.Errorf("index: reconcile needs a jobs manager")
	}
	if !ix.single.Begin() {
		return cov, nil
	}
	j, err := ix.startClaimed(m, Options{})
	if err != nil {
		return cov, err
	}
	ix.log.Info("index catch-up started", "job", j.Info().ID, "space", cov.Space, "eligible", cov.Eligible,
		"indexed", cov.Indexed, "stale", cov.Stale, "pending", cov.Pending, "selected", len(work))
	return cov, nil
}
