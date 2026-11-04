package store

import (
	"context"
	"database/sql"
	"fmt"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// The display backfill repairs two display-only gaps of mail stored by an
// older build: an unknown HTML state, and an HTML body that references an
// inline cid: image whose bytes (or whose attachment identity) were never
// captured. It never changes the plain body, the revision, the search index
// or any learning input.
//
// inline_assets_checked is the repair's completion marker. It is set after
// one successful re-read even when the source could not supply the bytes, so
// a message the source refuses is not re-read on every pass.
const backfillCandidateSQL = `(m.body_html_known = 0 OR (m.inline_assets_checked = 0 AND m.has_inline_refs = 1))`

// BackfillRef names one visible message waiting for the display backfill.
// Instant is the message's mail instant (sent time in a sent folder, received
// time otherwise) and, with RowID, the newest-first keyset cursor: skipped
// rows are never selected again in the same run.
type BackfillRef struct {
	RowID   int64
	Instant string
	Key     source.ItemRef
}

// BackfillCursor is the position of the last message a backfill read. The
// zero value starts at the newest message.
type BackfillCursor struct {
	Instant string
	RowID   int64
}

// instantSortSQL is the backfill's sort key: the stored instant. instant is
// NOT NULL DEFAULT ” and an empty value is the smallest possible string, so
// a plain DESC sort already puts absent instants after every dated message,
// exactly as COALESCE(NULLIF(…), '0000') did, while staying a column the
// backfill index can carry. The COALESCE wrapper defeated that index and
// forced a temp B-tree over the whole candidate set.
const instantSortSQL = instantSQL

// BackfillNewest lists up to limit visible messages of a source that need the
// display backfill, newest first. The newest-first order is deliberate: the
// owner opens recent mail first, so the display fix reaches it first even
// when an old mailbox takes hours to re-read.
func (s *Store) BackfillNewest(ctx context.Context, sourceID string, cursor BackfillCursor, limit int) ([]BackfillRef, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT id, store_id, msg_id, k FROM (
		SELECT m.id, m.store_id, m.msg_id, ` + instantSortSQL + ` AS k
		FROM messages m JOIN folders f ON f.id = m.folder_id AND ` + vis + `
		WHERE m.source_id = ? AND ` + backfillCandidateSQL + `
	)`
	args := []any{sourceID}
	if cursor.Instant != "" {
		q += ` WHERE (k < ? OR (k = ? AND id < ?))`
		args = append(args, cursor.Instant, cursor.Instant, cursor.RowID)
	}
	q += ` ORDER BY k DESC, id DESC LIMIT ?`
	args = append(args, max(limit, 1))
	return query[BackfillRef](ctx, s.db, q, args, func(rows *sql.Rows, r *BackfillRef) error {
		return rows.Scan(&r.RowID, &r.Key.StoreID, &r.Key.ID, &r.Instant)
	})
}

// CountBackfill counts the visible messages of a source that need the display
// backfill (unknown HTML, or an inline asset the older build never stored).
func (s *Store) CountBackfill(ctx context.Context, sourceID string) (int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id AND `+vis+`
		WHERE m.source_id = ? AND `+backfillCandidateSQL, sourceID).Scan(&n)
	return n, err
}

// BackfillUpdate is one message's re-read display state. Known is the
// connector's declaration that it actually looked at the body; a reader
// cannot update a message without it. AssetsKnown declares that the connector
// enumerates attachments, so the re-read may also repair inline images.
type BackfillUpdate struct {
	Key         source.ItemRef
	HTML        string
	Known       bool
	Truncated   bool
	Attachments []mail.Attachment
	AssetsKnown bool
}

// backfillBase is one stored message's identity and body-known flag.
type backfillBase struct {
	rowID     int64
	htmlKnown int64
}

// loadBackfillBases reads the stored identity of every key in bounded
// batches. The old loop paid one SELECT per update inside the write
// transaction; a 50-row backfill paid 50 reads.
func loadBackfillBases(ctx context.Context, q queryer, sourceID string, updates []BackfillUpdate) (map[string]backfillBase, error) {
	out := make(map[string]backfillBase, len(updates))
	err := eachKeyBatch(ctx, q, sourceID, updates,
		func(u BackfillUpdate) (string, string) { return u.Key.StoreID, u.Key.ID },
		func(keys int) string {
			return `SELECT m.id, m.body_html_known, m.store_id, m.msg_id
				FROM messages m WHERE (m.source_id, m.store_id, m.msg_id) IN (VALUES ` + keyTuples(keys) + `)`
		},
		func(rows *sql.Rows) error {
			var rowID, known int64
			var storeID, msgID string
			if err := rows.Scan(&rowID, &known, &storeID, &msgID); err != nil {
				return err
			}
			out[detailKey(storeID, msgID)] = backfillBase{rowID: rowID, htmlKnown: known}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyBackfill stores a batch of re-read display state in one transaction.
// It deliberately does not bump the message revision: HTML and inline assets
// are display-only and must not invalidate triage, evidence or learning
// state. It also leaves updated_at and last_seen_run alone.
//
// An unknown HTML body is filled once and never overwritten by a later read;
// attachments are merged by position, so a re-read that could not reach an
// accessor does not delete metadata an earlier read stored.
func (s *Store) ApplyBackfill(ctx context.Context, sourceID string, updates []BackfillUpdate) (int, error) {
	for _, u := range updates {
		if !u.Known && (u.HTML != "" || u.Truncated) {
			return 0, fmt.Errorf("store: backfilled body html without a declared read")
		}
		if u.Known && !utf8.ValidString(u.HTML) {
			return 0, fmt.Errorf("store: backfilled body html is not valid UTF-8")
		}
		if u.AssetsKnown && len(u.Attachments) > 0 && !u.Known {
			return 0, fmt.Errorf("store: backfilled inline assets without a declared read")
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}
	applied := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		exec := func(ctx context.Context, q string, args ...any) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}
		bodyStmt, err := tx.PrepareContext(ctx, `UPDATE messages
			SET body_html = CASE WHEN body_html_known = 0 THEN ? ELSE body_html END,
				body_html_known = 1,
				body_html_truncated = CASE WHEN body_html_known = 0 THEN ? ELSE body_html_truncated END,
				has_inline_refs = CASE WHEN ? THEN 1 ELSE has_inline_refs END,
				inline_assets_checked = 1
			WHERE id = ?`)
		if err != nil {
			return err
		}
		defer bodyStmt.Close()
		checkStmt, err := tx.PrepareContext(ctx, "UPDATE messages SET inline_assets_checked = 1 WHERE id = ? AND inline_assets_checked = 0")
		if err != nil {
			return err
		}
		defer checkStmt.Close()
		bases, err := loadBackfillBases(ctx, tx, sourceID, updates)
		if err != nil {
			return err
		}
		for _, u := range updates {
			key := detailKey(u.Key.StoreID, u.Key.ID)
			base, ok := bases[key]
			if !ok {
				continue
			}
			rowID, htmlKnown := base.rowID, base.htmlKnown
			changed := false
			if u.Known && htmlKnown == 0 {
				refs := inlineHTMLRefs(u.HTML, u.Attachments)
				if _, err := bodyStmt.ExecContext(ctx, u.HTML, boolInt(u.Truncated), boolInt(refs), rowID); err != nil {
					return err
				}
				base.htmlKnown = 1
				bases[key] = base
				changed = true
			}
			if u.Known && !u.AssetsKnown && !changed {
				// This source never enumerates attachments: one read settles
				// the display state, even when the body references a cid. A
				// body write above already set the marker, so the second
				// statement is only the "HTML was already known" case.
				if _, err := checkStmt.ExecContext(ctx, rowID); err != nil {
					return err
				}
			}
			if u.AssetsKnown {
				if err := mergeAttachments(ctx, exec, rowID, u.Attachments); err != nil {
					return err
				}
				if _, err := checkStmt.ExecContext(ctx, rowID); err != nil {
					return err
				}
				changed = true
			}
			if changed {
				applied++
			}
		}
		return pruneAttachmentBlobs(ctx, tx)
	})
	if err != nil {
		return 0, fmt.Errorf("store: apply display backfill to %d message(s): %w", len(updates), err)
	}
	return applied, nil
}
