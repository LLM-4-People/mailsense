package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// companyRecv builds a received message with no follow-up flag, so a test
// opts into a due date explicitly.
func companyRecv(id, from, name, when, conversation, subject string) mail.Message {
	m := mailtest.Message("src", id)
	m.SenderName, m.SenderAddress = name, from
	m.ReceivedAt, m.SentOn = when, when
	m.Subject = subject
	m.Body = "A substantive message about the account and the next steps."
	m.ConversationID = conversation
	m.IsRead = true
	m.HasAttachments = false
	m.TaskDueDate = ""
	m.LastVerbExecuted, m.LastVerbExecutionTime = mail.OptInt{}, ""
	return m
}

// companySent builds a sent message that starts its conversation: no reply
// headers and no last-verb marker, so the thread resolver can report the
// owner as the last speaker.
func companySent(id, when, conversation, subject string, to ...mail.Recipient) mail.Message {
	m := sent(id, when, to...)
	m.ConversationID = conversation
	m.Subject = subject
	m.InReplyTo, m.References = "", ""
	m.TaskDueDate = ""
	m.LastVerbExecuted, m.LastVerbExecutionTime = mail.OptInt{}, ""
	return m
}

// seedCompanyProfileMail builds the shared directory plus the mail,
// colleague reply, calendar and deadline evidence the profile tests read.
// Five Atelier Nord messages fall in the profile window. The due messages are
// dated August so they stay out of the mailbox totals.
func seedCompanyProfileMail(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	seedCompanyDirectory(t, s)
	msgs := []mail.Message{
		companyRecv("art-a", "alice@atelier-nord.test", "Alice Martin", "2026-09-10T09:00:00Z", "C-art-a", "Atelier Nord kickoff"),
		companySent("art-b", "2026-09-30T23:30:00Z", "C-art-b", "Atelier Nord follow-up",
			mail.Recipient{Kind: mail.To, Name: "Alice Martin", Address: "alice@atelier-nord.test"},
			mail.Recipient{Kind: mail.Cc, Name: "Bob Smith", Address: "bob@atelier-nord.test"}),
		companyRecv("art-c", "carol@atelier-nord.example", "Carol Jones", "2026-10-05T08:00:00Z", "C-art-c", "Atelier Nord invoice"),
		companySent("art-d", "2026-10-06T08:00:00Z", "C-art-d", "Atelier Nord handoff",
			mail.Recipient{Kind: mail.To, Name: "Carol Jones", Address: "carol@atelier-nord.example"}),
		companyRecv("art-e", "bob@atelier-nord.test", "Bob Smith", "2026-10-20T09:00:00Z", "C-art-e", "Atelier Nord renewal"),
		companyRecv("personal", "personal@gmail.com", "Personal Contact", "2026-10-07T09:00:00Z", "C-gmail", "Personal note"),
		companyRecv("acme", "eve@acme.test", "Eve Owner", "2026-10-08T09:00:00Z", "C-acme", "Acme internal"),
		companyRecv("delta", "dave@delta.co", "Dave Delta", "2026-10-09T09:00:00Z", "C-delta", "Delta quote"),
		companyRecv("due-art", "alice@atelier-nord.test", "Alice Martin", "2026-08-01T09:00:00Z", "C-due-1", "Atelier Nord renewal due"),
		companyRecv("due-done", "bob@atelier-nord.test", "Bob Smith", "2026-08-02T09:00:00Z", "C-due-2", "Atelier Nord signed"),
		companyRecv("due-other", "dave@delta.co", "Dave Delta", "2026-08-03T09:00:00Z", "C-due-3", "Delta terms"),
	}
	msgs[8].TaskDueDate, msgs[8].FlagStatus = "2026-11-10T09:00:00Z", mail.Int(2)
	msgs[9].TaskDueDate, msgs[9].FlagStatus = "2026-11-11T09:00:00Z", mail.Int(1) // completed
	msgs[10].TaskDueDate, msgs[10].FlagStatus = "2026-11-12T09:00:00Z", mail.Int(2)

	ids := make(map[string]int64, len(msgs)+1)
	for _, m := range msgs {
		ids[m.ID] = answerMessage(t, s, m)
	}
	for _, id := range []string{"art-a", "art-c", "art-e", "personal", "acme", "delta", "due-art", "due-done", "due-other"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonRecipientMatch)
	}
	for _, id := range []string{"art-b", "art-d"} {
		mailQueryVerdict(t, s, ids[id], scope.Owner, scope.ReasonSentByOwner)
	}

	// A colleague answered art-a from a shared mailbox: ThreadStates reads
	// it, the owner-visible profile pass does not.
	reply := mailtest.Message("src", "art-reply")
	reply.StoreID, reply.FolderPath, reply.FolderRole = "store-2", "Sent", mail.RoleSent
	reply.SenderName, reply.SenderAddress = "Colleague", "colleague@atelier-nord.test"
	reply.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Alex", Address: "alex@acme.test"}}
	reply.Subject, reply.Body = "Re: Atelier Nord kickoff", "I can take this from here."
	reply.ConversationID = "C-art-a"
	reply.SentOn, reply.ReceivedAt = "2026-10-21T09:00:00Z", "2026-10-21T09:00:00Z"
	reply.InReplyTo, reply.References = "<art-a@mail>", "<art-a@mail>"
	reply.HasAttachments, reply.TaskDueDate = false, ""
	ids["art-reply"] = answerMessage(t, s, reply)
	mailQueryVerdict(t, s, ids["art-reply"], scope.Shared, scope.ReasonSentByOther)

	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	events := []calendar.Event{
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "ev-art", Subject: "Atelier Nord kickoff",
			Start: worldTime("2026-11-03T10:00:00Z"), End: worldTime("2026-11-03T11:00:00Z"),
			Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Alice Martin", Address: "alice@atelier-nord.test"}}},
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "ev-delta", Subject: "Delta sync",
			Start: worldTime("2026-11-04T10:00:00Z"), End: worldTime("2026-11-04T11:00:00Z"),
			Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Dave Delta", Address: "dave@delta.co"}}},
	}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, CalendarRef{EntryID: "cal1", Name: "Calendar"},
		worldTime("2026-10-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z"), events); err != nil {
		t.Fatal(err)
	}
	return ids
}

func companyPersonIn(list []CompanyPerson, address string) (CompanyPerson, bool) {
	for _, person := range list {
		if person.Address == address {
			return person, true
		}
	}
	return CompanyPerson{}, false
}

func TestCompanyProfileTotalsMonthsPeopleAndDirectory(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}, "time.zone": "Europe/Paris",
		"store.max_open_conns": 1})
	ids := seedCompanyProfileMail(t, s)
	now := worldTime("2026-10-26T12:00:00Z")
	profile, err := s.CompanyProfile(t.Context(), "c:atelier nord", worldTime("2026-09-01T00:00:00Z"), time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}

	if profile.Key != "c:atelier nord" || profile.Label != "Atelier Nord" || !profile.Declared {
		t.Fatalf("profile identity %+v", profile)
	}
	// One sent message to Alice and Bob counts once; the shared colleague
	// reply is not owner-visible and never enters the totals.
	if profile.Messages != 5 || profile.Sent != 2 || profile.Received != 3 || profile.Threads != 5 {
		t.Fatalf("totals messages=%d sent=%d received=%d threads=%d",
			profile.Messages, profile.Sent, profile.Received, profile.Threads)
	}
	// The world service overlays classifier-backed states over exactly these
	// conversations, newest first.
	if want := []string{"C-art-e", "C-art-d", "C-art-c", "C-art-b", "C-art-a"}; !reflect.DeepEqual(profile.ConversationIDs, want) {
		t.Fatalf("conversation ids %v, want %v", profile.ConversationIDs, want)
	}
	// C-art-a also carries a shared colleague reply. The link map must name
	// the owner-visible message, never the shared row the message API hides.
	for conversation, message := range map[string]string{"C-art-a": "art-a", "C-art-d": "art-d", "C-art-e": "art-e"} {
		if got := profile.ConversationMessageIDs[conversation]; got != ids[message] {
			t.Fatalf("conversation %s links to message %d, want %d", conversation, got, ids[message])
		}
	}
	if profile.FirstAt != "2026-09-10T09:00:00Z" || profile.LastAt != "2026-10-20T09:00:00Z" {
		t.Fatalf("span %s..%s", profile.FirstAt, profile.LastAt)
	}
	if profile.LastInboundAt != "2026-10-20T09:00:00Z" || profile.LastOutboundAt != "2026-10-06T08:00:00Z" {
		t.Fatalf("direction %s/%s", profile.LastInboundAt, profile.LastOutboundAt)
	}
	// All five messages fall inside the 90 days ending at now, across five
	// active days and five conversations.
	if want := (CompanyActivity{Messages: 5, Sent: 2, Received: 3, Threads: 5, ActiveDays: 5}); profile.Activity90 != want {
		t.Fatalf("90-day activity %+v, want %+v", profile.Activity90, want)
	}
	if profile.RecentTotal != 5 || profile.OpenThreadsTotal != 0 ||
		profile.EventsTotal != 1 || profile.DeadlinesTotal != 1 ||
		profile.AddressesTotal != len(profile.Addresses) {
		t.Fatalf("totals recent=%d open=%d events=%d deadlines=%d addresses=%d/%d",
			profile.RecentTotal, profile.OpenThreadsTotal, profile.EventsTotal,
			profile.DeadlinesTotal, profile.AddressesTotal, len(profile.Addresses))
	}
	if want := []string{"atelier-nord.example", "atelier-nord.test"}; !reflect.DeepEqual(profile.Domains, want) {
		t.Fatalf("domains %v, want %v", profile.Domains, want)
	}
	if want := []string{"alice@atelier-nord.test", "bob@atelier-nord.test", "carol@atelier-nord.example", "frank@atelier-nord.test"}; !reflect.DeepEqual(profile.Addresses, want) {
		t.Fatalf("addresses %v, want %v", profile.Addresses, want)
	}

	// Twelve zero-filled months ending with the local month of until. The
	// 30 September 23:30 UTC sent message is 1 October in Paris.
	if len(profile.Months) != 12 || profile.Months[0].Month != "2025-11" || profile.Months[11].Month != "2026-10" {
		t.Fatalf("months %+v", profile.Months)
	}
	monthSum := 0
	for _, month := range profile.Months {
		monthSum += month.Messages
	}
	if monthSum != profile.Messages {
		t.Fatalf("month sum %d, messages %d", monthSum, profile.Messages)
	}
	if profile.Months[10] != (CompanyMonth{Month: "2026-09", Messages: 1, Received: 1}) {
		t.Fatalf("September %+v", profile.Months[10])
	}
	if profile.Months[11] != (CompanyMonth{Month: "2026-10", Messages: 4, Sent: 2, Received: 2}) {
		t.Fatalf("October %+v", profile.Months[11])
	}

	// Recent messages: newest first, metadata only. A sent message to two
	// people at the organization appears once.
	if len(profile.Recent) != 5 {
		t.Fatalf("recent %+v", profile.Recent)
	}
	if profile.Recent[0].MessageID != ids["art-e"] || profile.Recent[0].SenderName != "Bob Smith" || profile.Recent[0].Sent {
		t.Fatalf("newest %+v", profile.Recent[0])
	}
	if profile.Recent[1].MessageID != ids["art-d"] || !profile.Recent[1].Sent || !profile.Recent[1].HasAttachments {
		t.Fatalf("sent metadata %+v", profile.Recent[1])
	}
	if profile.Recent[3].MessageID != ids["art-b"] || profile.Recent[3].Subject != "Atelier Nord follow-up" {
		t.Fatalf("second sent message %+v", profile.Recent[3])
	}
	seenRecent := map[int64]bool{}
	for _, message := range profile.Recent {
		if seenRecent[message.MessageID] {
			t.Fatalf("a message appears twice in Recent: %+v", profile.Recent)
		}
		seenRecent[message.MessageID] = true
	}

	// People: three contacted, one declared-only.
	if profile.PeopleContacted != 3 || profile.PeopleKnown != 4 {
		t.Fatalf("people contacted=%d known=%d: %+v", profile.PeopleContacted, profile.PeopleKnown, profile.People)
	}
	alice, ok := companyPersonIn(profile.People, "alice@atelier-nord.test")
	if !ok {
		t.Fatalf("Alice missing: %+v", profile.People)
	}
	if alice.Name != "Alice Martin" || alice.Company != "Atelier Nord" || alice.Department != "Engineering" ||
		alice.JobTitle != "CTO" || alice.Phone != "+1 555 010 0100" || !alice.Declared || alice.Messages != 2 || alice.Sent != 1 ||
		alice.Received != 1 || alice.LastAt != "2026-09-30T23:30:00Z" {
		t.Fatalf("Alice %+v", alice)
	}
	frank, ok := companyPersonIn(profile.People, "frank@atelier-nord.test")
	if !ok || !frank.Declared || frank.Messages != 0 || frank.Company != "Atelier Nord" {
		t.Fatalf("declared-only Frank %+v/%v", frank, ok)
	}

	// Without classifier signals an owner-last closing message is not a
	// waiting thread. The world service overlays the classifier-backed
	// states (the test above this one) and recomputes the total; the store's
	// own nil-signal read reports only what it can prove.
	if len(profile.OpenThreads) != 0 {
		t.Fatalf("nil-signal open threads %+v", profile.OpenThreads)
	}
	wantStates := map[string]int{
		string(threadintel.AwaitingOwner):   0,
		string(threadintel.AwaitingOther):   0,
		string(threadintel.AnsweredByOwner): 0,
		string(threadintel.AnsweredByOther): 1,
		string(threadintel.NoAction):        4,
		string(threadintel.Unknown):         0,
	}
	if !reflect.DeepEqual(profile.ThreadStates, wantStates) {
		t.Fatalf("thread states %+v, want %+v", profile.ThreadStates, wantStates)
	}

	// Only the event with an Atelier Nord attendee matches.
	if len(profile.Events) != 1 || profile.Events[0].EntryID != "ev-art" {
		t.Fatalf("events %+v", profile.Events)
	}
	// Only the marked follow-up from Atelier Nord is due; the completed one and
	// the Delta one are excluded.
	if len(profile.Deadlines) != 1 || profile.Deadlines[0].MessageID != ids["due-art"] {
		t.Fatalf("deadlines %+v", profile.Deadlines)
	}

	// A shorter bucket list covers the same messages and is still ascending.
	short, err := s.CompanyProfile(t.Context(), "c:atelier nord", worldTime("2026-09-01T00:00:00Z"), time.Time{}, 2, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(short.Months) != 2 || short.Months[0].Month != "2026-09" || short.Months[1].Month != "2026-10" ||
		short.Months[0].Messages+short.Months[1].Messages != short.Messages {
		t.Fatalf("two months %+v (messages %d)", short.Months, short.Messages)
	}

	// The same read works for a bare external domain: the directory's Dave
	// is declared there and his one message is observed.
	delta, err := s.CompanyProfile(t.Context(), "d:delta.co", worldTime("2026-09-01T00:00:00Z"), time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Declared || delta.Messages != 1 || delta.PeopleContacted != 1 || delta.PeopleKnown != 1 ||
		len(delta.Domains) != 1 || delta.Domains[0] != "delta.co" {
		t.Fatalf("domain profile %+v", delta)
	}
	dave, ok := companyPersonIn(delta.People, "dave@delta.co")
	if !ok || !dave.Declared || dave.Company != "" || dave.Messages != 1 {
		t.Fatalf("domain person %+v/%v", dave, ok)
	}
}

// TestCompanyProfileThreadSignalBoundary records the v1 boundary: the
// resolver reports awaiting_owner once it gets the classifier's message-level
// reply signal, but CompanyProfile calls it with nil by contract, so the
// profile reports awaiting_owner as zero and cannot open that thread. Remove
// or update this test when the profile starts supplying signals.
func TestCompanyProfileThreadSignalBoundary(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyDirectory(t, s)
	ask := companyRecv("ask", "alice@atelier-nord.test", "Alice Martin", "2026-10-10T09:00:00Z", "C-ask", "Could you confirm?")
	askID := answerMessage(t, s, ask)
	mailQueryVerdict(t, s, askID, scope.Owner, scope.ReasonRecipientMatch)

	now := worldTime("2026-10-26T12:00:00Z")
	withSignal, err := s.ThreadStates(t.Context(), []string{"C-ask"}, map[int64]threadintel.Signal{askID: {ReplyNeeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := withSignal["C-ask"]; got.State != threadintel.AwaitingOwner {
		t.Fatalf("the resolver with a reply signal %+v", got)
	}
	profile, err := s.CompanyProfile(t.Context(), "c:atelier nord", time.Time{}, time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ThreadStates[string(threadintel.AwaitingOwner)] != 0 || len(profile.OpenThreads) != 0 {
		t.Fatalf("the nil-signal profile claimed an owner obligation: states %+v open %+v",
			profile.ThreadStates, profile.OpenThreads)
	}
	if profile.ThreadStates[string(threadintel.NoAction)] != 1 {
		t.Fatalf("nil-signal state %+v", profile.ThreadStates)
	}
}

func TestCompanyProfileReadsBoundedSignatureEvidence(t *testing.T) {
	s, _ := open(t, map[string]any{"store.max_open_conns": 1})
	body := "Bonjour,\n\nVoici les documents demandés.\n\n--\nJulie Roy\nAdjointe administrative\nClimatisation Exemple Inc.\nTél. : 819 555-0123\njulie@climatisation-exemple.test"
	message := companyRecv("sig-1", "julie@climatisation-exemple.test", "Julie Roy",
		"2026-10-20T09:00:00Z", "C-sig-1", "Documents")
	message.Body = body
	id := answerMessage(t, s, message)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)

	profile, err := s.CompanyProfile(t.Context(), "d:climatisation-exemple.test",
		time.Time{}, time.Time{}, 0, 30, worldTime("2026-10-26T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Label == "climatisation-exemple.test" || profile.Label == "" {
		t.Fatalf("company label is still the domain: %q", profile.Label)
	}
	person, ok := companyPersonIn(profile.People, "julie@climatisation-exemple.test")
	if !ok {
		t.Fatalf("person missing: %+v", profile.People)
	}
	if person.Name != "Julie Roy" || person.Phone != "819 555-0123" ||
		person.JobTitle != "Adjointe administrative" {
		t.Fatalf("signature person %+v", person)
	}
}

func TestCompanyProfileSignaturePassCoversMoreThanTheMostFrequentPeople(t *testing.T) {
	s, _ := open(t, map[string]any{"store.max_open_conns": 1})
	// Eight one-message senders. The only phone and title belong to the
	// oldest sender, which a top-six-by-frequency sample would never read.
	dates := []string{
		"2026-09-19", "2026-09-18", "2026-09-17", "2026-09-16",
		"2026-09-15", "2026-09-14", "2026-09-13", "2026-09-12",
	}
	names := []string{
		"Alice Martin", "Bruno Gagnon", "Claire Pelletier", "David Côté",
		"Esther Lavoie", "François Roy", "Gabrielle Morin", "Hélène Roy",
	}
	for index := 1; index <= 8; index++ {
		address := "person" + string(rune('0'+index)) + "@example.test"
		name := names[index-1]
		when := dates[index-1] + "T09:00:00Z"
		body := "Bonjour,\n\nMerci."
		if index == 8 {
			body = "Bonjour,\n\nMerci.\n\n--\nHélène Roy\nDirectrice des opérations\nExample Inc.\nTél. : 819 555-0188"
		}
		message := companyRecv("sig-many-"+string(rune('0'+index)), address, name, when,
			"C-sig-many-"+string(rune('0'+index)), "Suivi")
		message.Body = body
		id := answerMessage(t, s, message)
		mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	}

	profile, err := s.CompanyProfile(t.Context(), "d:example.test",
		time.Time{}, time.Time{}, 0, 30, worldTime("2026-10-26T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	person, ok := companyPersonIn(profile.People, "person8@example.test")
	if !ok {
		t.Fatalf("oldest sender missing: %+v", profile.People)
	}
	if person.Name != "Hélène Roy" || person.Phone != "819 555-0188" ||
		person.JobTitle != "Directrice des opérations" {
		t.Fatalf("signature evidence for the oldest sender %+v", person)
	}
}

func TestCompanyProfileUnknownKey(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyDirectory(t, s)
	_, err := s.CompanyProfile(t.Context(), "c:globex", time.Time{}, time.Time{}, 0, 30, worldTime("2026-10-26T12:00:00Z"))
	if !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("unknown key error %v", err)
	}
	if _, err := s.CompanyProfile(t.Context(), "d:gmail.com", time.Time{}, time.Time{}, 0, 30, worldTime("2026-10-26T12:00:00Z")); !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("a shared domain key must not resolve: %v", err)
	}
	if _, err := s.CompanyProfile(t.Context(), "d:no-such-domain.invalid", time.Time{}, time.Time{}, 0, 30,
		worldTime("2026-10-26T12:00:00Z")); !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("a domain with no contact and no mail must not resolve: %v", err)
	}
}

func TestCompanyProfileEmptyDomainOrg(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyDirectory(t, s)
	profile, err := s.CompanyProfile(t.Context(), "d:delta.co", time.Time{}, time.Time{}, 0, 30,
		worldTime("2026-10-26T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Key != "d:delta.co" || profile.Declared || profile.Messages != 0 ||
		len(profile.OpenThreads) != 0 || len(profile.Recent) != 0 {
		t.Fatalf("empty domain profile %+v", profile)
	}
	if len(profile.Months) != 12 || profile.Months[11].Messages != 0 {
		t.Fatalf("zero-filled months %+v", profile.Months)
	}
}

func TestDueDeadlinesExcludesCompletedFollowUps(t *testing.T) {
	s, _ := open(t, nil)
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	pending := companyRecv("due-open", "alice@atelier-nord.test", "Alice Martin", "2026-08-01T09:00:00Z", "C-due-open", "Open follow-up")
	pending.TaskDueDate, pending.FlagStatus = "2026-11-10T09:00:00Z", mail.OptInt{} // no status yet
	done := companyRecv("due-done", "bob@atelier-nord.test", "Bob Smith", "2026-08-02T09:00:00Z", "C-due-done", "Completed follow-up")
	done.TaskDueDate, done.FlagStatus = "2026-11-11T09:00:00Z", mail.Int(1)
	openID, doneID := answerMessage(t, s, pending), answerMessage(t, s, done)
	mailQueryVerdict(t, s, openID, scope.Owner, scope.ReasonRecipientMatch)
	mailQueryVerdict(t, s, doneID, scope.Owner, scope.ReasonRecipientMatch)

	now := worldTime("2026-10-26T12:00:00Z")
	due, err := s.DueDeadlines(t.Context(), now.Add(-30*24*time.Hour), now.Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].MessageID != openID {
		t.Fatalf("completed follow-up leaked into the deadline read: %+v", due)
	}
}

func TestCompanyProfilesBatchMatchesSingleProfiles(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}, "time.zone": "Europe/Paris",
		"store.max_open_conns": 1})
	seedCompanyProfileMail(t, s)
	since := worldTime("2026-09-01T00:00:00Z")
	now := worldTime("2026-10-26T12:00:00Z")
	keys := []string{"c:atelier nord", "d:delta.co"}
	batch, err := s.CompanyProfiles(t.Context(), keys, since, time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != len(keys) {
		t.Fatalf("batch returned %d profiles, want %d", len(batch), len(keys))
	}
	for _, key := range keys {
		single, err := s.CompanyProfile(t.Context(), key, since, time.Time{}, 0, 30, now)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(batch[key], single) {
			t.Fatalf("batch %q differs from the single profile:\nbatch  %+v\nsingle %+v", key, batch[key], single)
		}
	}
}

func TestCompanyProfilesBatchFailsOnUnknownKey(t *testing.T) {
	s, _ := open(t, nil)
	seedCompanyProfileMail(t, s)
	_, err := s.CompanyProfiles(t.Context(), []string{"c:atelier nord", "c:does-not-exist"}, time.Time{}, time.Time{}, 0, 30,
		worldTime("2026-10-26T12:00:00Z"))
	if !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("batch with an unknown key returned %v, want ErrUnknownCompany", err)
	}
}

// TestCompanyProfileEventMatchingUsesResolutionAndActiveWindow guards the two
// accuracy rules of calendar evidence: an address that resolves to another
// declared company is never attached by domain, and canceled, declined or
// free occurrences are not upcoming meetings. It also proves the requested
// event window, rather than a fixed 30 days, owns the read.
func TestCompanyProfileEventMatchingUsesResolutionAndActiveWindow(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := s.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Alpha", Company: "Alpha", Emails: []string{"alice@shared.test"}},
		{EntryID: "bob", DisplayName: "Bob Beta", Company: "Beta", Emails: []string{"bob@shared.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	now := worldTime("2026-10-26T12:00:00Z")
	event := func(id, subject, day string, attendee string, mutate func(*calendar.Event)) calendar.Event {
		e := calendar.Event{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: id,
			Subject: subject, Start: worldTime(day), End: worldTime(day).Add(time.Hour),
			Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: attendee, Address: attendee}}}
		if mutate != nil {
			mutate(&e)
		}
		return e
	}
	events := []calendar.Event{
		event("ev-alpha", "Alpha review", "2026-11-03T10:00:00Z", "alice@shared.test", nil),
		event("ev-beta", "Beta review", "2026-11-04T10:00:00Z", "bob@shared.test", nil),
		// An unknown colleague on the shared domain still resolves to Alpha
		// through the book's domain fallback.
		event("ev-charlie", "Alpha site visit", "2026-11-05T10:00:00Z", "charlie@shared.test", nil),
		event("ev-canceled", "Canceled", "2026-11-06T10:00:00Z", "alice@shared.test",
			func(e *calendar.Event) { e.Meeting = calendar.MeetingCanceled }),
		event("ev-declined", "Declined", "2026-11-07T10:00:00Z", "alice@shared.test",
			func(e *calendar.Event) { e.Response = calendar.Declined }),
		event("ev-free", "Free", "2026-11-08T10:00:00Z", "alice@shared.test",
			func(e *calendar.Event) { e.Busy = calendar.Free }),
		event("ev-later", "Later review", "2026-12-05T10:00:00Z", "alice@shared.test", nil),
	}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, CalendarRef{EntryID: "cal1", Name: "Calendar"},
		now.Add(-30*24*time.Hour), now.Add(120*24*time.Hour), events); err != nil {
		t.Fatal(err)
	}

	profile, err := s.CompanyProfile(t.Context(), "c:alpha", time.Time{}, time.Time{}, 12, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(profile.Events))
	for _, entry := range profile.Events {
		got = append(got, entry.EntryID)
	}
	if want := []string{"ev-alpha", "ev-charlie"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("30-day events %v, want %v", got, want)
	}
	if profile.EventsTotal != 2 {
		t.Fatalf("30-day event total %d", profile.EventsTotal)
	}

	long, err := s.CompanyProfile(t.Context(), "c:alpha", time.Time{}, time.Time{}, 12, 60, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(long.Events) != 3 || long.Events[2].EntryID != "ev-later" || long.EventsTotal != 3 {
		t.Fatalf("60-day events %+v total=%d", long.Events, long.EventsTotal)
	}
}

// A caller that already holds the authoritative assessment of a conversation
// can keep the store from resolving it again; the summaries are still read,
// and the conversation stays in the profile's id list.
func TestCompanyProfilesSkipStatesAlreadyResolved(t *testing.T) {
	s, _ := open(t, nil)
	seedCompanyProfileMail(t, s)
	since := worldTime("2026-09-01T00:00:00Z")
	now := worldTime("2026-10-26T12:00:00Z")
	full, err := s.CompanyProfiles(t.Context(), []string{"c:atelier nord"}, since, time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	profile := full["c:atelier nord"]
	var skipped string
	for _, id := range profile.ConversationIDs {
		if _, ok := profile.States[id]; ok {
			skipped = id
			break
		}
	}
	if skipped == "" {
		t.Fatal("the fixture has no resolved conversation to skip")
	}
	partial, err := s.CompanyProfiles(t.Context(), []string{"c:atelier nord"}, since, time.Time{}, 0, 30, now,
		CompanyProfileRead{SkipStates: func(id string) bool { return id == skipped }})
	if err != nil {
		t.Fatal(err)
	}
	got := partial["c:atelier nord"]
	if _, ok := got.States[skipped]; ok {
		t.Fatalf("the store resolved the skipped conversation %q", skipped)
	}
	if _, ok := got.Summaries[skipped]; !ok {
		t.Fatalf("the skipped conversation lost its summary %q", skipped)
	}
	found := false
	for _, id := range got.ConversationIDs {
		if id == skipped {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the skipped conversation left the id list: %v", got.ConversationIDs)
	}
	for id := range profile.States {
		if id == skipped {
			continue
		}
		if _, ok := got.States[id]; !ok {
			t.Fatalf("an unskipped conversation lost its state %q", id)
		}
	}
}

// A closed conversation still belongs to the profile, but its summary is
// never rendered; the caller's open-state filter must keep it out of the read.
func TestCompanyProfilesSummariesOnlyForOpenConversations(t *testing.T) {
	s, _ := open(t, nil)
	seedCompanyProfileMail(t, s)
	since := worldTime("2026-09-01T00:00:00Z")
	now := worldTime("2026-10-26T12:00:00Z")
	full, err := s.CompanyProfiles(t.Context(), []string{"c:atelier nord"}, since, time.Time{}, 0, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	profile := full["c:atelier nord"]
	var chosen string
	for id := range profile.Summaries {
		chosen = id
		break
	}
	if chosen == "" {
		t.Fatal("the fixture has no conversation summary")
	}
	got, err := s.CompanyProfiles(t.Context(), []string{"c:atelier nord"}, since, time.Time{}, 0, 30, now,
		CompanyProfileRead{OpenState: func(id string) bool { return id == chosen }})
	if err != nil {
		t.Fatal(err)
	}
	one := got["c:atelier nord"]
	for id := range one.Summaries {
		if id != chosen {
			t.Fatalf("a closed conversation kept its summary: %q", id)
		}
	}
	if _, ok := one.Summaries[chosen]; !ok {
		t.Fatalf("the open conversation lost its summary: %q", chosen)
	}
	if len(one.ConversationIDs) != len(profile.ConversationIDs) {
		t.Fatalf("the summary filter changed the id list: %d vs %d", len(one.ConversationIDs), len(profile.ConversationIDs))
	}
}

// TestCompanyProfilesMatchTheOrderedQuery proves the cached population and
// the removed ORDER BY cannot change a profile: the previous ordered
// statement feeds the shared fold as the oracle, and every requested window
// must produce the same exported profile from the cache. The fixture has
// mixed instants, equal timestamps and one undated pair.
func TestCompanyProfilesMatchTheOrderedQuery(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}, "time.zone": "Europe/Paris",
		"store.max_open_conns": 1})
	ctx := t.Context()
	seedCompanyProfileMail(t, s)

	extra := []mail.Message{
		companyRecv("art-equal-a", "alice@atelier-nord.test", "Alice Martin", "2026-10-05T08:00:00Z", "C-art-equal", "Equal instant A"),
		companySent("art-equal-b", "2026-10-05T08:00:00Z", "C-art-equal", "Equal instant B",
			mail.Recipient{Kind: mail.To, Name: "Alice Martin", Address: "alice@atelier-nord.test"},
			mail.Recipient{Kind: mail.Cc, Name: "Bob Smith", Address: "bob@atelier-nord.test"}),
		companyRecv("art-undated", "alice@atelier-nord.test", "Alice Martin", "", "C-art-undated", "Undated"),
	}
	for _, m := range extra {
		id := answerMessage(t, s, m)
		if m.FolderRole == mail.RoleSent {
			mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonSentByOwner)
			continue
		}
		mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	}

	now := worldTime("2026-10-26T12:00:00Z")
	keys := []string{"c:atelier nord", "d:delta.co"}
	windows := []struct {
		name  string
		since time.Time
		until time.Time
	}{
		{"all time", time.Time{}, time.Time{}},
		{"october", worldTime("2026-10-01T00:00:00Z"), worldTime("2026-11-01T00:00:00Z")},
		{"narrow", worldTime("2026-09-20T00:00:00Z"), worldTime("2026-10-10T00:00:00Z")},
		{"empty forward window", worldTime("2026-12-01T00:00:00Z"), worldTime("2027-01-01T00:00:00Z")},
	}
	for _, window := range windows {
		t.Run(window.name, func(t *testing.T) {
			got, err := s.CompanyProfiles(ctx, keys, window.since, window.until, 0, 30, now)
			if err != nil {
				t.Fatal(err)
			}
			want := orderedCompanyProfiles(t, s, keys, window.since, window.until, 12, 30, now)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("profiles differ from the ordered query:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// orderedCompanyProfiles runs the SQL parity oracle for one profile window
// through the same shared fold and finalization as production.
func orderedCompanyProfiles(t *testing.T, s *Store, keys []string, since, until time.Time,
	months, eventDays int, now time.Time) map[string]CompanyProfile {
	t.Helper()
	ctx := t.Context()
	if now.IsZero() {
		now = s.clock.Now()
	}
	if until.IsZero() {
		until = now
	}
	book, err := s.CompanyBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loc := s.companyProfileZone()
	monthList, monthAt := companyProfileMonths(loc, until, months)
	activityStart := until.Add(-companyProfileActivityWindow)
	plan := mailquery.Plan{Direction: mailquery.DirectionBoth,
		Since: instantString(since), Until: instantString(until)}
	cte, args, err := s.counterpartCTE(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	ordered := companyProfileRowsQuery(cte) + " ORDER BY p.instant, p.message_id"
	aggs, requested, err := companyProfileAggs(keys, book, monthList)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.companyProfilesWithQuery(ctx, book, aggs, requested, now, until, eventDays, loc, monthAt,
		activityStart, ordered, args)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// TestCompanyProfileRowsPlanHasNoSort pins the profile batch's SQL parity
// pair read: every accumulator is order-independent, so the oracle statement
// must not sort the whole counterpart population in a temporary B-tree.
func TestCompanyProfileRowsPlanHasNoSort(t *testing.T) {
	s, _ := open(t, map[string]any{"mail.domains.shared": []any{"gmail.com"}})
	seedCompanyProfileMail(t, s)
	now := worldTime("2026-10-26T12:00:00Z")
	plan := mailquery.Plan{Direction: mailquery.DirectionBoth,
		Since: instantString(time.Time{}), Until: instantString(now)}
	cte, args, err := s.counterpartCTE(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	got := priorityQueryPlan(t, s, companyProfileRowsQuery(cte), args...)
	assertIndexOrderedPlan(t, "company profile rows", got)
}
