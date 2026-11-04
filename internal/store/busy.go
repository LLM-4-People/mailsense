package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"

	"modernc.org/sqlite"
)

// ErrStoreBusy is the final error of a store write whose SQLITE_BUSY/LOCKED
// retries ran out. Callers that own a report or a message status map it to
// their own state; every other store write returns it unchanged.
var ErrStoreBusy = errors.New("store: the database stayed busy")

// The two busy-retry settings. They keep their historical index.* names so a
// settings.json written before the policy became store-wide still loads (the
// loader rejects unknown keys); both are Storage settings and are read per
// call, so a runtime change applies to the next write without a restart.
const (
	SettingBusyAttempts = "index.store_busy_attempts"
	SettingBusyDelay    = "index.store_busy_delay"
)

// BusyPolicy is the one bounded retry for store writes that SQLite rejects
// with SQLITE_BUSY/SQLITE_LOCKED. SQLite already waits store.busy_timeout
// inside each statement; these attempts cover another writer that holds the
// lock longer.
//
// The settings keep their historical index.* prefix: they were added for the
// indexer's vector writes, outlived that one caller, and renaming them would
// reject a settings.json a user already persisted (the loader refuses unknown
// keys).
type BusyPolicy struct {
	cfg   *config.Config
	clock clock.Clock
	log   *slog.Logger
}

func newBusyPolicy(cfg *config.Config, clk clock.Clock, log *slog.Logger) BusyPolicy {
	return BusyPolicy{cfg: cfg, clock: clock.Or(clk), log: log}
}

// WithBusyRetry runs one idempotent write. A non-busy error is returned at
// once; a busy write is retried while attempts remain and the context is
// alive, waiting delay through the store's clock between attempts. The final
// error names what was being written and wraps both ErrStoreBusy and its
// cause, never SQL text or row values.
func (p BusyPolicy) WithBusyRetry(ctx context.Context, what string, write func() error) error {
	attempts := max(1, p.cfg.Int(SettingBusyAttempts))
	delay := p.cfg.Duration(SettingBusyDelay)
	var err error
	for attempt := 1; ; attempt++ {
		if err = write(); err == nil {
			return nil
		}
		if !IsBusy(err) {
			return err
		}
		if attempt >= attempts || ctx.Err() != nil {
			break
		}
		p.log.Warn("store is busy; retrying", "write", what, "attempt", attempt, "of", attempts, "delay", delay)
		if serr := p.clock.Sleep(ctx, delay); serr != nil {
			return serr
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Wrap the busy cause itself, not the caller's contextual wrap: the
	// final error must name the write without leaking a message id, row
	// value or SQL text.
	cause := err
	var se *sqlite.Error
	if errors.As(err, &se) {
		cause = se
	}
	return fmt.Errorf("%w: %s after %d attempt(s): %w", ErrStoreBusy, what, attempts, cause)
}
