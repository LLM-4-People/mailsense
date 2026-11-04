package outlook

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func TestDiscoverStoresFoldersRolesAndOwners(t *testing.T) {
	o := profile()
	public := mailbox("Public", "S-PUB", 2, nil, nil)
	o.stores = append(o.stores, public)
	c, _ := testConnector(t, o, map[string]any{"outlook.discover.count_unpicked": true})
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Per mailbox: Inbox, Inbox\Projets, Sent; hidden, search and calendar
	// folders are skipped; public folders are not synced by default.
	if len(folders) != 6 {
		t.Fatalf("%d folders: %+v", len(folders), folders)
	}
	in := folderByRole(t, folders, mail.StorePrimary, mail.RoleInbox)
	if in.Path != `\\amelie.durand@example.com\Boîte de réception` || in.ItemCount != 5 ||
		in.SourceID != "outlook" || in.StoreID != "S-PRIMARY" {
		t.Fatalf("primary inbox %+v", in)
	}
	sent := folderByRole(t, folders, mail.StoreDelegate, mail.RoleSent)
	if sent.StoreName != "Service Desk" || sent.Path != `\\Service Desk\Éléments envoyés` {
		t.Fatalf("delegate sent %+v", sent)
	}
	boxes := c.Mailboxes()
	// Addresses are reported as Outlook gives them (the primary SMTP twice:
	// PrimarySmtpAddress and its "SMTP:" proxy); scope.NewIdentity is the
	// one place they are normalized and de-duplicated.
	if me := boxes[1]; !slices.Equal(scope.NewIdentity(nil, me.Addresses).Addresses, []string{"amelie.durand@example.com", "amelie@example.org",
		strings.ToLower(oldDN), strings.ToLower(meDN)}) || !me.Default || me.Kind != mail.StorePrimary || !me.IdentityComplete {
		t.Fatalf("the user's identity: primary SMTP, SMTP and X500 proxies, the DN; SIP/SPO left out: %+v", me)
	}
	if desk := boxes[0]; !slices.Equal(desk.Addresses, []string{"support@example.com"}) || desk.Default || !desk.IdentityComplete {
		t.Fatalf("the delegate store's owner: %+v", desk)
	}
	var sub mail.Folder
	for _, f := range folders {
		if strings.HasSuffix(f.Path, `\Projets`) && f.StoreKind == mail.StorePrimary {
			sub = f
		}
	}
	if sub.Role != mail.RoleOther || sub.Path != `\\amelie.durand@example.com\Boîte de réception\Projets` {
		t.Fatalf("subfolder role comes from identity, never the name: %+v", sub)
	}
	st := c.Stats()
	// The disabled public store is walked once, for its count, too.
	if st.Discover.Skipped["hidden"] != 3 || st.Discover.Skipped["search"] != 3 || st.Discover.Skipped["not_mail"] != 3 {
		t.Fatalf("skips %v", st.Discover.Skipped)
	}
	if pub := c.Stores()[2]; pub.Kind != mail.StorePublic || pub.Selected || pub.Folders != 3 || pub.Addresses != nil {
		t.Fatalf("public store listed, not picked, no identity reported: %+v", pub)
	}
	if callsTo(o, "Folder.DefaultItemType") != 0 || callsTo(o, "Folder.FolderPath") != 0 {
		t.Fatal("one GetProperties per folder: no DefaultItemType/FolderPath calls")
	}
}

func TestDiscoverFilters(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{"outlook.folders.exclude_patterns": []any{`\\Projets$`}})
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 4 {
		t.Fatalf("folder pattern: %+v", folders)
	}
	o2 := profile()
	c2, _ := testConnector(t, o2, map[string]any{source.SettingSelected: []any{deskKey}})
	if f, _ := c2.Discover(t.Context()); len(f) != 3 || f[0].StoreKind != mail.StoreDelegate {
		t.Fatalf("only the delegate store: %+v", f)
	}
}

// User decision 2026-10-15: mailbox selection is explicit. With nothing
// picked no folder is returned; every store is listed (key, kind,
// default, cached/online, counts) and none is preselected. Counting
// unpicked stores is opt-in (off by default); this test turns it on to
// pin that path.
func TestNothingIsPickedByDefaultAndEveryStoreIsListed(t *testing.T) {
	o := profile()
	c, err := newConnector("outlook", configtest.New(t, map[string]any{"outlook.discover.count_unpicked": true}), nil, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	folders, err := c.Discover(t.Context())
	if err != nil || len(folders) != 0 {
		t.Fatalf("nothing is picked: %v %+v", err, folders)
	}
	stores := c.Stores()
	if len(stores) != 2 {
		t.Fatalf("%+v", stores)
	}
	desk, me := stores[0], stores[1]
	if desk.Key != deskKey || desk.Selected || desk.Kind != mail.StoreDelegate || desk.Cached || desk.Folders != 3 || desk.Items != 4 ||
		me.Key != primaryKey || me.Selected || !me.Default || !me.Cached || me.Items != 8 || me.Addresses != nil {
		t.Fatalf("store list %+v", stores)
	}
	walks := callsTo(o, "Store.GetRootFolder")
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if callsTo(o, "Store.GetRootFolder")-walks != 0 || c.Stores()[0].Items != 4 {
		t.Fatal("an unpicked store is walked once per connector, then its count is kept")
	}
	if callsTo(o, "Item.Body") != 0 || callsTo(o, "Folder.GetTable") != 0 {
		t.Fatal("discovery reads no message")
	}
	if source.StoreKey("outlook", "S-DESK") != deskKey || len(deskKey) != len("store-")+12 {
		t.Fatal("keys are stable and short")
	}
}

// No fallback: when Outlook cannot say which store is the default, no
// store is marked default, no type-0 store is promoted, and the profile
// user is not assumed to own any store.
func TestUnreadableDefaultStoreMarksNothing(t *testing.T) {
	o := profile()
	o.stores[1].isDefault = false
	second := mailbox("other@x.com", "S-OTHER", olPrimaryExchangeMailbox, nil, nil)
	o.stores = append(o.stores, second)
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, s := range c.Stores() {
		if s.Default {
			t.Fatalf("no default store when Outlook names none: %+v", s)
		}
		keys[s.Key] = true
	}
	if len(keys) != 3 {
		t.Fatal("every store keeps its own key")
	}
	if me := c.Stores()[1]; me.Kind != mail.StorePrimary || len(me.Addresses) != 0 {
		t.Fatalf("type 0 is a primary mailbox; its owner is not assumed: %+v", me)
	}
}

func TestStoreKinds(t *testing.T) {
	cases := []struct {
		typ  int64
		path string
		want mail.StoreKind
	}{
		{0, "", mail.StorePrimary}, {1, "", mail.StoreDelegate}, {2, "", mail.StorePublic}, {4, "", mail.StoreShared},
		{3, `C:\x\Archive.PST`, mail.StorePST}, {3, `C:\x\imap.ost`, mail.StoreOther}, {3, "", mail.StoreOther}, {9, "", mail.StoreOther},
	}
	for _, k := range cases {
		if got := storeKind(k.typ, k.path); got != k.want {
			t.Errorf("%d %q: %s, want %s", k.typ, k.path, got, k.want)
		}
	}
}

// A delegate-type store the user owns is the user's online archive; a
// PST names its owner through the account delivering into it.
func TestArchiveAndAccountOwners(t *testing.T) {
	o := profile()
	arch := mailbox("Archive en ligne", "S-ARCH", 1, nil, nil)
	arch.ownerEID = hexID("me-eid")
	o.book[hexID("me-eid")] = o.book["me"]
	pst := mailbox("Courrier POP", "S-PST", 3, nil, nil)
	pst.filePath = `C:\Users\x\pop.pst`
	o.stores = append(o.stores, arch, pst)
	o.accounts = []fakeAccount{{smtp: "李娜@例子.中国", storeID: "S-PST"}, {smtp: "x@example.net", storeID: "S-NONE"}}
	c, _ := testConnector(t, o, map[string]any{source.SettingSelected: []any{source.StoreKey("outlook", "S-ARCH"), source.StoreKey("outlook", "S-PST")}})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := c.Stores()
	if st[2].Kind != mail.StoreArchive || st[2].Addresses[0] != "amelie.durand@example.com" {
		t.Fatalf("archive %+v", st[2])
	}
	if st[3].Kind != mail.StorePST || !slices.Equal(st[3].Addresses, []string{"李娜@例子.中国"}) {
		t.Fatalf("pst %+v", st[3])
	}
	if st[0].Kind != mail.StoreDelegate {
		t.Fatalf("someone else's mailbox stays a delegate store: %+v", st[0])
	}
}

func TestOfflineStoreIsSkippedOthersStillDiscovered(t *testing.T) {
	o := profile()
	o.stores[0].offline = true
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil || len(folders) != 3 || folders[0].StoreKind != mail.StorePrimary {
		t.Fatalf("%v %+v", err, folders)
	}
	if c.Stats().Discover.StoreErrors != 1 {
		t.Fatal("the offline store must be counted")
	}
	if desk := c.Mailboxes()[0]; desk.Online() || desk.Error == "" {
		t.Fatalf("a picked store that cannot be read is listed with its error: %+v", desk)
	}
}

func TestCursorNeedsChangeStateAndNewestFallback(t *testing.T) {
	unknown := folderState{count: 3, readAt: t0}
	if unknown.unchanged(unknown.cursor(), time.Hour) {
		t.Fatal("a non-empty folder without change state must always sync")
	}
	empty := folderState{readAt: t0}
	if !empty.unchanged(empty.cursor(), time.Hour) || empty.unchanged("junk", time.Hour) || empty.unchanged("", time.Hour) {
		t.Fatal("an empty folder has a state; a junk or missing cursor never matches")
	}
	later := empty
	later.readAt = t0.Add(time.Hour)
	if later.unchanged(empty.cursor(), time.Hour) || !later.unchanged(empty.cursor(), 2*time.Hour) {
		t.Fatal("the backstop: unchanged only while the last snapshot is younger than recheck_after")
	}
	o := profile()
	o.stores[1].defaults[6].commitMax = t0
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Inbox has a commit time; Sent (3 items) and Projets (empty) do not:
	// only Sent and the two non-empty delegate folders need the sorted read.
	if n := c.Stats().Discover.NewestReads; n != 3 {
		t.Fatalf("newest reads %d", n)
	}
	o.sortErr = 0x80070057
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.Stats().Discover.NewestFails != 3 {
		t.Fatal("a refused sort leaves the state unknown, it does not fail discovery")
	}
}

func TestNewOffWindowsIsUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("COM exists here")
	}
	if _, err := New("outlook", configtest.New(t, nil), nil, nil); err != ErrUnsupported {
		t.Fatalf("%v", err)
	}
}
