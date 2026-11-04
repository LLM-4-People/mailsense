package store

import (
	"context"
	"database/sql"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// usageDayLayout is the stored form of engine_usage.day. AddUsage writes it
// and ListUsage compares against it, so the window boundary has one owner.
const usageDayLayout = "2006-01-02"

// AddUsage adds one provider call to the persisted counters (the store is
// the engine.UsageSink). Usage accounting is best-effort: the provider call
// already happened, so a record that still fails after the store's busy
// retries is logged once (provider and model only) and dropped. A durable
// outbox is a deliberate follow-up, not hidden here.
func (s *Store) AddUsage(ctx context.Context, rec engine.UsageRecord) error {
	failed := 0
	if rec.Failed {
		failed = 1
	}
	err := s.busy.WithBusyRetry(ctx, "record engine usage", func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO engine_usage (day, provider, model, calls, failures, input_tokens, output_tokens, cost_usd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(day, provider, model) DO UPDATE SET
		calls = calls + excluded.calls, failures = failures + excluded.failures, input_tokens = input_tokens + excluded.input_tokens,
		output_tokens = output_tokens + excluded.output_tokens, cost_usd = cost_usd + excluded.cost_usd`,
			s.clock.Now().UTC().Format(usageDayLayout), rec.Provider, rec.Model, rec.Calls, failed, rec.InputTokens, rec.OutputTokens, rec.EstimatedUSD)
		return err
	})
	if err != nil {
		s.log.Warn("usage record not saved", "provider", rec.Provider, "model", rec.Model)
	}
	return nil
}

// UsageRow is the counters of one day, provider and model.
type UsageRow struct {
	Day          string  `json:"day"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Calls        int64   `json:"calls"`
	Failures     int64   `json:"failures"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ListUsage returns the counters of the last days calendar days, today
// included, newest day first. The window is inclusive at both ends: a row of
// the first day is in, a row of the day before it is out. The caller owns the
// public bound (the API accepts 1..365); days must be positive.
func (s *Store) ListUsage(ctx context.Context, days int) ([]UsageRow, error) {
	now := s.clock.Now().UTC()
	since := now.AddDate(0, 0, -(days - 1)).Format(usageDayLayout)
	today := now.Format(usageDayLayout)
	return query(ctx, s.db, `SELECT day, provider, model, calls, failures, input_tokens, output_tokens, cost_usd
		FROM engine_usage WHERE day >= ? AND day <= ? ORDER BY day DESC, provider, model`, []any{since, today}, func(r *sql.Rows, v *UsageRow) error {
		return r.Scan(&v.Day, &v.Provider, &v.Model, &v.Calls, &v.Failures, &v.InputTokens, &v.OutputTokens, &v.CostUSD)
	})
}
