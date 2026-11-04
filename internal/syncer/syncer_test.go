package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type item struct {
	msg  mail.Message
	skip error
}

// fakeConn serves fixed items per folder; the cursor is "v<version>".
type fakeConn struct {
	id         string
	version    int
	items      map[string][]item
	folders    []mail.Folder   // when set, the folders Discover returns
	incomplete map[string]bool // folder path → deliver an incomplete snapshot
	failOn     string          // folder whose Sync fails after streaming
	failErr    error           // the error it fails with (nil: a plain connector error)
	block      chan struct{}
	entered    chan struct{} // closed when Sync reaches the block point
	noOwner    bool          // the source reports no owner address
	offline    bool          // the store cannot be read: listed with its error, no folders
	partial    bool          // the owner identity is read only in part
	discovers  int           // how many folder discoveries ran
}

func (f *fakeConn) ID() string   { return f.id }
func (f *fakeConn) Kind() string { return "fake" }
func (f *fakeConn) Capabilities() source.Capabilities {
	return source.Capabilities{Incremental: true, OwnerIdentity: true}
}
func (f *fakeConn) Discover(context.Context) ([]mail.Folder, error) {
	f.discovers++
	if f.offline {
		return nil, nil // an unreadable store is listed with its error, its folders left out
	}
	if f.folders != nil {
		return f.folders, nil
	}
	return []mail.Folder{
		{SourceID: f.id, StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "Inbox", Role: mail.RoleInbox,
			ItemCount: int64(len(f.items["Inbox"]))},
		{SourceID: f.id, StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "Sent", Role: mail.RoleSent, ItemCount: -1},
	}, nil
}

// Mailboxes reports one mailbox and its owner (owner "" = none reported).
func (f *fakeConn) Mailboxes() []source.Mailbox {
	m := source.Mailbox{Key: source.StoreKey(f.id, "store-1"), SourceID: f.id, StoreID: "store-1", Name: "Amélie",
		Kind: mail.StorePrimary, Default: true, Folders: 2, Items: int64(len(f.items["Inbox"]) + len(f.items["Sent"]))}
	if !f.noOwner {
		m.Addresses = []string{mailtest.Owner}
	}
	m.IdentityComplete = !f.partial
	if f.partial {
		m.Addresses = m.Addresses[:0] // e.g. the SMTP address without the DN
	}
	if f.offline {
		m.Addresses, m.Error, m.IdentityComplete = nil, "0x80040115", false
	}
	return []source.Mailbox{m}
}

// fakeKey is the store key of every fakeConn source "fake".
var fakeKey = source.StoreKey("fake", "store-1")

func (f *fakeConn) Sync(ctx context.Context, folder mail.Folder, cursor source.Cursor, sink source.Sink) (source.Cursor, error) {
	next := source.Cursor(fmt.Sprintf("v%d", f.version))
	if cursor == next {
		return cursor, nil
	}
	its := f.items[folder.Path]
	versions := make([]source.Version, 0, len(its))
	for _, it := range its {
		if it.msg.ID != "" {
			versions = append(versions, source.Version{ID: it.msg.ID, Stamp: it.msg.Stamp})
		}
	}
	want, err := sink.Snapshot(ctx, versions, !f.incomplete[folder.Path])
	if err != nil {
		return "", err
	}
	for i, it := range its {
		if f.block != nil && i == 1 {
			if f.entered != nil {
				close(f.entered)
			}
			<-f.block
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		sink.Progress(source.Progress{Done: int64(i + 1), Total: int64(len(its)), Unit: "items"})
		if it.skip != nil {
			sink.Skip(fmt.Sprint(i), it.skip)
			continue
		}
		if it.msg.ID != "" && !want[it.msg.ID] {
			continue
		}
		if err := sink.Message(ctx, it.msg); err != nil {
			return "", err
		}
	}
	if folder.Path == f.failOn {
		if f.failErr != nil {
			return "", f.failErr
		}
		return "", errors.New("connector lost the store")
	}
	return next, nil
}

func inbox(id string) item { return item{msg: mailtest.Message("fake", id)} }

func sent(id string) item {
	m := mailtest.Message("fake", id)
	m.FolderPath, m.FolderRole, m.AddressedToMe = "Sent", mail.RoleSent, ""
	m.SenderAddress = "amelie.durand@example.com"
	return item{msg: m}
}

func items(n int, mk func(string) item, prefix string) []item {
	var out []item
	for i := range n {
		out = append(out, mk(fmt.Sprintf("%s%d", prefix, i)))
	}
	return out
}

func setup(t *testing.T, overrides map[string]any) (*Syncer, *store.Store, *events.Bus, *config.Config) {
	t.Helper()
	bus := events.New()
	if _, set := overrides[source.SettingSelected]; !set {
		withPick := map[string]any{source.SettingSelected: []any{fakeKey, source.StoreKey("fake2", "store-1"), source.StoreKey("fake", "me"), source.StoreKey("fake", "desk")}}
		for k, v := range overrides {
			withPick[k] = v
		}
		overrides = withPick
	}
	cfg := configtest.NewWithBus(t, bus, overrides)
	st, err := store.Open(t.Context(), cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, cfg, bus, nil), st, bus, cfg
}

func TestRunWritesReconcilesAndComputesScope(t *testing.T) {
	s, st, bus, _ := setup(t, map[string]any{"sync.batch_size": 3})
	progress, _ := events.Subscribe(bus, TopicProgress, 1024)
	finished, _ := events.Subscribe(bus, TopicFinished, 4)
	shared := mailtest.Message("fake", "shared")
	shared.AddressedToMe, shared.Recipients = "", []mail.Recipient{{Kind: mail.To, Address: "wordpress@example.com"}}
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": append(items(7, inbox, "i"), item{msg: shared}),
		"Sent":  items(4, sent, "s"),
	}}
	var reports int
	rep, err := s.Run(t.Context(), c, func(Progress) { reports++ })
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != runstate.Succeeded || rep.Totals.Written != 12 || rep.Totals.Seen != 12 || len(rep.Folders) != 2 {
		t.Fatalf("report %+v", rep)
	}
	if rep.Scope == nil || len(rep.Scope.Stores) != 1 || rep.Scope.Stores[0].Identity.Primary() != "amelie.durand@example.com" ||
		rep.Scope.Counts[scope.Owner] != 11 || rep.Scope.Counts[scope.Shared] != 1 {
		t.Fatalf("scope %+v", rep.Scope)
	}
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleInbox] != 8 || counts[mail.RoleSent] != 4 {
		t.Fatalf("stored %v", counts)
	}
	if reports == 0 || len(progress.C) == 0 {
		t.Fatal("progress must be reported")
	}
	if got := <-finished.C; got.RunID != rep.RunID {
		t.Fatal("finished event")
	}
	if runs, _ := st.ListRuns(t.Context(), 1); runs[0].ID != rep.RunID || runs[0].State != runstate.Succeeded {
		t.Fatalf("run %+v", runs)
	}
	again, err := s.Run(t.Context(), c, nil)
	if err != nil || !again.Folders[0].Unchanged || again.Totals.Seen != 0 {
		t.Fatalf("unchanged second run: %+v %v", again, err)
	}
}

func TestSkipRatioIsFatalAndKeepsPriorData(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(10, inbox, "i")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	// 1 skip in 50 = 2% ≥ 1%.
	c.version = 2
	c.items["Inbox"] = append(items(49, inbox, "j"), item{skip: errors.New("COM failure")})
	rep, err := s.Run(ctx, c, nil)
	if err == nil || !strings.Contains(err.Error(), "skip_ratio_fatal") || rep.Status != runstate.Failed {
		t.Fatalf("want the skip-ratio failure, got %v (%s)", err, rep.Status)
	}
	counts, _ := st.CountByRole(ctx)
	if counts[mail.RoleInbox] < 10 {
		t.Fatalf("prior data must survive a failed run: %v", counts)
	}
	f, _ := c.Discover(ctx)
	id, _ := st.UpsertFolder(ctx, f[0])
	if cur, _, _ := st.Cursor(ctx, id); cur != "v0" {
		t.Fatalf("a failed folder must not advance its cursor: %q", cur)
	}
	// 1 skip in 200 = 0.5% < 1%.
	c.items["Inbox"] = append(items(199, inbox, "k"), item{skip: errors.New("COM failure")})
	if rep, err := s.Run(ctx, c, nil); err != nil || rep.Totals.Skipped != 1 {
		t.Fatalf("below the ratio the run succeeds: %v %+v", err, rep.Totals)
	}
}

func TestItemLevelProblemsAreCountedNotWritten(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	bad := mailtest.Message("fake", "")
	foreign := mailtest.Message("other-source", "x")
	dup := mailtest.Message("fake", "i0")
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": append(items(300, inbox, "i"), item{msg: bad}, item{msg: foreign}, item{msg: dup})}}
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Written != 300 || rep.Totals.Skipped != 2 || rep.Totals.Duplicates != 1 || rep.Totals.Seen != 303 {
		t.Fatalf("totals %+v", rep.Totals)
	}
}

func TestConnectorFailureAndMisfiledMessages(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i")}, failOn: "Inbox"}
	if rep, err := s.Run(t.Context(), c, nil); err == nil || rep.Folders[0].Error == "" {
		t.Fatalf("connector failure must fail the run: %+v", rep)
	}
	misfiled := mailtest.Message("fake", "m")
	misfiled.FolderPath = "Elsewhere"
	c2 := &fakeConn{id: "fake", version: 1, items: map[string][]item{"Inbox": {{msg: misfiled}}}}
	if _, err := s.Run(t.Context(), c2, nil); err == nil || !strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("a message stored outside the synced folder must break reconciliation: %v", err)
	}
}

// detailCounter is a fakeConn that is also a DetailReader.
type detailCounter struct {
	fakeConn
	reads int
}

func (d *detailCounter) ReadDetails(context.Context, source.ItemRef) (mail.Details, error) {
	d.reads++
	return mail.Details{}, nil
}

// Incident 2026-10-15: after Outlook crashed under a folder's sync, the
// run moved on to the next folder, which reconnected to (or started) a
// restarting Outlook. An unavailable source now ends the whole run: no
// further folder, no detail pass, no removals.
func TestUnavailableSourceEndsTheRun(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := &detailCounter{fakeConn: fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i"), "Sent": items(2, inbox, "s")},
		failOn: "Inbox", failErr: fmt.Errorf("outlook: GetArray: %w", source.Unavailable("Outlook is gone"))}}
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, source.ErrUnavailable) || rep.Status == "succeeded" {
		t.Fatalf("%v %+v", err, rep)
	}
	if len(rep.Folders) != 1 || rep.Details != nil || c.reads != 0 {
		t.Fatalf("the run went on: %d folders, details %+v, %d reads", len(rep.Folders), rep.Details, c.reads)
	}
	// A plain folder error still lets the other folders sync.
	c2 := &detailCounter{fakeConn: fakeConn{id: "fake2", items: map[string][]item{"Inbox": items(2, inbox, "j"), "Sent": items(1, inbox, "t")},
		failOn: "Inbox"}}
	if rep, err := s.Run(t.Context(), c2, nil); err == nil || len(rep.Folders) != 2 {
		t.Fatalf("a folder error: %v, %d folders", err, len(rep.Folders))
	}
}

func TestStoreFailureAbortsRun(t *testing.T) {
	s, st, _, cfg := setup(t, map[string]any{"store.busy_timeout": "20ms"})
	locker, err := sql.Open("sqlite", "file://"+(&url.URL{Path: store.Path(cfg)}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, _ := locker.Conn(t.Context())
	defer conn.Close()
	// Register the source first so only the message writes hit the lock.
	_ = st.UpsertSource(t.Context(), "fake", "fake", "fake", source.Capabilities{})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i")}}
	if _, err := conn.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(t.Context(), c, nil)
	_, _ = conn.ExecContext(t.Context(), "ROLLBACK")
	if err == nil {
		t.Fatal("a locked database must fail the run")
	}
}

func TestCancelledRun(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	m := jobs.NewManager(configtest.New(t, nil), nil, nil)
	block, entered := make(chan struct{}), make(chan struct{})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(5, inbox, "i")}, block: block, entered: entered}
	j, err := s.Start(m, c)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	j.Cancel()
	close(block)
	<-j.Done()
	if info := j.Info(); info.Status != runstate.Cancelled {
		t.Fatalf("job %+v", info)
	}
	if counts, _ := st.VerdictCounts(t.Context()); len(counts) != 0 {
		t.Fatalf("nothing was written, no verdicts expected: %v", counts)
	}
	if runs, _ := st.ListRuns(t.Context(), 1); runs[0].State != runstate.Cancelled {
		t.Fatalf("run %+v", runs[0])
	}
}

type brokenDiscovery struct{ fakeConn }

func (b *brokenDiscovery) Discover(context.Context) ([]mail.Folder, error) {
	return nil, errors.New("store list unavailable")
}

func TestDiscoverFailureFailsTheRun(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	rep, err := s.Run(t.Context(), &brokenDiscovery{fakeConn{id: "fake"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "store list unavailable") || rep.Status != runstate.Failed {
		t.Fatalf("%v %+v", err, rep)
	}
	if runs, _ := st.ListRuns(t.Context(), 1); runs[0].State != runstate.Failed || runs[0].Error == "" {
		t.Fatalf("the failure must be recorded: %+v", runs)
	}
}

func TestProgressIsThrottledByTheClock(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC))
	bus := events.New()
	cfg := configtest.NewWithBus(t, bus, map[string]any{"sync.batch_size": 1000, source.SettingSelected: []any{fakeKey}})
	st, err := store.Open(t.Context(), cfg, bus, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var reports int
	if _, err := New(st, cfg, bus, clk).Run(t.Context(), &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(50, inbox, "i")}}, func(Progress) { reports++ }); err != nil {
		t.Fatal(err)
	}
	// Time never advances: one throttled emit at the first position, one at
	// the final flush, one after the folder completed (Inbox), then Sent's
	// completion — never one per item.
	if reports > 5 {
		t.Fatalf("%d progress reports for 50 items with a frozen clock", reports)
	}
}

func TestStartAsJobSucceeds(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	m := jobs.NewManager(configtest.New(t, nil), nil, nil)
	j, err := s.Start(m, &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i"), "Sent": items(1, sent, "s")}})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if info := j.Info(); info.Status != runstate.Succeeded || !strings.Contains(info.Progress.Message, "written") {
		t.Fatalf("job %+v", info)
	}
}

// User decision 2026-10-15: no identity, no verdicts — the owner is never
// guessed from Sent mail; the store is reported as blocked until the user
// declares an address; a declared address then judges it.
func TestNoIdentityBlocksTheStoreUntilDeclared(t *testing.T) {
	s, _, _, cfg := setup(t, nil)
	c := &fakeConn{id: "fake", noOwner: true, items: map[string][]item{"Inbox": items(2, inbox, "i"), "Sent": items(3, sent, "s")}}
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Scope.Stores[0].Identity.Known() || rep.Scope.Stores[0].Blocked == "" ||
		rep.Scope.Counts[""] != 5 || rep.Scope.Counts[scope.Owner] != 0 {
		t.Fatalf("%+v %v", rep.Scope, err)
	}
	if err := cfg.Set(source.SettingDeclared, []any{fakeKey + "=Amelie.Durand@example.com"}); err != nil {
		t.Fatal(err)
	}
	c.version++
	rep, _ = s.Run(t.Context(), c, nil)
	if id := rep.Scope.Stores[0].Identity; id.Primary() != "amelie.durand@example.com" || rep.Scope.Stores[0].Blocked != "" ||
		rep.Scope.Counts[scope.Owner] != 5 || rep.Scope.Stores[0].Judged != 5 {
		t.Fatalf("declared owner: %+v", rep.Scope)
	}
	// Incremental: an unchanged mailbox is not judged again.
	if rep, _ = s.Run(t.Context(), c, nil); rep.Scope.Stores[0].Judged != 0 {
		t.Fatalf("nothing changed, nothing judged: %+v", rep.Scope.Stores[0])
	}
}

// Nothing syncs until the user picks: the run lists the mailbox, reads no
// message and fails with ErrNothingPicked.
func TestNothingSyncsUntilPicked(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i")}}
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, ErrNothingPicked) || rep.Totals.Seen != 0 || len(rep.Mailboxes) != 1 || rep.Mailboxes[0].Selected {
		t.Fatalf("%v %+v", err, rep)
	}
	if counts, _ := st.CountByRole(t.Context()); len(counts) != 0 {
		t.Fatalf("nothing stored: %v", counts)
	}
	boxes, err := s.Discover(t.Context(), c)
	if err != nil || len(boxes) != 1 || boxes[0].Key != fakeKey || boxes[0].Addresses != nil && boxes[0].Selected {
		t.Fatalf("discover %+v %v", boxes, err)
	}
}

// Per-store scope: a delegate store is judged against its own reported
// owner; sending on its behalf never makes the user its owner.
type twoStores struct{ fakeConn }

func (f *twoStores) Discover(context.Context) ([]mail.Folder, error) {
	return []mail.Folder{
		{SourceID: f.id, StoreID: "me", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "Sent", Role: mail.RoleSent, ItemCount: -1},
		{SourceID: f.id, StoreID: "desk", StoreName: "Desk", StoreKind: mail.StoreDelegate, Path: "Inbox", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: f.id, StoreID: "desk", StoreName: "Desk", StoreKind: mail.StoreDelegate, Path: "Sent", Role: mail.RoleSent, ItemCount: -1},
	}, nil
}

func (f *twoStores) Mailboxes() []source.Mailbox {
	return []source.Mailbox{
		{Key: source.StoreKey(f.id, "me"), SourceID: f.id, StoreID: "me", Name: "Amélie", Kind: mail.StorePrimary, Addresses: []string{mailtest.Owner},
			IdentityComplete: true},
		{Key: source.StoreKey(f.id, "desk"), SourceID: f.id, StoreID: "desk", Name: "Desk", Kind: mail.StoreDelegate,
			Addresses: []string{"support@example.com"}, IdentityComplete: true},
	}
}

func (f *twoStores) Sync(ctx context.Context, folder mail.Folder, _ source.Cursor, sink source.Sink) (source.Cursor, error) {
	for i, it := range f.items[folder.StoreID+"/"+folder.Path] {
		m := it.msg
		m.StoreID, m.StoreName, m.FolderPath, m.FolderRole = folder.StoreID, folder.StoreName, folder.Path, folder.Role
		m.ID = fmt.Sprintf("%s-%s-%d", folder.StoreID, folder.Path, i)
		if err := sink.Message(ctx, m); err != nil {
			return "", err
		}
	}
	return "", nil
}

func TestScopeIsPerStore(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	toDesk := mailtest.Message("fake", "x")
	toDesk.AddressedToMe, toDesk.Recipients = "", []mail.Recipient{{Kind: mail.To, Address: "support@example.com"}}
	toAmelie := mailtest.Message("fake", "y")
	toAmelie.AddressedToMe = "" // recipients: amelie@ and lucas@
	c := &twoStores{fakeConn{id: "fake", items: map[string][]item{
		"me/Sent":    items(3, sent, "s"),
		"desk/Sent":  items(5, sent, "h"), // the user sending from Desk's Sent folder
		"desk/Inbox": {{msg: toDesk}, {msg: toAmelie}},
	}}}
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || len(rep.Scope.Stores) != 2 {
		t.Fatalf("%v %+v", err, rep.Scope)
	}
	byStore := map[string]StoreScope{}
	for _, ss := range rep.Scope.Stores {
		byStore[ss.Store.StoreID] = ss
	}
	me, desk := byStore["me"], byStore["desk"]
	if me.Identity.Primary() != mailtest.Owner || desk.Identity.Primary() != "support@example.com" {
		t.Fatalf("each store has its reported owner: %+v / %+v", me.Identity, desk.Identity)
	}
	// In Desk's mailbox, mail to support@ is Desk's own; mail to amelie@
	// and the user's own sending from Desk's Sent folder are not.
	if desk.Counts["owner"] != 1 || desk.Counts["shared"] != 6 || me.Counts["owner"] != 3 {
		t.Fatalf("verdicts me %+v desk %+v", me.Counts, desk.Counts)
	}
	if owners, _ := st.Owners(t.Context()); len(owners) != 2 {
		t.Fatalf("one identity per store: %+v", owners)
	}
}

func TestRunFoldersSyncsOnlyTheSelectedFolders(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i"), "Sent": items(3, sent, "s")}}
	rep, err := s.RunFolders(t.Context(), c, func(f mail.Folder) bool { return f.Role == mail.RoleSent }, nil)
	if err != nil || len(rep.Folders) != 1 || rep.Totals.Written != 3 {
		t.Fatalf("%v %+v", err, rep)
	}
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleInbox] != 0 {
		t.Fatalf("%v", counts)
	}
}
