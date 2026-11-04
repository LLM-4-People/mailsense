package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func partial(id, received, stamp string) mail.Message {
	m := mailtest.Message("src", id)
	m.Partial, m.Body, m.Headers, m.References, m.Recipients = true, "preview", "", "", nil
	m.ReceivedAt, m.Stamp = received, stamp
	return m
}

func TestPartialWritesKeepStoredDetails(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	full := mailtest.Message("src", "1")
	full.Stamp = "s1"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{full}); err != nil {
		t.Fatal(err)
	}
	// Same stamp: details kept, still complete, FTS still finds the body.
	if err := s.UpsertMessages(ctx, 0, []mail.Message{partial("1", full.ReceivedAt, full.Stamp)}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetMessage(ctx, "src", "store-1", "1")
	if m.Partial || m.Body != full.Body || len(m.Recipients) != 2 {
		t.Fatalf("kept %+v", m)
	}
	if res, _ := s.Search(ctx, "peaufiner", 5); res.Total != 1 {
		t.Fatal("the body must stay searchable")
	}
	// Moved stamp: details kept, but pending again.
	if err := s.UpsertMessages(ctx, 0, []mail.Message{partial("1", full.ReceivedAt, "s2")}); err != nil {
		t.Fatal(err)
	}
	if m, _ = s.GetMessage(ctx, "src", "store-1", "1"); !m.Partial || m.Body != full.Body {
		t.Fatalf("refresh needed %+v", m)
	}
	if n, _ := s.CountPending(ctx, "src", time.Time{}); n != 1 {
		t.Fatal(n)
	}
}

func TestPendingDetailsNewestFirstAndApply(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	msgs := []mail.Message{partial("old", "2026-01-01T00:00:00Z", "x"), partial("new", "2026-10-01T00:00:00Z", "x"),
		partial("mid", "2026-05-01T00:00:00Z", "x")}
	for i := range msgs {
		msgs[i].LastModificationTime = "2026-10-01T00:00:00Z"
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	keys, err := s.PendingDetails(ctx, "src", 2, time.Time{})
	if err != nil || len(keys) != 2 || keys[0].ID != "new" || keys[1].ID != "mid" {
		t.Fatalf("%v %+v", err, keys)
	}
	// sync.details.settle_after: a message received after the cutoff waits.
	settled := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if early, err := s.PendingDetails(ctx, "src", 5, settled); err != nil || len(early) != 2 || early[0].ID != "mid" {
		t.Fatalf("settled: %v %+v", err, early)
	}
	if n, _ := s.CountPending(ctx, "src", settled); n != 2 {
		t.Fatalf("settled count %d", n)
	}
	d := mail.Details{Body: "le corps complet", Recipients: []mail.Recipient{{Kind: mail.Bcc, Address: "x@y.z"}}}
	n, err := s.ApplyDetails(ctx, 0, "src", []DetailUpdate{{Key: keys[0], Details: d}, {Key: source.ItemRef{StoreID: "store-1", ID: "gone"}}})
	if err != nil || n != 1 {
		t.Fatalf("apply %d %v", n, err)
	}
	m, _ := s.GetMessage(ctx, "src", "store-1", "new")
	if m.Partial || m.Body != "le corps complet" || len(m.Recipients) != 1 {
		t.Fatalf("%+v", m)
	}
	if res, _ := s.Search(ctx, "complet", 5); res.Total != 1 {
		t.Fatal("FTS must index the completed body")
	}
	bad := mail.Details{Recipients: []mail.Recipient{{Kind: "fwd"}}}
	if _, err := s.ApplyDetails(ctx, 0, "src", []DetailUpdate{{Key: keys[1], Details: bad}}); err == nil {
		t.Fatal("invalid details must fail the batch")
	}
	if n, _ := s.CountPending(ctx, "src", time.Time{}); n != 2 {
		t.Fatalf("pending %d", n)
	}
}

func TestRemovalsClassifyMovesAndDeletes(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	a, b, moved := mailtest.Message("src", "a"), mailtest.Message("src", "b"), mailtest.Message("src", "m-new")
	moved.FolderPath, moved.InternetMessageID = "Archive", b.InternetMessageID
	moved.FolderRole = mail.RoleOther
	a.Stamp = "sa"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{a, b, moved}); err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.UpsertFolder(ctx, mail.FolderOf(&a))
	versions, _ := s.FolderVersions(ctx, inbox)
	if len(versions) != 2 || versions["a"] != "sa" {
		t.Fatalf("versions %v", versions)
	}
	// a vanished (deleted); b vanished but its Message-ID lives on in Archive (moved).
	ids, stored, err := s.AbsentMessages(ctx, inbox, map[string]bool{})
	if err != nil || len(ids) != 2 || stored != 2 {
		t.Fatalf("absent %v of %d: %v", ids, stored, err)
	}
	rm, err := s.RemoveMessages(ctx, ids)
	if err != nil || rm.Deleted != 1 || rm.Moved != 1 {
		t.Fatalf("%+v %v", rm, err)
	}
	if res, _ := s.Search(ctx, "peaufiner", 5); res.Total != 1 {
		t.Fatalf("FTS entries of removed messages must go: %d", res.Total)
	}
	if ids, _, _ := s.AbsentMessages(ctx, inbox, nil); len(ids) != 0 {
		t.Fatal("repeatable")
	}
}

func TestHeldRemovals(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	msgs := []mail.Message{mailtest.Message("src", "1"), mailtest.Message("src", "2"), mailtest.Message("src", "3")}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.UpsertFolder(ctx, mail.FolderOf(&msgs[0]))
	ids, _, _ := s.AbsentMessages(ctx, inbox, map[string]bool{"3": true})
	ripe, err := s.HoldRemovals(ctx, inbox, ids, "")
	if err != nil || len(ripe) != 0 {
		t.Fatalf("no due time: nothing ripe: %v %v", ripe, err)
	}
	// The caller owns the auto-apply rule; the projection carries its
	// deadline verbatim so the list can render it.
	held, _ := s.HeldRemovals(ctx, "src", func(string) string { return "2026-11-06T00:00:00Z" })
	if len(held) != 1 || held[0].Held != 2 || held[0].Stored != 3 || held[0].Since == "" ||
		held[0].AppliesAt != "2026-11-06T00:00:00Z" {
		t.Fatalf("held %+v", held)
	}
	// Held again later: the first held time stays, so the removal ripens.
	ripe, _ = s.HoldRemovals(ctx, inbox, ids, "9999-01-01T00:00:00Z")
	if len(ripe) != 2 {
		t.Fatalf("ripe %v", ripe)
	}
	// One message came back: only the other stays held.
	if _, err := s.HoldRemovals(ctx, inbox, ids[:1], ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HeldIDs(ctx, inbox); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("held ids %v", got)
	}
	if err := s.ReleaseHeld(ctx, inbox); err != nil {
		t.Fatal(err)
	}
	if held, _ := s.HeldRemovals(ctx, "src", nil); len(held) != 0 {
		t.Fatal("released")
	}
}

func TestInactiveFoldersStayStoredOutOfScope(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "1")
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	f := mail.FolderOf(&m)
	id, _ := s.UpsertFolder(ctx, f)
	if err := s.SetFolderActive(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if refs, _ := s.ScopeStores(ctx); len(refs) != 0 {
		t.Fatal("inactive folders are out of scope")
	}
	if c, _ := s.VerdictCounts(ctx); len(c) != 0 {
		t.Fatalf("and out of the statistics: %v", c)
	}
	if _, err := s.GetMessage(ctx, "src", "store-1", "1"); err != nil {
		t.Fatal("but their mail is kept")
	}
	if err := s.DeleteFolder(ctx, id); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListFolders(ctx, "src"); len(list) != 1 || list[0].Active {
		t.Fatalf("a folder with messages is never deleted: %+v", list)
	}
	if _, err := s.UpsertFolder(ctx, f); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListFolders(ctx, "src"); !list[0].Active {
		t.Fatal("rediscovery reactivates it")
	}
}

func TestFolderStoreKindIsValidated(t *testing.T) {
	s, _ := open(t, nil)
	f := mail.Folder{SourceID: "src", StoreID: "x", Path: "Inbox", Role: mail.RoleInbox, StoreKind: "someone's"}
	if _, err := s.UpsertFolder(t.Context(), f); err == nil {
		t.Fatal("unknown store kind must be rejected")
	}
	f.StoreKind = mail.StoreDelegate
	if _, err := s.UpsertFolder(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListFolders(t.Context(), "src")
	if len(list) != 1 || list[0].Folder.StoreKind != mail.StoreDelegate {
		t.Fatalf("%+v", list)
	}
}

// countingQueryer counts the reads a batch makes, so an N+1 cannot come back
// unnoticed.
type countingQueryer struct {
	queryer
	queries int
}

func (c *countingQueryer) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.queryer.QueryContext(ctx, q, args...)
}

// A detail batch read is bounded: the old loop loaded every message with
// three queries (row, recipients, items), so 500 updates were 1500 reads.
func TestDetailBatchReadsAreBounded(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	var msgs []mail.Message
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m := partial(id, "2026-01-01T00:00:00Z", "x")
		m.Folders = []mail.FolderRef{{Path: "Archive", Role: mail.RoleOther}}
		msgs = append(msgs, m)
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	keys := make([]source.ItemRef, len(msgs))
	for i, m := range msgs {
		keys[i] = source.ItemRef{StoreID: m.StoreID, ID: m.ID}
	}
	counter := &countingQueryer{queryer: s.db}
	bases, err := loadDetailBases(ctx, counter, "src", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != len(keys) {
		t.Fatalf("read %d of %d bases", len(bases), len(keys))
	}
	if counter.queries != 1 {
		t.Fatalf("the batch made %d reads for %d messages, want 1", counter.queries, len(keys))
	}
	// The detail-owned fields are left to Complete; the further folders are
	// not part of the detail contract and must survive.
	base := bases[detailKey("store-1", "a")]
	if base.m.Body != "" || base.m.Headers != "" || base.m.Subject != "" {
		t.Fatalf("the batch read a detail-owned field: %+v", base.m)
	}
	if len(base.m.Folders) != 1 || base.m.Folders[0].Path != "Archive" {
		t.Fatalf("the batch lost the further folders: %+v", base.m.Folders)
	}
	if base.m.Stamp != "x" || base.m.FolderRole != mail.RoleInbox {
		t.Fatalf("the batch lost the stored identity: %+v", base.m)
	}
}

// Completing from a batch preserves what Complete does not own: the further
// folders and the identity columns, while the detail read replaces the
// attachments.
func TestApplyDetailsKeepsFoldersAndReplacesAttachments(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := partial("move", "2026-02-01T00:00:00Z", "x")
	m.Folders = []mail.FolderRef{{Path: "Archive", Role: mail.RoleOther}}
	m.Attachments = []mail.Attachment{{Name: "old.txt"}}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	details := mail.Details{Body: "full body", Subject: "Complete",
		Attachments: []mail.Attachment{{Name: "new.txt"}}}
	if n, err := s.ApplyDetails(ctx, 0, "src", []DetailUpdate{{Key: source.ItemRef{StoreID: m.StoreID, ID: m.ID}, Details: details}}); err != nil || n != 1 {
		t.Fatalf("apply %d %v", n, err)
	}
	got, err := s.GetMessage(ctx, "src", m.StoreID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Complete" || got.Body != "full body" || got.Partial {
		t.Fatalf("details %+v", got)
	}
	if len(got.Folders) != 1 || got.Folders[0].Path != "Archive" {
		t.Fatalf("folders %+v", got.Folders)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Name != "new.txt" {
		t.Fatalf("attachments %+v", got.Attachments)
	}
}

// A move changes the entry id, so the syncer removes the old row and inserts
// a new one. The owner's labels are ground truth: they must follow the
// message, not vanish with the old row.
func TestMoveKeepsLabels(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	old := mailtest.Message("src", "old")
	fresh := mailtest.Message("src", "fresh")
	const internetID = "<move-keep-labels@example.test>"
	old.InternetMessageID, fresh.InternetMessageID = internetID, internetID
	if err := s.UpsertMessages(ctx, 0, []mail.Message{old, fresh}); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.UpsertFolder(ctx, mail.FolderOf(&old))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing but "old" is present in the folder: "fresh" is the survivor.
	absent, _, err := s.AbsentMessages(ctx, inbox, map[string]bool{"old": true})
	if err != nil || len(absent) != 1 {
		t.Fatalf("survivor %v: %v", absent, err)
	}
	survivorID := absent[0]
	oldIDs, _, err := s.AbsentMessages(ctx, inbox, map[string]bool{"fresh": true})
	if err != nil || len(oldIDs) != 1 {
		t.Fatalf("old row %v: %v", oldIDs, err)
	}
	if err := s.PutLabel(ctx, oldIDs[0], "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	rm, err := s.RemoveMessages(ctx, oldIDs)
	if err != nil || rm.Moved != 1 {
		t.Fatalf("remove %+v: %v", rm, err)
	}
	rows, err := s.ListLabels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range rows {
		if row.Question != "category" || row.Value != `"work"` {
			continue
		}
		found++
		if row.MessageID != survivorID {
			t.Fatalf("label stayed on moved-away row %d, want %d", row.MessageID, survivorID)
		}
	}
	if found != 1 {
		t.Fatalf("category labels %d, want one", found)
	}
}

// TestBatchKeyLoadersShareOneChunkingRule: both batch loaders pay one
// statement per detailBatchKeys keys, whatever the key count. They used to
// spell the loop twice, so the bound could drift between them; the table runs
// both through the same key count and asserts ceil(n/detailBatchKeys).
func TestBatchKeyLoadersShareOneChunkingRule(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	const n = detailBatchKeys + 3 // two batches
	var msgs []mail.Message
	for i := range n {
		msgs = append(msgs, mailtest.Message("src", fmt.Sprintf("chunk-%03d", i)))
	}
	seedMsgs(t, s, msgs...)
	keys := make([]source.ItemRef, n)
	updates := make([]BackfillUpdate, n)
	for i, m := range msgs {
		keys[i] = source.ItemRef{StoreID: m.StoreID, ID: m.ID}
		updates[i] = BackfillUpdate{Key: keys[i], HTML: "<p>x</p>", Known: true}
	}
	want := (n + detailBatchKeys - 1) / detailBatchKeys

	counter := &countingQueryer{queryer: s.db}
	bases, err := loadDetailBases(ctx, counter, "src", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(bases) != n {
		t.Fatalf("read %d of %d bases", len(bases), n)
	}
	if counter.queries != want {
		t.Fatalf("detail bases made %d reads for %d keys, want %d", counter.queries, n, want)
	}

	counter = &countingQueryer{queryer: s.db}
	backfill, err := loadBackfillBases(ctx, counter, "src", updates)
	if err != nil {
		t.Fatal(err)
	}
	if len(backfill) != n {
		t.Fatalf("read %d of %d backfill bases", len(backfill), n)
	}
	if counter.queries != want {
		t.Fatalf("backfill bases made %d reads for %d keys, want %d", counter.queries, n, want)
	}
}
