package store

import (
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func boolValue(v bool) *bool { return &v }

func TestCompanyCounterpartsBuildsOncePerGeneration(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)

	first, err := s.CompanyCounterparts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CompanyCounterparts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("same generation returned two counterpart populations")
	}
	if got := s.companyCounterpartsBuildCount(); got != 1 {
		t.Fatalf("builds after two reads = %d, want 1", got)
	}

	message := mailtest.Message("src", "a-new")
	message.SenderName, message.SenderAddress = "New", "new@atelier-nord.test"
	message.ReceivedAt, message.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
	message.ConversationID = "C-new"
	message.Subject = "New counterpart"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)
	third, err := s.CompanyCounterparts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("a mail write reused the previous counterpart population")
	}
	if got := s.companyCounterpartsBuildCount(); got != 2 {
		t.Fatalf("builds after the mail write = %d, want 2", got)
	}
}

func TestCompanyCounterpartsInstallsBuildUnderSampledGeneration(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.counterpartsBuildHook = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}

	type buildResult struct {
		counterparts *CompanyCounterparts
		err          error
	}
	sampled := s.MailGeneration()
	done := make(chan buildResult, 1)
	go func() {
		counterparts, err := s.CompanyCounterparts(t.Context())
		done <- buildResult{counterparts: counterparts, err: err}
	}()
	<-started
	message := mailtest.Message("src", "a-stale")
	message.SenderName, message.SenderAddress = "Stale", "stale@atelier-nord.test"
	message.ReceivedAt, message.SentOn = "2026-10-20T09:00:00Z", "2026-10-20T09:00:00Z"
	message.ConversationID = "C-stale"
	message.Subject = "Written during the build"
	mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, scope.ReasonRecipientMatch)
	close(release)
	first := <-done
	if first.err != nil {
		t.Fatal(first.err)
	}
	if first.counterparts == nil {
		t.Fatal("the build returned no result")
	}

	installed, installedGen, ok := s.counterparts.installedValue()
	if !ok {
		t.Fatal("no counterparts value was installed")
	}
	// The build must install under the generation it sampled rather than
	// being discarded. It is the coherent baseline the next generation's
	// delta and every snapshot reader use.
	if installed != first.counterparts {
		t.Fatal("a build that outlived its sampled generation was discarded")
	}
	if installedGen != sampled {
		t.Fatalf("build installed under generation %d, want %d", installedGen, sampled)
	}
	if got := s.companyCounterpartsBuildCount(); got != 1 {
		t.Fatalf("builds after the in-flight write = %d, want 1", got)
	}

	// The next exact read must include the write. The build may already have
	// read it (the hook runs before the scan) or apply it as one delta; both
	// leave the write visible and never repeat a full scan.
	second, err := s.CompanyCounterparts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !counterpartHasAddress(second, "stale@atelier-nord.test") {
		t.Fatal("the read after the in-flight write does not contain it")
	}
	if got := second.fullBuilds; got != 1 {
		t.Fatalf("the followup performed %d full builds, want 1 total", got)
	}
	if got := s.companyCounterpartsBuildCount(); got > 2 {
		t.Fatalf("builds after the followup = %d, want at most 2", got)
	}
}

// counterpartHasAddress reports whether the population carries one exact
// normalized counterpart address.
func counterpartHasAddress(counterparts *CompanyCounterparts, address string) bool {
	for _, pair := range counterparts.Pairs {
		if counterparts.Contacts[pair.addressIndex].norm == address {
			return true
		}
	}
	return false
}

func TestCompanyCounterpartsSingleFlight(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s.counterpartsBuildHook = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}

	const readers = 8
	results := make(chan *CompanyCounterparts, readers)
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counterparts, err := s.CompanyCounterparts(t.Context())
			results <- counterparts
			errs <- err
		}()
	}
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *CompanyCounterparts
	for counterparts := range results {
		if counterparts == nil {
			t.Fatal("a waiter received no counterpart population")
		}
		if first == nil {
			first = counterparts
			continue
		}
		if counterparts != first {
			t.Fatal("concurrent readers did not share one build result")
		}
	}
	if got := s.companyCounterpartsBuildCount(); got != 1 {
		t.Fatalf("concurrent readers started %d builds, want 1", got)
	}
}

// TestCompanyCounterpartsInternsContactsAndNames guards the compact pair
// layout: each pair names one exact contact entry and one exact display name
// instead of repeating three strings per row, index 0 stays the empty value,
// and a different spelling of the same address stays a different contact.
func TestCompanyCounterpartsInternsContactsAndNames(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyMailbox(t, s)

	recv := func(id, from, name, when string) mail.Message {
		m := mailtest.Message("src", id)
		m.SenderName, m.SenderAddress = name, from
		m.ReceivedAt, m.SentOn = when, when
		m.Subject = "Received " + id
		m.Body = "Synthetic received body " + id
		m.ConversationID = "C-" + id
		m.IsRead = true
		return m
	}
	extra := []mail.Message{
		// The exact contact and display name of a1 on another message.
		recv("a4", "alice@atelier-nord.test", "Alice", "2026-10-13T09:00:00Z"),
		// The same contact with a different observed display name.
		recv("a5", "alice@atelier-nord.test", "Alice A.", "2026-10-14T09:00:00Z"),
		// The same normalized address in another exact spelling: one more
		// contact, never a folded variant.
		recv("a6", "Alice@Atelier-Nord.test", "Alice", "2026-10-15T09:00:00Z"),
		// A sent recipient with no display name: the empty name is index 0.
		sent("b5", "2026-10-16T09:00:00Z", mail.Recipient{Kind: mail.To, Address: "carol@beta.test"}),
	}
	for _, message := range extra {
		reason := scope.ReasonRecipientMatch
		if message.FolderRole == mail.RoleSent {
			reason = scope.ReasonSentByOwner
		}
		mailQueryVerdict(t, s, answerMessage(t, s, message), scope.Owner, reason)
	}

	counterparts, err := s.CompanyCounterparts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pair := func(msgID, addressNorm string) (counterpartPair, counterpartContact, string) {
		t.Helper()
		for _, candidate := range counterparts.Pairs {
			message := counterparts.Messages[candidate.messageIndex]
			contact := counterparts.Contacts[candidate.addressIndex]
			if message.msgID == msgID && contact.norm == addressNorm {
				return candidate, contact, counterparts.Names[candidate.nameIndex]
			}
		}
		t.Fatalf("no pair for message %s and %s", msgID, addressNorm)
		return counterpartPair{}, counterpartContact{}, ""
	}

	if len(counterparts.Contacts) == 0 || counterparts.Contacts[0] != (counterpartContact{}) {
		t.Fatalf("contact index 0 is not the empty contact: %+v", counterparts.Contacts)
	}
	if len(counterparts.Names) == 0 || counterparts.Names[0] != "" {
		t.Fatalf("name index 0 is not the empty name: %q", counterparts.Names)
	}

	first, firstContact, firstName := pair("a1", "alice@atelier-nord.test")
	if firstName != "Alice" || firstContact.address != "alice@atelier-nord.test" {
		t.Fatalf("a1 pair resolved to %+v name %q", firstContact, firstName)
	}
	// a4 repeats the exact message fact; the sent a2 names the same person.
	for _, msgID := range []string{"a4", "a2"} {
		repeat, repeatContact, repeatName := pair(msgID, "alice@atelier-nord.test")
		if repeat.addressIndex != first.addressIndex || repeat.nameIndex != first.nameIndex {
			t.Fatalf("%s interned the repeated contact twice: %+v vs %+v", msgID, repeat, first)
		}
		if repeatContact != firstContact || repeatName != firstName {
			t.Fatalf("%s resolved to %+v name %q, want %+v name %q",
				msgID, repeatContact, repeatName, firstContact, firstName)
		}
	}
	variant, variantContact, variantName := pair("a5", "alice@atelier-nord.test")
	if variant.addressIndex != first.addressIndex {
		t.Fatalf("one exact contact got two contact indices: %d and %d", variant.addressIndex, first.addressIndex)
	}
	if variant.nameIndex == first.nameIndex || variantName != "Alice A." {
		t.Fatalf("a distinct display name reused name index %d: %q", variant.nameIndex, variantName)
	}
	if variantContact != firstContact {
		t.Fatalf("a display-name variant changed the contact: %+v vs %+v", variantContact, firstContact)
	}
	spelled, spelledContact, _ := pair("a6", "alice@atelier-nord.test")
	if spelled.addressIndex == first.addressIndex {
		t.Fatalf("two exact address spellings shared contact index %d", first.addressIndex)
	}
	if spelledContact.norm != firstContact.norm || spelledContact.address != "Alice@Atelier-Nord.test" {
		t.Fatalf("spelling contact %+v, want norm %q and address %q",
			spelledContact, firstContact.norm, "Alice@Atelier-Nord.test")
	}
	carol, carolContact, carolName := pair("b2", "carol@beta.test")
	if carolName != "Carol" || carolContact.address != "carol@beta.test" {
		t.Fatalf("b2 pair resolved to %+v name %q", carolContact, carolName)
	}
	nameless, namelessContact, namelessName := pair("b5", "carol@beta.test")
	if nameless.addressIndex != carol.addressIndex || namelessContact != carolContact {
		t.Fatalf("the nameless sent pair got another contact: %+v vs %+v", namelessContact, carolContact)
	}
	if nameless.nameIndex != 0 || namelessName != "" {
		t.Fatalf("an empty display name got index %d (%q), want 0", nameless.nameIndex, namelessName)
	}
}

// TestCounterpartPairStaysCompact pins the compact fact layout: a pair holds
// three indices and a direction, and the message grew only by the two facts
// moved out of the pair.
func TestCounterpartPairStaysCompact(t *testing.T) {
	if got := unsafe.Sizeof(counterpartPair{}); got > 16 {
		t.Fatalf("counterpartPair is %d bytes, want at most 16", got)
	}
	// Eight string headers (128 bytes on a 64-bit build), one int64 and two
	// bools pad to 144; before the move the same fields minus conversationID
	// and instant were 112 bytes, so the per-message growth is exactly those
	// two moved facts.
	if got := unsafe.Sizeof(counterpartMessage{}); got > 144 {
		t.Fatalf("counterpartMessage is %d bytes, want at most 144", got)
	}
}

// TestCompanyQueryCacheMatchesSQLFilters proves the pair cache is a faithful
// stand-in for the SQL ranking outside the one unsupported filter (body
// text): every direction, window, read-state, attachment and exact-address
// plan returns the same Result as the SQL path. The SQL plan adds a text
// pattern every fixture message contains, so it exercises the fallback while
// leaving the filters under test unchanged.
func TestCompanyQueryCacheMatchesSQLFilters(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	ids := seedCompanyMailbox(t, s)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE messages SET is_read = 0 WHERE id = ?`, ids["a1"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE messages SET has_attachments = 1 WHERE id = ?`, ids["b1"]); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		edit func(*mailquery.Plan)
	}{
		{"all", nil},
		{"unread", func(p *mailquery.Plan) { p.Unread = boolValue(true) }},
		{"read", func(p *mailquery.Plan) { p.Unread = boolValue(false) }},
		{"attachments", func(p *mailquery.Plan) { p.HasAttachments = boolValue(true) }},
		{"without attachments", func(p *mailquery.Plan) { p.HasAttachments = boolValue(false) }},
		{"sent", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionSent }},
		{"received", func(p *mailquery.Plan) { p.Direction = mailquery.DirectionReceived }},
		{"since", func(p *mailquery.Plan) { p.Since = "2026-10-05T00:00:00Z" }},
		{"until", func(p *mailquery.Plan) { p.Until = "2026-10-08T00:00:00Z" }},
		{"window", func(p *mailquery.Plan) {
			p.Since, p.Until = "2026-10-03T00:00:00Z", "2026-10-09T00:00:00Z"
		}},
		{"exact addresses", func(p *mailquery.Plan) {
			p.ContactAddresses = []string{"carol@beta.test", "dave@gamma.dev"}
		}},
		{"label filter", func(p *mailquery.Plan) { p.Contact = "beta inc" }},
		{"exchanges", func(p *mailquery.Plan) { p.Metric = mailquery.MetricExchanges }},
		{"threads ascending", func(p *mailquery.Plan) {
			p.Metric, p.Order, p.Limit = mailquery.MetricThreads, mailquery.OrderAsc, 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cached := companyPlan()
			if tc.edit != nil {
				tc.edit(&cached)
			}
			cachedResult, err := s.ExecuteMailQuery(t.Context(), cached, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			sqlPlan := cached
			sqlPlan.Text = "Synthetic"
			sqlResult, err := s.ExecuteMailQuery(t.Context(), sqlPlan, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cachedResult, sqlResult) {
				t.Fatalf("cache and SQL differ:\ncache %+v\nsql   %+v", cachedResult, sqlResult)
			}
		})
	}

	// The fallback itself still applies a restrictive body search.
	plan := companyPlan()
	plan.Text = "Synthetic received body a1"
	result, err := s.ExecuteMailQuery(t.Context(), plan, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Key != "c:atelier nord" || result.Total != 1 {
		t.Fatalf("text-filtered company ranking %+v", result)
	}
}
