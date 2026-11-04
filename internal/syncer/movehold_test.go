package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// seedStoredMessage writes one message and its folder directly, as a prior
// complete run would have left them, and returns their row ids.
func seedStoredMessage(t *testing.T, st *store.Store, m mail.Message) (folderID, rowID int64) {
	t.Helper()
	ctx := t.Context()
	if err := st.UpsertSource(ctx, m.SourceID, "fake", m.SourceID, source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	folderID, err := st.UpsertFolder(ctx, mail.FolderOf(&m))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == m.ID {
			rowID = r.RowID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rowID == 0 {
		t.Fatalf("no stored row for %s", m.ID)
	}
	return folderID, rowID
}

// moveConn reproduces a move that happens between two complete snapshots of
// one store. Discover returns B then A, so B is snapshotted while the message
// is still in A, the message then moves to B, and A is snapshotted after the
// move: both snapshots omit it and no destination row exists yet. The second
// run's B snapshot includes the moved message.
type moveConn struct {
	fakeConn
	run   int // 1: the move falls between B and A; 2: B lists the moved message
	fresh mail.Message
	calls []string
}

func (c *moveConn) Sync(ctx context.Context, f mail.Folder, _ source.Cursor, sink source.Sink) (source.Cursor, error) {
	c.calls = append(c.calls, f.Path)
	var msgs []mail.Message
	if f.Path == "B" && c.run >= 2 {
		msgs = append(msgs, c.fresh)
	}
	if err := deliverSnapshot(ctx, sink, msgs); err != nil {
		return "", err
	}
	return source.Cursor(fmt.Sprintf("v%d", c.run)), nil
}

func deliverSnapshot(ctx context.Context, sink source.Sink, msgs []mail.Message) error {
	versions := make([]source.Version, 0, len(msgs))
	for _, m := range msgs {
		versions = append(versions, source.Version{ID: m.ID, Stamp: m.Stamp})
	}
	want, err := sink.Snapshot(ctx, versions, true)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if want[m.ID] {
			if err := sink.Message(ctx, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// A folder snapshot order of B before the move and A after it leaves the
// message in neither complete snapshot. It must be held for one run, with its
// labels; the next run's destination row turns it into a move, labels copied.
func TestMoveBetweenOutOfOrderSnapshotsIsHeldThenReconciled(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	old := mailtest.Message("fake", "old")
	old.FolderPath, old.FolderRole = "A", mail.RoleInbox
	old.InternetMessageID = "<between-two-folders@example.test>"
	folderA, oldRow := seedStoredMessage(t, st, old)
	if err := st.PutLabel(ctx, oldRow, "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}

	fresh := mailtest.Message("fake", "fresh")
	fresh.FolderPath, fresh.FolderRole = "B", mail.RoleInbox
	fresh.InternetMessageID = old.InternetMessageID
	fresh.Stamp = "2026-10-13T00:00:00Z"

	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "B", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "A", Role: mail.RoleInbox, ItemCount: -1},
	}
	c := &moveConn{fakeConn: fakeConn{id: "fake", folders: folders}, run: 1, fresh: fresh}
	rep, err := s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.calls, ","); got != "B,A" {
		t.Fatalf("run 1 snapshot order %q, want B,A (the destination before the move)", got)
	}
	// The row and the owner's label must survive the run that saw neither side.
	labels, labelErr := st.ListLabels(ctx)
	_, rowErr := st.GetMessage(ctx, "fake", "store-1", "old")
	if rowErr != nil || labelErr != nil || len(labels) != 1 || labels[0].MessageID != oldRow {
		t.Fatalf("run 1 must keep the row and its label: row err %v, labels %+v (%v)", rowErr, labels, labelErr)
	}
	if rep.Removed != (store.Removal{Held: 1}) || rep.Folders[1].Removed.Held != 1 {
		t.Fatalf("run 1 removals %+v, folder A %+v; want one held", rep.Removed, rep.Folders[1].Removed)
	}
	if cursor, _, err := st.Cursor(ctx, folderA); err != nil || cursor != "v1" {
		t.Fatalf("a held removal must still commit the folder cursor: %q %v", cursor, err)
	}

	c.run = 2
	rep, err = s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Moved: 1}) {
		t.Fatalf("run 2 removals %+v, want one move", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old row must be reconciled as a move: %v", err)
	}
	moved, err := st.GetMessage(ctx, "fake", "store-1", "fresh")
	if err != nil {
		t.Fatalf("the destination row: %v", err)
	}
	freshRow := int64(0)
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == "fresh" {
			freshRow = r.RowID
		}
		return nil
	}); err != nil || freshRow == 0 {
		t.Fatalf("destination row %d: %v", freshRow, err)
	}
	labels, err = st.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != freshRow ||
		labels[0].Question != "category" || labels[0].Value != `"work"` {
		t.Fatalf("the manual label must follow the move: %+v %v (moved %+v)", labels, err, moved)
	}
	if cursor, _, err := st.Cursor(ctx, folderA); err != nil || cursor != "v2" {
		t.Fatalf("run 2 folder cursor %q %v", cursor, err)
	}
}

// A genuine deletion with a nonempty Message-ID is held once, then removed:
// the one-run move hold must not make a deleted message immortal.
func TestGenuineDeletionIsRemovedOnTheNextRun(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	m := mailtest.Message("fake", "gone")
	m.InternetMessageID = "<genuinely-gone@example.test>"
	seedStoredMessage(t, st, m)
	c := &fakeConn{id: "fake"}

	rep, err := s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Held: 1}) {
		t.Fatalf("first run removals %+v, want one held", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "gone"); err != nil {
		t.Fatalf("the first run must keep the row: %v", err)
	}

	c.version = 1
	rep, err = s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Deleted: 1}) {
		t.Fatalf("second run removals %+v, want one deleted", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the genuinely deleted message must be removed: %v", err)
	}
}

// A message without a Message-ID has nothing to reconcile with a move: it is
// deleted on the run that sees it absent, as before.
func TestAnonymousAbsenceIsRemovedImmediately(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	m := mailtest.Message("fake", "anon")
	m.InternetMessageID = ""
	seedStoredMessage(t, st, m)

	rep, err := s.Run(ctx, &fakeConn{id: "fake"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Deleted: 1}) {
		t.Fatalf("removals %+v, want one deleted", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "anon"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the anonymous message must be deleted at once: %v", err)
	}
}

// A mass hold must not outlive the messages it described. When the only
// absences left are move suspects, the valve is out of the picture: its old
// entries are released, so confirming removals can never delete messages that
// came back.
func TestMassHoldReleasedWhenOnlyMoveSuspectsRemain(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{"sync.removal.hold_count": 2})
	ctx := t.Context()
	keep := []mail.Message{mailtest.Message("fake", "keep0"), mailtest.Message("fake", "keep1")}
	for i := range keep {
		keep[i].InternetMessageID = ""
	}
	if err := st.UpsertSource(ctx, "fake", "fake", "fake", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertMessages(ctx, 0, keep); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertFolder(ctx, mail.FolderOf(&keep[0])); err != nil {
		t.Fatal(err)
	}
	c := &fakeConn{id: "fake"}
	rep, err := s.Run(ctx, c, nil)
	if err != nil || rep.Removed != (store.Removal{Held: 2}) {
		t.Fatalf("the mass removal must be valve-held: %v %+v", err, rep.Removed)
	}

	suspect := mailtest.Message("fake", "suspect")
	suspect.InternetMessageID = "<suspect@example.test>"
	if err := st.UpsertMessages(ctx, 0, []mail.Message{suspect}); err != nil {
		t.Fatal(err)
	}
	c.version = 1
	c.items = map[string][]item{"Inbox": {{msg: keep[0]}, {msg: keep[1]}}}
	rep, err = s.Run(ctx, c, nil)
	if err != nil || rep.Removed != (store.Removal{Held: 1}) {
		t.Fatalf("the move suspect is held one run: %v %+v", err, rep.Removed)
	}
	if held, err := st.HeldRemovals(ctx, "fake", nil); err != nil || len(held) != 0 {
		t.Fatalf("the returned messages must leave the mass valve: %+v %v", held, err)
	}
}

// The move hold is a second, separate policy: a mass removal of anonymous
// messages still goes through the existing safety valve.
func TestMassRemovalStillUsesTheSafetyValve(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{"sync.removal.hold_count": 2})
	ctx := t.Context()
	for i := range 3 {
		m := mailtest.Message("fake", fmt.Sprintf("m%d", i))
		m.InternetMessageID = ""
		seedStoredMessage(t, st, m)
	}

	rep, err := s.Run(ctx, &fakeConn{id: "fake"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Held: 3}) {
		t.Fatalf("removals %+v, want the safety valve to hold all three", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "m0"); err != nil {
		t.Fatalf("a held message must stay stored: %v", err)
	}
	held, err := st.HeldRemovals(ctx, "fake", nil)
	if err != nil || len(held) != 1 || held[0].Held != 3 || held[0].Stored != 3 {
		t.Fatalf("held removals %+v %v", held, err)
	}
}
