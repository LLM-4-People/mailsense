package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Each narrow generation follows exactly its own writes. A label edit is
// not a mail change (the companies, agenda and triage views survive it) and
// a mail sync is not a label edit (the hub label totals and recent answers
// survive it). Generation sees both.
func TestNarrowGenerationsFollowTheirOwnWrites(t *testing.T) {
	s, w := seedAnswerWorld(t)
	mailBefore, allBefore, labelBefore := s.MailGeneration(), s.Generation(), s.LabelGeneration()
	if err := s.PutLabel(t.Context(), w.a, "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	if got := s.MailGeneration(); got != mailBefore {
		t.Fatalf("a label write advanced MailGeneration: %d -> %d", mailBefore, got)
	}
	if got := s.LabelGeneration(); got == labelBefore {
		t.Fatalf("a label write did not advance LabelGeneration: %d", got)
	}
	if got := s.Generation(); got == allBefore {
		t.Fatalf("a label write did not advance Generation: %d", got)
	}

	labelAfter := s.LabelGeneration()
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "gen-test", StoreID: "S1"},
		[]string{"owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got != labelAfter {
		t.Fatalf("a mail write advanced LabelGeneration: %d -> %d", labelAfter, got)
	}
	if got := s.MailGeneration(); got == mailBefore {
		t.Fatalf("a mail write did not advance MailGeneration: %d", got)
	}
}

// TestVisibilityChangesAdvanceLabelGeneration protects the label caches: a
// selection, verdict or removal change can move a labeled message out of
// view, so it must rebuild them. An ordinary mail write still must not.
func TestVisibilityChangesAdvanceLabelGeneration(t *testing.T) {
	s, w := seedAnswerWorld(t)
	labelBefore := s.LabelGeneration()
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src", "src2"})); err != nil {
		t.Fatal(err)
	}
	if err := s.applySelection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got == labelBefore {
		t.Fatal("a mailbox selection change did not advance LabelGeneration")
	}
	mailBefore := s.LabelGeneration()
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "gen-test", StoreID: "S1"},
		[]string{"owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got != mailBefore {
		t.Fatalf("an ordinary mail write advanced LabelGeneration: %d -> %d", mailBefore, got)
	}

	labelBefore = s.LabelGeneration()
	if err := s.WriteVerdicts(t.Context(), "gen-test", []VerdictRow{{RowID: w.a, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch}}); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got == labelBefore {
		t.Fatal("a scope verdict change did not advance LabelGeneration")
	}

	folders, err := s.ListFolders(t.Context(), "src")
	if err != nil || len(folders) == 0 {
		t.Fatalf("folders: %v %d", err, len(folders))
	}
	labelBefore = s.LabelGeneration()
	if err := s.SetFolderActive(t.Context(), folders[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got == labelBefore {
		t.Fatal("a folder visibility change did not advance LabelGeneration")
	}

	if _, err := s.RemoveMessages(t.Context(), []int64{w.b}); err != nil {
		t.Fatal(err)
	}
	if got := s.LabelGeneration(); got == labelBefore {
		t.Fatal("a message removal did not advance LabelGeneration")
	}
}

// TestHoldMailWritesCoalescesABatchRun: a producer that writes many mail
// batches invalidates derived views once, at its boundary. The hold is
// idempotent and nested, and it never delays a selection, identity or world
// write, which keeps its immediate generation.
func TestHoldMailWritesCoalescesABatchRun(t *testing.T) {
	s, _ := seedAnswerWorld(t)
	mailBefore, allBefore := s.MailGeneration(), s.Generation()

	outer := s.HoldMailWrites()
	inner := s.HoldMailWrites()
	for range 3 {
		s.touchMail()
	}
	if s.MailGeneration() != mailBefore || s.Generation() != allBefore {
		t.Fatalf("a held mail write advanced a generation: %d/%d", s.MailGeneration(), s.Generation())
	}

	// A non-mail write (selection, identity, sources, world) is never held.
	s.touch()
	if s.MailGeneration() != mailBefore+1 {
		t.Fatalf("a hold delayed an immediate write: %d", s.MailGeneration())
	}

	inner()
	if s.MailGeneration() != mailBefore+1 {
		t.Fatalf("an inner release bumped while the outer hold was open: %d", s.MailGeneration())
	}
	outer()
	if s.MailGeneration() != mailBefore+2 {
		t.Fatalf("the released run did not bump once: %d", s.MailGeneration())
	}
	outer() // the release function is idempotent
	if s.MailGeneration() != mailBefore+2 {
		t.Fatalf("a second release double-bumped: %d", s.MailGeneration())
	}
}
