package syncer

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The absence candidates of a complete snapshot are computed at the snapshot
// boundary, while the presence set is in hand; only their row ids and the
// stored count survive the call.
func TestSnapshotComputesAbsentCandidatesAtTheBoundary(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	var folderID int64
	rows := map[string]int64{}
	for _, id := range []string{"b0", "b1", "b2"} {
		folderID, rows[id] = seedStoredMessage(t, st, mailtest.Message("fake", id))
	}
	sink := &batchSink{s: s, clock: s.clock, folderID: folderID, folder: "Inbox"}
	items := []source.Version{{ID: "b0"}, {ID: "b1"}}
	if _, err := sink.Snapshot(ctx, items, true); err != nil {
		t.Fatal(err)
	}
	if !sink.complete || sink.snapshotSize != 2 || sink.stored != 3 {
		t.Fatalf("boundary state complete=%v snapshot=%d stored=%d, want complete, 2, 3",
			sink.complete, sink.snapshotSize, sink.stored)
	}
	if len(sink.absent) != 1 || sink.absent[0] != rows["b2"] {
		t.Fatalf("absent candidates %v, want the stored row of b2 (%d)", sink.absent, rows["b2"])
	}

	// An incomplete snapshot proves nothing absent: it retains no candidates,
	// so the removal pass keeps every row.
	if _, err := sink.Snapshot(ctx, items[:1], false); err != nil {
		t.Fatal(err)
	}
	if sink.complete || sink.absent != nil || sink.stored != 0 {
		t.Fatalf("incomplete boundary retained state complete=%v absent=%v stored=%d",
			sink.complete, sink.absent, sink.stored)
	}
	if counts, err := st.CountByRole(t.Context()); err != nil || counts[mail.RoleInbox] != 3 {
		t.Fatalf("the incomplete snapshot must leave all rows stored: %v %v", counts, err)
	}
}

// laterMoveConn reproduces a move into a folder synced later in the run: the
// source folder A is snapshotted after the message left it and the destination
// B before the move became visible there, so both complete snapshots omit it
// and no destination row exists at the removal pass. The next run's B snapshot
// lists the moved message.
type laterMoveConn struct {
	fakeConn
	run   int // 1: neither folder lists it; 2: B lists the moved message
	fresh mail.Message
	calls []string
}

func (c *laterMoveConn) Sync(ctx context.Context, f mail.Folder, _ source.Cursor, sink source.Sink) (source.Cursor, error) {
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

// A message absent from the source folder's complete snapshot but moving into
// a folder synced later in the same run is still protected by the one-run move
// hold; the next run reconciles it as a move with its labels.
func TestAbsentCandidateOfALaterMoveIsHeldOneRun(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	old := mailtest.Message("fake", "old")
	old.FolderPath, old.FolderRole = "A", mail.RoleInbox
	old.InternetMessageID = "<moved-to-a-later-folder@example.test>"
	_, oldRow := seedStoredMessage(t, st, old)
	if err := st.PutLabel(ctx, oldRow, "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}

	fresh := mailtest.Message("fake", "fresh")
	fresh.FolderPath, fresh.FolderRole = "B", mail.RoleInbox
	fresh.InternetMessageID = old.InternetMessageID
	fresh.Stamp = "2026-10-13T00:00:00Z"

	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "A", Role: mail.RoleInbox, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "B", Role: mail.RoleInbox, ItemCount: -1},
	}
	c := &laterMoveConn{fakeConn: fakeConn{id: "fake", folders: folders}, run: 1, fresh: fresh}
	rep, err := s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.calls, ","); got != "A,B" {
		t.Fatalf("run 1 folder order %q, want A,B (the destination later)", got)
	}
	if rep.Removed != (store.Removal{Held: 1}) || rep.Folders[0].Removed.Held != 1 {
		t.Fatalf("run 1 removals %+v, folder A %+v; want one held", rep.Removed, rep.Folders[0].Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "old"); err != nil {
		t.Fatalf("run 1 must keep the message: %v", err)
	}

	c.run = 2
	rep, err = s.Run(ctx, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != (store.Removal{Moved: 1}) {
		t.Fatalf("run 2 removals %+v, want one move", rep.Removed)
	}
	if _, err := st.GetMessage(ctx, "fake", "store-1", "old"); err == nil {
		t.Fatal("the moved message's old row must be removed")
	}
	moved, err := st.GetMessage(ctx, "fake", "store-1", "fresh")
	if err != nil {
		t.Fatalf("the destination row: %v", err)
	}
	freshRow := int64(0)
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == moved.ID {
			freshRow = r.RowID
		}
		return nil
	}); err != nil || freshRow == 0 {
		t.Fatalf("destination row %d: %v", freshRow, err)
	}
	labels, err := st.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != freshRow {
		t.Fatalf("the label must follow the move: %+v %v", labels, err)
	}
}

// An incomplete snapshot retains every stored message row: it is not a
// smaller absence set, it is no absence proof at all.
func TestIncompleteSnapshotRetainsEveryStoredRow(t *testing.T) {
	s, st, _, _ := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "k")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(ctx)
	_, cursor := storeFolder(t, st, folders[0])

	c.version = 1
	c.items["Inbox"] = []item{inbox("k0")}
	c.incomplete = map[string]bool{"Inbox": true}
	rep, err := s.Run(ctx, c, nil)
	if err != nil || rep.Removed != (store.Removal{}) {
		t.Fatalf("the incomplete snapshot removes nothing: %v %+v", err, rep)
	}
	for _, id := range []string{"k0", "k1", "k2"} {
		if _, err := st.GetMessage(ctx, "fake", "store-1", id); err != nil {
			t.Errorf("%s must stay stored: %v", id, err)
		}
	}
	if held, err := st.HeldRemovals(ctx, "fake", nil); err != nil || len(held) != 0 {
		t.Fatalf("an incomplete snapshot creates no absence state: %+v %v", held, err)
	}
	if _, got := storeFolder(t, st, folders[0]); got != cursor {
		t.Fatalf("an incomplete snapshot must not advance the cursor: %q, want %q", got, cursor)
	}
}

// The heap shape of a run: what survives from one folder to the removal pass
// is the absent row ids and the stored count. A map field here would keep
// every folder's presence set (and the connector's id strings) alive for the
// whole run, which is exactly the retention the boundary computation removes.
func TestPerFolderRetentionHasNoPresenceMap(t *testing.T) {
	for _, v := range []any{folderSync{}, removal{}} {
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if strings.HasPrefix(f.Type.String(), "map[") {
				t.Fatalf("%s.%s retains a map (%s); per-folder state must keep only the absent ids and the stored count",
					typ, f.Name, f.Type)
			}
		}
	}
}
