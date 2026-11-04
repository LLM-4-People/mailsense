package outlook

import (
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestSyncSnapshotsThenStreamsWantedRowsNewestFirst(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{"outlook.table.page_rows": 2})
	folders, _ := c.Discover(t.Context())
	in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	sk := &memSink{}
	cur, err := c.Sync(t.Context(), in, "", sk)
	if err != nil || cur == "" {
		t.Fatalf("%v %q", err, cur)
	}
	if len(sk.versions) != 5 || !sk.complete || len(sk.msgs) != 5 {
		t.Fatalf("snapshot %d complete=%v delivered %d", len(sk.versions), sk.complete, len(sk.msgs))
	}
	for i := 1; i < len(sk.msgs); i++ {
		if sk.msgs[i-1].LastModificationTime < sk.msgs[i].LastModificationTime {
			t.Fatal("rows must stream newest change first")
		}
	}
	m := sk.msgs[0]
	if !m.Partial || m.Origin != Origin || m.StoreID != "S-PRIMARY" || m.FolderRole != mail.RoleInbox || m.ID == "" ||
		m.ID != m.LongTermEntryID || m.EntryID == "" || !m.IsRead || m.AddressedToMe != mail.AddressedTo || m.Body != "aperçu" {
		t.Fatalf("message %+v", m)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	// Pages of 2: the snapshot reads 5 rows in 3 GetArray calls.
	if n := callsTo(o, "Table.GetArray"); n < 3 {
		t.Fatalf("paging: %d GetArray calls", n)
	}

	// Unchanged: the cursor from Discover matches, no COM call at all.
	before := len(o.calls)
	if again, err := c.Sync(t.Context(), in, cur, &memSink{}); err != nil || again != cur || len(o.calls) != before {
		t.Fatalf("unchanged folder must cost nothing: %v, %d calls", err, len(o.calls)-before)
	}
}

func TestIncrementalDeliversOnlyChangedRowsAndStopsEarly(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{"outlook.table.page_rows": 1})
	folders, _ := c.Discover(t.Context())
	in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	first := &memSink{}
	if _, err := c.Sync(t.Context(), in, "", first); err != nil {
		t.Fatal(err)
	}
	known := map[string]string{}
	for _, m := range first.msgs {
		known[m.ID] = m.Stamp
	}
	// One item is modified (now the newest change).
	items := o.stores[1].defaults[6].items
	items[0].props[propLastModified] = DateOf(t0.Add(48 * time.Hour))
	before := callsTo(o, "Table.GetArray")
	sk := &memSink{known: known}
	if _, err := c.Sync(t.Context(), in, "", sk); err != nil {
		t.Fatal(err)
	}
	if len(sk.msgs) != 1 || sk.msgs[0].ID != items[0].id() {
		t.Fatalf("delivered %d", len(sk.msgs))
	}
	// Snapshot: 5 pages + 1 empty; stream: the changed row is first, so 1 page.
	if n := callsTo(o, "Table.GetArray") - before; n != 7 {
		t.Fatalf("GetArray calls %d (the stream must stop after the last wanted row)", n)
	}
}

func TestSortRefusedFallsBackToUnsortedRead(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	o.sortErr = 0x80070057
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", sk); err != nil || len(sk.msgs) != 5 {
		t.Fatalf("%v %d", err, len(sk.msgs))
	}
	if c.Stats().Sync.SortFallbacks != 1 {
		t.Fatal("fallback must be counted")
	}
}

func TestSnapshotIncompleteWhenTheFolderChangedMeanwhile(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	o.stores[1].defaults[6].items = append(o.stores[1].defaults[6].items, newItem(99, t0.Add(99*time.Hour)))
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", sk); err != nil {
		t.Fatal(err)
	}
	if sk.complete || len(sk.versions) != 6 {
		t.Fatalf("an item added after Discover makes the snapshot incomplete (no removals): %v %d", sk.complete, len(sk.versions))
	}
}

// Live finding (column acceptance, now in TestSmoke): Outlook tables refuse PidTagLocalCommitTime
// with E_FAIL on GetArray, failing the whole pass. It must never be a column.
func TestTableColumnsAvoidRefusedProperties(t *testing.T) {
	for _, col := range fullColumns(true) {
		if col.name == propLocalCommitTime {
			t.Fatal("PidTagLocalCommitTime is read in the detail pass, never in a table")
		}
	}
	o := profile()
	o.refuseCols[propSubject] = true
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", &memSink{}); err == nil {
		t.Fatal("a refused column fails the folder")
	}
}

// A SAFEARRAY read in the wrong dimension order must fail loudly, not
// scramble fields into each other.
func TestTransposedGetArrayIsALayoutError(t *testing.T) {
	o := profile()
	o.transpose = true
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	_, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", &memSink{})
	if !errors.Is(err, errLayout) {
		t.Fatalf("%v", err)
	}
}

func TestSyncOfAnUndiscoveredFolderFails(t *testing.T) {
	c, _ := testConnector(t, profile(), nil)
	if _, err := c.Sync(t.Context(), mail.Folder{StoreID: "x", Path: "y"}, "", &memSink{}); err == nil {
		t.Fatal("must fail")
	}
}

func TestSenderDNIsResolvedOnceAndCached(t *testing.T) {
	o := profile()
	items := o.stores[1].defaults[6].items
	dn := "/O=EXCHANGELABS/OU=EXCHANGE ADMINISTRATIVE GROUP/CN=RECIPIENTS/CN=CARL"
	o.book[hexID("lucas")] = fakeEntry{smtp: "lucas@example.com"}
	for _, it := range items[:3] {
		delete(it.props, propSenderSMTP)
		it.props[propSenderEmail] = dn
		it.props[propSenderEntryID] = []byte("lucas")
	}
	delete(items[3].props, propSenderSMTP)
	items[3].props[propSenderEmail] = "/o=x/cn=gone" // no entry id: kept as DN
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox), "", sk); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range sk.msgs {
		got[m.SenderAddress]++
	}
	if got["lucas@example.com"] != 3 || got["/o=x/cn=gone"] != 1 {
		t.Fatalf("senders %v", got)
	}
	if n := callsTo(o, "Namespace.GetAddressEntryFromID"); n != 2 { // desk owner + lucas once
		t.Fatalf("GetAddressEntryFromID %d times", n)
	}
	if r := c.Stats().Resolve; r.CacheHits != 2 || r.Resolved != 1 {
		t.Fatalf("resolve stats %+v", r)
	}
}

// Live finding: marking mail read does not move PR_LAST_MODIFICATION_TIME
// (1 stale read flag after a full + incremental sync). The folder's unread
// count is part of its cursor and the read state part of the change stamp.
func TestReadFlipIsDetectedWithoutAModificationTime(t *testing.T) {
	o := profile()
	inbox := o.stores[1].defaults[6]
	inbox.commitMax = t0
	c, _ := testConnector(t, o, nil)
	folders, _ := c.Discover(t.Context())
	in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	first := &memSink{}
	cur, err := c.Sync(t.Context(), in, "", first)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]string{}
	for _, m := range first.msgs {
		known[m.ID] = m.Stamp
	}
	inbox.items[2].props[propMessageFlags] = int64(0) // marked unread; nothing else moves
	inbox.unread = 1
	folders, _ = c.Discover(t.Context())
	in = folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	sk := &memSink{known: known}
	next, err := c.Sync(t.Context(), in, cur, sk)
	if err != nil || next == cur {
		t.Fatalf("the unread count must change the cursor: %v", err)
	}
	if len(sk.msgs) != 1 || sk.msgs[0].IsRead || sk.msgs[0].ID != inbox.items[2].id() {
		t.Fatalf("delivered %+v", sk.msgs)
	}
}
