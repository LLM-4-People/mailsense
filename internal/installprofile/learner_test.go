package installprofile

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

const owner = "amelie@example.com"

type world struct {
	t   *testing.T
	cfg *config.Config
	st  *store.Store
	n   int
}

func newWorld(t *testing.T) *world {
	cfg := configtest.New(t, map[string]any{"time.zone": "Europe/Paris", "mail.domains.learn.min_senders": 3, "mail.subject.learn.min_count": 3,
		source.SettingSelected: []any{source.StoreKey("src", "s")}})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "src", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveReported(t.Context(), store.StoreRef{SourceID: "src", StoreID: "s"}, []string{owner}); err != nil {
		t.Fatal(err)
	}
	return &world{t: t, cfg: cfg, st: st}
}

// thread writes one conversation: the first message from `from` to the
// owner (cc others), then a reply from the owner with subject prefix.
func (w *world) thread(from string, cc []string, subject, replyPrefix string) {
	w.t.Helper()
	w.n++
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(w.n) * time.Hour)
	id := fmt.Sprintf("<m%d@x>", w.n)
	in := mail.Message{SourceID: "src", StoreID: "s", FolderPath: "Inbox", FolderRole: mail.RoleInbox, ID: fmt.Sprintf("in%d", w.n),
		Subject: subject, SenderAddress: from, InternetMessageID: id, ReceivedAt: at.Format(mail.TimeLayout),
		Recipients: []mail.Recipient{{Kind: mail.To, Address: owner}}}
	for _, c := range cc {
		in.Recipients = append(in.Recipients, mail.Recipient{Kind: mail.Cc, Address: c})
	}
	out := mail.Message{SourceID: "src", StoreID: "s", FolderPath: "Sent", FolderRole: mail.RoleSent, ID: fmt.Sprintf("out%d", w.n),
		Subject: replyPrefix + subject, SenderAddress: owner, InReplyTo: id, InternetMessageID: fmt.Sprintf("<r%d@x>", w.n),
		SentOn: at.Add(time.Hour).Format(mail.TimeLayout), ReceivedAt: at.Add(time.Hour).Format(mail.TimeLayout),
		Recipients: []mail.Recipient{{Kind: mail.To, Address: from}}}
	if err := w.st.UpsertMessages(w.t.Context(), 0, []mail.Message{in, out}); err != nil {
		w.t.Fatal(err)
	}
}

// judge marks everything owner scope (outgoing = sent by the owner).
func (w *world) judge() {
	var rows []store.VerdictRow
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		reason := scope.ReasonRecipientMatch
		if r.Message.SenderAddress == owner {
			reason = scope.ReasonSentByOwner
		}
		rows = append(rows, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: reason})
		return nil
	})
	if err := w.st.WriteVerdicts(w.t.Context(), "t", rows); err != nil {
		w.t.Fatal(err)
	}
}

// #3 + #7: free-mail strangers make a shared domain, colleagues who copy
// each other do not; the word before the colon of replies becomes a
// learned prefix, a tag that also starts new mail does not.
func TestLearnerLearnsSharedDomainsAndPrefixes(t *testing.T) {
	w := newWorld(t)
	for i := range 4 { // four strangers on one free-mail domain, never together
		w.thread(fmt.Sprintf("p%d@freemail.example", i), nil, fmt.Sprintf("Question %d", i), "Svar: ")
	}
	for i := range 4 { // four colleagues of one company, copying each other
		w.thread(fmt.Sprintf("c%d@corp.example.co.uk", i), []string{fmt.Sprintf("c%d@mail.corp.example.co.uk", i+1)}, fmt.Sprintf("Projet %d", i), "Svar: ")
	}
	for i := range 4 { // "Ticket:" starts new mail and replies alike
		w.thread("desk@vendor.example", nil, fmt.Sprintf("Ticket: %d", i), "")
	}
	w.judge()
	rep, err := New(w.cfg, w.st, nil).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Shared.Domains, []string{"freemail.example"}) {
		t.Fatalf("shared domains %+v", rep.Shared)
	}
	if !slices.Equal(rep.Prefixes.Prefixes, []string{"svar"}) {
		t.Fatalf("prefixes %+v", rep.Prefixes)
	}
	snap := w.cfg.Snapshot()
	if snap.Layer("mail.domains.shared") != config.LayerLearned || snap.Strings("mail.subject.prefixes_learned")[0] != "svar" {
		t.Fatal("learned values apply through the install profile")
	}
	learned := w.cfg.Learned()
	if len(learned) != 2 || learned[0].Sample == 0 || learned[0].Provenance.Learner == "" {
		t.Fatalf("with sample and provenance: %+v", learned)
	}
	if mail.NewDomains(snap.Strings("mail.domains.shared")).Of("p9@freemail.example") != "" {
		t.Fatal("a shared domain groups nobody")
	}
}

// TestLearnerLearnsAddressBookSharedDomains covers the free-mail domain
// whose senders sometimes co-thread: the thread graph alone misses it, but
// several unrelated declared companies on one domain are decisive.
func TestLearnerLearnsAddressBookSharedDomains(t *testing.T) {
	w := newWorld(t)
	mk := func(id, address, company string) contacts.Contact {
		return contacts.Contact{EntryID: id, DisplayName: id, Company: company, Emails: []string{address}}
	}
	list := []contacts.Contact{
		mk("a", "a@freebook.example", "Alpha"),
		mk("b", "b@freebook.example", "Beta"),
		mk("c", "c@freebook.example", "Gamma"),
		mk("d", "d@freebook.example", "Delta"),
		mk("e", "e@freebook.example", "Alpha"),
	}
	if err := w.st.ReplaceContacts(t.Context(), store.StoreRef{SourceID: "src", StoreID: "s"}, "Contacts", list); err != nil {
		t.Fatal(err)
	}
	rep, err := New(w.cfg, w.st, nil).Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Shared.Domains, "freebook.example") {
		t.Fatalf("address-book shared domain not learned: %+v", rep.Shared)
	}
}

func TestLearnerOnAnEmptyMailbox(t *testing.T) {
	w := newWorld(t)
	rep, err := New(w.cfg, w.st, nil).Run(t.Context())
	if err != nil || rep.Messages != 0 || len(rep.Shared.Domains) != 0 || len(rep.Prefixes.Prefixes) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if len(w.cfg.Learned()) != 2 {
		t.Fatal("an empty measurement is recorded too (sample 0)")
	}
}
