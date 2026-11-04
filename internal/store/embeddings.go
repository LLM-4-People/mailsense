package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// instantSQL is mail.Instant as stored: the single write path computes
// messages.instant from mail.Instant (columns.go), and every read uses the
// column and its indexes. Recomputing the CASE here defeated messages_instant
// and forced a scan plus a temp sort in search, threads, embeddings and the
// mail query's date range.
const instantSQL = "m.instant"

// EmbeddingGeneration counts the embedding writes that change index
// coverage: saved vectors, confirmed vectors and recorded failed attempts.
// It is deliberately separate from MailGeneration and LabelGeneration: an
// embedding write changes no message (so every view of the mail survives it)
// and is not a label edit, while a mail write is not an embedding write.
//
// Every bump lands after its transaction commits, so a reader either sees
// the old counter with the old rows or the new counter with the new rows,
// never the new counter before the rows are visible.
func (s *Store) EmbeddingGeneration() uint64 { return s.embedGen.Load() }

// touchEmbeddings marks a committed embedding-state write (see
// EmbeddingGeneration).
func (s *Store) touchEmbeddings() { s.embedGen.Add(1) }

// EmbeddingState is what the indexer needs to know about one message,
// without its text: where it lives, whether it changed since its vector
// was written, and that vector's space and input hash.
type EmbeddingState struct {
	RowID       int64
	SourceID    string
	StoreID     string
	Role        mail.FolderRole
	Revision    int64 // messages.revision: moves on every write of the message
	Partial     bool  // details not read yet: indexed once complete
	Space       string
	TextHash    string
	TextVersion string // the version of the text the vector was computed from (the cleaning rules)
	// Checked is the revision the vector was written for or last
	// confirmed unchanged at (-1 = no vector).
	Checked int64
	// Failures counts failed attempts at this revision.
	Failures int
}

// embeddingStatesQuery is the one state read of the indexer. The order is the
// messages_instant key order, so the read streams the visible mailbox without
// a temporary sort.
func embeddingStatesQuery(where string) string {
	return `SELECT m.id, m.source_id, m.store_id, f.role, m.revision, m.partial,
		COALESCE(e.space, ''), COALESCE(e.text_hash, ''), COALESCE(e.text_version, ''), COALESCE(e.message_revision, -1), COALESCE(x.failures, 0)
		FROM messages m JOIN folders f ON f.id = m.folder_id LEFT JOIN embeddings e ON e.message_id = m.id
		LEFT JOIN index_failures x ON x.message_id = m.id AND x.revision = m.revision
		WHERE ` + where + ` ORDER BY ` + instantSQL + ` DESC, m.id DESC`
}

// EmbeddingStates lists the visible messages (visibleSQL) with the given
// roles (rolesSQL; none = all), newest first. instant is NOT NULL DEFAULT ”,
// so an undated message's empty string already sorts after every dated one
// under DESC; a leading `m.instant = ”` term only defeats messages_instant
// and forces a temporary sort.
func (s *Store) EmbeddingStates(ctx context.Context, roles []mail.FolderRole) ([]EmbeddingState, error) {
	where, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	var args []any
	if len(roles) > 0 {
		cond, a := rolesSQL(roles)
		where, args = where+" AND "+cond, a
	}
	return query(ctx, s.db, embeddingStatesQuery(where), args,
		func(r *sql.Rows, v *EmbeddingState) error {
			var role string
			err := r.Scan(&v.RowID, &v.SourceID, &v.StoreID, &role, &v.Revision, &v.Partial, &v.Space, &v.TextHash, &v.TextVersion, &v.Checked, &v.Failures)
			v.Role = mail.FolderRole(role)
			return err
		})
}

// IndexableIDs lists the visible message ids the indexer can actually
// vectorize: picked stores and roles, complete messages, and messages below
// the quarantine threshold. Partial and quarantined mail stay in
// EmbeddingStates and the index Coverage (the UI's honest counts), but they
// can never receive a vector, so the semantic search's coverage target must
// not count them: one such message would make the target unreachable and a
// waiting question would poll to its timeout.
func (s *Store) IndexableIDs(ctx context.Context, roles []mail.FolderRole, quarantine int) ([]int64, error) {
	where, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	var args []any
	if len(roles) > 0 {
		cond, a := rolesSQL(roles)
		where, args = where+" AND "+cond, a
	}
	args = append(args, quarantine)
	return query(ctx, s.db, `SELECT m.id FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN index_failures x ON x.message_id = m.id AND x.revision = m.revision
		WHERE `+where+` AND m.partial = 0 AND COALESCE(x.failures, 0) < ?`, args,
		func(r *sql.Rows, id *int64) error { return r.Scan(id) })
}

// MessageText is what an embedding is computed from.
type MessageText struct {
	Subject string
	Body    string
}

// MessageTexts returns the subject and body of the given messages
// (missing ids are absent from the map).
func (s *Store) MessageTexts(ctx context.Context, ids []int64) (map[int64]MessageText, error) {
	out := make(map[int64]MessageText, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, subject, body FROM messages WHERE id IN "+idSetSQL, idSet(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t MessageText
		if err := rows.Scan(&id, &t.Subject, &t.Body); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// EmbeddingRow is one vector to store.
type EmbeddingRow struct {
	MessageID   int64
	Space       string
	TextHash    string
	TextVersion string // the version of the cleaning rules the text was made with
	Revision    int64  // the message revision the text was read at
	Vector      vector.Quantized
}

// seqKey is the meta row of the embeddings write sequence.
const seqKey = "embedding_seq"

// SaveEmbeddings writes vectors in one transaction, each replacing the
// message's previous one, with write sequence numbers that are never
// reused (a counter in meta: MAX(seq)+1 would hand a deleted row's number
// to the next vector, which an incremental load past it never sees). A
// message removed meanwhile is skipped (its vector would be orphaned); a
// stored message's failure record is cleared.
func (s *Store) SaveEmbeddings(ctx context.Context, rows []EmbeddingRow) error {
	if len(rows) == 0 {
		return nil
	}
	err := s.busy.WithBusyRetry(ctx, "save embeddings", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			var seq int64
			err := tx.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key = ?", seqKey).Scan(&seq)
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM embeddings").Scan(&seq)
			}
			if err != nil {
				return err
			}
			st, err := tx.PrepareContext(ctx, `INSERT INTO embeddings (message_id, space, text_hash, text_version, message_revision, dims, vector, scale, seq, embedded_at)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
			ON CONFLICT(message_id) DO UPDATE SET space = excluded.space, text_hash = excluded.text_hash, text_version = excluded.text_version,
			message_revision = excluded.message_revision, dims = excluded.dims, vector = excluded.vector,
			scale = excluded.scale, seq = excluded.seq, embedded_at = excluded.embedded_at`)
			if err != nil {
				return err
			}
			defer st.Close()
			ts := s.now()
			for _, r := range rows {
				seq++
				if _, err := st.ExecContext(ctx, r.MessageID, r.Space, r.TextHash, r.TextVersion, r.Revision, len(r.Vector.Values),
					r.Vector.Bytes(), float64(r.Vector.Scale), seq, ts, r.MessageID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM index_failures WHERE message_id = ?", r.MessageID); err != nil {
					return err
				}
			}
			return putMeta(ctx, tx, seqKey, strconv.FormatInt(seq, 10))
		})
	})
	if err != nil {
		return err
	}
	s.touchEmbeddings()
	return nil
}

// IndexFailure is one failed embedding attempt.
type IndexFailure struct {
	MessageID int64
	Revision  int64
	Error     string
}

// RecordIndexFailures counts failed attempts per message: another failure
// at the same revision adds one, a new revision starts again at one.
func (s *Store) RecordIndexFailures(ctx context.Context, fails []IndexFailure) error {
	if len(fails) == 0 {
		return nil
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ts := s.now()
		for _, f := range fails {
			if _, err := tx.ExecContext(ctx, `INSERT INTO index_failures (message_id, revision, failures, error, failed_at)
				SELECT ?, ?, 1, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
				ON CONFLICT(message_id) DO UPDATE SET failures = CASE WHEN revision = excluded.revision THEN failures + 1 ELSE 1 END,
				revision = excluded.revision, error = excluded.error, failed_at = excluded.failed_at`,
				f.MessageID, f.Revision, f.Error, ts, f.MessageID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.touchEmbeddings()
	return nil
}

// ConfirmEmbeddings records that messages changed (a read flag, a move)
// or were cleaned by new rules without changing their embedded text: their
// vectors stay, their change marker moves to the revision checked
// (message id → revision) and their text version to textVersion.
func (s *Store) ConfirmEmbeddings(ctx context.Context, revisions map[int64]int64, textVersion string) error {
	if len(revisions) == 0 {
		return nil
	}
	err := s.busy.WithBusyRetry(ctx, "confirm embeddings", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			st, err := tx.PrepareContext(ctx, "UPDATE embeddings SET message_revision = ?, text_version = ? WHERE message_id = ?")
			if err != nil {
				return err
			}
			defer st.Close()
			for id, rev := range revisions {
				if _, err := st.ExecContext(ctx, rev, textVersion, id); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	s.touchEmbeddings()
	return nil
}

// StoredVector is one vector of a space as search loads it.
type StoredVector struct {
	MessageID int64
	Seq       int64
	Vector    vector.Quantized
}

// EachVector streams the vectors of a space written after afterSeq, in
// write order, without holding them: fn gets the raw int8 bytes (valid
// only during the call), so the caller's copy is the only one in memory.
func (s *Store) EachVector(ctx context.Context, space string, afterSeq int64, fn func(messageID, seq int64, values []byte, scale float32) error) error {
	rows, err := s.db.QueryContext(ctx, "SELECT message_id, seq, vector, scale FROM embeddings WHERE space = ? AND seq > ? ORDER BY seq",
		space, afterSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int64
		var blob sql.RawBytes
		var scale float64
		if err := rows.Scan(&id, &seq, &blob, &scale); err != nil {
			return err
		}
		if err := fn(id, seq, blob, float32(scale)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// LoadVectors returns the vectors of a space written after afterSeq, in
// write order (a copy of each: for tests and small spaces; search streams
// with EachVector).
func (s *Store) LoadVectors(ctx context.Context, space string, afterSeq int64) ([]StoredVector, error) {
	var out []StoredVector
	err := s.EachVector(ctx, space, afterSeq, func(id, seq int64, values []byte, scale float32) error {
		out = append(out, StoredVector{MessageID: id, Seq: seq, Vector: vector.FromBytes(values, scale)})
		return nil
	})
	return out, err
}

// CountVectors counts the vectors of a space.
func (s *Store) CountVectors(ctx context.Context, space string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM embeddings WHERE space = ?", space).Scan(&n)
	return n, err
}

// IndexRun is one recorded index run; Report is its JSON report.
type IndexRun struct {
	ID         int64          `json:"id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	State      runstate.State `json:"state"`
	Space      string         `json:"space"`
	Report     string         `json:"report"`
	Error      string         `json:"error"`
}

// BeginIndexRun records a running index run.
func (s *Store) BeginIndexRun(ctx context.Context, space string) (int64, error) {
	return s.beginRun(ctx, tableIndexRuns, "space", "?", space)
}

// FinishIndexRun records the outcome of a run.
func (s *Store) FinishIndexRun(ctx context.Context, id int64, state runstate.State, report, errMsg string) error {
	return s.finishRun(ctx, tableIndexRuns, id, state, report, errMsg)
}

// ListIndexRuns returns the most recent runs, newest first.
func (s *Store) ListIndexRuns(ctx context.Context, limit int) ([]IndexRun, error) {
	return listRuns(ctx, s, "index_runs", []string{"space", "report", "error"}, limit,
		func(r *sql.Rows, v *IndexRun) error {
			var st string
			err := r.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &st, &v.Space, &v.Report, &v.Error)
			v.State = runstate.State(st)
			return err
		})
}

// PruneIndexRuns keeps the newest keep index runs and returns how many it
// removed. The just-finished run is the newest, so it is never removed.
// SQLite reuses the freed pages: the database file may not shrink.
func (s *Store) PruneIndexRuns(ctx context.Context, keep int) (int64, error) {
	return pruneNewest(ctx, s.db, "index_runs", "id", "id DESC", keep)
}
