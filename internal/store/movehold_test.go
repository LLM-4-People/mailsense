package store

import (
	"errors"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// An absence with a Message-ID and no survivor is held for exactly one
// complete run: it may have moved between two folders' snapshots. The next
// run either reconciles the move (labels copied) or removes a real deletion.
func TestUnexplainedAbsenceIsHeldForOneRun(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	moved := mailtest.Message("src", "old")
	moved.FolderPath, moved.FolderRole = "Inbox", mail.RoleInbox
	moved.InternetMessageID = "<move-hold@example.test>"
	gone := mailtest.Message("src", "gone")
	gone.InternetMessageID = "<gone@example.test>"
	// A lookalike in another store is not a survivor: a move is same-store.
	lookalike := mailtest.Message("src", "lookalike")
	lookalike.StoreID = "store-2"
	lookalike.InternetMessageID = moved.InternetMessageID
	if err := s.UpsertMessages(ctx, 0, []mail.Message{moved, gone, lookalike}); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.UpsertFolder(ctx, mail.FolderOf(&moved))
	if err != nil {
		t.Fatal(err)
	}
	oldID, goneID := messageRowID(t, s, "old"), messageRowID(t, s, "gone")
	if err := s.PutLabel(ctx, oldID, "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}

	ids, _, err := s.AbsentMessages(ctx, inbox, nil)
	if err != nil || len(ids) != 2 {
		t.Fatalf("absent %v: %v", ids, err)
	}
	moves, unexplained, anonymous, err := s.ClassifyAbsences(ctx, ids)
	if err != nil || len(moves) != 0 || len(anonymous) != 0 || len(unexplained) != 2 {
		t.Fatalf("classify: moves %v unexplained %v anonymous %v (%v)", moves, unexplained, anonymous, err)
	}

	ripe, err := s.HoldMoveSuspects(ctx, inbox, unexplained, 1)
	if err != nil || len(ripe) != 0 {
		t.Fatalf("run 1 ripe %v: %v", ripe, err)
	}
	// A second hold in the same run keeps the first run: it is not ripe.
	ripe, err = s.HoldMoveSuspects(ctx, inbox, unexplained, 1)
	if err != nil || len(ripe) != 0 {
		t.Fatalf("same-run rehold ripe %v: %v", ripe, err)
	}
	for _, id := range []int64{oldID, goneID} {
		var heldRun int64
		if err := s.db.QueryRow("SELECT held_run FROM move_holds WHERE message_id = ?", id).Scan(&heldRun); err != nil || heldRun != 1 {
			t.Fatalf("hold of %d run %d: %v", id, heldRun, err)
		}
	}

	// The next complete run still finds them absent: they are ripe now.
	if err := s.ReleaseMoveHolds(ctx, inbox, ids); err != nil {
		t.Fatal(err)
	}
	ripe, err = s.HoldMoveSuspects(ctx, inbox, unexplained, 2)
	if err != nil || len(ripe) != 2 || !slices.Contains(ripe, oldID) || !slices.Contains(ripe, goneID) {
		t.Fatalf("run 2 ripe %v: %v", ripe, err)
	}

	// The destination row has appeared: the old row is a move again, and its
	// labels follow it; the other row is a genuine deletion.
	fresh := mailtest.Message("src", "fresh")
	fresh.FolderPath, fresh.FolderRole = "Archive", mail.RoleOther
	fresh.InternetMessageID = moved.InternetMessageID
	if err := s.UpsertMessages(ctx, 0, []mail.Message{fresh}); err != nil {
		t.Fatal(err)
	}
	moves, unexplained, anonymous, err = s.ClassifyAbsences(ctx, []int64{oldID})
	if err != nil || len(moves) != 1 || moves[0] != oldID || len(unexplained) != 0 || len(anonymous) != 0 {
		t.Fatalf("with a destination: moves %v unexplained %v anonymous %v (%v)", moves, unexplained, anonymous, err)
	}
	rm, err := s.RemoveMessages(ctx, ripe)
	if err != nil || rm != (Removal{Moved: 1, Deleted: 1}) {
		t.Fatalf("remove %+v: %v", rm, err)
	}
	for _, id := range []string{"old", "gone"} {
		if _, err := s.GetMessage(ctx, "src", "store-1", id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s must be gone: %v", id, err)
		}
	}
	freshRow := messageRowID(t, s, "fresh")
	labels, err := s.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != freshRow ||
		labels[0].Question != "category" || labels[0].Value != `"work"` {
		t.Fatalf("the label must follow the move: %+v %v", labels, err)
	}
	var holds int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM move_holds").Scan(&holds); err != nil || holds != 0 {
		t.Fatalf("holds must cascade with their rows: %d %v", holds, err)
	}
}

// A same-store survivor already present on the first run classifies the row
// as a move immediately, with no hold.
func TestMoveSurvivorIsClassifiedImmediately(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	old := mailtest.Message("src", "old")
	old.InternetMessageID = "<immediate-move@example.test>"
	fresh := mailtest.Message("src", "fresh")
	fresh.FolderPath, fresh.FolderRole = "Archive", mail.RoleOther
	fresh.InternetMessageID = old.InternetMessageID
	if err := s.UpsertMessages(ctx, 0, []mail.Message{old, fresh}); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.UpsertFolder(ctx, mail.FolderOf(&old))
	if err != nil {
		t.Fatal(err)
	}
	oldID := messageRowID(t, s, "old")
	if err := s.PutLabel(ctx, oldID, "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}

	ids, _, err := s.AbsentMessages(ctx, inbox, nil)
	if err != nil || len(ids) != 1 {
		t.Fatalf("absent %v: %v", ids, err)
	}
	moves, unexplained, anonymous, err := s.ClassifyAbsences(ctx, ids)
	if err != nil || len(moves) != 1 || moves[0] != oldID || len(unexplained) != 0 || len(anonymous) != 0 {
		t.Fatalf("classify: moves %v unexplained %v anonymous %v (%v)", moves, unexplained, anonymous, err)
	}
	rm, err := s.RemoveMessages(ctx, moves)
	if err != nil || rm != (Removal{Moved: 1}) {
		t.Fatalf("remove %+v: %v", rm, err)
	}
	freshRow := messageRowID(t, s, "fresh")
	labels, err := s.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != freshRow {
		t.Fatalf("the label must follow the move at once: %+v %v", labels, err)
	}
}

// A hold describes one absence: a message listed by the next snapshot again
// is released, and only the still-absent messages keep their hold.
func TestMoveHoldReleasedWhenTheMessageReturns(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	returning := mailtest.Message("src", "returning")
	returning.InternetMessageID = "<returns@example.test>"
	stillGone := mailtest.Message("src", "still-gone")
	stillGone.InternetMessageID = "<still-gone@example.test>"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{returning, stillGone}); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.UpsertFolder(ctx, mail.FolderOf(&returning))
	if err != nil {
		t.Fatal(err)
	}
	ids, _, err := s.AbsentMessages(ctx, inbox, nil)
	if err != nil || len(ids) != 2 {
		t.Fatalf("absent %v: %v", ids, err)
	}
	_, unexplained, _, err := s.ClassifyAbsences(ctx, ids)
	if err != nil || len(unexplained) != 2 {
		t.Fatalf("unexplained %v: %v", unexplained, err)
	}
	if _, err := s.HoldMoveSuspects(ctx, inbox, unexplained, 1); err != nil {
		t.Fatal(err)
	}

	// The message came back: the current absent set no longer lists it.
	stillGoneID := messageRowID(t, s, "still-gone")
	if err := s.ReleaseMoveHolds(ctx, inbox, []int64{stillGoneID}); err != nil {
		t.Fatal(err)
	}
	held := heldMoveIDs(t, s, inbox)
	if len(held) != 1 || held[0] != stillGoneID {
		t.Fatalf("only the still-absent message stays held: %v", held)
	}
	// It came back too: an empty absent set releases every hold.
	if err := s.ReleaseMoveHolds(ctx, inbox, nil); err != nil {
		t.Fatal(err)
	}
	if held := heldMoveIDs(t, s, inbox); len(held) != 0 {
		t.Fatalf("every returned message must be released: %v", held)
	}
}

func heldMoveIDs(t *testing.T, s *Store, folderID int64) []int64 {
	t.Helper()
	rows, err := s.db.Query("SELECT message_id FROM move_holds WHERE folder_id = ? ORDER BY message_id", folderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}
