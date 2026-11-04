package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func seeded(t *testing.T, n int, overrides map[string]any) (*Store, *ReadOnly) {
	t.Helper()
	s, cfg := open(t, overrides)
	var batch []mail.Message
	for i := range n {
		batch = append(batch, mailtest.Message("src", fmt.Sprint(i)))
	}
	if err := s.UpsertMessages(t.Context(), 0, batch); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	return s, ro
}

func TestReadOnlyQueryRowsCapAndTruncation(t *testing.T) {
	_, ro := seeded(t, 5, map[string]any{"query.max_rows": 3})
	ctx := t.Context()
	res, err := ro.Query(ctx, "SELECT msg_id, size, subject FROM messages ORDER BY msg_id", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || !res.Truncated || res.Limit != 3 || res.Columns[2] != "subject" {
		t.Fatalf("result %+v", res)
	}
	if _, ok := res.Rows[0][2].(string); !ok {
		t.Fatalf("text must come back as string, got %T", res.Rows[0][2])
	}
	res, _ = ro.Query(ctx, "SELECT COUNT(*) AS n FROM messages", 1)
	if res.Truncated || res.Rows[0][0] != int64(5) {
		t.Fatalf("count %+v", res)
	}
	res, _ = ro.Query(ctx, "WITH x AS (SELECT msg_id FROM messages) SELECT * FROM x -- trailing", 2)
	if len(res.Rows) != 2 || !res.Truncated {
		t.Fatalf("with: %+v", res)
	}
}

func TestReadOnlyRejectsAndCannotWrite(t *testing.T) {
	s, ro := seeded(t, 1, nil)
	ctx := t.Context()
	for _, sql := range []string{"DELETE FROM messages", "SELECT 1; DELETE FROM messages", "WITH u AS (UPDATE messages SET subject='x' RETURNING id) SELECT 1 FROM u"} {
		if _, err := ro.Query(ctx, sql, 0); !errors.Is(err, ErrUnsafeSQL) {
			t.Errorf("%q: %v", sql, err)
		}
	}
	// Defence in depth: even SQL that bypassed the validator cannot write.
	for _, sql := range []string{"DELETE FROM messages", "CREATE TABLE x (a)", "PRAGMA user_version = 7"} {
		if _, err := ro.db.ExecContext(ctx, sql); err == nil {
			t.Errorf("read-only pool executed %q", sql)
		}
	}
	if _, err := ro.Query(ctx, "SELECT nope FROM messages", 0); err == nil {
		t.Error("SQL errors must surface")
	}
	if counts, _ := s.CountByRole(ctx); counts[mail.RoleInbox] != 1 {
		t.Fatal("data changed")
	}
}

func TestReadOnlyTimeout(t *testing.T) {
	_, ro := seeded(t, 1, map[string]any{"query.timeout": "50ms"})
	_, err := ro.Query(t.Context(), "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c", 0)
	if err == nil || !strings.Contains(err.Error(), "query.timeout") {
		t.Fatalf("want a timeout, got %v", err)
	}
}

func TestReadOnlyNeedsExistingDatabase(t *testing.T) {
	_, cfg := open(t, nil)
	cfg2 := cfg
	if err := cfg2.Set("store.file", "missing.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(t.Context(), cfg2, nil); err == nil {
		t.Fatal("read-only open must not create a database")
	}
}

// scopeRig stores three inbox messages and one Sent message with
// delivery and list headers, and returns the store's ref and a reader of
// its stale evidence.
func scopeRig(t *testing.T) (*Store, StoreRef, func(identity string) []ScopeRow) {
	t.Helper()
	s, _ := seeded(t, 3, nil)
	ctx := t.Context()
	sent := mailtest.Message("src", "s1")
	sent.FolderPath, sent.FolderRole, sent.AddressedToMe, sent.Recipients = "Sent", mail.RoleSent, "", nil
	sent.Headers = "Delivered-To: amelie@example.org\r\nList-Id: <l.example.org>"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{sent}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ScopeStores(ctx)
	if err != nil || len(refs) != 1 || refs[0].SourceID != "src" || refs[0].StoreID != "store-1" || refs[0].Name != "Amélie" {
		t.Fatalf("stores %+v %v", refs, err)
	}
	return s, refs[0], func(identity string) []ScopeRow {
		var rows []ScopeRow
		if err := s.EachStaleScope(ctx, refs[0], identity, 3, func(page []ScopeRow) error {
			rows = append(rows, page...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
}

// judgeAll writes a verdict for every stale row of the rig (the first
// shared, the rest owner).
func judgeAll(t *testing.T, s *Store, rows []ScopeRow, identity string) {
	t.Helper()
	var verdicts []VerdictRow
	for i, r := range rows {
		v := scope.Owner
		if i == 0 {
			v = scope.Shared
		}
		verdicts = append(verdicts, VerdictRow{RowID: r.RowID, Revision: r.Revision, Verdict: v, Reason: scope.ReasonSentByOwner})
	}
	for range 2 { // writes are repeatable
		if err := s.WriteVerdicts(t.Context(), identity, verdicts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScopePersistence(t *testing.T) {
	s, _, stale := scopeRig(t)
	ctx := t.Context()
	got := stale("id1")
	if len(got) != 4 || got[0].Evidence.Role != mail.RoleInbox || len(got[0].Evidence.Recipients) != 2 ||
		got[3].Evidence.Role != mail.RoleSent || !got[3].Evidence.ListID || got[3].Evidence.DeliveredTo[0] != "amelie@example.org" {
		t.Fatalf("evidence rows: %+v", got)
	}
	judgeAll(t, s, got, "id1")
	counts, _ := s.VerdictCounts(ctx)
	if counts[scope.Owner] != 3 || counts[scope.Shared] != 1 || counts[""] != 0 {
		t.Fatalf("counts %v", counts)
	}
	if err := s.WriteVerdicts(ctx, "id", []VerdictRow{{RowID: 999999, Verdict: scope.Owner, Reason: scope.ReasonSentByOwner}}); err == nil {
		t.Fatal("a verdict for a missing message must fail (FK) and roll back")
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if size, err := s.SizeBytes(ctx); err != nil || size <= 0 {
		t.Fatalf("size %d %v", size, err)
	}
}

// Incremental scope: nothing is stale for the same identity; a changed
// message is; another identity makes every message stale; clearing
// removes every verdict of the store.
func TestScopeIsIncremental(t *testing.T) {
	s, ref, stale := scopeRig(t)
	ctx := t.Context()
	judgeAll(t, s, stale("id1"), "id1")
	if got := stale("id1"); len(got) != 0 {
		t.Fatalf("judged messages are not stale: %+v", got)
	}
	again := mailtest.Message("src", "1")
	again.Subject = "changed"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{again}); err != nil {
		t.Fatal(err)
	}
	if got := stale("id1"); len(got) != 1 {
		t.Fatalf("only the changed message is stale: %+v", got)
	}
	if got := stale("id2"); len(got) != 4 {
		t.Fatalf("an identity change re-judges the store: %d", len(got))
	}
	if n, err := s.ClearVerdicts(ctx, ref); err != nil || n != 4 {
		t.Fatalf("clear %d %v", n, err)
	}
	if counts, _ := s.VerdictCounts(ctx); counts[""] != 4 {
		t.Fatalf("cleared: %v", counts)
	}
}

// The page contract is bounded: an empty or negative page size is refused
// instead of scanning the whole store without a limit.
func TestEachStaleScopeRequiresABoundedPage(t *testing.T) {
	s, ref, _ := scopeRig(t)
	for _, size := range []int{0, -1} {
		if err := s.EachStaleScope(t.Context(), ref, "id", size, func([]ScopeRow) error { return nil }); err == nil {
			t.Errorf("page size %d must be refused", size)
		}
	}
}
