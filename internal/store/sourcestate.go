package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LoadState returns the state a connector kept under name (nil when
// none): source.StateKeeper.
func (s *Store) LoadState(ctx context.Context, sourceID, name string) ([]byte, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM source_state WHERE source_id = ? AND name = ?", sourceID, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: load state %s/%s: %w", sourceID, name, err)
	}
	return []byte(v), nil
}

// SaveState replaces the state a connector keeps under name.
func (s *Store) SaveState(ctx context.Context, sourceID, name string, value []byte) error {
	if sourceID == "" || name == "" {
		return errors.New("store: a kept state needs a source id and a name")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_state (source_id, name, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		sourceID, name, string(value), s.now())
	if err != nil {
		return fmt.Errorf("store: save state %s/%s: %w", sourceID, name, err)
	}
	return nil
}
