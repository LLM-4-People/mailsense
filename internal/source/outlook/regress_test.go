package outlook

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Review 1b #1a: read A and mark B unread between two runs: item count,
// unread count and newest modification time are all unchanged and the
// folder (no commit time) was skipped — B stayed read, A unread.
func TestReadFlipPairIsCaughtByTheSnapshotBackstop(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	in := o.stores[1].defaults[6]
	a, b := in.items[1], in.items[2]
	a.props[propMessageFlags] = int64(0)
	in.unread = 1
	run(t, s, c)
	if m, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", a.id()); m.IsRead {
		t.Fatal("setup: a single flip changes the unread count and must be seen")
	}
	a.props[propMessageFlags] = int64(msgFlagRead)
	b.props[propMessageFlags] = int64(0)
	rigClock(c).Advance(rigRecheck) // the backstop: every folder is re-snapshotted after outlook.sync.recheck_after
	run(t, s, c)
	ma, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", a.id())
	mb, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", b.id())
	if !ma.IsRead || mb.IsRead {
		t.Errorf("read-flip pair missed: a.read=%v (want true) b.read=%v (want false)", ma.IsRead, mb.IsRead)
	}
}

// Review 1b #1c: a second edit within the same second as the synced stamp
// was never seen (stamps had second precision).
func TestSameSecondModificationIsSeen(t *testing.T) {
	o := profile()
	in := o.stores[1].defaults[6]
	in.commitMax = t0 // the folder keeps a commit time; it moves with the edit
	it := in.items[4]
	base := t0.Add(100 * time.Hour)
	it.props[propLastModified] = DateOf(base.Add(200 * time.Millisecond))
	c, s, st := syncRig(t, o)
	run(t, s, c)
	it.props[propLastModified] = DateOf(base.Add(800 * time.Millisecond))
	it.props[propSubject] = "changed subject"
	in.commitMax = t0.Add(time.Minute)
	run(t, s, c)
	if m, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", it.id()); m.Subject != "changed subject" {
		t.Errorf("same-second modification missed: subject=%q", m.Subject)
	}
}

// Review 1b #1b: a change replicated with a modification time older than
// the folder's newest (flag set on a phone, cached mode) left count and
// newest time unchanged: missed until the backstop.
func TestOlderStampChangeIsCaughtByTheBackstop(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	in := o.stores[1].defaults[6]
	old := in.items[0]
	old.props[propLastModified] = DateOf(t0.Add(2*time.Hour + 30*time.Minute))
	old.props[propSubject] = "flagged on phone"
	rigClock(c).Advance(rigRecheck)
	run(t, s, c)
	if m, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", old.id()); m.Subject != "flagged on phone" {
		t.Errorf("non-newest change missed: subject=%q", m.Subject)
	}
}

// Review 1b #5: a subject longer than the table's 255-character cap stayed
// cut after the detail pass.
func TestDetailPassRepairsTableCappedFields(t *testing.T) {
	o := profile()
	it := o.stores[1].defaults[6].items[3]
	full := strings.Repeat("é", 300)
	it.props[propSubject] = full
	it.tableCap = 255
	c, s, st := syncRig(t, o)
	run(t, s, c)
	m, _ := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", it.id())
	if m.Partial || m.Subject != full {
		t.Errorf("subject stays at %d chars after the detail pass (partial %v)", len([]rune(m.Subject)), m.Partial)
	}
}

// Review 1b #4: a folder that vanished (hard delete, emptied Deleted
// Items) kept its messages forever.
func TestVanishedFolderMessagesAreRemoved(t *testing.T) {
	o := profile()
	in := o.stores[1].defaults[6]
	proj := in.children[0]
	proj.items = []*fakeItem{newItem(500, t0.Add(5*time.Hour)), newItem(501, t0.Add(6*time.Hour))}
	c, s, st := syncRig(t, o)
	run(t, s, c)
	id := proj.items[0].id()
	in.children = nil // hard-deleted: GetFolderFromID answers MAPI_E_NOT_FOUND
	run(t, s, c)
	rigClock(c).Advance(rigAutoApply) // a whole folder is a held removal: applied once it persisted
	run(t, s, c)
	if _, err := st.GetMessage(t.Context(), "outlook", "S-PRIMARY", id); err == nil {
		t.Error("message of a vanished folder is still stored")
	}
}

// Review 1b #2: a shrunken local cache (sync window "All" → "1 year", OST
// rebuild) made 4 of 5 Inbox messages vanish from a COMPLETE snapshot and
// deleted them, bodies included. Mass removals are now held.
func TestShrunkenCacheRemovalsAreHeld(t *testing.T) {
	o := profile()
	c, s, st := syncRig(t, o)
	run(t, s, c)
	in := o.stores[1].defaults[6]
	in.items = in.items[4:]
	rep := run(t, s, c)
	if rep.Removed.Deleted != 0 || rep.Removed.Held != 4 {
		t.Fatalf("4 of 5 inbox messages must be held, not deleted: %+v", rep.Removed)
	}
	if n, _ := st.CountByRole(t.Context()); n["inbox"] != 7 { // 5 primary (4 held) + 2 delegate
		t.Fatalf("held messages stay stored: %v", n)
	}
}

// Review 1b #3: one unreadable subfolder (no rights on a delegate
// subfolder) dropped the whole store.
func TestOneUnreadableFolderDoesNotDropTheStore(t *testing.T) {
	o := profile()
	o.stores[0].defaults[6].children[0].failProps = 0x80070005 // E_ACCESSDENIED on Desk\Inbox\Projets
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range folders {
		if f.StoreID == "S-DESK" {
			n++
		}
	}
	if n != 2 || c.Stats().Discover.Skipped["unreadable"] != 1 {
		t.Errorf("delegate store: %d folders (want Inbox and Sent), skips %v", n, c.Stats().Discover.Skipped)
	}
}

// Review 1b #8: Outlook answering Nothing where the connector asserted an
// object panicked the COM thread (a non-Exchange profile's CurrentUser has
// no AddressEntry). Nullable members are declared in model.go; any other
// member answering Nothing is ErrNoObject; a panic on the COM thread is
// ErrInternal instead of a crash.
func TestNothingFromOutlookIsAnErrorNotAPanic(t *testing.T) {
	o := profile()
	o.currentUser = "" // CurrentUser.AddressEntry answers Nothing
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil || len(c.Mailboxes()[1].Addresses) != 0 || len(folders) == 0 {
		t.Fatalf("no owner, no panic: %v", err)
	}
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, "primary", "inbox"), "", sk); err != nil {
		t.Fatal(err)
	}
	it := o.stores[1].defaults[6].items[0]
	it.nothing = true
	for _, m := range sk.msgs {
		if m.ID == it.id() {
			_, err := c.ReadDetails(t.Context(), refOf(m))
			if !errors.Is(err, ErrNoObject) {
				t.Fatalf("Nothing for an item: %v", err)
			}
		}
	}
	if err := protect(func() error { var m map[string]int; m["x"]++; return nil }); !errors.Is(err, ErrInternal) {
		t.Fatalf("panic on the COM thread: %v", err)
	}
}

// Review 1b #9 (verified against MS-OXOTASK 2.2.2.2.4–5: "the date is in
// the user's local time zone", time component midnight): task dates are
// calendar dates, not instants. A local-midnight value keeps its date, and
// a value a non-conforming writer stored with a time of day (here 23:30,
// which an instant reading would push into the next day once shifted) keeps
// its date too.
func TestTaskDatesAreDatesNotInstants(t *testing.T) {
	times := mailTimes()
	if got := asDate(DateOf(time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)), times); got != "2026-03-08T00:00:00Z" {
		t.Fatalf("midnight date: %s", got)
	}
	if got := asDate(DateOf(time.Date(2026, 3, 8, 23, 30, 0, 0, time.UTC)), times); got != "2026-03-08T00:00:00Z" {
		t.Fatalf("date with a time of day: %s", got)
	}
	if asDate(Date(949998), times) != "" || asDate(nil, times) != "" {
		t.Fatal("no-date sentinel (4501-01-01) and unset are empty")
	}
	o := profile()
	it := o.stores[1].defaults[6].items[0]
	it.props[propTaskDueDate] = DateOf(time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC))
	c, msgs := syncedInbox(t, o, nil)
	_ = c
	for _, m := range msgs {
		if m.ID == it.id() && m.TaskDueDate != "2026-10-20T00:00:00Z" {
			t.Fatalf("task due date %s", m.TaskDueDate)
		}
	}
}

func mailTimes() mail.Times { return mail.Times{NoDateYearAtMost: 1900, NoDateYearAtLeast: 4500} }
