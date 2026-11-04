package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// answerTable is one fingerprint-scoped answer table. The route answers and
// the threshold-free previews differ only by name and by the shape of their
// decoded answers, so every read, stream write and prune lives here once:
// the two tables stay in lockstep instead of drifting apart one WHERE clause
// at a time.
type answerTable struct {
	name   string
	what   string // how the table is named in an error
	seen   string // the temporary "seen" table of one commit
	insert string // the one upsert
}

var routeAnswerTable = answerTable{
	name: "route_answers", what: "route", seen: "seen_routes",
	insert: `INSERT INTO route_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
	SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
	ON CONFLICT(message_id, fingerprint) DO UPDATE SET revision = excluded.revision,
	answers = excluded.answers, run_id = excluded.run_id, decided_at = excluded.decided_at
	WHERE excluded.revision <> route_answers.revision OR excluded.answers <> route_answers.answers`,
}

var previewAnswerTable = answerTable{
	name: "route_preview_answers", what: "preview", seen: "seen_previews",
	insert: `INSERT INTO route_preview_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
	SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
	ON CONFLICT(message_id, fingerprint) DO UPDATE SET revision = excluded.revision,
	answers = excluded.answers, run_id = excluded.run_id, decided_at = excluded.decided_at
	WHERE excluded.revision <> route_preview_answers.revision OR excluded.answers <> route_preview_answers.answers`,
}

// answerRecord is one stored answer row, decoded into T.
type answerRecord[T any] struct {
	MessageID int64
	Revision  int64
	RunID     int64
	Answers   T
	DecidedAt string
}

// answerRow is a row a commit writes: the two public answer types name the
// same five fields.
type answerRow interface {
	answerFields() (messageID, revision, runID int64, answers string)
}

func (a RouteAnswer) answerFields() (int64, int64, int64, string) {
	return a.MessageID, a.Revision, a.RunID, a.Answers
}

func (a PreviewAnswer) answerFields() (int64, int64, int64, string) {
	return a.MessageID, a.Revision, a.RunID, a.Answers
}

// loadAnswerRecords returns the answers of the given messages with their
// revision and time. A message with no row is absent (never zero-filled),
// and an unreadable row fails the whole read: an answer is not silently an
// abstention.
func loadAnswerRecords[T any](s *Store, ctx context.Context, t answerTable, fingerprint string, ids []int64) (map[int64]answerRecord[T], error) {
	out := make(map[int64]answerRecord[T], len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT message_id, revision, run_id, answers, decided_at FROM "+t.name+
		" WHERE fingerprint = ? AND message_id IN "+idSetSQL+" ORDER BY message_id", fingerprint, idSet(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r answerRecord[T]
		var raw string
		if err := rows.Scan(&r.MessageID, &r.Revision, &r.RunID, &raw, &r.DecidedAt); err != nil {
			return nil, err
		}
		trimmed := bytes.TrimSpace([]byte(raw))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, fmt.Errorf("store: %s answers of message %d: no answers object", t.what, r.MessageID)
		}
		if err := jsonx.DecodeStrict(trimmed, &r.Answers); err != nil {
			return nil, fmt.Errorf("store: %s answers of message %d: %w", t.what, r.MessageID, err)
		}
		out[r.MessageID] = r
	}
	return out, rows.Err()
}

// streamAnswerTable writes the rows each yields, inside tx, and prunes the
// fingerprint's rows the commit no longer answers.
func streamAnswerTable[T answerRow](s *Store, ctx context.Context, tx *sql.Tx, t answerTable, fingerprint string,
	each func(fn func(T) error) error) error {
	seen, err := newSeen(ctx, tx, t.seen)
	if err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, t.insert)
	if err != nil {
		return err
	}
	defer ins.Close()
	now := s.now()
	if err := each(func(a T) error {
		id, revision, runID, answers := a.answerFields()
		return writeAnswerRow(ctx, tx, ins, seen, t, fingerprint, id, revision, runID, answers, now)
	}); err != nil {
		return err
	}
	_, err = seen.deleteStale(ctx, tx, t.name, "fingerprint = ?", fingerprint)
	return err
}

// writeAnswerRow writes one row of t inside tx and records the message in
// the commit's seen set. It is the one place a row of any answer table is
// written, so a single-pass commit and a per-table stream cannot disagree.
func writeAnswerRow(ctx context.Context, tx *sql.Tx, ins *sql.Stmt, seen *seenRows, t answerTable,
	fingerprint string, id, revision, runID int64, answers, now string) error {
	// The message may have been deleted between the run's id snapshot and
	// this transaction. The existence check is part of the insert, under the
	// transaction's write lock, so it skips the row atomically instead of
	// failing the whole commit on the foreign key.
	if _, err := ins.ExecContext(ctx, id, fingerprint, revision, answers, runID, now, id); err != nil {
		return fmt.Errorf("store: save %s answers of message %d: %w", t.what, id, err)
	}
	return seen.add(ctx, tx, id)
}

// pruneAnswerTable removes every fingerprint's rows but keep's: the rows of
// an old profile are unreachable (they are read by the current fingerprint
// only). It returns how many rows it removed.
func pruneAnswerTable(ctx context.Context, ex execer, t answerTable, keep string) (int64, error) {
	if keep == "" {
		return 0, errors.New("store: refusing to prune every answer row under an empty key")
	}
	res, err := ex.ExecContext(ctx, "DELETE FROM "+t.name+" WHERE fingerprint <> ?", keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
