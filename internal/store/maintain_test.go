package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// triagePrunePlan is the EXPLAIN QUERY PLAN of the exact prune statement the
// store executes, one line per step.
func triagePrunePlan(t *testing.T, s *Store, table, at string) string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+triagePruneSQL(table, at), 3, pruneBatch)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

// The old prune re-evaluated the kept key list per outer row (a correlated
// scalar subquery over 69k decisions held the write lock from boot). The
// replacement materializes the kept keys once per batched DELETE.
func TestPruneTriageKeysIsLinearAndBatched(t *testing.T) {
	s, _ := open(t, map[string]any{SettingRetainTriage: 3})
	ctx := t.Context()

	for _, table := range []struct{ name, at string }{
		{"triage_decisions", "decided_at"},
		{"triage_failures", "failed_at"},
	} {
		plan := triagePrunePlan(t, s, table.name, table.at)
		if !strings.Contains(plan, "LIST SUBQUERY") {
			t.Fatalf("%s prune does not materialize the kept keys as a list:\n%s", table.name, plan)
		}
		if strings.Contains(plan, "CORRELATED SCALAR SUBQUERY") {
			t.Fatalf("%s prune still re-evaluates the kept keys per row:\n%s", table.name, plan)
		}
	}

	// Five keys: the two oldest carry 2,750 of the 5,000 rows, so the prune
	// needs more than one batch.
	ids := seed(t, s, 2000)
	counts := []int{2000, 750, 750, 750, 750}
	keys := make([]TriageKey, len(counts))
	for i, n := range counts {
		keys[i] = TriageKey{Fingerprint: fmt.Sprintf("fp-%d", i), Decider: "layer1"}
		decisions := make([]Decision, 0, n)
		for j := range n {
			decisions = append(decisions, Decision{MessageID: ids[j], Revision: 1, StateHash: "h", Answers: `{}`})
		}
		if err := s.SaveDecisions(ctx, keys[i], decisions); err != nil {
			t.Fatalf("seed key %d: %v", i, err)
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE triage_decisions SET decided_at = ? WHERE fingerprint = ? AND decider = ?",
			fmt.Sprintf("2026-11-%02dT00:00:00Z", i+1), keys[i].Fingerprint, keys[i].Decider); err != nil {
			t.Fatal(err)
		}
	}

	// One batch removes at most pruneBatch rows: the write lock is released
	// between batches instead of being held for the whole prune.
	res, err := s.db.ExecContext(ctx, triagePruneSQL("triage_decisions", "decided_at"), 3, pruneBatch)
	if err != nil {
		t.Fatal(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if n != pruneBatch {
		t.Fatalf("one batch deleted %d rows, want the %d-row cap", n, pruneBatch)
	}

	passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep, err := s.Maintain(passCtx, 50)
	if err != nil {
		t.Fatalf("the batched prune did not finish: %v", err)
	}
	if want := int64(2750 - pruneBatch); rep.TriageKeysDeleted != want {
		t.Fatalf("maintenance deleted %d rows after the manual batch, want %d", rep.TriageKeysDeleted, want)
	}

	rows, err := s.db.QueryContext(ctx, "SELECT fingerprint, COUNT(*) FROM triage_decisions GROUP BY fingerprint ORDER BY fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kept := map[string]int{}
	for rows.Next() {
		var fingerprint string
		var count int
		if err := rows.Scan(&fingerprint, &count); err != nil {
			t.Fatal(err)
		}
		kept[fingerprint] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"fp-2": 750, "fp-3": 750, "fp-4": 750}
	if len(kept) != len(want) {
		t.Fatalf("kept %d keys %v, want the newest three %v", len(kept), kept, want)
	}
	for fingerprint, count := range want {
		if kept[fingerprint] != count {
			t.Fatalf("key %s kept %d rows, want %d (all kept keys: %v)", fingerprint, kept[fingerprint], count, kept)
		}
	}

	rep, err = s.Maintain(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TriageKeysDeleted != 0 {
		t.Fatalf("a second pass deleted %d rows, want 0", rep.TriageKeysDeleted)
	}
}

// Equal timestamps are resolved by the deterministic key tie-break, so a
// repeated pass keeps the same keys and deletes nothing.
func TestPruneTriageKeysKeepsTheNewestRows(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	keys := []TriageKey{
		{Fingerprint: "fp-a", Decider: "d1"},
		{Fingerprint: "fp-a", Decider: "d2"},
		{Fingerprint: "fp-b", Decider: "d1"},
	}
	for _, key := range keys {
		if err := s.SaveDecision(ctx, key, Decision{MessageID: ids[0], Revision: 1, StateHash: "h", Answers: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE triage_decisions SET decided_at = '2026-11-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}

	remaining := func() map[string]bool {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, "SELECT fingerprint, decider FROM triage_decisions")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var fingerprint, decider string
			if err := rows.Scan(&fingerprint, &decider); err != nil {
				t.Fatal(err)
			}
			out[fingerprint+"|"+decider] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// fingerprint DESC, then decider DESC: fp-b|d1 and fp-a|d2 are newest.
	if n, err := s.pruneTriageKeys(ctx, 2); err != nil || n != 1 {
		t.Fatalf("first pass deleted %d rows (%v), want 1", n, err)
	}
	kept := remaining()
	if len(kept) != 2 || !kept["fp-b|d1"] || !kept["fp-a|d2"] || kept["fp-a|d1"] {
		t.Fatalf("the tie-break did not keep the deterministic newest keys: %v", kept)
	}
	if n, err := s.pruneTriageKeys(ctx, 2); err != nil || n != 0 {
		t.Fatalf("a repeated pass deleted %d rows (%v), want 0", n, err)
	}
	if again := remaining(); len(again) != 2 || !again["fp-b|d1"] || !again["fp-a|d2"] {
		t.Fatalf("a repeated pass changed the kept keys: %v", again)
	}
}
