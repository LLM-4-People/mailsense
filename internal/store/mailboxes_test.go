package store

import (
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// SyncProgress uses the stale rule of EachStaleScope: a verdict is current
// only when it exists at the message revision and under the store's owner
// identity. An owner change owes every message again, and a store with no
// identity owes all of them, even though the rows still carry a verdict.
func TestSyncProgressCountsIdentityStaleVerdicts(t *testing.T) {
	key := source.StoreKey("src", "store-1")
	s, cfg := open(t, map[string]any{source.SettingDeclared: []any{key + "=a@example.org"}})
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}

	var batch []mail.Message
	for i := range 3 {
		batch = append(batch, mailtest.Message("src", fmt.Sprint(i)))
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}

	stale := func(digest string) []ScopeRow {
		t.Helper()
		var rows []ScopeRow
		if err := s.EachStaleScope(ctx, ref, digest, 2, func(page []ScopeRow) error {
			rows = append(rows, page...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	write := func(digest string, rows []ScopeRow) {
		t.Helper()
		verdicts := make([]VerdictRow, 0, len(rows))
		for _, r := range rows {
			verdicts = append(verdicts, VerdictRow{RowID: r.RowID, Revision: r.Revision, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag})
		}
		if err := s.WriteVerdicts(ctx, digest, verdicts); err != nil {
			t.Fatal(err)
		}
	}
	progress := func() SyncCounts {
		t.Helper()
		got, err := s.SyncProgress(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got[[2]string{"src", "store-1"}]
	}

	owners, err := s.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digestA := owners[key].Identity.Digest()
	rowsA := stale(digestA)
	if len(rowsA) != 3 {
		t.Fatalf("stale under A: %+v", rowsA)
	}
	write(digestA, rowsA)
	if c := progress(); c.Messages != 3 || c.Unjudged != 0 || c.OwnerMail != 3 {
		t.Fatalf("judged under A: %+v", c)
	}

	// The declared owner changes: the stored verdicts were judged against
	// another identity, so sync progress must not report them as judged.
	if err := cfg.Set(source.SettingDeclared, []any{key + "=b@example.org"}); err != nil {
		t.Fatal(err)
	}
	if c := progress(); c.Unjudged != 3 || c.Unjudged != c.Messages {
		t.Fatalf("identity changed: %+v", c)
	}

	owners, err = s.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digestB := owners[key].Identity.Digest()
	if digestB == digestA {
		t.Fatal("the declared-owner change must change the identity digest")
	}
	rowsB := stale(digestB)
	if len(rowsB) != 3 {
		t.Fatalf("stale under B: %+v", rowsB)
	}
	// Two identity groups at once: one re-judged row under B, two rows still
	// under A. Only the missing/revision rows of the current group are not
	// stale; the whole other group is.
	write(digestB, rowsB[:1])
	if c := progress(); c.Unjudged != 2 {
		t.Fatalf("mixed identity groups: %+v", c)
	}
	write(digestB, rowsB[1:])
	if c := progress(); c.Unjudged != 0 || c.OwnerMail != 3 {
		t.Fatalf("judged under B: %+v", c)
	}

	// No identity at all: scope owes every verdict, whatever is stored.
	if err := cfg.Set(source.SettingDeclared, []any{}); err != nil {
		t.Fatal(err)
	}
	if c := progress(); c.Unjudged != 3 {
		t.Fatalf("no owner identity: %+v", c)
	}
}
