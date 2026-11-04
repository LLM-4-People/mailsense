package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

// QAPlanCacheMax bounds the local plan cache. The cache is a semantic
// shortcut, not history: the newest plans win.
const QAPlanCacheMax = 500

// GetQAPlan reads one cached executable plan by its opaque question key.
// The key is a hash, never the question text.
func (s *Store) GetQAPlan(ctx context.Context, key string) (mailquery.Plan, bool, error) {
	if err := validatePlanKey(key); err != nil {
		return mailquery.Plan{}, false, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT plan FROM qa_plan_cache WHERE question_key = ?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return mailquery.Plan{}, false, nil
	}
	if err != nil {
		return mailquery.Plan{}, false, fmt.Errorf("store: read qa plan cache: %w", err)
	}
	var plan mailquery.Plan
	if err := jsonx.DecodeStrict([]byte(raw), &plan); err != nil {
		return mailquery.Plan{}, false, fmt.Errorf("store: qa plan cache %s: %w", key, err)
	}
	plan = plan.Normalize()
	if err := plan.Validate(); err != nil {
		return mailquery.Plan{}, false, fmt.Errorf("store: qa plan cache %s: %w", key, err)
	}
	return plan, true, nil
}

// SaveQAPlan stores one validated executable plan and prunes the cache to
// the newest QAPlanCacheMax rows, atomically.
func (s *Store) SaveQAPlan(ctx context.Context, key, language string, plan mailquery.Plan) error {
	if err := validatePlanKey(key); err != nil {
		return err
	}
	language = strings.TrimSpace(language)
	if language == "" || len(language) > 16 {
		return fmt.Errorf("store: qa plan cache needs a language")
	}
	plan = plan.Normalize()
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("store: qa plan cache: %w", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("store: qa plan cache: %w", err)
	}
	return s.busy.WithBusyRetry(ctx, "save qa plan", func() error {
		// The upsert and the prune are idempotent: a busy retry replaces the
		// same row and re-prunes to the same bound.
		ts := s.now()
		return s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO qa_plan_cache (question_key, language, plan, created_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(question_key) DO UPDATE SET
			language = excluded.language, plan = excluded.plan, created_at = excluded.created_at`,
				key, language, string(raw), ts); err != nil {
				return err
			}
			_, err := pruneNewest(ctx, tx, "qa_plan_cache", "question_key", "created_at DESC, question_key DESC", QAPlanCacheMax)
			return err
		})
	})
}

// ClearQAPlans removes every cached plan and returns how many it removed.
func (s *Store) ClearQAPlans(ctx context.Context) (int64, error) {
	var n int64
	err := s.busy.WithBusyRetry(ctx, "clear qa plans", func() error {
		res, err := s.db.ExecContext(ctx, "DELETE FROM qa_plan_cache")
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func validatePlanKey(key string) error {
	if len(key) < 16 || len(key) > 128 {
		return fmt.Errorf("store: qa plan key must be 16..128 characters")
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("store: qa plan key must be lowercase hexadecimal")
		}
	}
	return nil
}
