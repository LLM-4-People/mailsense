package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// quarantinedSQL is true for a message (alias m) set aside at its current
// revision: the one predicate of the detail quarantine.
const quarantinedSQL = `EXISTS (SELECT 1 FROM detail_quarantine q WHERE q.message_id = m.id AND q.revision = m.revision)`

// QuarantineDetails sets a message aside at its current revision: its
// detail read made the source fail as a whole, so the detail pass skips it
// until it changes (a new revision) or ClearQuarantine. An unknown message
// is not an error (it was removed meanwhile).
func (s *Store) QuarantineDetails(ctx context.Context, sourceID string, key source.ItemRef, code string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO detail_quarantine (message_id, revision, code, quarantined_at)
		SELECT id, revision, ?, ? FROM messages WHERE source_id = ? AND store_id = ? AND msg_id = ?
		ON CONFLICT(message_id) DO UPDATE SET revision = excluded.revision, code = excluded.code, quarantined_at = excluded.quarantined_at`,
		code, s.now(), sourceID, key.StoreID, key.ID)
	if err != nil {
		return fmt.Errorf("store: set aside the details of %s: %w", key.ID, err)
	}
	return nil
}

// CountQuarantined counts a source's visible partial messages set aside
// at their current revision.
func (s *Store) CountQuarantined(ctx context.Context, sourceID string) (int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id AND `+vis+
		` WHERE m.source_id = ? AND m.partial = 1 AND `+quarantinedSQL, sourceID).Scan(&n)
	return n, err
}

// ClearQuarantine is the user's retry: every message of the source set
// aside is read again by the next detail pass. It returns how many were
// set aside at their current revision.
func (s *Store) ClearQuarantine(ctx context.Context, sourceID string) (int, error) {
	var n int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m WHERE m.source_id = ? AND `+quarantinedSQL, sourceID).Scan(&n); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM detail_quarantine WHERE message_id IN (SELECT id FROM messages WHERE source_id = ?)`, sourceID)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: clear the set-aside details of %s: %w", sourceID, err)
	}
	return n, nil
}
