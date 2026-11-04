package store

import (
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// TestRemoveMessagesBatchesAFolderSizedSet: a confirmation larger than one
// batch must complete across several transactions, keep move classification
// and label copying exact at the batch boundaries, and prune the FTS rows.
func TestRemoveMessagesBatchesAFolderSizedSet(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	total := removalBatchSize*2 + 1

	msgs := make([]mail.Message, 0, total+1)
	for i := 0; i < total; i++ {
		msgs = append(msgs, mailtest.Message("src", "bulk-"+strconv.Itoa(i)))
	}
	// The last removed row is a move whose destination already exists in
	// another folder: its label must follow it even though it sits in the
	// final batch.
	msgs[total-1].InternetMessageID = "<bulk-move@example.test>"
	moved := msgs[total-1]
	fresh := mailtest.Message("src", "bulk-destination")
	fresh.FolderPath, fresh.FolderRole = "Archive", mail.RoleOther
	fresh.InternetMessageID = moved.InternetMessageID
	msgs = append(msgs, fresh)
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.UpsertFolder(ctx, mail.FolderOf(&msgs[0]))
	if err != nil {
		t.Fatal(err)
	}
	movedID := messageRowID(t, s, moved.ID)
	if err := s.PutLabel(ctx, movedID, "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	ids, stored, err := s.AbsentMessages(ctx, inbox, nil)
	if err != nil || len(ids) != total || stored != total {
		t.Fatalf("absent %d of %d: %v", len(ids), stored, err)
	}

	rm, err := s.RemoveMessages(ctx, ids)
	if err != nil {
		t.Fatalf("batch removal: %v", err)
	}
	if want := (Removal{Deleted: total - 1, Moved: 1}); rm != want {
		t.Fatalf("removal = %+v, want %+v", rm, want)
	}
	if left, stored, err := s.AbsentMessages(ctx, inbox, nil); err != nil || len(left) != 0 || stored != 0 {
		t.Fatalf("inbox after removal: %d left of %d (%v)", len(left), stored, err)
	}
	freshID := messageRowID(t, s, fresh.ID)
	labels, err := s.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != freshID {
		t.Fatalf("the label must follow the move across batches: %+v %v", labels, err)
	}
	if res, err := s.Search(ctx, "Réunion budget bulk-1", 5); err != nil || res.Total != 0 {
		t.Fatalf("removed messages must leave the FTS index: %d %v", res.Total, err)
	}
}
