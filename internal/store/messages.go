package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

var (
	messageUpsertSQL = func() string {
		names := append([]string{"source_id", "store_id", "msg_id", "folder_id"}, messageColumns.names...)
		names = append(names, instantColumn)
		names = append(names, senderColumns...)
		names = append(names, bodyHTMLColumns...)
		names = append(names, "has_inline_refs", "inline_assets_checked", "first_seen_at", "updated_at", "last_seen_run")
		set := []string{"folder_id = excluded.folder_id"}
		for _, n := range messageColumns.names {
			set = append(set, n+" = excluded."+n)
		}
		set = append(set, instantColumn+" = excluded."+instantColumn)
		for _, n := range senderColumns {
			set = append(set, n+" = excluded."+n)
		}
		// A writer that does not know the HTML state (archive, an older
		// connector, a partial summary row) must never erase a stored one.
		// A writer that does know replaces it, including with an empty body.
		set = append(set,
			"body_html = CASE WHEN excluded.body_html_known = 1 THEN excluded.body_html ELSE messages.body_html END",
			"body_html_known = MAX(messages.body_html_known, excluded.body_html_known)",
			"body_html_truncated = CASE WHEN excluded.body_html_known = 1 THEN excluded.body_html_truncated ELSE messages.body_html_truncated END",
			"has_inline_refs = MAX(messages.has_inline_refs, excluded.has_inline_refs)",
			"inline_assets_checked = MAX(messages.inline_assets_checked, excluded.inline_assets_checked)")
		set = append(set, "updated_at = excluded.updated_at", "last_seen_run = excluded.last_seen_run", "revision = messages.revision + 1")
		return "INSERT INTO messages (" + strings.Join(names, ", ") + ") VALUES (" +
			placeholders(len(names)) + ")" +
			" ON CONFLICT(source_id, store_id, msg_id) DO UPDATE SET " + strings.Join(set, ", ") + " RETURNING id"
	}()
	messageSelectSQL = "SELECT m.id, m.source_id, m.store_id, m.msg_id, f.store_name, f.path, f.role, " +
		messageColumns.list("m.") + ", " + bodyHTMLList("m.") + " FROM messages m JOIN folders f ON f.id = m.folder_id"
)

// UpsertMessages writes a batch in one transaction: message row, recipients
// and FTS entry per message. A Partial message keeps the details already
// stored for its key (mail.Message.MergeStored). Every message is validated first; an invalid
// one fails the batch without writing anything. runID (0 = none) marks the
// rows seen by a sync run for reconciliation.
func (s *Store) UpsertMessages(ctx context.Context, runID int64, msgs []mail.Message) error {
	for i := range msgs {
		if err := msgs[i].Validate(); err != nil {
			return err
		}
	}
	var run any
	if runID > 0 {
		run = runID
	}
	start := s.clock.Now()
	err := s.busy.WithBusyRetry(ctx, "upsert messages", func() error {
		if err := s.applySelection(ctx); err != nil {
			return err
		}
		_, picked := s.selection()
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			w, err := newWriter(ctx, tx, picked)
			if err != nil {
				return err
			}
			defer w.close()
			ts := s.now()
			for i := range msgs {
				if err := w.write(ctx, &msgs[i], run, ts); err != nil {
					return err
				}
			}
			if err := w.flushFTS(ctx); err != nil {
				return err
			}
			return pruneAttachmentBlobs(ctx, tx)
		})
		if err != nil {
			return fmt.Errorf("store: upsert %d message(s): %w", len(msgs), err)
		}
		s.log.Debug("messages upserted", "count", len(msgs), "duration", s.clock.Now().Sub(start))
		return nil
	})
	if err != nil {
		return err
	}
	s.touchMail()
	return nil
}

// ErrNotFound is returned when a message does not exist.
var ErrNotFound = errors.New("store: not found")

// GetMessage reads one message with its recipients.
func (s *Store) GetMessage(ctx context.Context, sourceID, storeID, id string) (mail.Message, error) {
	m, _, err := loadMessage(ctx, s.db, sourceID, storeID, id)
	return m, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadMessage reads one message by key (on the pool or inside a
// transaction) and returns its row id; ErrNotFound when absent.
func loadMessage(ctx context.Context, q queryer, sourceID, storeID, id string) (mail.Message, int64, error) {
	var m mail.Message
	var rowID int64
	var role string
	var htmlKnown, htmlTruncated int
	dest := append([]any{&rowID, &m.SourceID, &m.StoreID, &m.ID, &m.StoreName, &m.FolderPath, &role}, messageColumns.dests(&m)...)
	dest = append(dest, bodyHTMLDests(&m, &htmlKnown, &htmlTruncated)...)
	err := q.QueryRowContext(ctx, messageSelectSQL+" WHERE m.source_id = ? AND m.store_id = ? AND m.msg_id = ?", sourceID, storeID, id).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return m, 0, ErrNotFound
	}
	if err != nil {
		return m, 0, err
	}
	m.FolderRole = mail.FolderRole(role)
	setBodyHTMLFlags(&m, htmlKnown, htmlTruncated)
	recipients, err := query[mail.Recipient](ctx, q,
		"SELECT kind, name, address FROM recipients WHERE message_id = ? ORDER BY position", []any{rowID},
		func(rows *sql.Rows, r *mail.Recipient) error {
			var kind string
			if err := rows.Scan(&kind, &r.Name, &r.Address); err != nil {
				return err
			}
			r.Kind = mail.RecipientKind(kind)
			return nil
		})
	if err != nil {
		return m, 0, err
	}
	m.Recipients = append(m.Recipients, recipients...)
	return m, rowID, readItems(ctx, q, rowID, &m)
}

// CountByRole returns the number of stored messages per folder role.
func (s *Store) CountByRole(ctx context.Context) (map[mail.FolderRole]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT f.role, COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id GROUP BY f.role")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[mail.FolderRole]int{}
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, err
		}
		out[mail.FolderRole(role)] = n
	}
	return out, rows.Err()
}

// CountSeenInRun counts the rows of a folder written by a sync run.
func (s *Store) CountSeenInRun(ctx context.Context, folderID, runID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE folder_id = ? AND last_seen_run = ?", folderID, runID).Scan(&n)
	return n, err
}
