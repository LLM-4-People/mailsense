package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// The viewer contract carries what the message screen renders, not the
// storage model: raw headers, the recipient list and connector metadata are
// the biggest fields of the largest response and no screen reads them.
func TestWireMessageDropsStorageOnlyFields(t *testing.T) {
	m := &mail.Message{
		SourceID: "src", StoreID: "store", StoreName: "Mailbox",
		FolderPath: "Inbox", FolderRole: mail.RoleInbox, ID: "id-1",
		EntryID: "entry", LongTermEntryID: "long", SearchKey: "search",
		Subject: "Subject", SenderName: "Sender", SenderAddress: "sender@example.test",
		SentRepresentingAddress: "boss@example.test",
		Recipients:              []mail.Recipient{{Kind: mail.To, Name: "To", Address: "to@example.test"}},
		Headers:                 "Authentication-Results: mx; spf=pass\nX-Large: " + strings.Repeat("h", 2048),
		Body:                    "plain",
	}
	m.Attachments = []mail.Attachment{{Name: "a.png", ContentType: "image/png", Size: 12, Inline: true}}
	wire := WireMessage(m)
	if wire == nil {
		t.Fatal("no projection")
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"headers", "recipients", "entry_id", "search_key", "importance", "alterations"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Errorf("the viewer contract still carries %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{"subject", "sender_name", "sender_address", "folder_path", "folder_role",
		"received_at", "sent_on", "body", "body_truncated", "attachments", "conversation_id", "partial"} {
		if !strings.Contains(string(raw), `"`+required+`"`) {
			t.Errorf("the viewer contract lost %q", required)
		}
	}
	if len(wire.Attachments) != 1 || wire.Attachments[0].Name != "a.png" {
		t.Fatalf("attachments %+v", wire.Attachments)
	}
	if wire.Body != "plain" || wire.Subject != "Subject" || wire.SenderAddress != "sender@example.test" {
		t.Fatalf("fields %+v", wire)
	}
}

// A message without attachments still serializes an array, so every list in
// the contract keeps its type.
func TestWireMessageKeepsEmptyAttachmentsAnArray(t *testing.T) {
	raw, err := json.Marshal(WireMessage(&mail.Message{ID: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"attachments":[]`) {
		t.Fatalf("attachments are not an empty array: %s", raw)
	}
	if WireMessage(nil) != nil {
		t.Fatal("a nil message projected to a value")
	}
}
