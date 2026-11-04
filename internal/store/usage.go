package store

import (
	"context"
	"database/sql"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

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
			s.clock.Now().UTC().Format("2006-01-02"), rec.Provider, rec.Model, rec.Calls, failed, rec.InputTokens, rec.OutputTokens, rec.EstimatedUSD)
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

// ListUsage returns the counters, newest day first.
func (s *Store) ListUsage(ctx context.Context) ([]UsageRow, error) {
	return query(ctx, s.db, `SELECT day, provider, model, calls, failures, input_tokens, output_tokens, cost_usd
		FROM engine_usage ORDER BY day DESC, provider, model`, nil, func(r *sql.Rows, v *UsageRow) error {
		return r.Scan(&v.Day, &v.Provider, &v.Model, &v.Calls, &v.Failures, &v.InputTokens, &v.OutputTokens, &v.CostUSD)
	})
}
