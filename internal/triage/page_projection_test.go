package triage

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestPageReadProjectsIdentityOnly is the page's row-read contract: the page
// window asks the store for the identity projection only, so a page build
// loads no subject, sender, body, headers, recipients or items, while the
// full read of the same row still loads the whole message. Both reads go
// through the one triage row-read owner, so handing the page the full
// projection again fails here.
func TestPageReadProjectsIdentityOnly(t *testing.T) {
	w := newWorld(t, nil)
	m := w.message("in-projected", mail.RoleInbox, "Inbox", "a@client.com", "A", "Budget", base, me)
	m.Body = "the page must never load this body"
	m.Recipients = append(m.Recipients, mail.Recipient{Kind: mail.Cc, Address: "cc@client.com"})
	m.Attachments = []mail.Attachment{{Name: "budget.pdf", ContentType: "application/pdf", Size: 3}}
	m.Folders = []mail.FolderRef{{Path: "Inbox/2026", Role: mail.RoleArchive}}
	w.write(m)
	id := w.rowOf(m.ID)
	w.useRoute(t, w.st)

	page, err := w.tr.prepareView(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got store.MessageRow
	if err := page.eachEligible(t.Context(), w.st, []int64{id}, func(r *store.MessageRow) error {
		got = *r
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if got.RowID != id || got.Revision != w.revision(t, id) {
		t.Fatalf("page row identity and revision: %+v", got)
	}
	if got.Verdict != scope.Owner || got.Reason == "" {
		t.Fatalf("page row lost the always-read scope verdict: %+v", got)
	}
	// Exactly the identity EachMessage always reads: any mapped column or
	// subquery in the projection would leave content here.
	want := mail.Message{SourceID: m.SourceID, StoreID: m.StoreID, ID: m.ID, StoreName: m.StoreName,
		FolderPath: m.FolderPath, FolderRole: m.FolderRole}
	if !reflect.DeepEqual(got.Message, want) {
		t.Fatalf("page read loaded message content: %+v", got.Message)
	}

	// The full read of the same row keeps its full projection: message
	// content is what the detail result reads, so it must not be projected.
	full, err := w.tr.core(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var complete store.MessageRow
	if err := full.eachEligible(t.Context(), w.st, nil, []int64{id}, func(r *store.MessageRow) error {
		complete = *r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if complete.Message.Subject != m.Subject || complete.Message.SenderAddress != m.SenderAddress ||
		complete.Message.Body != m.Body || len(complete.Message.Recipients) != len(m.Recipients) ||
		len(complete.Message.Attachments) != 1 || len(complete.Message.Folders) != 1 {
		t.Fatalf("the full read lost its content projection: %+v", complete.Message)
	}
	if complete.RowID != got.RowID || complete.Revision != got.Revision {
		t.Fatalf("the reads disagree on identity and revision: page %+v, full %+v", got, complete)
	}
}
