package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"reflect"
)

// The live false positive: the French question word "liste" is a substring of
// "listening@lenovo.com", and "nance" is an inner fragment of the domain token
// "finance" in "ivoire-finance.test". A term must match one whole name or
// address token, never an inner fragment.
func TestPersonCandidatesMatchWholeTokensOnly(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(ctx, ref, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, name string) int64 {
		t.Helper()
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
		m.Subject = "Received " + id
		m.ConversationID = "C-" + id
		rowID := answerMessage(t, s, m)
		mailQueryVerdict(t, s, rowID, scope.Owner, scope.ReasonRecipientMatch)
		return rowID
	}
	recv("adam", "listening@lenovo.com", "Adam Laurent")
	recv("benoit", "benoit@ivoire-finance.test", "Benoît Vachon")
	recv("nadia", "nadia.smith@beta.test", "Nadia Smith")

	cases := []struct {
		term string
		want int
	}{
		{"liste", 0},
		{"nance", 0},
		{"nadia", 1},
		{"smith", 1},
		{"lenovo", 1},
		{"ivoire", 1},
		{"finance", 1},
	}
	for _, tc := range cases {
		rows, err := s.PersonCandidates(ctx, []string{tc.term}, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != tc.want {
			t.Errorf("term %q matched %d candidates, want %d: %+v", tc.term, len(rows), tc.want, rows)
		}
		if tc.want == 1 && rows[0].Address == "" {
			t.Errorf("term %q lost its exact address: %+v", tc.term, rows[0])
		}
	}
}

// The live miss: the owner typed "simon" while the mailbox says
// "Simon Berthier" at the company he named. Both sides fold, so the
// obvious person is a candidate; an accented term folds the same way.
func TestPersonCandidatesFoldAccents(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(ctx, ref, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	m := mailtest.Message("src", "simon")
	m.SenderName, m.SenderAddress = "Simon Berthier", "sberthier@services-exemple.test"
	m.ReceivedAt, m.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
	m.Subject = "Nouveaux employés"
	m.ConversationID = "C-simon"
	rowID := answerMessage(t, s, m)
	mailQueryVerdict(t, s, rowID, scope.Owner, scope.ReasonRecipientMatch)

	for _, term := range []string{"simon", "Simon", "SIMON"} {
		rows, err := s.PersonCandidates(ctx, []string{term}, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Address != "sberthier@services-exemple.test" || rows[0].Name != "Simon Berthier" {
			t.Fatalf("term %q matched %+v, want the accented Simon", term, rows)
		}
	}
}

func TestPersonCandidatesExactAddressesAndAddressBook(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(ctx, ref, []string{"owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	recv := func(id, from, name string) int64 {
		t.Helper()
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
		m.Subject = "Received " + id
		m.ConversationID = "C-" + id
		rowID := answerMessage(t, s, m)
		mailQueryVerdict(t, s, rowID, scope.Owner, scope.ReasonRecipientMatch)
		return rowID
	}
	recv("n1", "nadia.tremblay@atelier-nord.test", "Nadia Tremblay")
	recv("n2", "nadia@autre.ca", "Nadia Tremblay")
	recv("n3", "nadia.smith@beta.test", "Nadia Smith")
	ownerNag := recv("owner-nadia", "owner@example.com", "Nadia Owner")
	if ownerNag == 0 {
		t.Fatal("owner message not seeded")
	}
	sentID := answerMessage(t, s, sent("n4", "2026-10-22T09:00:00Z",
		mail.Recipient{Kind: mail.To, Name: "Nadia Tremblay", Address: "nadia.tremblay@atelier-nord.test"}))
	mailQueryVerdict(t, s, sentID, scope.Owner, scope.ReasonSentByOwner)

	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2",
		StoreName: "Shared", Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	shared := mailtest.Message("src", "shared-nadia")
	shared.StoreID, shared.FolderPath = "store-2", "Inbox"
	shared.SenderName, shared.SenderAddress = "Nadia Shared", "nadia@shared.example"
	shared.ReceivedAt, shared.SentOn = "2026-10-23T09:00:00Z", "2026-10-23T09:00:00Z"
	shared.ConversationID = "C-shared-nadia"
	sharedID := answerMessage(t, s, shared)
	mailQueryVerdict(t, s, sharedID, scope.Owner, scope.ReasonRecipientMatch)

	if err := s.ReplaceContacts(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, "Contacts",
		[]contacts.Contact{{EntryID: "nadia-book", DisplayName: "Nadia Tremblay", Company: "Atelier Nord",
			Emails: []string{"nadia.book@atelier-nord.test"}}}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.PersonCandidates(ctx, []string{"nadia"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	byAddress := map[string]PersonCandidate{}
	for _, row := range rows {
		byAddress[row.Address] = row
	}
	if len(rows) != 4 || len(byAddress) != 4 {
		t.Fatalf("candidates %+v, want four distinct addresses", rows)
	}
	if row := byAddress["nadia.tremblay@atelier-nord.test"]; row.Name != "Nadia Tremblay" || row.Messages != 2 {
		t.Fatalf("primary Nadia row %+v", row)
	}
	if row := byAddress["nadia@autre.ca"]; row.Name != "Nadia Tremblay" || row.Messages != 1 {
		t.Fatalf("second address row %+v", row)
	}
	if row := byAddress["nadia.smith@beta.test"]; row.Name != "Nadia Smith" || row.Messages != 1 {
		t.Fatalf("same first name, other person %+v", row)
	}
	if row := byAddress["nadia.book@atelier-nord.test"]; !row.Declared || row.Messages != 0 || row.Company != "Atelier Nord" {
		t.Fatalf("address-book-only candidate %+v", row)
	}
	for _, address := range []string{"owner@example.com", "nadia@shared.example"} {
		if _, ok := byAddress[address]; ok {
			t.Fatalf("excluded address leaked into candidates: %s", address)
		}
	}
}

// Ask repeats the same names while the mail has not moved; the candidate read
// is one full-mailbox scan, so it is cached per mail generation.
func TestPersonCandidatesAreCachedPerMailGeneration(t *testing.T) {
	s, _ := open(t, nil)
	seedCompanyMailbox(t, s)
	ctx := t.Context()
	terms := []string{"carol"}
	first, err := s.PersonCandidates(ctx, terms, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("the fixture names no Carol")
	}
	second, err := s.PersonCandidates(ctx, terms, 120)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("the cached candidates differ:\n first %+v\n second %+v", first, second)
	}
	s.candMu.Lock()
	cached := len(s.cands)
	s.candMu.Unlock()
	if cached != 1 {
		t.Fatalf("one read cached %d entries", cached)
	}
	s.touch()
	if _, err := s.PersonCandidates(ctx, terms, 120); err != nil {
		t.Fatal(err)
	}
	s.candMu.Lock()
	cached = len(s.cands)
	s.candMu.Unlock()
	if cached != 2 {
		t.Fatalf("a mail write did not add a fresh candidate read: %d entries", cached)
	}
}

// Contacts live in picked stores only. A selection change that updates no
// folder row must still invalidate the candidate cache.
func TestPersonCandidatesRebuildsWhenSelectionChangesWithoutFolderRows(t *testing.T) {
	s, cfg := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-9"}
	if err := s.ReplaceContacts(ctx, ref, "Contacts", []contacts.Contact{
		{EntryID: "ghost", DisplayName: "Ghost", Company: "Ghost LLC", Emails: []string{"ghost@ghost.example"}},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.PersonCandidates(ctx, []string{"ghost"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 0 {
		t.Fatalf("an unpicked store leaked %d candidates", len(first))
	}
	if err := cfg.Set(source.SettingSelected, []any{source.StoreKey("src", "store-9")}); err != nil {
		t.Fatal(err)
	}
	second, err := s.PersonCandidates(ctx, []string{"ghost"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Address != "ghost@ghost.example" {
		t.Fatalf("a selection change did not rebuild the candidates: %+v", second)
	}
}

// A cached empty result must stay a non-nil empty slice, matching a miss.
func TestPersonCandidatesCachedEmptyIsNonNil(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	first, err := s.PersonCandidates(ctx, []string{"nobody-matches-xyz"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PersonCandidates(ctx, []string{"nobody-matches-xyz"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || second == nil {
		t.Fatal("an empty candidate read is nil")
	}
}

// A declared owner address is configuration, not an owner_identities row:
// the SQL exclusion cannot see it, so the canonical owner set filters it.
func TestPersonCandidatesExcludesDeclaredOwnerAddress(t *testing.T) {
	s, cfg := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.ReplaceContacts(ctx, ref, "Contacts", []contacts.Contact{
		{EntryID: "boss", DisplayName: "Boss", Company: "Owner Org", Emails: []string{"boss@declared-owner.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(source.SettingSelected, []any{ref.Key()}); err != nil {
		t.Fatal(err)
	}
	before, err := s.PersonCandidates(ctx, []string{"boss"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("the contact is not a candidate before the declaration: %+v", before)
	}
	if err := cfg.Set(source.SettingDeclared, []any{ref.Key() + "=boss@declared-owner.test"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.PersonCandidates(ctx, []string{"boss"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("a declared owner address appears as a person: %+v", after)
	}
}
