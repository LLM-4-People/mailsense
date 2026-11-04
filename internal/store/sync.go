package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Cursor returns the sync cursor of a folder ("" and false when none).
func (s *Store) Cursor(ctx context.Context, folderID int64) (string, bool, error) {
	var c string
	err := s.db.QueryRowContext(ctx, "SELECT cursor FROM sync_cursors WHERE folder_id = ?", folderID).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return c, err == nil, err
}

// SetCursors stores the cursors of a successful run atomically: one
// transaction, empty cursors skipped, and one store-clock timestamp for
// every row. A failure commits none.
func (s *Store) SetCursors(ctx context.Context, cursors map[int64]string) error {
	ids := make([]int64, 0, len(cursors))
	for folderID, cursor := range cursors {
		if cursor != "" {
			ids = append(ids, folderID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return s.busy.WithBusyRetry(ctx, "set sync cursors", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			ts := s.now()
			for _, folderID := range ids {
				if _, err := tx.ExecContext(ctx, `INSERT INTO sync_cursors (folder_id, cursor, updated_at) VALUES (?, ?, ?)
					ON CONFLICT(folder_id) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
					folderID, cursors[folderID], ts); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// RunTotals are the reconciled counters of a sync run.
type RunTotals struct {
	Seen       int `json:"seen"`
	Written    int `json:"written"`
	Skipped    int `json:"skipped"`
	Duplicates int `json:"duplicates"`
	// Altered counts the declared alterations of the written messages
	// by code (mail.Alteration): nothing changed or dropped is silent.
	Altered map[string]int `json:"altered,omitempty"`
}

// Add returns the sum of two totals.
func (t RunTotals) Add(o RunTotals) RunTotals {
	out := RunTotals{Seen: t.Seen + o.Seen, Written: t.Written + o.Written, Skipped: t.Skipped + o.Skipped, Duplicates: t.Duplicates + o.Duplicates}
	for _, m := range []map[string]int{t.Altered, o.Altered} {
		for k, n := range m {
			if out.Altered == nil {
				out.Altered = map[string]int{}
			}
			out.Altered[k] += n
		}
	}
	return out
}

// BeginRun records the start of a sync run and returns its id.
func (s *Store) BeginRun(ctx context.Context, sourceID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, "INSERT INTO sync_runs (source_id, started_at, status) VALUES (?, ?, ?) RETURNING id",
		sourceID, s.now(), string(runstate.Running)).Scan(&id)
	if err == nil {
		publish(s, TopicRunChanged, RunChanged{RunID: id, SourceID: sourceID, State: runstate.Running})
	}
	return id, err
}

// FinishRun records the outcome of a sync run.
func (s *Store) FinishRun(ctx context.Context, runID int64, state runstate.State, t RunTotals, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	altered, err := json.Marshal(t.Altered)
	if err != nil || t.Altered == nil {
		altered = []byte("{}")
	}
	var sourceID string
	err = s.db.QueryRowContext(ctx, `UPDATE sync_runs SET finished_at = ?, status = ?, seen = ?, written = ?, skipped = ?,
		duplicates = ?, altered = ?, error = ? WHERE id = ? RETURNING source_id`, s.now(), string(state), t.Seen, t.Written, t.Skipped,
		t.Duplicates, string(altered), msg, runID).Scan(&sourceID)
	if err == nil {
		publish(s, TopicRunChanged, RunChanged{RunID: runID, SourceID: sourceID, State: state, Totals: t, Error: msg})
	}
	return err
}
