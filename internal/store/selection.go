package store

import (
	"context"
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// visibleSQL selects the folders whose mail the app sees: still
// discovered (active) and of a picked store (source.SettingSelected). It
// is the one predicate of every query over the user's mail (search,
// vectors, scope, triage, learners, progress), so unpicking a store takes
// its mail out of all of them at once; the mail itself is kept and comes
// back when the store is picked again.
const visibleSQL = "f.active = 1 AND f.picked = 1"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("mailsense_store_key", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		src, ok1 := args[0].(string)
		id, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("mailsense_store_key: %T, %T are not text", args[0], args[1])
		}
		return source.StoreKey(src, id), nil
	})
}

// selection is the current selection of the configuration: its scope
// string (sorted keys) and the set.
func (s *Store) selection() (string, map[string]bool) {
	snap := s.cfg.Snapshot()
	return source.Scope(snap), source.Selected(snap)
}

// applySelection brings folders.picked in line with the configuration when
// the selection changed since it was last applied (cheap otherwise: one
// string comparison). Every reader of visible mail and Generation call it,
// so an edit of settings.json applies at the next read.
func (s *Store) applySelection(ctx context.Context) error {
	scope, picked := s.selection()
	if p := s.applied.Load(); p != nil && *p == scope {
		return nil
	}
	s.selMu.Lock()
	defer s.selMu.Unlock()
	if p := s.applied.Load(); p != nil && *p == scope {
		return nil
	}
	keys := make([]string, 0, len(picked))
	for k := range picked {
		keys = append(keys, k)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE folders SET picked = (store_key IN (SELECT value FROM json_each(?1)))
		WHERE picked <> (store_key IN (SELECT value FROM json_each(?1)))`, jsonList(keys))
	if err != nil {
		return fmt.Errorf("store: apply the mailbox selection: %w", err)
	}
	n, _ := res.RowsAffected()
	// The scope can change without a folder row changing (a picked store
	// with no folders yet, or a contact-only store). Every derived view
	// keyed by the generation still has to rebuild, so the bump belongs to
	// the scope change, not to the row count.
	s.touchVisibility()
	if n > 0 {
		s.log.Info("mailbox selection applied", "picked", len(keys), "folders_changed", n)
	}
	s.applied.Store(&scope)
	return nil
}

// visible is visibleSQL once the selection is applied.
func (s *Store) visible(ctx context.Context) (string, error) {
	return visibleSQL, s.applySelection(ctx)
}

// pickedKey reports whether a store key is picked now (new folder rows).
func (s *Store) pickedKey(key string) bool {
	_, picked := s.selection()
	return picked[key]
}
