package store

import (
	"context"
	"database/sql"
	"fmt"
)

// seenRows is the set of message ids a stream answered in this commit. The
// answer tables are keyed by message, so after a stream the rows the stream
// did not visit are stale and are the only ones a commit must delete. Using
// a temp table (in memory, never in the WAL) keeps the commit proportional
// to what changed instead of rewriting the whole mailbox every run.
type seenRows struct {
	name string
}

// newSeen creates the temp table inside tx. It drops first: a connection
// reused after a rolled-back commit must not inherit rows.
func newSeen(ctx context.Context, tx *sql.Tx, name string) (*seenRows, error) {
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp."+name); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE "+name+" (id INTEGER PRIMARY KEY)"); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", name, err)
	}
	return &seenRows{name: name}, nil
}

// add records one answered message.
func (s *seenRows) add(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO "+s.name+" (id) VALUES (?)", id)
	return err
}

// deleteStale removes the rows of table that the stream did not visit.
// where and args narrow the delete to the commit's key (fingerprint, and the
// decider for triage decisions).
func (s *seenRows) deleteStale(ctx context.Context, tx *sql.Tx, table, where string, args ...any) (int64, error) {
	q := "DELETE FROM " + table + " WHERE " + where + " AND message_id NOT IN (SELECT id FROM " + s.name + ")"
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("store: prune %s: %w", table, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
