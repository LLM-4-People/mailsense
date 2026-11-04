package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// ReadOnly runs validated SQL on a separate read-only, query_only pool with
// the row cap and time limit from query.*.
type ReadOnly struct {
	db    *sql.DB
	cfg   *config.Config
	clock clock.Clock
	log   *slog.Logger
}

// Result is the outcome of one read-only query.
type Result struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"` // more rows existed than Limit
	Limit     int      `json:"limit"`
}

// OpenReadOnly opens the read-only pool on an existing database; clk may be nil.
func OpenReadOnly(ctx context.Context, cfg *config.Config, clk clock.Clock) (*ReadOnly, error) {
	params := baseParams(cfg.Duration("store.busy_timeout"))
	params.Set("mode", "ro")
	params.Add("_pragma", "query_only(1)")
	db, err := sql.Open("sqlite", dsn(Path(cfg), params))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.Int(SettingQueryMaxOpenConns))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open read-only: %w", err)
	}
	return &ReadOnly{db: db, cfg: cfg, clock: clock.Or(clk), log: logx.For("store.query")}, nil
}

// Close closes the pool.
func (r *ReadOnly) Close() error { return r.db.Close() }

// Query validates and runs one SELECT/WITH statement. limit < 1 or above
// query.max_rows means query.max_rows.
func (r *ReadOnly) Query(ctx context.Context, sqlText string, limit int) (*Result, error) {
	stmt, err := ValidateSQL(sqlText)
	if err != nil {
		r.log.Warn("query rejected", "err", err)
		return nil, err
	}
	if maxRows := r.cfg.Int(SettingQueryMaxRows); limit < 1 || limit > maxRows {
		limit = maxRows
	}
	// SQLite enforces the limit by interrupting the statement when the
	// context deadline passes.
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Duration(SettingQueryTimeout))
	defer cancel()
	start := r.clock.Now()
	rows, err := r.db.QueryContext(ctx, "SELECT * FROM ("+stmt+") LIMIT "+strconv.Itoa(limit+1))
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	defer rows.Close()
	res := &Result{Limit: limit}
	if res.Columns, err = rows.Columns(); err != nil {
		return nil, err
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.Truncated = true
			break
		}
		vals := make([]any, len(res.Columns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, r.fail(ctx, err)
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		res.Rows = append(res.Rows, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, r.fail(ctx, err)
	}
	r.log.Info("query", "rows", len(res.Rows), "truncated", res.Truncated, "duration", r.clock.Now().Sub(start))
	return res, nil
}

func (r *ReadOnly) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		err = fmt.Errorf("store: query exceeded %s %s: %w", SettingQueryTimeout, r.cfg.Duration(SettingQueryTimeout), ctx.Err())
	}
	r.log.Warn("query failed", "err", err)
	return err
}
