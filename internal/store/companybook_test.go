package store

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// seedCompanyDirectory builds the address book the book and profile tests
// share: Atelier Nord has four declared people across two domains, a personal
// gmail contact declares nothing, the owner's own domain carries an Acme
// contact and an external gmail contact declares the same Acme company,
// delta.co is a bare domain, and an unpicked store's contact must never
// enter the book.
func seedCompanyDirectory(t *testing.T, s *Store) {
	t.Helper()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Martin", Company: "Atelier Nord", Department: "Engineering",
			JobTitle: "CTO", Phone: "+1 555 010 0100", Emails: []string{"alice@atelier-nord.test"}},
		{EntryID: "bob", DisplayName: "Bob Smith", Company: "Atelier Nord", Department: "Sales",
			Emails: []string{"bob@atelier-nord.test"}},
		{EntryID: "carol", DisplayName: "Carol Jones", Company: "Atelier Nord", Department: "Operations",
			Emails: []string{"carol@atelier-nord.example"}},
		{EntryID: "frank", DisplayName: "Frank Brown", Company: "Atelier Nord", Department: "Finance",
			Emails: []string{"frank@atelier-nord.test"}},
		{EntryID: "personal", DisplayName: "Personal Contact", Emails: []string{"personal@gmail.com"}},
		{EntryID: "eve", DisplayName: "Eve Owner", Company: "Acme", Emails: []string{"eve@acme.test"}},
		{EntryID: "contractor", DisplayName: "Contractor", Company: "Acme", Emails: []string{"contractor@gmail.com"}},
		{EntryID: "dave", DisplayName: "Dave Delta", Emails: []string{"dave@delta.co"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Not picked: a contact of this store must not change the book.
	if err := s.ReplaceContacts(t.Context(), StoreRef{SourceID: "src", StoreID: "store-9"}, "Contacts",
		[]contacts.Contact{{EntryID: "ghost", DisplayName: "Ghost", Company: "Ghost LLC",
			Emails: []string{"ghost@ghost.example"}}}); err != nil {
		t.Fatal(err)
	}
}

func companyOrgIn(list []CompanyOrg, key string) (CompanyOrg, bool) {
	for _, org := range list {
		if org.Key == key {
			return org, true
		}
	}
	return CompanyOrg{}, false
}

// TestCompanyBookKeepsTheRankingsObservedSpelling is the regression guard
// for extracting the book: the company ranking labels a row from the
// spellings it actually observed in the plan window, not from the book's
// canonical (all-addresses) spelling. Frank alone wrote in the window, so
// his "ATELIER NORD" spelling must win even though the directory's dominant
// spelling is "Atelier Nord" (the two fold to the same company key).
// TestCompanyBookTreatsMultiCompanyDomainAsShared is the regression guard
// for a free-mail domain the structural learner has not listed: many
// unrelated declared companies on one domain must not turn its dominant
// company into every undeclared address's employer.
func TestCompanyBookTreatsMultiCompanyDomainAsShared(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	mk := func(id, address, company string) contacts.Contact {
		c := contacts.Contact{EntryID: id, DisplayName: id, Company: company, Emails: []string{address}}
		return c
	}
	list := []contacts.Contact{
		mk("alpha1", "alpha1@shared.test", "Alpha"),
		mk("beta1", "beta1@shared.test", "Beta"),
		mk("gamma1", "gamma1@shared.test", "Gamma"),
		mk("delta1", "delta1@shared.test", "Delta"),
		mk("alpha2", "alpha2@shared.test", "Alpha"),
		mk("nobody", "nobody@shared.test", ""),
		mk("corp1", "one@corp.test", "Corp"),
		mk("corp2", "two@corp.test", ""),
	}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", list); err != nil {
		t.Fatal(err)
	}
	book, err := s.CompanyBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if org, ok := book.Resolve("alpha1@shared.test"); !ok || org.Key != "c:alpha" {
		t.Fatalf("a declared address must keep its company: %+v %v", org, ok)
	}
	if org, ok := book.Resolve("nobody@shared.test"); ok {
		t.Fatalf("an undeclared address on a shared domain must have no company: %+v", org)
	}
	if _, ok := book.Org("d:shared.test"); ok {
		t.Fatal("a structurally shared domain must not have a domain organization")
	}
	if got := book.StructurallySharedDomains(); len(got) != 1 || got[0] != "shared.test" {
		t.Fatalf("structurally shared domains %v", got)
	}
	// A domain with one dominant company still groups its undeclared
	// colleague: the guard is multi-company evidence, not a domain list.
	if org, ok := book.Resolve("two@corp.test"); !ok || org.Key != "c:corp" {
		t.Fatalf("corporate domain fallback %+v %v", org, ok)
	}
}

func TestCompanyBookKeepsTheRankingsObservedSpelling(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice", Company: "Atelier Nord", Emails: []string{"alice@atelier-nord.test"}},
		{EntryID: "gina", DisplayName: "Gina", Company: "Atelier Nord", Emails: []string{"gina@atelier-nord.test"}},
		{EntryID: "frank", DisplayName: "Frank", Company: "ATELIER NORD", Emails: []string{"frank@atelier-nord.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	old := companyRecv("old", "alice@atelier-nord.test", "Alice", "2026-01-05T09:00:00Z", "C-old", "Old")
	recent := companyRecv("recent", "frank@atelier-nord.test", "Frank", "2026-10-05T09:00:00Z", "C-recent", "Recent")
	for _, m := range []mail.Message{old, recent} {
		id := answerMessage(t, s, m)
		mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	}

	book, err := s.CompanyBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if org, ok := book.Org("c:atelier nord"); !ok || org.Label != "Atelier Nord" {
		t.Fatalf("the book's canonical label %+v/%v", org, ok)
	}

	plan := companyPlan()
	plan.Since, plan.Until = "2026-09-01T00:00:00Z", "2026-11-01T00:00:00Z"
	result, err := s.ExecuteMailQuery(t.Context(), plan, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Key != "c:atelier nord" || result.Rows[0].Label != "ATELIER NORD" ||
		result.Rows[0].Messages != 1 {
		t.Fatalf("ranking row %+v", result.Rows)
	}
}

func TestCompanyBookResolvesDeclaredDomainsAndInternalOwners(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyDirectory(t, s)
	book, err := s.CompanyBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if got, want := book.Coverage(), (CompanyCoverage{Contacts: 8, ContactsWithCompany: 6, Domains: 4,
		DeclaredOrgs: 2, OwnerDomains: 1, OwnerCompanies: 1}); got != want {
		t.Fatalf("coverage %+v, want %+v", got, want)
	}

	atelierNord, ok := book.Resolve("alice@atelier-nord.test")
	if !ok {
		t.Fatal("the declared company did not resolve")
	}
	if atelierNord.Key != "c:atelier nord" || atelierNord.Label != "Atelier Nord" || !atelierNord.Declared {
		t.Fatalf("Atelier Nord org %+v", atelierNord)
	}
	if want := []string{"atelier-nord.example", "atelier-nord.test"}; !reflect.DeepEqual(atelierNord.Domains, want) {
		t.Fatalf("domains %v, want %v", atelierNord.Domains, want)
	}
	if want := []string{"alice@atelier-nord.test", "bob@atelier-nord.test", "carol@atelier-nord.example", "frank@atelier-nord.test"}; !reflect.DeepEqual(atelierNord.Addresses, want) {
		t.Fatalf("addresses %v, want %v", atelierNord.Addresses, want)
	}
	if len(atelierNord.People) != 4 {
		t.Fatalf("people %+v", atelierNord.People)
	}
	alice := atelierNord.People[0]
	if alice.Address != "alice@atelier-nord.test" || alice.Name != "Alice Martin" || alice.Company != "Atelier Nord" ||
		alice.Department != "Engineering" || alice.JobTitle != "CTO" || alice.Phone != "+1 555 010 0100" || !alice.Declared {
		t.Fatalf("Alice person %+v", alice)
	}

	// A contact at the same domain that declares nothing still resolves to
	// the domain's dominant declared company.
	if viaDomain, ok := book.Resolve("sales@atelier-nord.test"); !ok || viaDomain.Key != "c:atelier nord" {
		t.Fatalf("domain-dominant resolution %+v/%v", viaDomain, ok)
	}

	// A shared domain without a declaration is no organization at all.
	if org, ok := book.Resolve("personal@gmail.com"); ok {
		t.Fatalf("a personal gmail address became an organization: %+v", org)
	}

	// Owner domains resolve, but are internal; the Acme contact on the
	// owner's domain makes the declared company internal too.
	if eve, ok := book.Resolve("eve@acme.test"); !ok || eve.Key != "c:acme" || !eve.Declared {
		t.Fatalf("owner-domain org %+v/%v", eve, ok)
	}
	if !book.IsInternal("eve@acme.test") {
		t.Fatal("a contact on the owner's domain must be internal")
	}
	if !book.IsInternal("someone@acme.test") {
		t.Fatal("an undeclared owner-domain address must be internal")
	}
	// An external free-mail address declaring the owner's company is
	// internal too: the company, not the domain, is the owner's.
	if contractor, ok := book.Resolve("contractor@gmail.com"); !ok || contractor.Key != "c:acme" ||
		!book.IsInternal("contractor@gmail.com") {
		t.Fatalf("owner company on a shared domain %+v/%v", contractor, ok)
	}
	acme, ok := book.Org("c:acme")
	if !ok || len(acme.Addresses) != 2 || len(acme.Domains) != 1 || acme.Domains[0] != "acme.test" {
		t.Fatalf("owner company org %+v/%v", acme, ok)
	}

	// A bare external domain is a domain organization, not a declared one.
	delta, ok := book.Resolve("dave@delta.co")
	if !ok || delta.Key != "d:delta.co" || delta.Label != "delta.co" || delta.Declared {
		t.Fatalf("delta org %+v/%v", delta, ok)
	}
	if book.IsInternal("dave@delta.co") {
		t.Fatal("an external domain must not be internal")
	}
	if fresh, ok := book.Resolve("someone@fresh.example"); !ok || fresh.Key != "d:fresh.example" {
		t.Fatalf("resolution of an unlisted non-shared domain %+v/%v", fresh, ok)
	}
	if fresh, ok := book.Org("d:fresh.example"); !ok || fresh.Label != "fresh.example" {
		t.Fatalf("a domain key must resolve even without a contact: %+v/%v", fresh, ok)
	}
	// A domain dominated by a declared company has no domain organization:
	// Resolve can never produce that key.
	if dominated, ok := book.Org("d:atelier-nord.test"); ok {
		t.Fatalf("a dominated domain became a domain organization: %+v", dominated)
	}

	// The book only reads contacts of picked stores.
	if ghost, ok := book.Org("c:ghost llc"); ok {
		t.Fatalf("an unpicked store's contact entered the book: %+v", ghost)
	}

	list := book.List()
	if len(list) != 3 {
		t.Fatalf("list %+v", list)
	}
	wantOrder := []string{"c:acme", "c:atelier nord", "d:delta.co"}
	for i, key := range wantOrder {
		if list[i].Key != key {
			t.Fatalf("list order %v, want %v", list, wantOrder)
		}
	}
	if again := book.List(); !reflect.DeepEqual(list, again) {
		t.Fatalf("List is not deterministic:\n%+v\n%+v", list, again)
	}
	if ownerOrg, ok := companyOrgIn(list, "c:acme"); !ok || ownerOrg.Key != "c:acme" {
		t.Fatalf("internal organization missing from the list: %+v/%v", ownerOrg, ok)
	}

	if org, ok := book.Org("c:atelier nord"); !ok || org.Label != "Atelier Nord" || len(org.People) != 4 {
		t.Fatalf("Org lookup %+v/%v", org, ok)
	}
	if _, ok := book.Org("c:no such company"); ok {
		t.Fatal("an unknown declared company key resolved")
	}
}

// The company list and its coverage are one read: the book is built once per
// address-book/owner generation and shared-domain configuration, not once per
// caller. A mail write and a label click cannot change it; a contact or owner
// write does.
func TestCompanyBookIsBuiltOncePerAddressBookGeneration(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)
	ctx := t.Context()
	first, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("the company book was rebuilt without a change")
	}
	s.touchLabels()
	afterLabel, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterLabel != first {
		t.Fatal("a label click rebuilt the company book")
	}
	s.touch()
	afterMail, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterMail != first {
		t.Fatal("a mail write rebuilt the book from unchanged contacts")
	}
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(ctx, ref, []string{"alex@acme.test", "new@declared-owner.test"}); err != nil {
		t.Fatal(err)
	}
	afterOwner, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterOwner == first {
		t.Fatal("an owner identity write reused the old company book")
	}
	if err := s.ReplaceContacts(ctx, ref, "Contacts", []contacts.Contact{
		{EntryID: "newco", DisplayName: "New Person", Company: "NewCo", Emails: []string{"new@newco.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	afterContacts, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterContacts == afterOwner {
		t.Fatal("a contact write reused the old company book")
	}
	if _, ok := afterContacts.Org("c:newco"); !ok {
		t.Fatal("the contact write is not visible in the rebuilt book")
	}
}

// A rebuild after a contact write stores the digest of the rows it read, not a
// zero or stale one: the cache pass already computed it, and the build reuses
// it instead of hashing the same address book a second time.
func TestCompanyBookKeepsTheComputedContactFingerprint(t *testing.T) {
	s, _ := open(t, nil)
	seedCompanyMailbox(t, s)
	ctx := t.Context()
	if _, err := s.CompanyBook(ctx); err != nil {
		t.Fatal(err)
	}
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.ReplaceContacts(ctx, ref, "Contacts", []contacts.Contact{
		{EntryID: "newco", DisplayName: "New Person", Company: "NewCo", Emails: []string{"new@newco.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	book, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.companyBookContactFingerprint(ctx, s.cfg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, expected := book.contactFingerprint, hex.EncodeToString(want[:]); got != expected {
		t.Fatalf("rebuilt book stores contact fingerprint %q, want %q", got, expected)
	}
}

// The mail counts read mail only: a label write must not discard the cached
// role counts, while a mail write must.
func TestMailCountsSurviveALabelWrite(t *testing.T) {
	s, _ := open(t, nil)
	seedMsgs(t, s, msgAt("a", mail.RoleInbox, "2026-01-01T00:00:00Z"))
	ctx := t.Context()
	q := MessageListQuery{Counts: true, Limit: 10}
	if _, err := s.MessageList(ctx, q); err != nil {
		t.Fatal(err)
	}
	s.countsMu.Lock()
	before := len(s.counts)
	s.countsMu.Unlock()
	if before == 0 {
		t.Fatal("the first read did not cache the counts")
	}
	s.touchLabels()
	if _, err := s.MessageList(ctx, q); err != nil {
		t.Fatal(err)
	}
	s.countsMu.Lock()
	afterLabel := len(s.counts)
	s.countsMu.Unlock()
	if afterLabel != before {
		t.Fatalf("a label write rebuilt the mail counts: %d -> %d entries", before, afterLabel)
	}
	s.touch()
	if _, err := s.MessageList(ctx, q); err != nil {
		t.Fatal(err)
	}
	s.countsMu.Lock()
	afterMail := len(s.counts)
	s.countsMu.Unlock()
	if afterMail == afterLabel {
		t.Fatal("a mail write did not rebuild the mail counts")
	}
}

// Declared owner addresses are configuration, not a database write. The book
// keys on the config snapshot so a declared owner change rebuilds it.
func TestCompanyBookRebuildsWhenDeclaredOwnerChanges(t *testing.T) {
	s, cfg := open(t, nil)
	seedCompanyMailbox(t, s)
	ctx := t.Context()
	first, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsInternal("boss@declared-owner.test") {
		t.Fatal("the fixture already treats the declared domain as internal")
	}
	entry := (StoreRef{SourceID: "src", StoreID: "store-1"}).Key() + "=boss@declared-owner.test"
	if err := cfg.Set(source.SettingDeclared, []any{entry}); err != nil {
		t.Fatal(err)
	}
	second, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a declared owner change reused the old company book")
	}
	if !second.IsInternal("boss@declared-owner.test") {
		t.Fatal("the declared owner address is not internal")
	}
}

// The shared-domain setting is part of the book's key.
func TestCompanyBookRebuildsWhenSharedDomainsChange(t *testing.T) {
	s, cfg := open(t, nil)
	seedCompanyDirectory(t, s)
	ctx := t.Context()
	first, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(mail.SettingSharedDomains, []any{"atelier-nord.test"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a shared-domain change reused the old company book")
	}
}

// Startup builds the world, commitments and conversations concurrently, and
// each can ask for the company book. They must share one scan: without
// single-flight the same 4,000-contact book is built up to three times.
func TestCompanyBookSharesOneConcurrentBuild(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	list := make([]contacts.Contact, 0, 4000)
	for i := range 4000 {
		list = append(list, contacts.Contact{
			EntryID:     fmt.Sprintf("c%04d", i),
			DisplayName: fmt.Sprintf("Person %04d", i),
			Company:     fmt.Sprintf("Company %d", i%50),
			Emails:      []string{fmt.Sprintf("p%04d@example.test", i)},
		})
	}
	if err := s.ReplaceContacts(t.Context(), ref, "", list); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	books := make([]*CompanyBook, readers)
	errs := make([]error, readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(readers)
	for i := range readers {
		go func(i int) {
			defer wg.Done()
			<-start
			books[i], errs[i] = s.CompanyBook(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range readers {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if books[i] != books[0] {
			t.Fatalf("reader %d built a second company book", i)
		}
	}
}
