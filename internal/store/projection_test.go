package store

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// TestEachMessageProjectionReadsOnlyItsFields is the store-side contract of
// MessageFilter.Fields: the selected columns and the recipients subquery are
// read, every other mail.Message field stays zero, and itemsSQL does not run
// unless attachments/folders are asked for.
func TestEachMessageProjectionReadsOnlyItsFields(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "projected")
	m.BodyHTML, m.BodyHTMLKnown = "<p>sentinel</p>", true
	m.Attachments = []mail.Attachment{{Name: "sentinel.bin", ContentType: "application/octet-stream", Size: 3}}
	m.Folders = []mail.FolderRef{{Path: "Sentinel/2026", Role: mail.RoleArchive}}
	m.Alterations = []mail.Alteration{{Code: mail.AltHeaderBytes, Detail: "windows-1252"}}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE msg_id = ?", m.ID).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	read := func(f MessageFilter) MessageRow {
		t.Helper()
		var got MessageRow
		if err := s.EachMessage(ctx, f, func(r *MessageRow) error { got = *r; return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}

	full := read(MessageFilter{RowIDs: []int64{rowID}})
	if full.Message.Body == "" || len(full.Message.Recipients) != 2 || len(full.Message.Attachments) != 1 ||
		len(full.Message.Folders) != 1 || len(full.Message.Alterations) != 1 || full.Message.BodyHTML != "" {
		t.Fatalf("full read: %+v", full.Message)
	}

	fields := MessageFieldsOf("subject", "sender_address", "references", "recipients", "alterations")
	projected := read(MessageFilter{RowIDs: []int64{rowID}, Fields: fields})
	if projected.Message.Subject != m.Subject || projected.Message.SenderAddress != m.SenderAddress ||
		projected.Message.References != m.References || len(projected.Message.Recipients) != 2 ||
		len(projected.Message.Alterations) != 1 {
		t.Fatalf("selected fields missing: %+v", projected.Message)
	}
	if projected.Message.Body != "" || projected.Message.EntryID != "" || projected.Message.StoreName == "" {
		t.Fatalf("an omitted or identity field is wrong: %+v", projected.Message)
	}
	if projected.Message.Attachments != nil || projected.Message.Folders != nil {
		t.Fatalf("itemsSQL ran for a projection that does not read it: %+v", projected.Message)
	}
	if projected.Message.BodyHTML != "" {
		t.Fatal("body HTML was read without WithBodyHTML")
	}

	rich := read(MessageFilter{RowIDs: []int64{rowID}, Fields: fields, WithBodyHTML: true})
	if rich.Message.BodyHTML != m.BodyHTML || len(rich.Message.Attachments) != 0 || rich.Message.Body != "" {
		t.Fatalf("WithBodyHTML must add only the HTML columns: %+v", rich.Message)
	}

	noRecipients := read(MessageFilter{RowIDs: []int64{rowID}, Fields: MessageFieldsOf("subject")})
	if noRecipients.Message.Recipients != nil || noRecipients.Message.Body != "" {
		t.Fatalf("recipients read without being selected: %+v", noRecipients.Message)
	}

	items := read(MessageFilter{RowIDs: []int64{rowID}, Fields: MessageFieldsOf("attachments")})
	if len(items.Message.Attachments) != 1 || len(items.Message.Folders) != 1 || items.Message.Subject != "" {
		t.Fatalf("attachments/folders are the one item subquery: %+v", items.Message)
	}
}

func TestMessageFieldsResolveThroughTheOneColumnMap(t *testing.T) {
	if got := MessageFieldsOf("references").Select(); got != "m.message_references" {
		t.Fatalf("references projection %q", got)
	}
	all := MessageFields{}.Select()
	if !strings.Contains(all, "m.body") || !strings.Contains(all, "FROM recipients") || !strings.Contains(all, "FROM attachments") {
		t.Fatalf("the zero projection is not the full read")
	}
	if strings.Contains(all, "body_html") {
		t.Fatal("body HTML is not part of the message mapping")
	}
}

// The empty projection is the identity-only read: no mapped message column,
// no recipients and no items. The triage page asks for exactly this, so an
// added column must never leak into a read that did not select it.
func TestMessageFieldsOfNoNamesIsTheIdentityProjection(t *testing.T) {
	if got := MessageFieldsOf().Select(); got != "" {
		t.Fatalf("identity projection selects %q, want no mapped column", got)
	}
}

func TestMessageFieldsRejectUnknownNames(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("an unknown field must panic when the projection is built")
		}
	}()
	_ = MessageFieldsOf("subject", "no_such_field")
}
