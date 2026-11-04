package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// writer is the one message write path of a transaction: prepared
// statements for the message row, recipients, attachments, further
// folders and both full-text indexes. Index entries are written after the
// batch's other writes (flushFTS): an FTS insert followed by another
// statement of the same transaction makes SQLite open a statement journal,
// whose savepoint flushes FTS5's pending terms as a one-message segment
// and merges (profiled: a quarter of the import's CPU).
type writer struct {
	tx         *sql.Tx
	stmts      map[string]*sql.Stmt
	fts        ftsStmts
	folderByID map[mail.Folder]int64
	picked     map[string]bool // store keys picked now (new folder rows)
	pending    []pendingFTS
}

type pendingFTS struct {
	id int64
	m  *mail.Message
}

// Statements of the write path.
const (
	sqlDelRecips = "DELETE FROM recipients WHERE message_id = ?"
	sqlAddRecip  = "INSERT INTO recipients (message_id, position, kind, name, address, address_norm) VALUES (?, ?, ?, ?, ?, ?)"
	sqlDelAtts   = "DELETE FROM attachments WHERE message_id = ?"
	sqlAddAtt    = "INSERT INTO attachments (message_id, position, name, content_type, size, inline, content_id, content_location, sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
	sqlMergeAtt  = `INSERT INTO attachments (message_id, position, name, content_type, size, inline, content_id, content_location, sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id, position) DO UPDATE SET
			name = excluded.name, content_type = excluded.content_type, size = excluded.size, inline = excluded.inline,
			content_id = CASE WHEN excluded.content_id <> '' THEN excluded.content_id ELSE attachments.content_id END,
			content_location = CASE WHEN excluded.content_location <> '' THEN excluded.content_location ELSE attachments.content_location END,
			sha256 = CASE WHEN excluded.sha256 <> '' THEN excluded.sha256 ELSE attachments.sha256 END`
	sqlAddBlob    = "INSERT INTO attachment_blobs (sha256, content_type, size, bytes) VALUES (?, ?, ?, ?) ON CONFLICT(sha256) DO NOTHING"
	sqlDelFolders = "DELETE FROM message_folders WHERE message_id = ?"
	sqlAddFolder  = "INSERT OR IGNORE INTO message_folders (message_id, folder_id) VALUES (?, ?)"
)

func newWriter(ctx context.Context, tx *sql.Tx, picked map[string]bool) (*writer, error) {
	w := &writer{tx: tx, stmts: map[string]*sql.Stmt{}, folderByID: map[mail.Folder]int64{}, picked: picked}
	for _, q := range []string{messageUpsertSQL, sqlDelRecips, sqlAddRecip, sqlDelAtts, sqlAddAtt, sqlAddBlob, sqlDelFolders, sqlAddFolder} { // the backfill owns sqlMergeAtt
		st, err := tx.PrepareContext(ctx, q)
		if err != nil {
			w.close()
			return nil, err
		}
		w.stmts[q] = st
	}
	var err error
	if w.fts, err = prepareFTS(ctx, tx); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *writer) close() {
	for _, st := range w.stmts {
		_ = st.Close()
	}
	w.fts.close()
}

func (w *writer) exec(ctx context.Context, q string, args ...any) error {
	_, err := w.stmts[q].ExecContext(ctx, args...)
	return err
}

func (w *writer) write(ctx context.Context, msg *mail.Message, run any, ts string) error {
	// The write path is the trust boundary: a caller that forgot its own
	// check still cannot store an invalid message (and the transaction rolls
	// every earlier write of the batch back).
	if err := msg.Validate(); err != nil {
		return err
	}
	m := msg
	if msg.Partial {
		old, _, err := loadMessage(ctx, w.tx, msg.SourceID, msg.StoreID, msg.ID)
		switch {
		case err == nil:
			merged := *msg
			merged.MergeStored(&old)
			m = &merged
		case !errors.Is(err, ErrNotFound):
			return fmt.Errorf("message %q: %w", msg.ID, err)
		}
	}
	folderID, err := w.folder(ctx, mail.FolderOf(m), ts)
	if err != nil {
		return err
	}
	cols, err := messageColumns.args(m)
	if err != nil {
		return fmt.Errorf("message %q: %w", m.ID, err)
	}
	args := append([]any{m.SourceID, m.StoreID, m.ID, folderID}, cols...)
	args = append(args, instantArgs(m)...)
	args = append(args, senderArgs(m)...)
	args = append(args, bodyHTMLArgs(m)...)
	args = append(args, boolInt(messageHasInlineRefs(m)), boolInt(m.BodyHTMLKnown), ts, ts, run)
	var id int64
	if err := w.stmts[messageUpsertSQL].QueryRowContext(ctx, args...).Scan(&id); err != nil {
		return fmt.Errorf("message %q: %w", m.ID, err)
	}
	if err := w.exec(ctx, sqlDelRecips, id); err != nil {
		return err
	}
	for pos, r := range m.Recipients {
		if err := w.exec(ctx, sqlAddRecip, id, pos, string(r.Kind), r.Name, r.Address, mail.NormalizeAddress(r.Address)); err != nil {
			return err
		}
	}
	if err := w.writeItems(ctx, id, m, ts); err != nil {
		return err
	}
	w.pending = append(w.pending, pendingFTS{id, m})
	return nil
}

// folder returns the row id of a message's folder, creating the row when
// the message write path meets it first.
func (w *writer) folder(ctx context.Context, f mail.Folder, ts string) (int64, error) {
	if id, ok := w.folderByID[f]; ok {
		return id, nil
	}
	id, err := writeFolder(ctx, w.tx, folderEnsureSQL, f, ts, w.picked[source.StoreKey(f.SourceID, f.StoreID)])
	if err != nil {
		return 0, err
	}
	w.folderByID[f] = id
	return id, nil
}

// statementRunner runs one statement inside a transaction. The message write
// path and the display backfill share the attachment helpers through it.
type statementRunner func(ctx context.Context, query string, args ...any) error

// inlineHTMLRefs reports whether HTML or attachment metadata can reference an
// inline part. The message writer and the display backfill share it so the
// materialized flag cannot drift from the body they store.
func inlineHTMLRefs(html string, atts []mail.Attachment) bool {
	if strings.Contains(strings.ToLower(html), "cid:") {
		return true
	}
	for _, a := range atts {
		if a.Inline && (a.ContentID != "" || a.ContentLocation != "") &&
			mail.InlineImageType(a.ContentType, a.Name) != "" {
			return true
		}
	}
	return false
}

// messageHasInlineRefs reports whether the stored HTML can reference an inline
// part. It is materialized with the message so the display backfill never
// scans every HTML body to find its candidates.
func messageHasInlineRefs(m *mail.Message) bool {
	if !m.BodyHTMLKnown {
		return false
	}
	return inlineHTMLRefs(m.BodyHTML, m.Attachments)
}

// attachmentIdentity is the storage identity of one attachment: its content
// address, stored size and the type the blob is served with. The hash is
// computed only when the connector carried bytes.
func attachmentIdentity(a mail.Attachment) (sha string, size int64, contentType string, data []byte) {
	sha, size, contentType = strings.TrimSpace(a.SHA256), a.Size, a.ContentType
	if len(a.Data) == 0 {
		return sha, size, contentType, nil
	}
	sum := sha256.Sum256(a.Data)
	sha = hex.EncodeToString(sum[:])
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	return sha, int64(len(a.Data)), contentType, a.Data
}

// storeBlob writes one attachment's bytes once per SHA-256.
func storeBlob(ctx context.Context, exec statementRunner, a mail.Attachment) (sha string, size int64, contentType string, err error) {
	sha, size, contentType, data := attachmentIdentity(a)
	if len(data) > 0 {
		err = exec(ctx, sqlAddBlob, sha, contentType, len(data), data)
	}
	return sha, size, contentType, err
}

// replaceAttachments replaces a message's attachment rows and stores any new
// inline bytes once per SHA-256. It is the writer's authoritative shape: the
// message's full attachment list is known. The caller asserts that
// completeness (mail.Details.Attachments): a read that could not see every
// attachment must fail before it reaches the writer, so this never merges or
// guesses what is missing.
func replaceAttachments(ctx context.Context, exec statementRunner, id int64, atts []mail.Attachment) error {
	if err := exec(ctx, sqlDelAtts, id); err != nil {
		return err
	}
	for pos, a := range atts {
		sha, size, _, err := storeBlob(ctx, exec, a)
		if err != nil {
			return err
		}
		if err := exec(ctx, sqlAddAtt, id, pos, a.Name, a.ContentType, size, boolInt(a.Inline),
			a.ContentID, a.ContentLocation, sha); err != nil {
			return err
		}
	}
	return nil
}

// mergeAttachments upserts the attachments a display backfill re-read without
// deleting metadata an earlier read stored. A re-read that lost its accessor
// or returned no part is not evidence that the attachment disappeared.
func mergeAttachments(ctx context.Context, exec statementRunner, id int64, atts []mail.Attachment) error {
	for pos, a := range atts {
		sha, size, _, err := storeBlob(ctx, exec, a)
		if err != nil {
			return err
		}
		if err := exec(ctx, sqlMergeAtt, id, pos, a.Name, a.ContentType, size, boolInt(a.Inline),
			a.ContentID, a.ContentLocation, sha); err != nil {
			return err
		}
	}
	return nil
}

// writeItems replaces a message's attachments and further folders.
func (w *writer) writeItems(ctx context.Context, id int64, m *mail.Message, ts string) error {
	if err := replaceAttachments(ctx, w.exec, id, m.Attachments); err != nil {
		return err
	}
	if err := w.exec(ctx, sqlDelFolders, id); err != nil {
		return err
	}
	for _, ref := range m.Folders {
		f := mail.FolderOf(m)
		f.Path, f.Role = ref.Path, ref.Role
		folderID, err := w.folder(ctx, f, ts)
		if err != nil {
			return err
		}
		if err := w.exec(ctx, sqlAddFolder, id, folderID); err != nil {
			return err
		}
	}
	return nil
}

// flushFTS writes the index entries of the batch, after its other writes.
func (w *writer) flushFTS(ctx context.Context) error {
	for _, p := range w.pending {
		if err := writeFTS(ctx, w.fts, p.id, p.m); err != nil {
			return err
		}
	}
	w.pending = w.pending[:0]
	return nil
}
