package store

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// AcquireLease takes the named lease for holder until the given time; it
// succeeds when the lease is free, expired or already the holder's.
func (s *Store) AcquireLease(ctx context.Context, name, holder string, until time.Time) (bool, error) {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO leases (name, holder, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE leases.expires_at < ? OR leases.holder = excluded.holder`,
		name, holder, until.UTC().Format(mail.TimeLayout), now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReleaseLease frees a lease the holder owns.
func (s *Store) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM leases WHERE name = ? AND holder = ?", name, holder)
	return err
}
