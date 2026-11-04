package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Removal counts the messages of a sync run that vanished from their
// folder: Moved when a message with the same Internet Message-ID is still
// stored in the same store (EntryIDs change on a move), else Deleted; Held
// when a removal was kept for a later run — by the mass-removal safety valve,
// or by the one-run move hold (move_holds) that gives a message moving
// between two folders' snapshots time to reappear.
type Removal struct {
	Deleted int `json:"deleted"`
	Moved   int `json:"moved"`
	Held    int `json:"held"`
}

// Add sums two removals.
func (r Removal) Add(o Removal) Removal {
	return Removal{Deleted: r.Deleted + o.Deleted, Moved: r.Moved + o.Moved, Held: r.Held + o.Held}
}

// AbsentMessages is the one absence query. It returns the row ids of a
// folder's messages whose ids are not in present (nil = none present: the
// folder is gone), and how many messages the folder holds. The syncer calls
// it at the snapshot boundary, where the presence set exists anyway, and
// passes the returned ids and count to the end-of-run removal pass, so the
// presence set itself is never retained across folders.
func (s *Store) AbsentMessages(ctx context.Context, folderID int64, present map[string]bool) ([]int64, int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, msg_id FROM messages WHERE folder_id = ?", folderID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var absent []int64
	stored := 0
	for rows.Next() {
		var id int64
		var msgID string
		if err := rows.Scan(&id, &msgID); err != nil {
			return nil, 0, err
		}
		stored++
		if !present[msgID] {
			absent = append(absent, id)
		}
	}
	return absent, stored, rows.Err()
}

// moveSurvivorMatch defines a move's surviving copy once: another row of the
// same store with the same nonempty Internet Message-ID (a move changes the
// entry id, so the destination is a new row). RemoveMessages and
// ClassifyAbsences share it so classification and label copying cannot drift.
const moveSurvivorMatch = `o.source_id = m.source_id
	AND o.store_id = m.store_id
	AND o.id <> m.id
	AND m.internet_message_id <> ''
	AND o.internet_message_id = m.internet_message_id`

const moveSurvivorSQL = `SELECT o.id FROM messages m JOIN messages o ON ` + moveSurvivorMatch + ` WHERE m.id = ? LIMIT 1`

// moveSurvivor returns the same-store row that already carries the Message-ID
// of id's message, if any: the destination a move left the message in.
func moveSurvivor(ctx context.Context, stmt *sql.Stmt, id int64) (int64, bool, error) {
	var dest int64
	err := stmt.QueryRowContext(ctx, id).Scan(&dest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return dest, true, nil
}

// removalBatchSize is how many messages one removal transaction deletes. A
// whole-folder confirmation can hold tens of thousands of rows; one
// transaction for all of them keeps the write lock past the request timeout,
// so nothing ever completes. Each batch commits on its own and the next
// confirmation resumes from the remaining held rows.
const removalBatchSize = 500

// RemoveMessages deletes messages by row id and classifies each as moved or
// deleted. Recipients, verdicts and held entries go by cascade; the FTS
// entry through the one FTS write path.
//
// A small set is one atomic transaction, exactly as before. A larger set is
// deleted in bounded batches so a folder-sized confirmation finishes inside
// the route's long budget; the blob prune runs once, in the last batch. A
// failure returns the batches already committed with the error: the caller
// retries, and the rows already gone are no longer held.
func (s *Store) RemoveMessages(ctx context.Context, ids []int64) (Removal, error) {
	defer s.touchVisibility()
	var rm Removal
	if len(ids) == 0 {
		return rm, nil
	}
	if len(ids) <= removalBatchSize {
		if err := s.removeBatch(ctx, ids, true, &rm); err != nil {
			return Removal{}, fmt.Errorf("store: remove %d message(s): %w", len(ids), err)
		}
		return rm, nil
	}
	for start := 0; start < len(ids); start += removalBatchSize {
		end := min(start+removalBatchSize, len(ids))
		if err := s.removeBatch(ctx, ids[start:end], end == len(ids), &rm); err != nil {
			return rm, fmt.Errorf("store: remove messages %d to %d of %d (%d already removed): %w",
				start+1, end, len(ids), rm.Moved+rm.Deleted, err)
		}
	}
	return rm, nil
}

// removeBatch removes one bounded set in one transaction and adds its counts
// to rm only after the commit, so a rolled-back batch is never reported as
// deleted. prune runs the content-addressed blob prune in this transaction.
func (s *Store) removeBatch(ctx context.Context, ids []int64, prune bool, rm *Removal) error {
	var batch Removal
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// A move changes the entry id: the old row is removed and the new one
		// inserted. Labels are the owner's ground truth and must not die with
		// the old row, so copy them to the surviving message first. The
		// primary key makes the copy idempotent when both rows already carry
		// the same question/source.
		survivor, err := tx.PrepareContext(ctx, moveSurvivorSQL)
		if err != nil {
			return err
		}
		defer survivor.Close()
		copyLabels, err := tx.PrepareContext(ctx, `INSERT INTO labels (message_id, question, value, source, created_at, prompt)
			SELECT ?, question, value, source, created_at, prompt FROM labels WHERE message_id = ?
			ON CONFLICT(message_id, question, source) DO NOTHING`)
		if err != nil {
			return err
		}
		defer copyLabels.Close()
		for _, id := range ids {
			dest, isMove, err := moveSurvivor(ctx, survivor, id)
			if err != nil {
				return err
			}
			if isMove {
				if _, err := copyLabels.ExecContext(ctx, dest, id); err != nil {
					return err
				}
			}
			if err := deleteMessage(ctx, tx, id); err != nil {
				return err
			}
			if isMove {
				batch.Moved++
			} else {
				batch.Deleted++
			}
		}
		if !prune {
			return nil
		}
		return pruneAttachmentBlobs(ctx, tx)
	})
	if err != nil {
		return err
	}
	*rm = rm.Add(batch)
	return nil
}

func deleteMessage(ctx context.Context, tx *sql.Tx, id int64) error {
	if err := deleteFTS(ctx, tx, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", id)
	return err
}

// ClassifyAbsences splits absent message ids in one scan: moves have a
// nonempty Internet Message-ID with a same-store survivor; unexplained have a
// nonempty Message-ID but no survivor yet (the one-run move hold applies);
// anonymous have no Message-ID, so nothing can reconcile them and they are
// deleted as before.
func (s *Store) ClassifyAbsences(ctx context.Context, ids []int64) (moves, unexplained, anonymous []int64, err error) {
	if len(ids) == 0 {
		return nil, nil, nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.internet_message_id = '',
			EXISTS (SELECT 1 FROM messages o WHERE `+moveSurvivorMatch+`)
		FROM messages m WHERE m.id IN `+idSetSQL+` ORDER BY m.id`, idSet(ids))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("store: classify %d absent message(s): %w", len(ids), err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, anonymousID, hasSurvivor int64
		if err := rows.Scan(&id, &anonymousID, &hasSurvivor); err != nil {
			return nil, nil, nil, fmt.Errorf("store: classify %d absent message(s): %w", len(ids), err)
		}
		switch {
		case anonymousID != 0:
			anonymous = append(anonymous, id)
		case hasSurvivor != 0:
			moves = append(moves, id)
		default:
			unexplained = append(unexplained, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("store: classify %d absent message(s): %w", len(ids), err)
	}
	return moves, unexplained, anonymous, nil
}

// HoldMoveSuspects records ids as unexplained absences of folderID on their
// first holding run (a later call in the same run keeps the first held_run)
// and returns those already held by an earlier run: they are ripe now. The
// one-run move-hold policy is owned here: a message that moved between two
// snapshots of one run gets the next complete run to show a survivor; if none
// appears, it is removed like a genuine deletion.
func (s *Store) HoldMoveSuspects(ctx context.Context, folderID int64, ids []int64, runID int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var ripe []int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		insert, err := tx.PrepareContext(ctx, `INSERT INTO move_holds (message_id, folder_id, held_run) VALUES (?, ?, ?)
			ON CONFLICT(message_id) DO NOTHING`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for _, id := range ids {
			if _, err := insert.ExecContext(ctx, id, folderID, runID); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT message_id FROM move_holds
			WHERE folder_id = ? AND held_run < ? AND message_id IN `+idSetSQL+`
			ORDER BY message_id`, folderID, runID, idSet(ids))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ripe = append(ripe, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("store: hold move suspects of folder %d: %w", folderID, err)
	}
	return ripe, nil
}

// ReleaseMoveHolds forgets the move holds of folderID whose message is no
// longer absent (keep is the folder's current absent set). The syncer calls it
// on every run, including an empty set, so a message that comes back is never
// held against a stale absence.
func (s *Store) ReleaseMoveHolds(ctx context.Context, folderID int64, keep []int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM move_holds WHERE folder_id = ? AND message_id NOT IN "+idSetSQL, folderID, idSet(keep))
		return err
	})
	if err != nil {
		return fmt.Errorf("store: release move holds of folder %d: %w", folderID, err)
	}
	return nil
}

// HoldRemovals makes ids the held removals of a folder: ids already held
// keep their first held time, ids no longer absent are released. It returns
// the ids held since at or before due (all ids when due is "": none).
func (s *Store) HoldRemovals(ctx context.Context, folderID int64, ids []int64, due string) ([]int64, error) {
	ts := s.now()
	var ripe []int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := clearHeld(ctx, tx, folderID, ids); err != nil {
			return err
		}
		st, err := tx.PrepareContext(ctx, `INSERT INTO held_removals (message_id, folder_id, held_at) VALUES (?, ?, ?)
			ON CONFLICT(message_id) DO UPDATE SET folder_id = excluded.folder_id RETURNING held_at`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, id := range ids {
			var since string
			if err := st.QueryRowContext(ctx, id, folderID, ts).Scan(&since); err != nil {
				return err
			}
			if due != "" && since <= due {
				ripe = append(ripe, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: hold removals of folder %d: %w", folderID, err)
	}
	return ripe, nil
}

// clearHeld releases a folder's held removals except keep.
func clearHeld(ctx context.Context, tx *sql.Tx, folderID int64, keep []int64) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM held_removals WHERE folder_id = ? AND message_id NOT IN "+idSetSQL, folderID, idSet(keep))
	return err
}

// ReleaseHeld forgets every held removal of a folder (its messages are back).
func (s *Store) ReleaseHeld(ctx context.Context, folderID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return clearHeld(ctx, tx, folderID, nil) })
}

// HeldFolder is the held removals of one folder. AppliesAt is the moment a
// positive sync.removal.auto_apply_after applies the whole held set on a later
// sync (empty when the setting is 0 and only the owner's confirmation
// applies it). The list renders that deadline instead of promising the
// messages stay.
type HeldFolder struct {
	FolderID  int64  `json:"folder_id"`
	StoreID   string `json:"store_id"`
	Path      string `json:"path"`
	Held      int    `json:"held"`
	Stored    int    `json:"stored"` // messages the folder holds, held ones included
	Since     string `json:"since"`  // when the first was held
	AppliesAt string `json:"applies_at,omitempty"`
}

// HeldRemovals lists the folders of a source with held removals. appliesAt is
// the earliest held_at plus the auto-apply window (empty when the window is
// zero), computed by the caller that owns the setting.
func (s *Store) HeldRemovals(ctx context.Context, sourceID string, appliesAt func(since string) string) ([]HeldFolder, error) {
	return query(ctx, s.db, `SELECT f.id, f.store_id, f.path, COUNT(*), (SELECT COUNT(*) FROM messages m WHERE m.folder_id = f.id), MIN(h.held_at)
		FROM held_removals h JOIN folders f ON f.id = h.folder_id WHERE f.source_id = ? GROUP BY f.id ORDER BY f.id`, []any{sourceID},
		func(r *sql.Rows, v *HeldFolder) error {
			if err := r.Scan(&v.FolderID, &v.StoreID, &v.Path, &v.Held, &v.Stored, &v.Since); err != nil {
				return err
			}
			if appliesAt != nil {
				v.AppliesAt = appliesAt(v.Since)
			}
			return nil
		})
}

// HeldIDs returns the held removals of a folder.
func (s *Store) HeldIDs(ctx context.Context, folderID int64) ([]int64, error) {
	return query(ctx, s.db, "SELECT message_id FROM held_removals WHERE folder_id = ? ORDER BY message_id", []any{folderID},
		func(r *sql.Rows, v *int64) error { return r.Scan(v) })
}
