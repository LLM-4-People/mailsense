package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Layer1Answer is one message's answer outputs of a layer-1 run: its
// decision (the stored raw answers), its route answer and its
// threshold-free preview. Empty strings mean the run wrote no row of that
// kind for the message.
//
// The answer phase produces all three from one inference pass, so the store
// writes them in one traversal instead of three: the progress bar counts
// messages, and the commit visits every message once.
type Layer1Answer struct {
	MessageID int64
	Revision  int64
	RunID     int64
	StateHash string
	Decision  string
	Route     string
	Preview   string
}

// Layer1Answers is the one-pass answer stream of a layer-1 commit. Routes
// and Preview say whether this run owns those tables (stale rows of a kind
// the run did not write are kept, exactly like the separate streams).
//
// Derive is the production form: only the decision (the expensive inference
// output) is staged, and Derive rebuilds the message's route answer and
// threshold-free preview from it during the short commit transaction, so the
// disk never carries a second copy of two answer tables that are functions
// of the first. A nil Derive is the direct form: Each's rows are staged
// whole, which is what a test or a caller that already holds all three
// outputs uses.
type Layer1Answers struct {
	Each    func(fn func(Layer1Answer) error) error
	Derive  func(row *Layer1Answer) error
	Routes  bool
	Preview bool
}

// The staging table a layer-1 commit fills before it touches the main
// database. The compact form holds the decision and its row identity; route
// and preview answers are derived during the replay (Layer1Answers.Derive).
// The full form (nil Derive) keeps all three outputs.
const (
	layer1StageTable       = "layer1_staged_answers"
	layer1StageCompactCols = "message_id, revision, run_id, state_hash, decision"
	layer1StageFullCols    = layer1StageCompactCols + ", route, preview"
	// layer1StageBatch bounds one staged INSERT: seven values per row, 128
	// rows, below SQLite's historical 999-variable floor.
	layer1StageBatch = 128
)

// layer1StagePayload, when non-nil, receives the byte size of the staged
// answer text after the staging phase. Tests use it to pin that the compact
// commit stages the decision only; production leaves it nil.
var layer1StagePayload func(bytes int64)

// layer1Stage is one commit's staging table on its dedicated connection.
// TEMP writes are connection-private: filling it takes no lock on the main
// database, so sync, triage, labels and usage keep writing while the answers
// are computed and streamed. The table is disposable: a failed or cancelled
// answer phase leaves the main database untouched.
type layer1Stage struct {
	conn    *sql.Conn
	compact bool
}

// newLayer1Stage (re)creates the staging table. A pooled connection reused
// after a failed commit must not inherit rows, so it drops first; clearing it
// here is the start-of-commit rule. The primary key is the row's message id:
// WITHOUT ROWID makes the table itself the ordered store, so the replay's
// ORDER BY message_id needs no second index and no sort.
func newLayer1Stage(ctx context.Context, conn *sql.Conn, compact bool) (*layer1Stage, error) {
	st := &layer1Stage{conn: conn, compact: compact}
	if err := st.drop(ctx); err != nil {
		return nil, err
	}
	cols := "message_id INTEGER PRIMARY KEY, revision INTEGER NOT NULL, run_id INTEGER NOT NULL, " +
		"state_hash TEXT NOT NULL, decision TEXT NOT NULL"
	if !compact {
		cols += ", route TEXT NOT NULL, preview TEXT NOT NULL"
	}
	if _, err := conn.ExecContext(ctx, "CREATE TEMP TABLE "+layer1StageTable+" ("+cols+") WITHOUT ROWID"); err != nil {
		return nil, fmt.Errorf("store: create layer-1 staging table: %w", err)
	}
	return st, nil
}

// fill streams the commit's answers into the staging table in bounded
// batches. It is called outside every main-database transaction.
func (st *layer1Stage) fill(ctx context.Context, each func(func(Layer1Answer) error) error) error {
	batch := make([]Layer1Answer, 0, layer1StageBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := st.insert(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	if err := each(func(row Layer1Answer) error {
		batch = append(batch, row)
		if len(batch) < layer1StageBatch {
			return nil
		}
		return flush()
	}); err != nil {
		return err
	}
	return flush()
}

// insert stages one bounded batch in one statement.
func (st *layer1Stage) insert(ctx context.Context, rows []Layer1Answer) error {
	cols, width := layer1StageCompactCols, 5
	if !st.compact {
		cols, width = layer1StageFullCols, 7
	}
	var q strings.Builder
	q.WriteString("INSERT INTO temp." + layer1StageTable + " (" + cols + ") VALUES ")
	args := make([]any, 0, len(rows)*width)
	for i, row := range rows {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString("(?, ?, ?, ?, ?")
		if !st.compact {
			q.WriteString(", ?, ?")
		}
		q.WriteString(")")
		args = append(args, row.MessageID, row.Revision, row.RunID, row.StateHash, row.Decision)
		if !st.compact {
			args = append(args, row.Route, row.Preview)
		}
	}
	if _, err := st.conn.ExecContext(ctx, q.String(), args...); err != nil {
		return fmt.Errorf("store: stage layer-1 answers: %w", err)
	}
	return nil
}

// payload reports the staged answer bytes (the payload that remains on disk
// between the answer phase and the write transaction).
func (st *layer1Stage) payload(ctx context.Context) (int64, error) {
	if layer1StagePayload == nil {
		return 0, nil
	}
	expr := "LENGTH(CAST(decision AS BLOB))"
	if !st.compact {
		expr = "LENGTH(CAST(decision AS BLOB)) + LENGTH(CAST(route AS BLOB)) + LENGTH(CAST(preview AS BLOB))"
	}
	var n int64
	err := st.conn.QueryRowContext(ctx, "SELECT COALESCE(SUM("+expr+"), 0) FROM temp."+layer1StageTable).Scan(&n)
	return n, err
}

// each replays the staged rows inside tx in message order, so the seen,
// prune and upsert logic sees exactly the population the run computed.
// Derive completes a compact row (its route and preview answers) from the
// staged decision before the writer sees it; it runs under the transaction,
// so a derive failure rolls the whole commit back.
func (st *layer1Stage) each(ctx context.Context, tx *sql.Tx, derive func(*Layer1Answer) error, fn func(Layer1Answer) error) error {
	cols := layer1StageFullCols
	if st.compact {
		cols = layer1StageCompactCols
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+cols+" FROM temp."+layer1StageTable+" ORDER BY message_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row Layer1Answer
		dest := []any{&row.MessageID, &row.Revision, &row.RunID, &row.StateHash, &row.Decision}
		if !st.compact {
			dest = append(dest, &row.Route, &row.Preview)
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		if st.compact && derive != nil {
			if err := derive(&row); err != nil {
				return err
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// drop removes the staging table after the commit or a failed answer phase.
func (st *layer1Stage) drop(ctx context.Context) error {
	if _, err := st.conn.ExecContext(ctx, "DROP TABLE IF EXISTS temp."+layer1StageTable); err != nil {
		return fmt.Errorf("store: drop layer-1 staging table: %w", err)
	}
	return nil
}

// streamLayer1Answers writes a run's decisions, route answers and previews
// in one pass. The seen table is the union of the messages the run
// handled, so each table loses exactly the rows the run no longer answers.
// In the production one-pass commit Each replays the staged rows (see
// layer1Stage).
func (s *Store) streamLayer1Answers(ctx context.Context, tx *sql.Tx, k TriageKey, routeKey string, a Layer1Answers) error {
	if (a.Routes || a.Preview) && routeKey == "" {
		return fmt.Errorf("store: a layer-1 route commit needs its route key")
	}
	seen, err := newSeen(ctx, tx, "seen_answers")
	if err != nil {
		return err
	}
	dec, err := tx.PrepareContext(ctx, insertDecisionSQL)
	if err != nil {
		return err
	}
	defer dec.Close()
	del, err := tx.PrepareContext(ctx, "DELETE FROM triage_failures WHERE message_id = ? AND fingerprint = ? AND decider = ?")
	if err != nil {
		return err
	}
	defer del.Close()
	var route, preview *sql.Stmt
	if a.Routes {
		if route, err = tx.PrepareContext(ctx, routeAnswerTable.insert); err != nil {
			return err
		}
		defer route.Close()
	}
	if a.Preview {
		if preview, err = tx.PrepareContext(ctx, previewAnswerTable.insert); err != nil {
			return err
		}
		defer preview.Close()
	}
	now := s.now()
	if err := a.Each(func(row Layer1Answer) error {
		if err := seen.add(ctx, tx, row.MessageID); err != nil {
			return err
		}
		if row.Decision != "" {
			if _, err := dec.ExecContext(ctx, row.MessageID, k.Fingerprint, k.Decider, row.StateHash, row.Decision, row.RunID, now, row.Revision, row.MessageID); err != nil {
				return fmt.Errorf("store: save decision %d: %w", row.MessageID, err)
			}
			if _, err := del.ExecContext(ctx, row.MessageID, k.Fingerprint, k.Decider); err != nil {
				return err
			}
		}
		if route != nil && row.Route != "" {
			if err := writeAnswerRow(ctx, tx, route, seen, routeAnswerTable, routeKey, row.MessageID, row.Revision, row.RunID, row.Route, now); err != nil {
				return err
			}
		}
		if preview != nil && row.Preview != "" {
			if err := writeAnswerRow(ctx, tx, preview, seen, previewAnswerTable, routeKey, row.MessageID, row.Revision, row.RunID, row.Preview, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if _, err := seen.deleteStale(ctx, tx, "triage_decisions", "fingerprint = ? AND decider = ?", k.Fingerprint, k.Decider); err != nil {
		return err
	}
	if a.Routes {
		if _, err := seen.deleteStale(ctx, tx, routeAnswerTable.name, "fingerprint = ?", routeKey); err != nil {
			return err
		}
	}
	if a.Preview {
		if _, err := seen.deleteStale(ctx, tx, previewAnswerTable.name, "fingerprint = ?", routeKey); err != nil {
			return err
		}
	}
	return nil
}
