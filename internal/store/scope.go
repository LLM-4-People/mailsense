package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// StoreRef is one mailbox store of a source as discovery recorded it.
type StoreRef struct {
	SourceID string         `json:"source_id"`
	StoreID  string         `json:"store_id"`
	Name     string         `json:"name"`
	Kind     mail.StoreKind `json:"kind"`
}

// Key is the store's stable key.
func (r StoreRef) Key() string { return source.StoreKey(r.SourceID, r.StoreID) }

// ScopeStores lists every picked store with a visible folder (visibleSQL).
func (s *Store) ScopeStores(ctx context.Context) ([]StoreRef, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	return query(ctx, s.db, `SELECT f.source_id, f.store_id, MAX(f.store_name), MAX(f.store_kind) FROM folders f WHERE `+vis+`
		GROUP BY f.source_id, f.store_id ORDER BY source_id, store_id`, nil, func(r *sql.Rows, v *StoreRef) error {
		var kind string
		err := r.Scan(&v.SourceID, &v.StoreID, &v.Name, &kind)
		v.Kind = mail.StoreKind(kind)
		return err
	})
}

// ScopeRow is the scope evidence of one stored message.
type ScopeRow struct {
	RowID    int64
	Revision int64
	Evidence scope.Evidence
}

// EachStaleScope calls fn with one bounded page at a time: the evidence of
// the visible messages of one store whose verdict is missing, older than the
// message (revision) or computed against another identity (its digest), in
// row-id order. The page rows are read and closed before fn runs, so a
// caller may write the page (scope verdicts) while the only pooled
// connection is free: the read never holds the connection a write needs.
// Pages are keyset-paged by m.id and fn getting an empty page is impossible;
// a short page ends the walk. Recipients come with each row.
func (s *Store) EachStaleScope(ctx context.Context, ref StoreRef, identity string, pageSize int, fn func([]ScopeRow) error) error {
	if pageSize <= 0 {
		return fmt.Errorf("store: stale scope page size %d: must be positive", pageSize)
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return err
	}
	var after int64
	for {
		page, err := s.staleScopePage(ctx, vis, ref, identity, after, pageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		if err := fn(page); err != nil {
			return err
		}
		if len(page) < pageSize {
			return nil
		}
		after = page[len(page)-1].RowID
	}
}

// staleScopePage reads one page of EachStaleScope whose row id is greater
// than after. It returns with the cursor closed.
func (s *Store) staleScopePage(ctx context.Context, vis string, ref StoreRef, identity string, after int64, limit int) ([]ScopeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, m.revision, f.role, m.sender_address, m.sent_representing_address,
		m.addressed_to_me, m.headers,
		COALESCE((SELECT group_concat(r.address, char(31)) FROM recipients r WHERE r.message_id = m.id), '')
		FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+vis+` AND m.source_id = ? AND m.store_id = ?
		AND (v.message_id IS NULL OR v.revision <> m.revision OR v.identity <> ?) AND m.id > ?
		ORDER BY m.id LIMIT ?`,
		ref.SourceID, ref.StoreID, identity, after, limit)
	if err != nil {
		return nil, err
	}
	var page []ScopeRow
	for rows.Next() {
		var r ScopeRow
		var role, headers, recips string
		ev := &r.Evidence
		if err := rows.Scan(&r.RowID, &r.Revision, &role, &ev.SenderAddress, &ev.SentRepresenting, &ev.AddressedToMe,
			&headers, &recips); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ev.Role = mail.FolderRole(role)
		if recips != "" {
			ev.Recipients = strings.Split(recips, "\x1f")
		}
		ev.AddHeaders(headers)
		page = append(page, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// The caller may write the page: the cursor must not hold the connection
	// (store.max_open_conns can be 1).
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return page, nil
}

// VerdictRow is one computed verdict at the message revision it judged.
type VerdictRow struct {
	RowID    int64
	Revision int64
	Verdict  scope.Verdict
	Reason   scope.Reason
}

// WriteVerdicts stores verdicts computed against the identity with this
// digest (replacing older ones). Every verdict and reason is validated
// first; nothing is written if one is invalid.
func (s *Store) WriteVerdicts(ctx context.Context, identity string, verdicts []VerdictRow) error {
	for _, v := range verdicts {
		if !v.Verdict.Valid() || !v.Reason.Valid() {
			return fmt.Errorf("store: invalid scope verdict %q/%q for message %d", v.Verdict, v.Reason, v.RowID)
		}
	}
	if len(verdicts) == 0 {
		return nil
	}
	ts := s.now()
	if err := s.busy.WithBusyRetry(ctx, "write scope verdicts", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error {
			st, err := tx.PrepareContext(ctx, `INSERT INTO scope_verdicts (message_id, verdict, reason, computed_at, revision, identity)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(message_id) DO UPDATE SET verdict = excluded.verdict, reason = excluded.reason,
			computed_at = excluded.computed_at, revision = excluded.revision, identity = excluded.identity`)
			if err != nil {
				return err
			}
			defer st.Close()
			for _, v := range verdicts {
				if _, err := st.ExecContext(ctx, v.RowID, string(v.Verdict), string(v.Reason), ts, v.Revision, identity); err != nil {
					return fmt.Errorf("store: verdict for message %d: %w", v.RowID, err)
				}
			}
			return nil
		})
	}); err != nil {
		return err
	}
	s.touchVisibility()
	return nil
}

// ClearVerdicts removes every verdict of a store (its owner identity is
// unknown: none of its mail can be judged) and returns how many there were.
func (s *Store) ClearVerdicts(ctx context.Context, ref StoreRef) (int64, error) {
	defer s.touchVisibility()
	res, err := s.db.ExecContext(ctx, `DELETE FROM scope_verdicts WHERE message_id IN
		(SELECT id FROM messages WHERE source_id = ? AND store_id = ?)`, ref.SourceID, ref.StoreID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// VerdictCounts returns the number of visible messages (visibleSQL) per
// verdict, plus the number without a verdict under the "" key. It is the sum
// of the per-store counts, so the mailbox-wide total and the per-store map are
// one population by construction and cannot disagree.
func (s *Store) VerdictCounts(ctx context.Context) (map[scope.Verdict]int, error) {
	byStore, err := s.VerdictCountsByStore(ctx)
	if err != nil {
		return nil, err
	}
	out := map[scope.Verdict]int{}
	for _, counts := range byStore {
		for verdict, n := range counts {
			out[verdict] += n
		}
	}
	return out, nil
}

// VerdictCountsByStore is the one verdict count query, grouped by (source id,
// store id) and verdict.
func (s *Store) VerdictCountsByStore(ctx context.Context) (map[[2]string]map[scope.Verdict]int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.source_id, m.store_id, COALESCE(v.verdict, ''), COUNT(*) FROM messages m
		JOIN folders f ON f.id = m.folder_id AND `+vis+`
		LEFT JOIN scope_verdicts v ON v.message_id = m.id GROUP BY 1, 2, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[[2]string]map[scope.Verdict]int{}
	for rows.Next() {
		var src, store, v string
		var n int
		if err := rows.Scan(&src, &store, &v, &n); err != nil {
			return nil, err
		}
		k := [2]string{src, store}
		if out[k] == nil {
			out[k] = map[scope.Verdict]int{}
		}
		out[k][scope.Verdict(v)] = n
	}
	return out, rows.Err()
}

// ReasonCount is how many visible messages got one verdict by one rule.
type ReasonCount struct {
	Verdict scope.Verdict `json:"verdict"`
	Reason  scope.Reason  `json:"reason"`
	Count   int           `json:"count"`
}

// VerdictReasons counts the verdicts of visible messages per rule (the
// scope breakdown the UI and the fixture report show).
func (s *Store) VerdictReasons(ctx context.Context) ([]ReasonCount, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	return query(ctx, s.db, `SELECT v.verdict, v.reason, COUNT(*) FROM scope_verdicts v JOIN messages m ON m.id = v.message_id
		JOIN folders f ON f.id = m.folder_id AND `+vis+` GROUP BY 1, 2 ORDER BY 1, 2`, nil,
		func(r *sql.Rows, v *ReasonCount) error {
			var verdict, reason string
			err := r.Scan(&verdict, &reason, &v.Count)
			v.Verdict, v.Reason = scope.Verdict(verdict), scope.Reason(reason)
			return err
		})
}
