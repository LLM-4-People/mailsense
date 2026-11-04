package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// NewestReceivedAt is the newest received mail stored across every mailbox.
// It is sync evidence, not a scope count: an unpicked store's old row cannot
// make the picked mailbox look current, while a hidden message still proves
// the connector delivered. An empty database returns the zero time.
func (s *Store) NewestReceivedAt(ctx context.Context) (time.Time, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT received_at FROM messages ORDER BY received_at DESC LIMIT 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	at, ok := mail.Parse(raw)
	if !ok {
		return time.Time{}, fmt.Errorf("store: newest received_at %q: not a stored timestamp", raw)
	}
	return at.UTC(), nil
}
