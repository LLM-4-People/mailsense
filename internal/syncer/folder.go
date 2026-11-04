package syncer

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// folderSync is one folder's outcome: the report, the complete snapshot's
// absence candidates (nil when the connector delivered none or its snapshot
// was incomplete) and the next cursor to stage. Only the absent row ids and
// the stored count are kept, not the snapshot's presence set. The cursor is
// committed only after the run's removal pass succeeds.
type folderSync struct {
	report   FolderReport
	complete bool    // the connector delivered a complete snapshot
	removal  removal // the absent candidates and stored count (complete only)
	next     source.Cursor
}

// syncFolder runs one folder through the connector. It never writes the
// cursor: the caller stages the returned one after the removal pass.
func (s *Syncer) syncFolder(ctx context.Context, runID int64, c source.Connector, f mail.Folder, keys map[string]bool, report func(Progress)) (folderSync, error) {
	// One folder is the coalescing boundary of the mail generation: every
	// message batch it writes invalidates derived views once, at the end,
	// instead of once per sync.batch_size.
	release := s.store.HoldMailWrites()
	defer release()
	fr := FolderReport{Path: f.Path, Role: f.Role, StoreName: f.StoreName}
	fail := func(err error) (folderSync, error) {
		fr.Error = err.Error()
		s.log.Error("folder sync failed", "folder", f.Path, "err", err)
		return folderSync{report: fr}, err
	}
	f.SourceID = c.ID()
	folderID, err := s.store.UpsertFolder(ctx, f)
	if err != nil {
		return fail(output(err))
	}
	fr.FolderID = folderID
	cursor, _, err := s.store.Cursor(ctx, folderID)
	if err != nil {
		return fail(output(err))
	}
	snap := s.cfg.Snapshot()
	sink := &batchSink{s: s, clock: s.clock, runID: runID, sourceID: c.ID(), storeID: f.StoreID, folderID: folderID, folder: f.Path, keys: keys,
		batch: snap.Int(SettingBatchSize), maxLog: snap.Int(SettingMaxLoggedSkips), interval: snap.Duration(SettingProgressInterval),
		report: report}
	sink.emit() // started
	next, err := c.Sync(ctx, f, source.Cursor(cursor), sink)
	if err == nil {
		err = sink.flush(ctx)
	}
	fr.Totals, fr.Snapshot = sink.totals, sink.snapshotSize
	if err != nil {
		return fail(err)
	}
	fr.Unchanged = cursor != "" && string(next) == cursor && sink.totals.Seen == 0
	if err := s.check(ctx, folderID, runID, sink.totals); err != nil {
		return fail(err)
	}
	sink.emit()
	s.log.Info("folder synced", "folder", f.Path, "seen", fr.Totals.Seen, "written", fr.Totals.Written,
		"skipped", fr.Totals.Skipped, "duplicates", fr.Totals.Duplicates, "snapshot", fr.Snapshot, "unchanged", fr.Unchanged)
	return folderSync{report: fr, complete: sink.complete,
		removal: removal{storeID: f.StoreID, folderID: folderID, path: f.Path, absent: sink.absent, stored: sink.stored},
		next:    next}, nil
}

// check applies the skip-ratio rule and reconciles the counters with the database.
func (s *Syncer) check(ctx context.Context, folderID, runID int64, t store.RunTotals) error {
	if ratio := s.cfg.Float(SettingSkipRatioFatal); t.Skipped > 0 && float64(t.Skipped)/float64(t.Seen) >= ratio {
		return fmt.Errorf("syncer: %d of %d items skipped (%.2f%%), at or above sync.skip_ratio_fatal %.2f%%",
			t.Skipped, t.Seen, 100*float64(t.Skipped)/float64(t.Seen), 100*ratio)
	}
	if t.Seen != t.Written+t.Skipped+t.Duplicates {
		return fmt.Errorf("syncer: counts do not reconcile: seen %d ≠ written %d + skipped %d + duplicates %d",
			t.Seen, t.Written, t.Skipped, t.Duplicates)
	}
	stored, err := s.store.CountSeenInRun(ctx, folderID, runID)
	if err != nil {
		return output(err)
	}
	if stored != t.Written {
		return fmt.Errorf("syncer: counts do not reconcile: %d written but %d stored for this run", t.Written, stored)
	}
	return nil
}
