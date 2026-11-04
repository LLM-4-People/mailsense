package store

import (
	"context"
	"fmt"
)

// Checkpoint folds the WAL into the main database file and truncates it. A
// reader or writer that blocks the pass makes SQLite report busy in the
// result row without returning an error, so the row is checked: an ignored
// busy result reported success while the WAL stayed at its full size.
func (s *Store) Checkpoint(ctx context.Context) error {
	var busy, log, checkpointed int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("store: checkpoint busy: %d of %d pages checkpointed", checkpointed, log)
	}
	return nil
}

// SizeBytes is the logical database size (page_count × page_size).
func (s *Store) SizeBytes(ctx context.Context) (int64, error) {
	var pages, size int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return 0, err
	}
	return pages * size, nil
}
