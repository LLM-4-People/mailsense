package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// firstOpenDiscovery performs one best-effort discovery pass on an install
// whose store has never listed a mailbox and whose selection is empty. It
// lists stores only: it never reads mail, picks a mailbox or repeats. A
// source already being discovered is benign (ErrSourceBusy).
func firstOpenDiscovery(ctx context.Context, st *store.Store, cfg *config.Config, s *syncer.Syncer, connectors []source.Connector) error {
	views, err := st.MailboxViews(ctx)
	if err != nil {
		return err
	}
	if len(views) > 0 || len(source.Selected(cfg.Snapshot())) > 0 {
		return nil
	}
	log := logx.For("startup")
	var errs []error
	for _, c := range connectors {
		if _, err := s.Discover(ctx, c); err != nil {
			if errors.Is(err, syncer.ErrSourceBusy) {
				log.Info("first-open discovery skipped: the source is already being discovered", "source", c.ID())
				continue
			}
			log.Warn("first-open mailbox discovery failed", "source", c.ID(), "err", logx.RedactedError(err))
			errs = append(errs, fmt.Errorf("%s: %w", c.ID(), err))
		}
	}
	return errors.Join(errs...)
}

// firstOpenRun is the discovery pass startFirstOpenDiscovery runs. Production
// always points at firstOpenDiscovery; tests replace it with a blocking pass
// to prove shutdown joins the real background task.
var firstOpenRun = firstOpenDiscovery

// startFirstOpenDiscovery launches firstOpenRun after the warm has finished
// (ready closes) or returns on shutdown. The returned stop cancels the pass
// and waits for it, so the caller joins it before the store closes.
func startFirstOpenDiscovery(a *app, shutdown *shutdownRequest, ready <-chan struct{}) func() {
	return startWhenReady(shutdown, ready, func(ctx context.Context) {
		if err := firstOpenRun(ctx, a.st, a.cfg, a.syncer, a.registry.List()); err != nil && !errors.Is(err, context.Canceled) {
			logx.For("startup").Warn("first-open mailbox discovery failed", "err", logx.RedactedError(err))
		}
	})
}
