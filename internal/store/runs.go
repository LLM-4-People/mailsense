package store

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Run history tables. They share (id, started_at, finished_at, status,
// report, error) and each adds its own input columns; the begin and finish
// statements are identical, so they live here once. Four copies of the same
// UPDATE meant a lifecycle fix reached one table at a time.
const (
	tableSyncRuns     = "sync_runs"
	tableTriageRuns   = "triage_runs"
	tableIndexRuns    = "index_runs"
	tableEvidenceRuns = "evidence_runs"
	tableLayer1Runs   = "layer1_runs"
)

// beginRun inserts a running row: (started_at, status) then the table's own
// input columns and values.
func (s *Store) beginRun(ctx context.Context, table, columns, placeholders string, args ...any) (int64, error) {
	cols, marks := "started_at, status", "?, ?"
	values := append([]any{s.now(), string(runstate.Running)}, args...)
	if columns != "" {
		cols += ", " + columns
		marks += ", " + placeholders
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO "+table+" ("+cols+") VALUES ("+marks+")", values...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// finishRun records a run's outcome: the same four fields of every table.
func (s *Store) finishRun(ctx context.Context, table string, id int64, state runstate.State, report, errMsg string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE "+table+" SET finished_at = ?, status = ?, report = ?, error = ? WHERE id = ?",
		s.now(), string(state), report, errMsg, id)
	return err
}
