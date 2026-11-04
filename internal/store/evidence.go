package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// EvidenceRow is one message's stored evidence (internal/evidence): the
// values, a JSON array aligned with the schema they were computed under,
// at the message revision they were computed at, with the digest of the
// message's context they read (Context: verdict, owner, capabilities…).
type EvidenceRow struct {
	MessageID int64
	Revision  int64
	SchemaID  int64
	Context   string
	Values    string
	// Current is the message's revision now (read back only): a row of
	// another revision is stale until the next run.
	Current int64
}

// EvidenceSchemaID returns the id of a schema (its JSON form), recording
// it the first time.
func (s *Store) EvidenceSchemaID(ctx context.Context, signals string) (int64, error) {
	if signals == "" {
		return 0, errors.New("store: an evidence schema needs its signals")
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO evidence_schemas (signals) VALUES (?) ON CONFLICT(signals) DO NOTHING", signals); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM evidence_schemas WHERE signals = ?", signals).Scan(&id)
	return id, err
}

// LookupEvidenceSchema returns the id of a recorded schema (0 when it was
// never recorded: no message has evidence under it).
func (s *Store) LookupEvidenceSchema(ctx context.Context, signals string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM evidence_schemas WHERE signals = ?", signals).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// EvidenceSchemas returns every recorded schema by id.
func (s *Store) EvidenceSchemas(ctx context.Context) (map[int64]string, error) {
	type row struct {
		id  int64
		sig string
	}
	rows, err := query(ctx, s.db, "SELECT id, signals FROM evidence_schemas", nil,
		func(r *sql.Rows, v *row) error { return r.Scan(&v.id, &v.sig) })
	out := map[int64]string{}
	for _, r := range rows {
		out[r.id] = r.sig
	}
	return out, err
}

// EvidenceOf returns the stored evidence of the given messages (absent
// ids have none).
func (s *Store) EvidenceOf(ctx context.Context, ids []int64) (map[int64]EvidenceRow, error) {
	out := make(map[int64]EvidenceRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := query(ctx, s.db, `SELECT e.message_id, e.revision, e.schema_id, e.context, e.vals, m.revision FROM evidence e
		JOIN messages m ON m.id = e.message_id WHERE e.message_id IN `+idSetSQL, []any{idSet(ids)}, scanEvidence)
	for _, r := range rows {
		out[r.MessageID] = r
	}
	return out, err
}

func scanEvidence(r *sql.Rows, v *EvidenceRow) error {
	return r.Scan(&v.MessageID, &v.Revision, &v.SchemaID, &v.Context, &v.Values, &v.Current)
}

// SaveEvidence writes evidence rows in one transaction, each replacing
// the message's previous one; a message removed meanwhile is skipped.
func (s *Store) SaveEvidence(ctx context.Context, rows []EvidenceRow) error {
	if len(rows) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO evidence (message_id, revision, schema_id, context, vals)
			SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
			ON CONFLICT(message_id) DO UPDATE SET revision = excluded.revision, schema_id = excluded.schema_id,
			context = excluded.context, vals = excluded.vals`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, r := range rows {
			if _, err := st.ExecContext(ctx, r.MessageID, r.Revision, r.SchemaID, r.Context, r.Values, r.MessageID); err != nil {
				return fmt.Errorf("store: evidence of message %d: %w", r.MessageID, err)
			}
		}
		return nil
	})
}

// EachEvidence streams the stored evidence of the visible messages
// (visibleSQL) in message order.
func (s *Store) EachEvidence(ctx context.Context, fn func(EvidenceRow) error) error {
	vis, err := s.visible(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.message_id, e.revision, e.schema_id, e.context, e.vals, m.revision FROM evidence e
		JOIN messages m ON m.id = e.message_id JOIN folders f ON f.id = m.folder_id WHERE `+vis+` ORDER BY e.message_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r EvidenceRow
		if err := scanEvidence(rows, &r); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// EvidenceCounts are the visible messages, those whose evidence was
// computed at their current revision under a schema, and those still
// waiting for their details (Partial: their values are pending).
type EvidenceCounts struct {
	Total, Done, Pending int
}

// EvidenceProgress counts the visible messages, those whose evidence was
// computed at their current revision under the schema and the partial ones.
func (s *Store) EvidenceProgress(ctx context.Context, schemaID int64) (EvidenceCounts, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return EvidenceCounts{}, err
	}
	var c EvidenceCounts
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(e.message_id IS NOT NULL AND e.revision = m.revision AND e.schema_id = ?), 0),
		COALESCE(SUM(m.partial = 1), 0)
		FROM messages m JOIN folders f ON f.id = m.folder_id LEFT JOIN evidence e ON e.message_id = m.id WHERE `+vis, schemaID).Scan(&c.Total, &c.Done, &c.Pending)
	return c, err
}

// EmbeddingSequence is the embeddings write sequence (every vector write
// moves it): with Generation, what tells a reader of vectors that its
// view is still current.
func (s *Store) EmbeddingSequence(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key = ?), 0)", seqKey).Scan(&seq)
	return seq, err
}

// EvidenceInputs is the fingerprint of the stored data evidence reads:
// every visible message (id, revision, folder role, scope verdict, its
// reason and the identity it was judged against) and its vector's write
// sequence. Any
// write, removal, verdict change, new vector or selection change moves it.
func (s *Store) EvidenceInputs(ctx context.Context) (string, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.revision, f.role, COALESCE(v.verdict, '') || ' ' || COALESCE(v.reason, ''), COALESCE(v.identity, ''), COALESCE(e.seq, -1)
		FROM messages m JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id
		LEFT JOIN embeddings e ON e.message_id = m.id WHERE `+vis+` ORDER BY m.id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := digest.NewHasher()
	buf := make([]byte, 0, 128)
	for rows.Next() {
		var id, rev, seq int64
		var role, verdict, identity string
		if err := rows.Scan(&id, &rev, &role, &verdict, &identity, &seq); err != nil {
			return "", err
		}
		buf = strconv.AppendInt(buf[:0], id, 10)
		buf = append(buf, ' ')
		buf = strconv.AppendInt(buf, rev, 10)
		buf = append(append(append(append(append(append(buf, ' '), role...), ' '), verdict...), ' '), identity...)
		buf = strconv.AppendInt(append(buf, ' '), seq, 10)
		_, _ = h.Write(append(buf, '\n'))
	}
	return h.Hex(), rows.Err()
}

// EvidenceRun is one recorded evidence run; Report is its JSON report.
type EvidenceRun struct {
	ID         int64          `json:"id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	State      runstate.State `json:"state"`
	Inputs     string         `json:"inputs"`
	Report     string         `json:"report"`
	Error      string         `json:"error"`
}

// BeginEvidenceRun records a running evidence run over the given inputs.
func (s *Store) BeginEvidenceRun(ctx context.Context, inputs string) (int64, error) {
	return s.beginRun(ctx, tableEvidenceRuns, "inputs", "?", inputs)
}

// FinishEvidenceRun records the outcome of a run.
func (s *Store) FinishEvidenceRun(ctx context.Context, id int64, state runstate.State, report, errMsg string) error {
	return s.finishRun(ctx, tableEvidenceRuns, id, state, report, errMsg)
}

// ListEvidenceRuns returns the most recent runs, newest first.
func (s *Store) ListEvidenceRuns(ctx context.Context, limit int) ([]EvidenceRun, error) {
	return listRuns(ctx, s, "evidence_runs", []string{"inputs", "report", "error"}, limit,
		func(r *sql.Rows, v *EvidenceRun) error {
			var st string
			err := r.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &st, &v.Inputs, &v.Report, &v.Error)
			v.State = runstate.State(st)
			return err
		})
}

// PruneEvidence keeps the newest keep evidence runs and the schemas that
// some message's evidence or the current schema still uses; it returns
// how many runs and schemas it removed.
func (s *Store) PruneEvidence(ctx context.Context, keep int, current int64) (runs, schemas int64, err error) {
	runs, err = pruneNewest(ctx, s.db, "evidence_runs", "id", "id DESC", keep)
	if err != nil {
		return 0, 0, err
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM evidence_schemas WHERE id <> ? AND NOT EXISTS (SELECT 1 FROM evidence e WHERE e.schema_id = evidence_schemas.id)", current)
	if err != nil {
		return runs, 0, err
	}
	schemas, _ = res.RowsAffected()
	return runs, schemas, nil
}

// EvidenceReasonIDs returns the ids of reasons, recording the new ones.
func (s *Store) EvidenceReasonIDs(ctx context.Context, reasons []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(reasons) == 0 {
		return out, nil
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, r := range reasons {
			if _, err := tx.ExecContext(ctx, "INSERT INTO evidence_reasons (reason) VALUES (?) ON CONFLICT(reason) DO NOTHING", r); err != nil {
				return err
			}
			var id int64
			if err := tx.QueryRowContext(ctx, "SELECT id FROM evidence_reasons WHERE reason = ?", r).Scan(&id); err != nil {
				return err
			}
			out[r] = id
		}
		return nil
	})
	return out, err
}

// EvidenceReasons returns every interned reason by id.
func (s *Store) EvidenceReasons(ctx context.Context) (map[int64]string, error) {
	type row struct {
		id     int64
		reason string
	}
	rows, err := query(ctx, s.db, "SELECT id, reason FROM evidence_reasons", nil, func(r *sql.Rows, v *row) error { return r.Scan(&v.id, &v.reason) })
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.id] = r.reason
	}
	return out, err
}
