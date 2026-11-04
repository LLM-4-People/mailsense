package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// PendingDetails returns up to limit visible messages of a source
// (visibleSQL: picked mailboxes, active folders) still waiting for their
// detail pass (Partial) and not set aside (QuarantineDetails), newest
// first, received before settled (zero: any time; a message without a
// received time counts as settled).
func (s *Store) PendingDetails(ctx context.Context, sourceID string, limit int, settled time.Time) ([]source.ItemRef, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	where, args := pendingSQL(sourceID, settled)
	return query(ctx, s.db, `SELECT m.store_id, m.msg_id FROM messages m JOIN folders f ON f.id = m.folder_id AND `+vis+where+
		` ORDER BY m.received_at DESC, m.id DESC LIMIT ?`, append(args, max(limit, 1)),
		func(r *sql.Rows, k *source.ItemRef) error { return r.Scan(&k.StoreID, &k.ID) })
}

// CountPending counts the messages of a source's picked mailboxes waiting
// for their detail pass, received before settled (zero: all of them).
func (s *Store) CountPending(ctx context.Context, sourceID string, settled time.Time) (int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return 0, err
	}
	where, args := pendingSQL(sourceID, settled)
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages m JOIN folders f ON f.id = m.folder_id AND `+vis+where, args...).Scan(&n)
	return n, err
}

// pendingSQL is the one filter of the pending messages: partial, of the
// source, not set aside at their current revision (quarantinedSQL).
func pendingSQL(sourceID string, settled time.Time) (string, []any) {
	where, args := ` WHERE m.source_id = ? AND m.partial = 1 AND NOT `+quarantinedSQL, []any{sourceID}
	if !settled.IsZero() {
		where += ` AND m.received_at < ?`
		args = append(args, settled.UTC().Format(mail.TimeLayout))
	}
	return where, args
}

// DetailUpdate is the detail pass result of one message.
type DetailUpdate struct {
	Key     source.ItemRef
	Details mail.Details
}

// detailBase is the stored message a detail update completes: everything the
// write path needs except the fields mail.Details owns.
type detailBase struct {
	m mail.Message
}

// detailBatchKeys bounds one batch read. Three bind parameters per key, and a
// batch is a few hundred messages, so this keeps every read far below
// SQLite's parameter limit while still replacing hundreds of reads with one.
const detailBatchKeys = 200

// detailBaseSQL reads the stored base of a batch of keys. Attachments are
// omitted with the other detail fields; the message's further folders are
// not part of mail.Details and must survive the completion, so they are read
// with the row.
func detailBaseSQL(keys int) string {
	return `SELECT m.source_id, m.store_id, m.msg_id, f.store_name, f.path, f.role, ` +
		detailColumns.list("m.") + `, ` + foldersSQL + `
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE (m.source_id, m.store_id, m.msg_id) IN (VALUES ` + keyTuples(keys) + `)`
}

// keyTuples is n (?, ?, ?) groups.
func keyTuples(n int) string {
	return strings.TrimSuffix(strings.Repeat("(?, ?, ?), ", max(n, 1)), ", ")
}

// eachKeyBatch reads one projection over (source, store, message) keys in
// bounded batches: three bind parameters per key and at most detailBatchKeys
// keys per statement. It is the one chunk loop of the batch loaders, so a new
// loader cannot reintroduce the per-row read the old details and backfill
// loops paid. scan receives rows positioned on one row and may return an
// error to stop the batch.
func eachKeyBatch[T any](ctx context.Context, q queryer, sourceID string, items []T,
	key func(T) (storeID, id string), querySQL func(keys int) string, scan func(*sql.Rows) error) error {
	for start := 0; start < len(items); start += detailBatchKeys {
		chunk := items[start:min(start+detailBatchKeys, len(items))]
		args := make([]any, 0, len(chunk)*3)
		for _, item := range chunk {
			storeID, id := key(item)
			args = append(args, sourceID, storeID, id)
		}
		rows, err := q.QueryContext(ctx, querySQL(len(chunk)), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := scan(rows); err != nil {
				_ = rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	return nil
}

// loadDetailBases reads the stored base of every key in a bounded number of
// queries, keyed by store and message id (the source is the call's source).
// A key that no longer exists is simply absent: the caller skips it.
func loadDetailBases(ctx context.Context, q queryer, sourceID string, keys []source.ItemRef) (map[string]detailBase, error) {
	out := make(map[string]detailBase, len(keys))
	err := eachKeyBatch(ctx, q, sourceID, keys,
		func(key source.ItemRef) (string, string) { return key.StoreID, key.ID },
		detailBaseSQL,
		func(rows *sql.Rows) error {
			var m mail.Message
			var role, folders string
			dest := []any{&m.SourceID, &m.StoreID, &m.ID, &m.StoreName, &m.FolderPath, &role}
			dest = append(dest, detailColumns.dests(&m)...)
			dest = append(dest, &folders)
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			m.FolderRole = mail.FolderRole(role)
			if err := parseItems("[]", folders, &m); err != nil {
				return err
			}
			out[detailKey(m.StoreID, m.ID)] = detailBase{m: m}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// detailKey is the in-batch identity of a message; the source is fixed by the
// call and the tuple is unique in the store.
func detailKey(storeID, id string) string { return storeID + "\x00" + id }

// ApplyDetails completes stored messages with their details in one
// transaction (recipients and the FTS entry are rewritten through the one
// message write path). The details are authoritative: each update's
// Attachments is the message's complete attachment list and replaces the
// stored rows, so a reader that could not see every attachment must fail
// before it reaches here — an incomplete list is never merged or guessed.
// A message that no longer exists is not an error: it was removed after it
// was listed. It returns the number completed.
func (s *Store) ApplyDetails(ctx context.Context, runID int64, sourceID string, updates []DetailUpdate) (int, error) {
	defer s.touchMail()
	var run any
	if runID > 0 {
		run = runID
	}
	if err := s.applySelection(ctx); err != nil {
		return 0, err
	}
	_, picked := s.selection()
	applied := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		w, err := newWriter(ctx, tx, picked)
		if err != nil {
			return err
		}
		defer w.close()
		ts := s.now()
		keys := make([]source.ItemRef, len(updates))
		for i, u := range updates {
			keys[i] = u.Key
		}
		bases, err := loadDetailBases(ctx, tx, sourceID, keys)
		if err != nil {
			return err
		}
		for _, u := range updates {
			base, ok := bases[detailKey(u.Key.StoreID, u.Key.ID)]
			if !ok {
				continue
			}
			m := base.m
			m.Complete(u.Details)
			if err := m.Validate(); err != nil {
				return err
			}
			if err := w.write(ctx, &m, run, ts); err != nil {
				return err
			}
			applied++
		}
		if err := w.flushFTS(ctx); err != nil {
			return err
		}
		return pruneAttachmentBlobs(ctx, tx)
	})
	if err != nil {
		return 0, fmt.Errorf("store: apply details to %d message(s): %w", len(updates), err)
	}
	return applied, nil
}

// FolderVersions returns the change stamp (mail.Message.Stamp) of every
// message stored in a folder, by message id.
func (s *Store) FolderVersions(ctx context.Context, folderID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT msg_id, stamp FROM messages WHERE folder_id = ?", folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			return nil, err
		}
		out[id] = stamp
	}
	return out, rows.Err()
}
