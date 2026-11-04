package outlook

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// The connector through the real syncer and store: what a user's sync does.
func syncRig(t *testing.T, o *fakeOutlook) (*Connector, *syncer.Syncer, *store.Store) {
	t.Helper()
	cfg := configtest.New(t, bothStores(map[string]any{"sync.details.batch_size": 2, "outlook.table.page_rows": 3,
		"outlook.sync.recheck_after": "1h", "sync.removal.hold_count": 3, "sync.removal.auto_apply_after": "2h"}))
	clk := clocktest.New(tNow)
	o.clock = clk
	st, err := store.Open(t.Context(), cfg, nil, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := newConnector("outlook", cfg, nil, clk, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		if o.open != 0 || len(o.misuse) > 0 {
			t.Errorf("open objects %d, misuse %v", o.open, o.misuse)
		}
	})
	return c, syncer.New(st, cfg, nil, clk), st
}

// The rig's backstop and safety-valve durations (syncRig settings), plus a minute.
const (
	rigRecheck   = time.Hour + time.Minute
	rigAutoApply = 2*time.Hour + time.Minute
)

// rigClock is the fake clock shared by the rig's connector, syncer and store.
func rigClock(c *Connector) *clocktest.Fake { return c.clock.(*clocktest.Fake) }

func run(t *testing.T, s *syncer.Syncer, c *Connector) syncer.Report {
	t.Helper()
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil {
		t.Fatalf("run: %v (%+v)", err, rep)
	}
	return rep
}

func TestFullThenIncrementalSync(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	ctx := t.Context()
	rep := run(t, s, c)
	if rep.Totals.Written != 12 || rep.Details == nil || rep.Details.Completed != 12 || rep.Details.Pending != 0 {
		t.Fatalf("full run %+v details %+v", rep.Totals, rep.Details)
	}
	// Details are read newest first across stores.
	if len(o.opened) != 12 {
		t.Fatalf("%d items opened", len(o.opened))
	}
	for i := 1; i < len(o.opened); i++ {
		if o.opened[i-1].props[propDeliveryTime].(Date) < o.opened[i].props[propDeliveryTime].(Date) {
			t.Fatalf("item %d opened before a newer one", i)
		}
	}
	m, err := st.GetMessage(ctx, "outlook", "S-PRIMARY", o.stores[1].defaults[6].items[4].id())
	if err != nil || m.Partial || m.Body == "aperçu" || len(m.Recipients) != 1 || m.LocalCommitTime == "" {
		t.Fatalf("stored %+v %v", m, err)
	}

	// Incremental right after: every folder unchanged, no table, no item.
	before := len(o.calls)
	again := run(t, s, c)
	for _, call := range o.calls[before:] {
		if call == "Namespace.GetItemFromID" || call == "Namespace.GetFolderFromID" {
			t.Fatalf("unchanged folders must not be read: %s", call)
		}
	}
	unchanged := 0
	for _, f := range again.Folders {
		if f.Unchanged {
			unchanged++
		}
	}
	if unchanged != len(again.Folders) || again.Totals.Seen != 0 {
		t.Fatalf("incremental %+v", again.Folders)
	}

	// Modify one item, delete one, move one to another folder (new EntryID,
	// same Message-ID), add one.
	in := o.stores[1].defaults[6]
	sent := o.stores[1].defaults[5]
	in.items[0].props[propLastModified] = DateOf(t0.Add(72 * time.Hour))
	in.items[0].body = "nouveau corps"
	deleted := in.items[1].id()
	moved := in.items[2]
	in.items = append(in.items[:1], in.items[3:]...)
	copyOf := newItem(77, t0)
	copyOf.props[propInternetMessageID] = moved.props[propInternetMessageID]
	sent.items = append(sent.items, copyOf)
	in.items = append(in.items, newItem(88, t0.Add(80*time.Hour)))
	third := run(t, s, c)
	// The moved row has a survivor and is removed now; the deleted row has
	// a nonempty Message-ID and no survivor yet, so the one-run move hold
	// keeps it until the next complete snapshot. That guard is what protects
	// a message that moves between folders during a run.
	if third.Removed.Deleted != 0 || third.Removed.Moved != 1 || third.Removed.Held != 1 {
		t.Fatalf("removals %+v", third.Removed)
	}
	if third.Details.Completed != 3 { // modified + moved copy + new
		t.Fatalf("details %+v", third.Details)
	}
	if _, err := st.GetMessage(ctx, "outlook", "S-PRIMARY", deleted); err != nil {
		t.Fatalf("held deletion must stay stored one run: %v", err)
	}
	// The next complete snapshot (the recheck backstop forces one) removes
	// the genuinely deleted message.
	rigClock(c).Advance(2 * time.Hour)
	if fourth := run(t, s, c); fourth.Removed.Deleted != 1 || fourth.Removed.Held != 0 {
		t.Fatalf("deletion after the one-run hold %+v", fourth.Removed)
	}
	if _, err := st.GetMessage(ctx, "outlook", "S-PRIMARY", deleted); err != store.ErrNotFound {
		t.Fatalf("deleted message still stored: %v", err)
	}
	m, _ = st.GetMessage(ctx, "outlook", "S-PRIMARY", in.items[0].id())
	if m.Body != "nouveau corps" || m.Partial {
		t.Fatalf("modified message not refreshed: %+v", m)
	}
}

func TestScopeRunsPerStore(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	rep := run(t, s, c)
	views, err := st.MailboxViews(t.Context())
	if err != nil || len(views) != 2 {
		t.Fatalf("%v %+v", err, views)
	}
	owners := map[string]string{}
	for _, v := range views {
		owners[v.StoreID] = v.Identity.Primary()
	}
	// The delegate mailbox's Sent items were sent by the user on its
	// behalf; its owner is still the mailbox, as Outlook reports it.
	if owners["S-PRIMARY"] != "amelie.durand@example.com" || owners["S-DESK"] != "support@example.com" {
		t.Fatalf("owners %v", owners)
	}
	for _, ss := range rep.Scope.Stores {
		if ss.Counts[scope.Owner] == 0 || ss.Blocked != "" {
			t.Fatalf("store %s has no owner mail: %+v", ss.Store.StoreID, ss)
		}
	}
}

// The owner is what Outlook reports, never inferred from Sent mail: when
// Outlook names no owner for a store, that store is blocked (no verdicts)
// even though its Sent folder is full of one sender, and the user's
// identity is never borrowed from elsewhere.
func TestAStoreWithoutAReportedOwnerIsBlockedNotGuessed(t *testing.T) {
	o := profile()
	o.currentUser = "unknown" // CurrentUser cannot be resolved
	o.stores[0].ownerEID = "" // Outlook reports no owner for the delegate store
	c, s, st := syncRig(t, o)
	rep := run(t, s, c)
	if owners, _ := st.Owners(t.Context()); len(owners) != 0 {
		t.Fatalf("no identity is invented: %+v", owners)
	}
	for _, ss := range rep.Scope.Stores {
		if ss.Blocked == "" || len(ss.Counts) != 1 || ss.Counts[""] == 0 {
			t.Fatalf("blocked, every message unjudged: %+v", ss)
		}
	}
	// The remedy: the user declares the owner; the next run judges.
	if err := c.cfg.Set(source.SettingDeclared, []any{primaryKey + "=amelie.durand@example.com"}); err != nil {
		t.Fatal(err)
	}
	rep = run(t, s, c)
	for _, ss := range rep.Scope.Stores {
		if (ss.Store.StoreID == "S-PRIMARY") == (ss.Blocked != "") {
			t.Fatalf("declared store judged, the other still blocked: %+v", ss)
		}
	}
}

func TestDetailFailuresStayPartialAndAreRetriedNextRun(t *testing.T) {
	o := profile()
	o.stores[1].defaults[6].items[0].failBody = 0x80004005
	c, s, st := syncRig(t, o)
	rep := run(t, s, c)
	if rep.Details.Failed != 1 || rep.Details.Failures["0x80004005"] != 1 || rep.Details.Pending != 1 {
		t.Fatalf("details %+v", rep.Details)
	}
	o.stores[1].defaults[6].items[0].failBody = 0
	if rep := run(t, s, c); rep.Details.Completed != 1 || rep.Details.Pending != 0 {
		t.Fatalf("retry %+v", rep.Details)
	}
	if n, _ := st.CountPending(context.Background(), "outlook", time.Time{}); n != 0 {
		t.Fatal(n)
	}
}

func TestOfflineDelegateDoesNotStopThePrimary(t *testing.T) {
	o := profile()
	o.stores[0].offline = true
	c, s, st := syncRig(t, o)
	run(t, s, c)
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleInbox] != 5 || counts[mail.RoleSent] != 3 {
		t.Fatalf("primary must sync: %v", counts)
	}
}

// Held removals (review 1b #2) are applied once they persisted for
// sync.removal.auto_apply_after, or at once when confirmed.
func TestHeldRemovalsApplyWhenPersistentOrConfirmed(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	in := o.stores[1].defaults[6]
	in.items = in.items[4:]
	if rep := run(t, s, c); rep.Removed.Held != 4 {
		t.Fatalf("held %+v", rep.Removed)
	}
	held, _ := st.HeldRemovals(t.Context(), "outlook", nil)
	if len(held) != 1 || held[0].Held != 4 || held[0].Stored != 5 {
		t.Fatalf("held list %+v", held)
	}
	run(t, s, c) // unchanged folder: nothing re-evaluated, the hold persists
	if held, _ := st.HeldRemovals(t.Context(), "outlook", nil); len(held) != 1 || held[0].Held != 4 {
		t.Fatalf("still held before auto_apply_after: %+v", held)
	}
	rigClock(c).Advance(rigAutoApply) // past the backstop too: the folder is re-snapshotted
	if rep := run(t, s, c); rep.Removed.Deleted != 4 || rep.Removed.Held != 0 {
		t.Fatalf("applied once persistent: %+v", rep.Removed)
	}

	o2 := profile()
	c2, s2, st2 := syncRig(t, o2)
	run(t, s2, c2)
	o2.stores[1].defaults[6].items = o2.stores[1].defaults[6].items[4:]
	run(t, s2, c2)
	held, _ = st2.HeldRemovals(t.Context(), "outlook", nil)
	rm, err := s2.ConfirmRemovals(t.Context(), held[0].FolderID)
	if err != nil || rm.Deleted != 4 {
		t.Fatalf("confirm: %+v %v", rm, err)
	}
	if held, _ := st2.HeldRemovals(t.Context(), "outlook", nil); len(held) != 0 {
		t.Fatal("nothing held after confirming")
	}
}

// Rules review 1b §4.6, with explicit picking: unpicking a store keeps
// its mail, marks its folders inactive (out of scope and statistics);
// picking it again reactivates them and resumes incrementally.
func TestUnpickedStoreKeepsMailOutOfScopeAndResumes(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	if _, err := s.Unpick(t.Context(), deskKey); err != nil {
		t.Fatal(err)
	}
	rep := run(t, s, c)
	if rep.Removed.Deleted+rep.Removed.Held != 0 {
		t.Fatalf("unpicking removes nothing: %+v", rep.Removed)
	}
	if _, err := st.GetMessage(t.Context(), "outlook", "S-DESK", o.stores[0].defaults[6].items[0].id()); err != nil {
		t.Fatal("the delegate's mail is kept")
	}
	if len(rep.Scope.Stores) != 1 || rep.Scope.Stores[0].Store.StoreID != "S-PRIMARY" {
		t.Fatalf("only the primary store is in scope: %+v", rep.Scope.Stores)
	}
	folders, _ := st.ListFolders(t.Context(), "outlook")
	inactive := 0
	for _, f := range folders {
		if !f.Active {
			inactive++
		}
	}
	if inactive != 3 {
		t.Fatalf("the delegate's 3 folders are inactive: %d", inactive)
	}
	if _, err := s.Pick(t.Context(), deskKey); err != nil {
		t.Fatal(err)
	}
	rep = run(t, s, c)
	if rep.Totals.Seen != 0 || len(rep.Scope.Stores) != 2 {
		t.Fatalf("picking again resumes incrementally (nothing re-read) and brings scope back: seen %d, %+v", rep.Totals.Seen, rep.Scope.Stores)
	}
}

// Nothing syncs until a mailbox is picked: the run lists the mailboxes,
// reads no message and says why.
func TestNothingSyncsBeforeAPick(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	if err := c.cfg.Set(source.SettingSelected, []any{}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, syncer.ErrNothingPicked) || rep.Totals.Seen != 0 || len(rep.Mailboxes) != 2 {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, m := range rep.Mailboxes {
		if m.Selected {
			t.Fatalf("nothing preselected: %+v", m)
		}
	}
	if callsTo(o, "Folder.GetTable") != 0 || callsTo(o, "Namespace.GetItemFromID") != 0 {
		t.Fatal("no message read before a pick")
	}
	boxes, _ := st.MailboxViews(t.Context())
	if len(boxes) != 2 || boxes[0].Key == boxes[1].Key {
		t.Fatalf("the list is persisted: %+v", boxes)
	}
	if owners, _ := st.Owners(t.Context()); len(owners) != 0 {
		t.Fatal("identities are recorded for picked mailboxes only")
	}
}

// Review L0 bugs #1 (failed first: GetExchangeUser failing gave the SMTP
// address only, recorded as the whole identity — the DN and proxies
// lost, the owner's Sent mail flipped to sent_by_other): a read that fails
// in part is reported incomplete, and the recorded identity and verdicts
// stay until a complete read.
func TestAnIdentityReadInPartKeepsTheRecordedOne(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	before, _ := st.Owners(t.Context())
	verdicts, _ := st.VerdictCounts(t.Context())
	me := o.book["me"]
	me.failUser = 0x80040115 // MAPI_E_NETWORK_ERROR: the address book is offline
	o.book["me"] = me
	rep := run(t, s, c)
	if rep.IdentityKept != 1 {
		t.Fatalf("the partial read is reported: kept %d", rep.IdentityKept)
	}
	for _, m := range c.Mailboxes() {
		if m.Key == primaryKey && m.IdentityComplete {
			t.Fatalf("incomplete: %+v", m)
		}
	}
	after, _ := st.Owners(t.Context())
	again, _ := st.VerdictCounts(t.Context())
	if len(after[primaryKey].Identity.Addresses) != len(before[primaryKey].Identity.Addresses) || again[scope.Owner] != verdicts[scope.Owner] {
		t.Fatalf("identity %+v → %+v, verdicts %v → %v", before[primaryKey], after[primaryKey], verdicts, again)
	}
}
