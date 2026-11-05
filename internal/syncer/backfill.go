package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// BackfillJobKind is the jobs kind of one display backfill: the stored HTML
// body and the inline images it references.
const BackfillJobKind = "messages.html"

// ErrBackfillBusy is returned when an HTML backfill is already running.
// HTML backfills are one at a time across sources: two would read the same
// rows twice and compete for the source's single COM thread.
var ErrBackfillBusy = errors.New("syncer: an HTML backfill is already running")

// StartBackfill runs the one-time display-HTML pass of a source as a
// background job. It reads the source's stored, still-unknown messages
// through the connector's DetailReader, one bounded batch at a time, and
// stores only the HTML columns (never the plain body, revision or search
// index). A source that cannot re-read a message is reported as skipped, not
// failed; an item the source refuses is skipped and counted; a source-level
// failure ends the job with an error the user can retry. The sync claim is
// deliberately not taken: a backfill may run beside a sync (Outlook
// serialises its own COM operations), and a sync is never blocked by it.
func (s *Syncer) StartBackfill(m *jobs.Manager, c source.Connector) (*jobs.Job, error) {
	if !s.backfill.Begin() {
		return nil, ErrBackfillBusy
	}
	return jobs.StartClaimed(&s.backfill, m, BackfillJobKind, c.ID(),
		func(ctx context.Context, report func(jobs.Progress)) error {
			return s.backfillBodies(ctx, c, report)
		})
}

func (s *Syncer) backfillBodies(ctx context.Context, c source.Connector, report func(jobs.Progress)) error {
	total, err := s.store.CountBackfill(ctx, c.ID())
	if err != nil {
		return output(err)
	}
	reportBackfill(report, 0, total, "counting messages")
	dr, ok := c.(source.DetailReader)
	if !ok {
		if total > 0 {
			s.log.Info("display backfill skipped: this source cannot re-read a message", "source", c.ID(), "messages", total)
		}
		reportBackfill(report, total, total,
			fmt.Sprintf("%d message(s) skipped: this source cannot re-read a message", total))
		return nil
	}
	assetsKnown := c.Capabilities().Attachments

	batch := max(s.cfg.Int(SettingDetailsBatch), 1)
	maxLog := s.cfg.Int(SettingMaxLoggedSkips)
	var done, updated, skipped int
	var cursor store.BackfillCursor
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		refs, err := s.store.BackfillNewest(ctx, c.ID(), cursor, batch)
		if err != nil {
			return output(err)
		}
		if len(refs) == 0 {
			break
		}
		updates := make([]store.BackfillUpdate, 0, len(refs))
		assetBytes := 0
		var unavailable error
		for _, ref := range refs {
			if err := ctx.Err(); err != nil {
				return err
			}
			cursor = store.BackfillCursor{Instant: ref.Instant, RowID: ref.RowID}
			d, err := dr.ReadDetails(ctx, ref.Key)
			var ie *source.ItemError
			switch {
			case errors.As(err, &ie):
				done, skipped = done+1, skipped+1
				if skipped <= maxLog {
					s.log.Warn("message display data unreadable; skipped", "source", c.ID(), "code", ie.Code)
				}
				if errors.Is(err, source.ErrUnavailable) {
					unavailable = err
				}
			case err != nil:
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("syncer: display backfill of %s: %w", c.ID(), err)
			default:
				done++
				if !d.BodyHTMLKnown {
					skipped++
				} else {
					for _, a := range d.Attachments {
						assetBytes += len(a.Data)
					}
					updates = append(updates, store.BackfillUpdate{Key: ref.Key, HTML: d.BodyHTML,
						Known: true, Truncated: d.BodyHTMLTruncated,
						Attachments: d.Attachments, AssetsKnown: assetsKnown})
				}
			}
			if unavailable != nil || assetBytes >= assetBatchBytes {
				break
			}
		}
		// Every read of this batch is committed, including when the source
		// then turns out to be unavailable: one-time work is never lost.
		applyCtx := ctx
		if unavailable != nil {
			applyCtx = context.WithoutCancel(ctx)
		}
		n, err := s.store.ApplyBackfill(applyCtx, c.ID(), updates)
		if err != nil {
			return output(err)
		}
		updated += n
		skipped += len(updates) - n // rows removed or filled since they were listed
		reportBackfill(report, done, total, fmt.Sprintf("%d updated, %d skipped", updated, skipped))
		if unavailable != nil {
			return fmt.Errorf("syncer: display backfill of %s: %w", c.ID(), unavailable)
		}
	}
	if updated+skipped < total {
		// Rows that were removed or became invisible while the job ran.
		skipped = total - updated
	}
	msg := fmt.Sprintf("%d message(s) updated, %d skipped", updated, skipped)
	reportBackfill(report, total, total, msg)
	s.log.Info("display backfill finished", "source", c.ID(), "updated", updated, "skipped", skipped, "total", total)
	return nil
}

func reportBackfill(report func(jobs.Progress), done, total int, message string) {
	report(jobs.Progress{Done: int64(done), Total: int64(total), Unit: learning.UnitMessages,
		Stage: "backfill", Message: message})
}
