package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// sqlPruneAttachmentBlobs removes content-addressed bytes no attachment row
// references any more. The explicit sha256 <> ” predicate is not
// redundant: it is the partial index's own condition, so SQLite can answer
// the NOT EXISTS with one index probe per blob instead of scanning every
// attachment row (measured: 441 ms -> 2.5 ms per transaction on a 40k-row
// mailbox).
const sqlPruneAttachmentBlobs = `DELETE FROM attachment_blobs
	WHERE NOT EXISTS (
		SELECT 1 FROM attachments a
		WHERE a.sha256 = attachment_blobs.sha256 AND a.sha256 <> ''
	)`

// pruneAttachmentBlobs removes content-addressed bytes no attachment row
// references any more. It runs once per write transaction, never per message:
// a rewritten message or a removal can orphan the blobs it used to share.
func pruneAttachmentBlobs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, sqlPruneAttachmentBlobs)
	return err
}

// ErrAssetUnavailable reports an attachment whose metadata exists but whose
// bytes were never captured (old mail, a non-image part, or a source that
// only supplies metadata). It is distinct from ErrNotFound so the API can
// answer a stable 404 without suggesting the message is gone.
var ErrAssetUnavailable = errors.New("store: attachment content is not stored")

// AttachmentData is one inline attachment read for the renderer. Bytes are
// returned only through the authenticated attachment route; the message and
// list reads never load them.
type AttachmentData struct {
	Position    int
	Name        string
	ContentType string
	Size        int64
	Inline      bool
	SHA256      string
	Bytes       []byte
}

// AttachmentData reads one attachment's bytes from the content-addressed
// store. It applies the same owner-content and picked-folder boundary as the
// message card: a shared/delegate row or an unpicked store cannot be fetched
// by guessing a row id and position.
func (s *Store) AttachmentData(ctx context.Context, rowID int64, position int) (AttachmentData, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return AttachmentData{}, err
	}
	var a AttachmentData
	var sha, blobType string
	var data []byte
	err = s.db.QueryRowContext(ctx, `SELECT a.position, a.name, a.content_type, a.size, a.inline, a.sha256,
			COALESCE(b.content_type, ''), b.bytes
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN attachments a ON a.message_id = m.id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id
		LEFT JOIN attachment_blobs b ON b.sha256 = a.sha256
		WHERE m.id = ? AND a.position = ? AND `+vis+` AND `+ownerContentWhere,
		rowID, position).Scan(&a.Position, &a.Name, &a.ContentType, &a.Size, &a.Inline, &sha, &blobType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentData{}, ErrNotFound
	}
	if err != nil {
		return AttachmentData{}, fmt.Errorf("store: attachment %d/%d: %w", rowID, position, err)
	}
	a.SHA256 = sha
	if sha == "" || len(data) == 0 {
		return a, ErrAssetUnavailable
	}
	if blobType != "" {
		a.ContentType = blobType
	}
	a.Bytes = data
	return a, nil
}
