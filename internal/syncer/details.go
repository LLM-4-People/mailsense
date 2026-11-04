package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// assetBatchBytes bounds the decoded inline-image bytes one read batch
// holds. It is one package-level memory bound, shared by the sync detail pass
// and the display backfill: both stop filling a batch at the same point and
// leave the remaining keys for the next batch, so neither an image-heavy
// detail run nor a one-time backfill can hold hundreds of megabytes of
// pending updates at once.
const assetBatchBytes = 64 << 20

// syncDetails completes the source's partial messages, newest first, in
// batches of sync.details.batch_size (one store transaction each); at most
// sync.details.max_per_run per run (0: all), and only messages received
// at least sync.details.settle_after ago (the source may still be
// processing newer ones). The load on the source is paced by its connector
// (Outlook: outlook.pace.*), never here. An item-level failure leaves the
// message partial (retried next run); any other connector error stops the
// pass and fails the run. An item whose read made the source fail as a
// whole (a quarantine item error) is set aside first (store: skipped
// until it changes or RetryDetails), then the run ends. Stored data is
// never lost: every batch is committed as it completes — the reads of a
// batch done before the source failed included. sync.details.enabled off:
// no detail read at all.
func (s *Syncer) syncDetails(ctx context.Context, runID int64, c source.Connector, dr source.DetailReader, report func(Progress)) (*DetailReport, error) {
	start := s.clock.Now()
	snap := s.cfg.Snapshot()
	pass := &detailPass{s: s, c: c, dr: dr, runID: runID, rep: &DetailReport{Failures: map[string]int{}},
		failed: map[source.ItemRef]bool{}, maxLog: snap.Int(SettingMaxLoggedSkips), batch: snap.Int(SettingDetailsBatch),
		limit: snap.Int(SettingDetailsMaxPerRun)}
	if d := snap.Duration(SettingDetailsSettleAfter); d > 0 {
		pass.settled = start.Add(-d)
	}
	var err error
	if pass.rep.Disabled = !snap.Bool(SettingDetailsEnabled); !pass.rep.Disabled {
		err = pass.run(ctx, report)
	}
	pass.rep.Duration = s.clock.Now().Sub(start)
	keep := context.WithoutCancel(ctx)
	if n, cerr := s.store.CountPending(keep, c.ID(), time.Time{}); cerr == nil {
		pass.rep.Pending = n
	}
	if n, cerr := s.store.CountQuarantined(keep, c.ID()); cerr == nil {
		pass.rep.Quarantined = n
		pass.rep.Pending += n // still partial: waiting too, for the user's retry or a change
	}
	s.log.Info("details synced", "source", c.ID(), "completed", pass.rep.Completed, "failed", pass.rep.Failed,
		"pending", pass.rep.Pending, "set_aside", pass.rep.Quarantined, "capped", pass.rep.Capped, "disabled", pass.rep.Disabled,
		"duration", pass.rep.Duration, "err", err)
	return pass.rep, err
}

// detailPass is one run's detail phase.
type detailPass struct {
	s       *Syncer
	c       source.Connector
	dr      source.DetailReader
	runID   int64
	rep     *DetailReport
	failed  map[source.ItemRef]bool // item failures of this run (not retried in it)
	maxLog  int
	batch   int
	limit   int       // most items read in this run (0: no cap)
	settled time.Time // only messages received before it (zero: all)
}

func (p *detailPass) run(ctx context.Context, report func(Progress)) error {
	// A detail pass is many ApplyDetails transactions; hold the mail
	// generation so the whole pass invalidates derived views once.
	release := p.s.store.HoldMailWrites()
	defer release()
	total, err := p.s.store.CountPending(ctx, p.c.ID(), p.settled)
	if err != nil {
		return output(err)
	}
	if p.limit > 0 && total > p.limit {
		total, p.rep.Capped = p.limit, true
	}
	progress := func() {
		p.s.progress(Progress{RunID: p.runID, SourceID: p.c.ID(), Phase: source.PhaseDetails,
			Position: source.Progress{Done: int64(p.rep.Completed + p.rep.Failed), Total: int64(total), Unit: "items"}}, report)
	}
	progress() // started
	for total > 0 {
		n := p.batch
		if p.limit > 0 {
			if n = min(n, p.limit-p.rep.Completed-p.rep.Failed); n <= 0 {
				return nil
			}
		}
		keys, err := p.s.store.PendingDetails(ctx, p.c.ID(), n+len(p.failed), p.settled)
		if err != nil {
			return output(err)
		}
		if keys = keysExcept(keys, p.failed, n); len(keys) == 0 {
			return nil
		}
		updates, rerr := p.read(ctx, keys)
		done, err := p.s.store.ApplyDetails(context.WithoutCancel(ctx), p.runID, p.c.ID(), updates)
		if err != nil {
			return output(err)
		}
		p.rep.Completed += done
		progress()
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// read reads one batch; item failures are counted, anything else stops
// (the reads done so far are returned with the error). A quarantine item
// error is set aside in the store before it stops the pass.
func (p *detailPass) read(ctx context.Context, keys []source.ItemRef) ([]store.DetailUpdate, error) {
	updates := make([]store.DetailUpdate, 0, len(keys))
	assetBytes := 0
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return updates, err
		}
		d, err := p.dr.ReadDetails(ctx, k)
		var ie *source.ItemError
		switch {
		case errors.As(err, &ie):
			p.failed[k] = true
			p.rep.Failed++
			p.rep.Failures[ie.Code]++
			if ie.Quarantine {
				if qerr := p.s.store.QuarantineDetails(context.WithoutCancel(ctx), p.c.ID(), k, ie.Code); qerr != nil {
					return updates, output(qerr)
				}
				p.s.log.Error("reading this message's details made the source fail; it is set aside until it changes or the user retries",
					"item", k.ID, "code", ie.Code, "err", ie.Err)
			} else if p.rep.Failed <= p.maxLog {
				p.s.log.Warn("item details unreadable; it stays partial", "item", k.ID, "code", ie.Code, "err", ie.Err)
			}
			if errors.Is(err, source.ErrUnavailable) {
				return updates, fmt.Errorf("syncer: details of %s: %w", p.c.ID(), err)
			}
		case err != nil:
			if ctx.Err() == nil {
				err = fmt.Errorf("syncer: details of %s: %w", p.c.ID(), err)
			}
			return updates, err
		default:
			p.altered(k, d.Alterations)
			updates = append(updates, store.DetailUpdate{Key: k, Details: d})
			for _, a := range d.Attachments {
				assetBytes += len(a.Data)
			}
		}
		if assetBytes >= assetBatchBytes {
			break // the keys still pending are read by the next batch
		}
	}
	return updates, nil
}

// altered counts a detail read's declared alterations (the first ones
// of each code are logged, like skips).
func (p *detailPass) altered(k source.ItemRef, alts []mail.Alteration) {
	for _, a := range alts {
		if p.rep.Altered == nil {
			p.rep.Altered = map[string]int{}
		}
		p.rep.Altered[a.Code]++
		if p.rep.Altered[a.Code] <= p.maxLog {
			args := append([]any{"item", k.ID}, alterationAttrs(a)...)
			p.s.log.Info("message altered while its details were read", args...)
		}
	}
}

// keysExcept drops the keys that already failed in this run and keeps at most n.
func keysExcept(keys []source.ItemRef, failed map[source.ItemRef]bool, n int) []source.ItemRef {
	out := keys[:0]
	for _, k := range keys {
		if !failed[k] && len(out) < n {
			out = append(out, k)
		}
	}
	return out
}

// RetryDetails is the user's action on the messages of a source set aside
// after their detail read made the source fail: the next run reads them
// again. It returns how many were set aside.
func (s *Syncer) RetryDetails(ctx context.Context, sourceID string) (int, error) {
	n, err := s.store.ClearQuarantine(ctx, sourceID)
	if err == nil {
		s.log.Info("set-aside details will be read again", "source", sourceID, "messages", n)
	}
	return n, err
}
