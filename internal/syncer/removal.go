package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// removal is what one synced folder's complete snapshot says is there.
type removal struct {
	index    int    // into Report.Folders, -1 for a folder that is gone
	storeID  string // the folder's store; also set for a folder that is gone
	folderID int64  // stored folder
	path     string
	// absent is every stored row id missing from the complete snapshot,
	// computed when the snapshot was taken; stored is how many messages the
	// folder held then. Only these ids and that count are retained until the
	// removal pass. A vanished folder (index < 0) carries no absence set: the
	// removal pass reads its whole row set.
	absent []int64
	stored int
}

// HeldRemovals is published when the safety valve holds removals.
type HeldRemovals struct {
	SourceID string `json:"source_id"`
	FolderID int64  `json:"folder_id"`
	Path     string `json:"path"`
	Held     int    `json:"held"`
	Stored   int    `json:"stored"`
}

// TopicHeld carries held removals (the UI asks the user to confirm them).
var TopicHeld = events.NewTopic[HeldRemovals]("sync.removals_held")

// applyRemovals removes what vanished from complete snapshots, checks the
// stored folders discovery no longer returned (gone → removed; still there
// but not synced → inactive). Each complete snapshot's absence candidates
// were computed at the snapshot boundary (Snapshot); this pass only
// classifies and applies them. Moves and anonymous messages go through the
// mass-removal valve; an absence with a Message-ID and no survivor is held
// one complete run first (removeAbsent). A store whose view is incomplete
// keeps its message rows: an id missing from one folder may have moved into
// the folder that failed or delivered an incomplete snapshot. Vanished
// folders are still handled unchanged.
func (s *Syncer) applyRemovals(ctx context.Context, c source.Connector, discovered []mail.Folder, rems []removal, complete map[string]bool, full bool, rep *Report) error {
	var gone []removal
	if full {
		var err error
		if gone, err = s.vanishedFolders(ctx, c, discovered); err != nil {
			return err
		}
	}
	for _, r := range append(rems, gone...) {
		// A store whose view is incomplete keeps every message row,
		// including those of a folder discovery no longer returned: a
		// message may have moved into the folder that failed. A store with
		// no discovered folder at all has no entry, so its gone folders are
		// still removed (an entirely vanished store is not a partial view).
		if known, ok := complete[r.storeID]; ok && !known {
			continue
		}
		ids, stored := r.absent, r.stored
		if r.index < 0 { // a vanished folder: every stored row is absent
			var err error
			ids, stored, err = s.store.AbsentMessages(ctx, r.folderID, nil)
			if err != nil {
				return output(err)
			}
		}
		rm, err := s.removeAbsent(ctx, c.ID(), r, ids, stored, rep.RunID)
		if err != nil {
			return output(err)
		}
		if r.index >= 0 {
			rep.Folders[r.index].Removed = rm
		}
		rep.Removed = rep.Removed.Add(rm)
		if r.index < 0 && rm.Held == 0 {
			if err := s.store.DeleteFolder(ctx, r.folderID); err != nil {
				return output(err)
			}
		}
	}
	return nil
}

// removeAbsent reconciles one folder's complete snapshot. ids are the stored
// rows that were missing from it when the snapshot was taken, stored the
// messages the folder held then. Every candidate is classified once
// (ClassifyAbsences):
//
//   - a same-store row with the same Message-ID is a move: labels are copied
//     and the old row removed now;
//   - an empty Message-ID has nothing to reconcile and is removed now;
//   - a nonempty Message-ID with no survivor yet is held for one complete run.
//     Folders are snapshotted one after another, so a message that moves
//     during the run can be absent from the destination's earlier snapshot and
//     from the source's later one; no destination row exists when the removal
//     pass runs. The next run either finds the survivor (a move, labels
//     copied) or removes a genuinely deleted message. The hold is released on
//     every run for the folder's current absent set, so a message that comes
//     back is never held against a stale absence.
//
// Moves and anonymous messages still go through the mass-removal valve: a
// removal of sync.removal.hold_count or more messages, or of more than
// sync.removal.hold_ratio of a folder holding at least hold_count messages,
// is held (a shrunken local cache or an OST rebuild looks exactly like a
// mass delete). Held removals are applied once they persisted for
// sync.removal.auto_apply_after (0: only when confirmed: ConfirmRemovals).
// The one-run move hold is a separate policy and never waits for that
// confirmation.
func (s *Syncer) removeAbsent(ctx context.Context, sourceID string, r removal, ids []int64, stored int, runID int64) (store.Removal, error) {
	if err := s.store.ReleaseMoveHolds(ctx, r.folderID, ids); err != nil {
		return store.Removal{}, err
	}
	if len(ids) == 0 {
		return store.Removal{}, s.store.ReleaseHeld(ctx, r.folderID)
	}
	policy := s.removalPolicy()
	// The mass-removal valve owns the whole absence set whenever its
	// threshold triggers. A shrunken cache or an OST rebuild must stay in
	// held_removals and the owner confirmation path even when every row has
	// a Message-ID that a survivor lookup cannot explain yet. The one-run
	// move hold only guards small removals; it must never demote a mass
	// hold into an automatic next-run delete.
	if policy.massHold(len(ids), stored) {
		return s.applyRemovalValve(ctx, sourceID, r, ids, stored, policy)
	}
	moves, unexplained, anonymous, err := s.store.ClassifyAbsences(ctx, ids)
	if err != nil {
		return store.Removal{}, err
	}
	if len(moves)+len(anonymous) == 0 {
		// Nothing in this snapshot belongs to the mass-removal valve, so no
		// mass hold can still describe it: release a stale one (a message
		// that came back, or one now handled by the one-run move hold) before
		// the move holds are recorded.
		if err := s.store.ReleaseHeld(ctx, r.folderID); err != nil {
			return store.Removal{}, err
		}
	}
	var rm store.Removal
	if applied := append(moves, anonymous...); len(applied) > 0 {
		rm, err = s.applyRemovalValve(ctx, sourceID, r, applied, stored, policy)
		if err != nil {
			return store.Removal{}, err
		}
	}
	ripe, err := s.store.HoldMoveSuspects(ctx, r.folderID, unexplained, runID)
	if err != nil {
		return store.Removal{}, err
	}
	if len(ripe) > 0 {
		removed, err := s.store.RemoveMessages(ctx, ripe)
		if err != nil {
			return store.Removal{}, err
		}
		rm = rm.Add(removed)
	}
	if held := len(unexplained) - len(ripe); held > 0 {
		s.log.Info("removals held one run for a possible move between folders", "folder", r.path, "held", held, "run", runID)
		rm.Held += held
	}
	return rm, nil
}

// removalPolicy is one snapshot of the removal settings for one absence
// decision: the mass-hold threshold and the auto-apply grace. The whole-set
// check and the subset application read the same values, so a concurrent
// settings write cannot make them disagree.
type removalPolicy struct {
	holdCount int
	ratio     float64
	after     time.Duration
}

func (s *Syncer) removalPolicy() removalPolicy {
	snap := s.cfg.Snapshot()
	return removalPolicy{
		holdCount: snap.Int(SettingRemovalHoldCount),
		ratio:     snap.Float(SettingRemovalHoldRatio),
		after:     snap.Duration(SettingRemovalAutoApplyAfter),
	}
}

// massHold reports whether an absence set is large enough for the
// mass-removal safety valve.
func (p removalPolicy) massHold(count, stored int) bool {
	return count >= p.holdCount || (stored >= p.holdCount && float64(count) > p.ratio*float64(stored))
}

// applyRemovalValve removes ids unless the folder's mass-removal safety valve
// holds them (a shrunken local cache or an OST rebuild looks exactly like a
// mass delete). Held removals are applied once they persisted for
// sync.removal.auto_apply_after, or when the owner confirms them.
func (s *Syncer) applyRemovalValve(ctx context.Context, sourceID string, r removal, ids []int64, stored int, policy removalPolicy) (store.Removal, error) {
	if !policy.massHold(len(ids), stored) {
		if err := s.store.ReleaseHeld(ctx, r.folderID); err != nil {
			return store.Removal{}, err
		}
		return s.store.RemoveMessages(ctx, ids)
	}
	due := ""
	if policy.after > 0 {
		due = s.clock.Now().Add(-policy.after).UTC().Format(mail.TimeLayout)
	}
	ripe, err := s.store.HoldRemovals(ctx, r.folderID, ids, due)
	if err != nil {
		return store.Removal{}, err
	}
	rm, err := s.store.RemoveMessages(ctx, ripe)
	rm.Held = len(ids) - len(ripe)
	if err == nil && rm.Held > 0 {
		s.log.Warn("removals held by the safety valve (confirm, or they apply once sync.removal.auto_apply_after passed)",
			"folder", r.path, "held", rm.Held, "stored", stored, "applied", len(ripe))
		if s.bus != nil {
			events.Publish(s.bus, TopicHeld, HeldRemovals{SourceID: sourceID, FolderID: r.folderID, Path: r.path, Held: rm.Held, Stored: stored})
		}
	}
	return rm, err
}

// vanishedFolders asks the connector about every stored folder discovery
// did not return: a gone one is returned for removal, a not-synced one is
// marked inactive (mail kept, outside scope), an unknown one is left alone;
// an unavailable source (or cancellation) ends the check and the run.
func (s *Syncer) vanishedFolders(ctx context.Context, c source.Connector, discovered []mail.Folder) ([]removal, error) {
	prober, ok := c.(source.FolderProber)
	if !ok {
		return nil, nil
	}
	seen := map[[2]string]bool{}
	for _, f := range discovered {
		seen[[2]string{f.StoreID, f.Path}] = true
	}
	stored, err := s.store.ListFolders(ctx, c.ID())
	if err != nil {
		return nil, output(err)
	}
	var gone []removal
	for _, fi := range stored {
		if seen[[2]string{fi.Folder.StoreID, fi.Folder.Path}] {
			continue
		}
		status, err := prober.FolderStatus(ctx, fi.Folder)
		switch {
		case err != nil && (ctx.Err() != nil || errors.Is(err, source.ErrUnavailable)):
			// the source is gone or recovering: the next probe would only hammer it, and nothing is removed
			s.log.Warn("source unavailable while checking folders no longer discovered; the run ends here", "source", c.ID(), "err", err)
			return nil, fmt.Errorf("folder %s: %w", fi.Folder.Path, err)
		case err != nil:
			s.log.Warn("cannot check a folder that is no longer discovered; left as is", "folder", fi.Folder.Path, "err", err)
		case status == source.FolderGone:
			gone = append(gone, removal{index: -1, storeID: fi.Folder.StoreID, folderID: fi.ID, path: fi.Folder.Path})
		case status == source.FolderNotSynced && fi.Active:
			if err := s.store.SetFolderActive(ctx, fi.ID, false); err != nil {
				return nil, output(err)
			}
			s.log.Info("folder no longer synced; its mail is kept, outside scope", "folder", fi.Folder.Path)
		}
	}
	return gone, nil
}

// ConfirmRemovals applies the held removals of a folder now (the user
// confirmed that the mail really is gone).
func (s *Syncer) ConfirmRemovals(ctx context.Context, folderID int64) (store.Removal, error) {
	ids, err := s.store.HeldIDs(ctx, folderID)
	if err != nil {
		return store.Removal{}, err
	}
	rm, err := s.store.RemoveMessages(ctx, ids)
	if err != nil {
		return rm, fmt.Errorf("syncer: confirm removals of folder %d: %w", folderID, err)
	}
	s.log.Info("held removals confirmed", "folder_id", folderID, "deleted", rm.Deleted, "moved", rm.Moved)
	return rm, nil
}
