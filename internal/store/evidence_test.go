package store

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// L0 evidence: EachMessage streams a message with its attachments and
// further folders (it streamed them empty: the evidence engine counted no
// attachment of any message).
func TestEachMessageCarriesAttachmentsAndFolders(t *testing.T) {
	s, _ := open(t, nil)
	m := mailtest.Message("src", "g1")
	m.Attachments = []mail.Attachment{{Name: "a.pdf", ContentType: "application/pdf", Size: 12}, {Name: "l.png", ContentType: "image/png", Size: -1, Inline: true}}
	m.Folders = []mail.FolderRef{{Path: "Clients", Role: mail.RoleArchive}}
	if err := s.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var got []MessageRow
	if err := s.EachMessage(t.Context(), MessageFilter{}, func(r *MessageRow) error { got = append(got, *r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Message.Attachments, m.Attachments) || len(got[0].Message.Folders) != 1 {
		t.Fatalf("%+v", got)
	}
	ids, err := s.MessageIDs(t.Context(), MessageFilter{})
	if err != nil || len(ids) != 1 || ids[0] != got[0].RowID {
		t.Fatalf("ids %v %v", ids, err)
	}
}

func TestEvidenceRowsSchemasRunsAndInputs(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if _, err := s.EvidenceSchemaID(ctx, ""); err == nil {
		t.Error("an empty schema must be refused")
	}
	a, _ := s.EvidenceSchemaID(ctx, `[["x","v1"]]`)
	b, _ := s.EvidenceSchemaID(ctx, `[["x","v1"]]`)
	if a == 0 || a != b {
		t.Fatalf("one id per schema: %d %d", a, b)
	}
	if id, _ := s.LookupEvidenceSchema(ctx, `[["y","v1"]]`); id != 0 {
		t.Error("an unknown schema has no id")
	}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "m1"), mailtest.Message("src", "m2")}); err != nil {
		t.Fatal(err)
	}
	ids, _ := s.MessageIDs(ctx, MessageFilter{})
	in1, _ := s.EvidenceInputs(ctx)
	rev := map[int64]int64{}
	_ = s.EachMessage(ctx, MessageFilter{}, func(r *MessageRow) error { rev[r.RowID] = r.Revision; return nil })
	if err := s.SaveEvidence(ctx, []EvidenceRow{{MessageID: ids[0], Revision: rev[ids[0]], SchemaID: a, Values: "[true]"},
		{MessageID: 999, Revision: 1, SchemaID: a, Values: "[true]"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.EvidenceOf(ctx, []int64{ids[0], ids[1], 999})
	if len(got) != 1 || got[ids[0]].Values != "[true]" {
		t.Fatalf("a removed message is skipped: %+v", got)
	}
	if c, _ := s.EvidenceProgress(ctx, a); c.Done != 1 || c.Total != 2 || c.Pending != 0 {
		t.Fatalf("progress %+v", c)
	}
	if in2, _ := s.EvidenceInputs(ctx); in2 != in1 {
		t.Error("evidence writes are no input of evidence")
	}
	m := mailtest.Message("src", "m2")
	m.Body = "changed"
	_ = s.UpsertMessages(ctx, 0, []mail.Message{m})
	if in3, _ := s.EvidenceInputs(ctx); in3 == in1 {
		t.Error("a message write moves the inputs")
	}
	run, _ := s.BeginEvidenceRun(ctx, in1)
	if err := s.FinishEvidenceRun(ctx, run, runstate.Succeeded, "{}", ""); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.ListEvidenceRuns(ctx, 5); len(runs) != 1 || runs[0].Inputs != in1 || runs[0].State != runstate.Succeeded {
		t.Fatalf("runs %+v", runs)
	}
	n := 0
	_ = s.EachEvidence(ctx, func(EvidenceRow) error { n++; return nil })
	if n != 1 {
		t.Errorf("each evidence: %d", n)
	}
}
