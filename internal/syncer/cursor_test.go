package syncer

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func openRawStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file://"+(&url.URL{Path: path}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func storeFolder(t *testing.T, st *store.Store, f mail.Folder) (int64, string) {
	t.Helper()
	id, err := st.UpsertFolder(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	cursor, _, err := st.Cursor(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, cursor
}

// A removal failure after the folder syncs must leave the previous cursor in
// place: the next run re-reads the folder and retries the removal.
func TestRemovalFailureDoesNotCommitCursor(t *testing.T) {
	s, st, _, cfg := setup(t, nil)
	ctx := t.Context()
	// Anonymous messages take the immediate removal path; the one-run move
	// hold for messages with a Message-ID is covered by movehold_test.go.
	anonymous := func(id string) item {
		it := inbox(id)
		it.msg.InternetMessageID = ""
		return it
	}
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, anonymous, "i")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(ctx)
	_, cursor := storeFolder(t, st, folders[0])
	if cursor != "v0" {
		t.Fatalf("first run cursor %q", cursor)
	}

	raw := openRawStore(t, store.Path(cfg))
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER fail_removal BEFORE DELETE ON messages
		BEGIN SELECT RAISE(ABORT, 'removal blocked'); END`); err != nil {
		t.Fatal(err)
	}

	c.version = 1
	c.items["Inbox"] = items(1, inbox, "i") // i1 vanished from the complete snapshot
	rep, err := s.Run(ctx, c, nil)
	if err == nil || !strings.Contains(err.Error(), "removal blocked") || rep.Removed.Deleted != 0 {
		t.Fatalf("the removal must fail the run: %v %+v", err, rep)
	}
	if _, cursor := storeFolder(t, st, folders[0]); cursor != "v0" {
		t.Fatalf("a failed removal must not advance the cursor: %q", cursor)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "i1"); err != nil {
		t.Fatalf("the vanished message must survive a failed removal: %v", err)
	}

	if _, err := raw.ExecContext(ctx, `DROP TRIGGER fail_removal`); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Run(ctx, c, nil)
	if err != nil || rep.Removed.Deleted != 1 {
		t.Fatalf("the next run retries the removal: %v %+v", err, rep)
	}
	if _, cursor := storeFolder(t, st, folders[0]); cursor != "v1" {
		t.Fatalf("the successful run must commit the cursor: %q", cursor)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "i1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the vanished message must be removed: %v", err)
	}
}

// Two folders of one store advance in one run: both staged cursors commit.
func TestSuccessfulRunCommitsAllCursors(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(1, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(ctx)
	inboxID, inboxCursor := storeFolder(t, st, folders[0])
	sentID, sentCursor := storeFolder(t, st, folders[1])
	if inboxCursor != "v0" || sentCursor != "v0" {
		t.Fatalf("first cursors %q / %q", inboxCursor, sentCursor)
	}

	c.version = 1
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name string
		id   int64
		want string
	}{{"Inbox", inboxID, "v1"}, {"Sent", sentID, "v1"}} {
		if cursor, _, err := st.Cursor(ctx, check.id); err != nil || cursor != check.want {
			t.Errorf("%s cursor %q (%v), want %q", check.name, cursor, err, check.want)
		}
	}
}

// The staged cursors of one run commit together: a failure writing one of
// them rolls back every cursor.
func TestCursorCommitIsAtomic(t *testing.T) {
	s, st, _, cfg := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(1, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(ctx)
	inboxID, _ := storeFolder(t, st, folders[0])
	sentID, _ := storeFolder(t, st, folders[1])

	raw := openRawStore(t, store.Path(cfg))
	if _, err := raw.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER fail_cursor BEFORE INSERT ON sync_cursors
		WHEN NEW.folder_id = %d BEGIN SELECT RAISE(ABORT, 'cursor blocked'); END`, sentID)); err != nil {
		t.Fatal(err)
	}
	c.version = 1
	rep, err := s.Run(ctx, c, nil)
	if err == nil || !strings.Contains(err.Error(), "cursor blocked") || rep.Status == "succeeded" {
		t.Fatalf("the cursor write must fail the run: %v %+v", err, rep)
	}
	for _, check := range []struct {
		name string
		id   int64
	}{{"Inbox", inboxID}, {"Sent", sentID}} {
		if cursor, _, err := st.Cursor(ctx, check.id); err != nil || cursor != "v0" {
			t.Errorf("%s cursor %q (%v), want v0 after the rollback", check.name, cursor, err)
		}
	}
}

// A message that moved into a folder that failed must not be classified as
// deleted: the whole store view is partial, so no removal and no cursor
// advance happens until a complete run sees the new row.
func TestMoveIntoAFailedFolderKeepsTheRow(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "S", StoreKind: mail.StorePrimary, Path: "A", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "S", StoreKind: mail.StorePrimary, Path: "B", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "S", StoreKind: mail.StorePrimary, Path: "C", Role: mail.RoleInbox, ItemCount: -1},
	}
	old := mailtest.Message("fake", "old")
	old.FolderPath, old.FolderRole, old.InternetMessageID = "A", mail.RoleInbox, "<move@example.test>"
	fresh := mailtest.Message("fake", "fresh")
	fresh.FolderPath, fresh.FolderRole, fresh.InternetMessageID = "B", mail.RoleInbox, old.InternetMessageID
	other := mailtest.Message("fake", "other")
	other.FolderPath, other.FolderRole = "C", mail.RoleInbox
	c := &fakeConn{id: "fake", folders: folders, items: map[string][]item{
		"A": {{msg: old}},
		"C": {{msg: other}},
	}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, f := range folders {
		id, cursor := storeFolder(t, st, f)
		ids[f.Path] = id
		if cursor != "v0" {
			t.Fatalf("%s first cursor %q", f.Path, cursor)
		}
	}
	var oldRow int64
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == "old" {
			oldRow = r.RowID
		}
		return nil
	}); err != nil || oldRow == 0 {
		t.Fatalf("old row %d: %v", oldRow, err)
	}
	if err := st.PutLabel(ctx, oldRow, "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}

	c.version = 1
	c.failOn = "B"
	c.items = map[string][]item{"A": nil, "C": {{msg: other}}}
	if rep, err := s.Run(ctx, c, nil); err == nil || !strings.Contains(err.Error(), "folder B") {
		t.Fatalf("folder B must fail: %v %+v", err, rep)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "old"); err != nil {
		t.Fatalf("the moved row must survive the failed folder: %v", err)
	}
	labels, err := st.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != oldRow {
		t.Fatalf("the label must stay on the surviving row: %+v %v", labels, err)
	}
	for _, f := range folders {
		if _, cursor := storeFolder(t, st, f); cursor != "v0" {
			t.Fatalf("%s cursor advanced on a partial store view: %q", f.Path, cursor)
		}
	}

	c.version = 2
	c.failOn = ""
	c.items = map[string][]item{"A": nil, "B": {{msg: fresh}}, "C": {{msg: other}}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the next complete run reconciles the move: %v", err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "fresh"); err != nil {
		t.Fatalf("the moved message: %v", err)
	}
	labels, err = st.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID == oldRow {
		t.Fatalf("the label must follow the move: %+v %v", labels, err)
	}
	for _, f := range folders {
		if _, cursor := storeFolder(t, st, f); cursor != "v2" {
			t.Fatalf("%s cursor after the complete run: %q", f.Path, cursor)
		}
	}
}

// An incomplete snapshot writes its new messages but cannot advance the
// cursor or remove what it did not prove absent.
func TestIncompleteSnapshotDoesNotAdvanceCursor(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(ctx)
	if _, cursor := storeFolder(t, st, folders[0]); cursor != "v0" {
		t.Fatalf("first cursor %q", cursor)
	}

	c.version = 1
	c.items["Inbox"] = []item{inbox("i2")}
	c.incomplete = map[string]bool{"Inbox": true}
	rep, err := s.Run(ctx, c, nil)
	if err != nil || rep.Totals.Written != 1 || rep.Removed != (store.Removal{}) {
		t.Fatalf("the incomplete snapshot writes its item but removes nothing: %v %+v", err, rep)
	}
	if _, cursor := storeFolder(t, st, folders[0]); cursor != "v0" {
		t.Fatalf("an incomplete snapshot must not advance the cursor: %q", cursor)
	}
	for _, id := range []string{"i0", "i1", "i2"} {
		if _, err := st.GetMessage(ctx, "fake", "store-1", id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// A partial store view only gates its own removals and cursors: the complete
// store of the same source still removes and advances.
func TestOtherStoreStillRemovesWhenOneStoreIsIncomplete(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{source.SettingSelected: []any{fakeKey, source.StoreKey("fake", "store-2")}})
	ctx := t.Context()
	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "One", StoreKind: mail.StorePrimary, Path: "Inbox", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-2", StoreName: "Two", StoreKind: mail.StorePrimary, Path: "Other", Role: mail.RoleInbox, ItemCount: -1},
	}
	mk := func(id, storeID, path string) item {
		m := mailtest.Message("fake", id)
		m.StoreID, m.StoreName, m.FolderPath, m.FolderRole = storeID, storeID, path, mail.RoleInbox
		// Anonymous, so the complete store's removal is applied on the first
		// absent snapshot; this test is about the per-store completeness gate.
		m.InternetMessageID = ""
		return item{msg: m}
	}
	c := &fakeConn{id: "fake", folders: folders, items: map[string][]item{
		"Inbox": {mk("s1gone", "store-1", "Inbox"), mk("s1keep", "store-1", "Inbox")},
		"Other": {mk("s2gone", "store-2", "Other"), mk("s2keep", "store-2", "Other")},
	}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	_, _ = storeFolder(t, st, folders[0])
	if _, cursor := storeFolder(t, st, folders[1]); cursor != "v0" {
		t.Fatalf("second store first cursor %q", cursor)
	}

	c.version = 1
	c.items = map[string][]item{
		"Inbox": {mk("s1keep", "store-1", "Inbox")},
		"Other": {mk("s2keep", "store-2", "Other")},
	}
	c.incomplete = map[string]bool{"Other": true}
	rep, err := s.Run(ctx, c, nil)
	if err != nil || rep.Removed.Deleted != 1 {
		t.Fatalf("the complete store still removes: %v %+v", err, rep)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "s1gone"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the complete store's vanished message: %v", err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-2", "s2gone"); err != nil {
		t.Errorf("the incomplete store's vanished message must stay: %v", err)
	}
	if _, cursor := storeFolder(t, st, folders[0]); cursor != "v1" {
		t.Errorf("complete store cursor %q, want v1", cursor)
	}
	if _, cursor := storeFolder(t, st, folders[1]); cursor != "v0" {
		t.Errorf("incomplete store cursor %q, want v0", cursor)
	}
}

// A folder discovery no longer returns must carry its store, so the
// per-store completeness gate also covers vanished folders.
func TestVanishedFolderCarriesItsStore(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	ctx := t.Context()
	c := &probeConn{snapConn: newSnapConn()}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	all, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storeID := ""
	for _, f := range all {
		if f.Path == "Sent" {
			storeID = f.StoreID
		}
	}
	if storeID == "" {
		t.Fatal("the fixture has no Sent store")
	}
	c.hideSent = true
	folders, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.vanishedFolders(ctx, c, folders)
	if err != nil || len(gone) != 1 {
		t.Fatalf("gone %v, err %v", gone, err)
	}
	if gone[0].storeID != storeID {
		t.Fatalf("a vanished folder carries store %q, want %q", gone[0].storeID, storeID)
	}
}

// A store with a failed folder keeps the rows of a folder discovery no
// longer returned: a message may have moved into the failed folder. A store
// with no discovered folder at all is not a partial view and is removed.
func TestVanishedFolderInAnIncompleteStoreKeepsItsRows(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "S", StoreKind: mail.StorePrimary, Path: "A", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "S", StoreKind: mail.StorePrimary, Path: "B", Role: mail.RoleInbox, ItemCount: -1},
	}
	msg := mailtest.Message("fake", "a1")
	msg.FolderPath, msg.FolderRole = "A", mail.RoleInbox
	c := &fakeConn{id: "fake", folders: folders, items: map[string][]item{"A": {{msg: msg}}}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folderID, _ := storeFolder(t, st, folders[0])
	gone := removal{index: -1, storeID: "store-1", folderID: folderID, path: "A"}

	if err := s.applyRemovals(ctx, c, folders, []removal{gone},
		map[string]bool{"store-1": false}, false, &Report{RunID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "a1"); err != nil {
		t.Fatalf("an incomplete store must keep a vanished folder's rows: %v", err)
	}

	// The whole store is gone: no discovered folder means no completeness
	// entry, and the removal proceeds. The first run with a Message-ID and no
	// survivor holds it once; the next run removes it for real.
	if err := s.applyRemovals(ctx, c, nil, []removal{gone},
		map[string]bool{}, false, &Report{RunID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "a1"); err != nil {
		t.Fatalf("an unexplained absence is held for one run: %v", err)
	}
	if err := s.applyRemovals(ctx, c, nil, []removal{gone},
		map[string]bool{}, false, &Report{RunID: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "a1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an entirely gone store must be removed on the next run: %v", err)
	}
}
