package main

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestCompaniesServiceToleratesAPrioritylessCommitmentsSource: the
// commitments service is an optional world dependency. An app wired without
// the priority service has no commitment set, so a company profile must still
// build and omit open loops instead of failing on the absent overlay.
func TestCompaniesServiceToleratesAPrioritylessCommitmentsSource(t *testing.T) {
	st, cfg, _ := firstRunWorld(t, nil)
	if err := cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	ref := store.StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := st.SaveReported(t.Context(), ref, []string{"alice@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Martin", Company: "Acme", Emails: []string{"alice@acme.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	msg := mailtest.Message("src", "acme-1")
	msg.SenderName, msg.SenderAddress = "Alice Martin", "alice@acme.test"
	msg.ReceivedAt, msg.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
	msg.Subject, msg.Body = "Acme renewal", "A substantive message about the account and the next steps."
	msg.ConversationID = "C-acme"
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{msg}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(row *store.MessageRow) error {
		rowID = row.RowID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{
		{RowID: rowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch},
	}); err != nil {
		t.Fatal(err)
	}

	w := &wire{app: &app{st: st, cfg: cfg}}
	if _, err := w.commitmentsService(); err == nil {
		t.Fatal("a priority-less app built a commitments service")
	}
	svc, err := w.companiesService()
	if err != nil {
		t.Fatalf("the world build failed on the absent optional commitments source: %v", err)
	}
	view, err := svc.Profile(t.Context(), "c:acme", 12, 30)
	if err != nil {
		t.Fatalf("the profile failed on the absent optional commitments source: %v", err)
	}
	if view.Label != "Acme" || view.OpenLoops != nil {
		t.Fatalf("profile %+v", view)
	}
}
