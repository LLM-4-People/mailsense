package world

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func worldTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(mail.TimeLayout, value)
	if err != nil {
		t.Fatalf("time %q: %v", value, err)
	}
	return at
}

// openWorldStore builds the same picked-store world the company tests use:
// one picked source and the two shared settings the book and the profile
// read.
func openWorldStore(t *testing.T) (*store.Store, *config.Config) {
	t.Helper()
	cfg := configtest.New(t, map[string]any{
		"mail.domains.shared":  []any{"gmail.com"},
		"time.zone":            "Europe/Paris",
		"store.max_open_conns": 1,
		source.SettingSelected: mailtest.Picked([]string{"src"}),
	})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	return st, cfg
}

func worldRecv(id, from, name, when, conversation, subject string) mail.Message {
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

func worldSent(id, when, conversation, subject string, to ...mail.Recipient) mail.Message {
	m := mailtest.Message("src", id)
	m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
	m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Owner", mailtest.Owner, mailtest.Owner
	m.AddressedToMe = mail.AddressedNo
	m.Recipients = to
	m.SentOn, m.ReceivedAt = when, when
	m.Subject = subject
	m.Body = "A substantive message about the account and the next steps."
	m.ConversationID = conversation
	m.IsRead = true
	m.HasAttachments = false
	m.TaskDueDate = ""
	m.LastVerbExecuted, m.LastVerbExecutionTime = mail.OptInt{}, ""
	m.InReplyTo, m.References = "", ""
	return m
}

// seedWorldMail builds the directory, the mail, the calendar and the due
// follow-up the list and profile tests read. The three external
// organizations deliberately cover the three key shapes: a declared company
// on a real domain, a bare domain, and a declared company known only through
// a shared domain.
func seedWorldMail(t *testing.T, st *store.Store) map[string]int64 {
	t.Helper()
	ref := store.StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := st.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Martin", Company: "Atelier Nord", Department: "Engineering",
			JobTitle: "CTO", Emails: []string{"alice@atelier-nord.test"}},
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
		{EntryID: "solo", DisplayName: "Solo Contact", Company: "Solo LLC", Emails: []string{"solo@gmail.com"}},
	}); err != nil {
		t.Fatal(err)
	}

	msgs := []mail.Message{
		worldRecv("art-a", "alice@atelier-nord.test", "Alice Martin", "2026-09-10T09:00:00Z", "C-art-a", "Atelier Nord kickoff"),
		worldSent("art-b", "2026-09-30T23:30:00Z", "C-art-b", "Atelier Nord follow-up",
			mail.Recipient{Kind: mail.To, Name: "Alice Martin", Address: "alice@atelier-nord.test"},
			mail.Recipient{Kind: mail.Cc, Name: "Bob Smith", Address: "bob@atelier-nord.test"}),
		worldRecv("art-c", "carol@atelier-nord.example", "Carol Jones", "2026-10-05T08:00:00Z", "C-art-c", "Atelier Nord invoice"),
		worldSent("art-d", "2026-10-06T08:00:00Z", "C-art-d", "Atelier Nord handoff",
			mail.Recipient{Kind: mail.To, Name: "Carol Jones", Address: "carol@atelier-nord.example"}),
		worldRecv("art-e", "bob@atelier-nord.test", "Bob Smith", "2026-10-20T09:00:00Z", "C-art-e", "Atelier Nord renewal"),
		worldRecv("personal", "personal@gmail.com", "Personal Contact", "2026-10-07T09:00:00Z", "C-gmail", "Personal note"),
		worldRecv("acme", "eve@acme.test", "Eve Owner", "2026-10-08T09:00:00Z", "C-acme", "Acme internal"),
		worldRecv("delta-in", "dave@delta.co", "Dave Delta", "2026-10-09T09:00:00Z", "C-delta-in", "Delta quote"),
		worldSent("delta-out", "2026-10-14T09:00:00Z", "C-delta-out", "Delta terms",
			mail.Recipient{Kind: mail.To, Name: "Dave Delta", Address: "dave@delta.co"}),
		worldRecv("solo-in", "solo@gmail.com", "Solo Contact", "2026-10-11T09:00:00Z", "C-solo-in", "Solo quote"),
		worldSent("solo-out", "2026-10-12T09:00:00Z", "C-solo-out", "Solo terms",
			mail.Recipient{Kind: mail.To, Name: "Solo Contact", Address: "solo@gmail.com"}),
	}
	msgs[2].TaskDueDate, msgs[2].FlagStatus = "2026-11-10T09:00:00Z", mail.Int(2)
	ids := make(map[string]int64, len(msgs))
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(row *store.MessageRow) error {
		ids[row.Message.ID] = row.RowID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	verdicts := make([]store.VerdictRow, 0, len(msgs))
	for _, m := range msgs {
		verdict, reason := scope.Owner, scope.ReasonRecipientMatch
		if m.FolderRole == mail.RoleSent {
			reason = scope.ReasonSentByOwner
		}
		verdicts = append(verdicts, store.VerdictRow{RowID: ids[m.ID], Verdict: verdict, Reason: reason})
	}
	if err := st.WriteVerdicts(t.Context(), "test", verdicts); err != nil {
		t.Fatal(err)
	}

	settings := []calendar.Event{
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "ev-art",
			Subject: "Atelier Nord kickoff", Start: worldTime(t, "2026-11-03T10:00:00Z"),
			End: worldTime(t, "2026-11-03T11:00:00Z"), Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Alice Martin", Address: "alice@atelier-nord.test"}}},
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "ev-delta",
			Subject: "Delta sync", Start: worldTime(t, "2026-11-04T10:00:00Z"),
			End: worldTime(t, "2026-11-04T11:00:00Z"), Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Dave Delta", Address: "dave@delta.co"}}},
		// Not an address-book contact: the organization's declared domain is
		// the only thing that ties this event to Atelier Nord.
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "ev-art-later",
			Subject: "Atelier Nord review", Start: worldTime(t, "2026-12-20T10:00:00Z"),
			End: worldTime(t, "2026-12-20T11:00:00Z"), Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Events", Address: "events@atelier-nord.example"}}},
	}
	if err := st.ReplaceCalendarWindow(t.Context(), ref, store.CalendarRef{EntryID: "cal1", Name: "Calendar"},
		worldTime(t, "2026-10-01T00:00:00Z"), worldTime(t, "2027-02-01T00:00:00Z"), settings); err != nil {
		t.Fatal(err)
	}
	return ids
}

type fakePriority struct {
	mu      sync.Mutex
	states  map[string]threadintel.Assessment
	err     error
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (f *fakePriority) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	f.mu.Lock()
	f.calls++
	states, err := f.states, f.err
	entered, release := f.entered, f.release
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	return states, err
}

func (f *fakePriority) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// hold makes every following call announce itself on entered and then wait
// until release is closed. Tests install it only after the synchronous first
// build, so they control exactly when background refreshes run.
func (f *fakePriority) hold() (entered <-chan struct{}, release chan struct{}) {
	in, out := make(chan struct{}, 1), make(chan struct{})
	f.mu.Lock()
	f.entered, f.release = in, out
	f.mu.Unlock()
	return in, out
}

func (f *fakePriority) set(states map[string]threadintel.Assessment, err error) {
	f.mu.Lock()
	f.states, f.err = states, err
	f.mu.Unlock()
}

func TestListRanksExternalCompanies(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.GeneratedAt != "2026-10-26T12:00:00Z" {
		t.Fatalf("generated_at %q", result.GeneratedAt)
	}
	if len(result.Companies) != 3 {
		t.Fatalf("companies %+v", result.Companies)
	}
	wantKeys := []string{"c:atelier nord", "d:delta.co", "c:solo llc"}
	gotKeys := make([]string, 0, len(result.Companies))
	for _, row := range result.Companies {
		gotKeys = append(gotKeys, row.Key)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("keys %v, want %v", gotKeys, wantKeys)
	}

	atelierNord := result.Companies[0]
	if atelierNord.Label != "Atelier Nord" || !atelierNord.Declared ||
		!reflect.DeepEqual(atelierNord.Domains, []string{"atelier-nord.test"}) ||
		atelierNord.People != 3 || atelierNord.Messages != 5 || atelierNord.Sent != 2 ||
		atelierNord.Received != 3 || atelierNord.Threads != 5 || atelierNord.LastAt != "2026-10-20T09:00:00Z" {
		t.Fatalf("Atelier Nord row %+v", atelierNord)
	}
	delta := result.Companies[1]
	if delta.Label != "Delta" || delta.Declared ||
		!reflect.DeepEqual(delta.Domains, []string{"delta.co"}) ||
		delta.People != 1 || delta.Messages != 2 || delta.Sent != 1 || delta.Received != 1 ||
		delta.Threads != 2 || delta.LastAt != "2026-10-14T09:00:00Z" {
		t.Fatalf("Delta row %+v", delta)
	}
	// A declared company on a shared domain has no non-shared domain.
	solo := result.Companies[2]
	if solo.Label != "Solo LLC" || !solo.Declared || len(solo.Domains) != 0 ||
		solo.People != 1 || solo.Messages != 2 || solo.Sent != 1 || solo.Received != 1 {
		t.Fatalf("Solo row %+v", solo)
	}

	if want := (store.CompanyCoverage{Contacts: 9, ContactsWithCompany: 7, Domains: 4,
		DeclaredOrgs: 3, OwnerDomains: 1, OwnerCompanies: 1}); result.Coverage != want {
		t.Fatalf("coverage %+v, want %+v", result.Coverage, want)
	}

	page, err := svc.List(t.Context(), ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Companies) != 2 || page.Companies[0].Key != "c:atelier nord" || page.Companies[1].Key != "d:delta.co" {
		t.Fatalf("limit 2 %+v", page.Companies)
	}
	if page.Total != 3 || !page.HasMore {
		t.Fatalf("limit 2 population: total=%d has_more=%v", page.Total, page.HasMore)
	}
	// A narrower page is a copy sliced from the wider cached list, never an
	// alias that a caller can mutate into the cache.
	page.Companies[0].Key = "mutated"
	again, err := svc.List(t.Context(), ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if again.Companies[0].Key != "c:atelier nord" {
		t.Fatalf("narrow list aliased the cached backing array: %+v", again.Companies)
	}
	// A direct limit outside the API range is clamped, never an unbounded read.
	if all, err := svc.List(t.Context(), ListQuery{Limit: 0}); err != nil || len(all.Companies) != 3 {
		t.Fatalf("default limit: %d rows, %v", len(all.Companies), err)
	}
	// The executable plan uses the mailquery validator's bound: an oversized
	// direct request still sends a valid plan instead of failing validation.
	if capped, err := svc.List(t.Context(), ListQuery{Limit: mailquery.MaxCompanyLimit + 1000}); err != nil || len(capped.Companies) != 3 {
		t.Fatalf("over-bound limit: %d rows, %v", len(capped.Companies), err)
	}
}

// The search filters the whole ranking before the page limit, so a company
// beyond the first page is reachable and the population stays honest.
func TestListSearchCoversTheWholeRanking(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	found, err := svc.List(t.Context(), ListQuery{Limit: 1, Search: "delta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Companies) != 1 || found.Companies[0].Key != "d:delta.co" ||
		found.Total != 1 || found.HasMore {
		t.Fatalf("search page %+v total=%d has_more=%v", found.Companies, found.Total, found.HasMore)
	}
	upper, err := svc.List(t.Context(), ListQuery{Limit: 10, Search: "DELTA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(upper.Companies) != 1 || upper.Companies[0].Key != "d:delta.co" {
		t.Fatalf("folded search %+v", upper.Companies)
	}
}

func TestProfileTotalsMonthsPeopleAndThreadOverlay(t *testing.T) {
	st, cfg := openWorldStore(t)
	ids := seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		// The classifier-backed priority state says the owner still owes a
		// reply in C-art-b; C-art-d is the owner's request, waiting on the
		// other side. The store's nil-signal read cannot see either signal.
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, ActorName: "Alice Martin", ActorAddress: "alice@atelier-nord.test",
			OwnerActorName: "Alice Martin", OwnerActorAddress: "alice@atelier-nord.test",
			LastAt: "2026-10-21T09:00:00Z", Visibility: threadintel.Complete, Confidence: .85,
			Messages: 2},
		"C-art-d": {ConversationID: "C-art-d", State: threadintel.AwaitingOther,
			Reason: threadintel.ReasonOwnerLast, LastAt: "2026-10-06T08:00:00Z",
			Visibility: threadintel.Complete, Confidence: .85, Messages: 2},
	}}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clocktest.New(now)})
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if view.Key != "c:atelier nord" || view.Label != "Atelier Nord" || !view.Declared {
		t.Fatalf("identity %+v", view)
	}
	if view.Messages != 5 || view.Sent != 2 || view.Received != 3 || view.Threads != 5 {
		t.Fatalf("totals %+v", view)
	}
	if view.GeneratedAt != "2026-10-26T12:00:00Z" {
		t.Fatalf("generated_at %q", view.GeneratedAt)
	}
	if view.FirstAt != "2026-09-10T09:00:00Z" || view.LastAt != "2026-10-20T09:00:00Z" {
		t.Fatalf("span %s..%s", view.FirstAt, view.LastAt)
	}
	if view.LastInboundAt != "2026-10-20T09:00:00Z" || view.LastOutboundAt != "2026-10-06T08:00:00Z" {
		t.Fatalf("direction %s/%s", view.LastInboundAt, view.LastOutboundAt)
	}
	if want := (store.CompanyActivity{Messages: 5, Sent: 2, Received: 3, Threads: 5, ActiveDays: 5}); view.Activity90 != want {
		t.Fatalf("90-day activity %+v, want %+v", view.Activity90, want)
	}
	if view.RecentTotal != 5 || view.OpenThreadsTotal != 2 || view.EventsTotal != 1 ||
		view.DeadlinesTotal != 1 || view.AddressesTotal != len(view.Addresses) {
		t.Fatalf("totals recent=%d open=%d events=%d deadlines=%d addresses=%d/%d",
			view.RecentTotal, view.OpenThreadsTotal, view.EventsTotal,
			view.DeadlinesTotal, view.AddressesTotal, len(view.Addresses))
	}
	if !reflect.DeepEqual(view.Domains, []string{"atelier-nord.example", "atelier-nord.test"}) {
		t.Fatalf("domains %v", view.Domains)
	}
	if len(view.Months) != 12 || view.Months[0].Month != "2025-11" || view.Months[11].Month != "2026-10" {
		t.Fatalf("months %+v", view.Months)
	}
	if view.Months[10] != (store.CompanyMonth{Month: "2026-09", Messages: 1, Received: 1}) {
		t.Fatalf("September %+v", view.Months[10])
	}
	if view.Months[11] != (store.CompanyMonth{Month: "2026-10", Messages: 4, Sent: 2, Received: 2}) {
		t.Fatalf("October %+v", view.Months[11])
	}
	if len(view.Recent) != 5 || view.Recent[0].MessageID != ids["art-e"] {
		t.Fatalf("recent %+v", view.Recent)
	}
	if view.PeopleContacted != 3 || view.PeopleKnown != 4 {
		t.Fatalf("people contacted=%d known=%d: %+v", view.PeopleContacted, view.PeopleKnown, view.People)
	}
	if alice, ok := worldPerson(view.People, "alice@atelier-nord.test"); !ok || alice.Messages != 2 ||
		alice.Sent != 1 || alice.Received != 1 || alice.LastAt != "2026-09-30T23:30:00Z" {
		t.Fatalf("Alice %+v/%v", alice, ok)
	}
	if frank, ok := worldPerson(view.People, "frank@atelier-nord.test"); !ok || !frank.Declared || frank.Messages != 0 {
		t.Fatalf("declared-only Frank %+v/%v", frank, ok)
	}

	// The overlay changes exactly one conversation: awaiting_owner becomes
	// reachable and that thread sorts before the still-open awaiting_other.
	wantStates := map[string]int{
		string(threadintel.AwaitingOwner):   1,
		string(threadintel.AwaitingOther):   1,
		string(threadintel.AnsweredByOwner): 0,
		string(threadintel.AnsweredByOther): 0,
		string(threadintel.NoAction):        3,
		string(threadintel.Unknown):         0,
	}
	if !reflect.DeepEqual(view.ThreadStates, wantStates) {
		t.Fatalf("thread states %+v, want %+v", view.ThreadStates, wantStates)
	}
	if priority.callCount() != 1 {
		t.Fatalf("priority called %d times", priority.callCount())
	}
	if len(view.OpenThreads) != 2 {
		t.Fatalf("open threads %+v", view.OpenThreads)
	}
	first, second := view.OpenThreads[0], view.OpenThreads[1]
	if first.ConversationID != "C-art-b" || first.State != string(threadintel.AwaitingOwner) ||
		first.Subject != "Atelier Nord follow-up" || first.Actor != "Alice Martin" ||
		first.LastAt != "2026-10-21T09:00:00Z" || first.MessageID != ids["art-b"] ||
		first.Messages != 2 {
		t.Fatalf("owner thread %+v", first)
	}
	if second.ConversationID != "C-art-d" || second.State != string(threadintel.AwaitingOther) ||
		second.Subject != "Atelier Nord handoff" || second.MessageID != ids["art-d"] ||
		second.Actor != "" {
		t.Fatalf("other thread %+v", second)
	}

	// The overlay can also close a thread the store still shows open.
	closedPriority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, LastAt: "2026-10-21T09:00:00Z"},
		"C-art-d": {ConversationID: "C-art-d", State: threadintel.NoAction,
			Reason: threadintel.ReasonNoReplyExpected, LastAt: "2026-10-06T08:00:00Z"},
	}}
	closedSvc, err := New(Deps{Store: st, Priority: closedPriority, Config: cfg, Clock: clocktest.New(now)})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := closedSvc.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if closed.ThreadStates[string(threadintel.AwaitingOther)] != 0 ||
		closed.ThreadStates[string(threadintel.NoAction)] != 4 || len(closed.OpenThreads) != 1 ||
		closed.OpenThreads[0].ConversationID != "C-art-b" {
		t.Fatalf("closed-thread overlay states %+v open %+v", closed.ThreadStates, closed.OpenThreads)
	}

	// Without the priority provider the store's nil-signal state stands.
	plain, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(now)})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := plain.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	// Without classifier signals an owner-last closing message is not a
	// waiting thread: the store's structural read cannot invent an ask.
	if fallback.ThreadStates[string(threadintel.AwaitingOwner)] != 0 ||
		fallback.ThreadStates[string(threadintel.AwaitingOther)] != 0 ||
		fallback.ThreadStates[string(threadintel.NoAction)] != 5 {
		t.Fatalf("fallback states %+v", fallback.ThreadStates)
	}

	// Only the Atelier Nord event inside the requested window matches; the
	// Delta event and the later Atelier Nord review stay out. At the store
	// profile's own 30-day window the view's identity rule must agree with
	// the store's matcher exactly.
	if len(view.Events) != 1 || view.Events[0].EntryID != "ev-art" {
		t.Fatalf("30-day events %+v", view.Events)
	}
	stored, err := st.CompanyProfile(t.Context(), "c:atelier nord", time.Time{}, now, 12, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Events, stored.Events) {
		t.Fatalf("event identity diverges from the store: view %+v, store %+v", view.Events, stored.Events)
	}
	wide, err := svc.Profile(t.Context(), "c:atelier nord", 12, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.Events) != 2 || wide.Events[0].EntryID != "ev-art" || wide.Events[1].EntryID != "ev-art-later" {
		t.Fatalf("90-day events %+v", wide.Events)
	}
	narrow, err := svc.Profile(t.Context(), "c:atelier nord", 12, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow.Events) != 0 {
		t.Fatalf("1-day events %+v", narrow.Events)
	}

	// The due follow-up from Atelier Nord is the only deadline.
	if len(view.Deadlines) != 1 || view.Deadlines[0].MessageID != ids["art-c"] {
		t.Fatalf("deadlines %+v", view.Deadlines)
	}
}

func TestProfilesBatchMatchesSingleProfiles(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	states := map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, OwnerActorName: "Alice Martin",
			LastAt: "2026-10-21T09:00:00Z", Visibility: threadintel.Complete, Confidence: .85},
	}
	newService := func() (*Service, *fakePriority) {
		priority := &fakePriority{states: states}
		svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clocktest.New(now)})
		if err != nil {
			t.Fatal(err)
		}
		return svc, priority
	}
	keys := []string{"c:atelier nord", "d:delta.co"}

	batchSvc, batchPriority := newService()
	batch, err := batchSvc.Profiles(t.Context(), keys, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != len(keys) {
		t.Fatalf("batch returned %d profiles, want %d", len(batch), len(keys))
	}
	if batchPriority.callCount() != 1 {
		t.Fatalf("the batch read priority states %d times, want one", batchPriority.callCount())
	}

	for _, key := range keys {
		singleSvc, _ := newService()
		single, err := singleSvc.Profile(t.Context(), key, 12, 30)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(batch[key], single) {
			t.Fatalf("batch %q differs from the single profile:\nbatch  %+v\nsingle %+v", key, batch[key], single)
		}
	}

	// A second batch inside the freshness window is served from the cache.
	if _, err := batchSvc.Profiles(t.Context(), keys, 12, 30); err != nil {
		t.Fatal(err)
	}
	if priorityCalls := batchPriority.callCount(); priorityCalls != 1 {
		t.Fatalf("a fresh batch re-read priority states (%d calls)", priorityCalls)
	}
}

func TestProfileUnknownCompany(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Profile(t.Context(), "c:globex", 12, 30); !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("unknown key error %v", err)
	}
	if _, err := svc.Profile(t.Context(), "d:gmail.com", 12, 30); !errors.Is(err, ErrUnknownCompany) {
		t.Fatalf("shared domain key error %v", err)
	}
}

func TestProfileColdBuildIsSingleFlight(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	const n = 8
	now := worldTime(t, "2026-10-26T12:00:00Z")
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, LastAt: "2026-10-21T09:00:00Z"},
	}}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg,
		Clock: clocktest.NewBarrier(clocktest.New(now), n)})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := priority.hold()

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Profile(context.Background(), "c:atelier nord", 12, 30)
		}()
	}
	<-entered
	// Every caller has passed the clock barrier; let the rest reach the
	// cache before the one build finishes.
	for range 100 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	if got := priority.callCount(); got != 1 {
		t.Fatalf("%d builds for one cold profile, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

// A write that moves the generations while a profile build is blocked
// installs nothing; the next request builds again.
func TestProfileGenerationMoveDuringBuildInstallsNothing(t *testing.T) {
	st, cfg := openWorldStore(t)
	ids := seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	priority := &fakePriority{}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clocktest.New(now)})
	if err != nil {
		t.Fatal(err)
	}
	const key = "c:atelier nord"
	racedGen := st.MailGeneration()
	entered, release := priority.hold()
	type result struct {
		view ProfileView
		err  error
	}
	done := make(chan result, 1)
	go func() {
		view, err := svc.Profile(context.Background(), key, 12, 30)
		done <- result{view: view, err: err}
	}()
	<-entered

	if err := st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{
		{RowID: ids["art-a"], Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch},
	}); err != nil {
		t.Fatal(err)
	}
	if st.MailGeneration() == racedGen {
		t.Fatal("the write did not move the mail generation")
	}
	close(release)
	first := <-done
	if first.err != nil {
		t.Fatal(first.err)
	}

	probeErr := errors.New("probe")
	cacheKey := profileCacheKey(key, 12, 30)
	if _, err := svc.profiles.Get(context.Background(), cacheKey, func(context.Context) (profileValue, error) {
		return profileValue{}, probeErr
	}); !errors.Is(err, probeErr) {
		t.Fatalf("the raced profile was installed: %v", err)
	}

	if _, err := svc.Profile(t.Context(), key, 12, 30); err != nil {
		t.Fatal(err)
	}
	if got := priority.callCount(); got != 2 {
		t.Fatalf("the post-write profile builds: %d, want the raced one and one rebuild", got)
	}
}

// Profiles routes every unique key through the same cache: one cold batch
// builds all three exactly once, and a second call reads them from the cache.
func TestProfilesBuildsEachMissingProfileOnce(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	priority := &fakePriority{}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clocktest.New(now)})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"c:atelier nord", "d:delta.co", "c:solo llc"}
	views, err := svc.Profiles(t.Context(), keys, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != len(keys) {
		t.Fatalf("profiles %d, want %d", len(views), len(keys))
	}
	for _, key := range keys {
		if views[key].Key == "" {
			t.Fatalf("missing profile %q", key)
		}
	}
	if got := priority.callCount(); got != 1 {
		t.Fatalf("cold profiles built %d times, want the one batch", got)
	}
	if _, err := svc.Profiles(t.Context(), keys, 12, 30); err != nil {
		t.Fatal(err)
	}
	if got := priority.callCount(); got != 1 {
		t.Fatalf("a fresh Profiles call rebuilt: %d builds", got)
	}
}

func TestWorldCachesReuseFreshViews(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	priority := &fakePriority{}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}

	firstList, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if firstList.GeneratedAt != "2026-10-26T12:00:00Z" {
		t.Fatalf("generated_at %q", firstList.GeneratedAt)
	}
	clock.Advance(time.Second)
	secondList, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if secondList.GeneratedAt != firstList.GeneratedAt {
		t.Fatalf("fresh list rebuilt: %q then %q", firstList.GeneratedAt, secondList.GeneratedAt)
	}
	// A narrower request is sliced from the cached wider list, so changing
	// the page size never pays a second ranking read.
	clock.Advance(time.Second)
	page, err := svc.List(t.Context(), ListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.GeneratedAt != firstList.GeneratedAt || len(page.Companies) != 2 {
		t.Fatalf("limit 2 did not reuse the limit 10 entry: %+v", page)
	}

	firstProfile, err := svc.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	secondProfile, err := svc.Profile(t.Context(), "c:atelier nord", 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if priority.callCount() != 1 {
		t.Fatalf("fresh profile rebuilt: priority called %d times", priority.callCount())
	}
	if !reflect.DeepEqual(firstProfile, secondProfile) {
		t.Fatalf("cached profile diverged:\nfirst %+v\nsecond %+v", firstProfile, secondProfile)
	}
}

func TestListServesStaleThenRefreshes(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}

	first, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(cacheTTL + time.Second)
	stale, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GeneratedAt != first.GeneratedAt {
		t.Fatalf("an expired list rebuilt instead of serving stale: %q", stale.GeneratedAt)
	}
	refreshed := now.Add(cacheTTL + time.Second)
	waitFor(t, "list refresh", func() bool {
		state := readListCache(svc)
		return !state.refreshing && state.at.Equal(refreshed)
	})
	fresh, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := refreshed.UTC().Format(time.RFC3339); fresh.GeneratedAt != want {
		t.Fatalf("list not refreshed: %q, want %q", fresh.GeneratedAt, want)
	}
	if got := readListCache(svc); got.at != refreshed || got.refreshing {
		t.Fatalf("list cache state %+v", got)
	}
}

func TestListRefreshFailureKeepsStaleAndRetries(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	logs := &warnRecorder{}
	svc.log = slog.New(logs)

	first, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The store is the list's only input. Closing it makes every later build
	// fail immediately, so the stale path and the retry are deterministic.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(cacheTTL + time.Second)

	stale, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stale, first) {
		t.Fatalf("a failed refresh changed the served list: %+v", stale)
	}
	waitFor(t, "failed list refresh", func() bool {
		state := readListCache(svc)
		return !state.refreshing && state.at.Equal(now) && logs.warnings() == 1
	})
	// refreshing was cleared after the failure, so a later stale read starts
	// exactly one more attempt instead of staying stuck.
	if _, err := svc.List(t.Context(), ListQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second failed list refresh", func() bool {
		return !readListCache(svc).refreshing && logs.warnings() == 2
	})
	if state := readListCache(svc); !state.at.Equal(now) {
		t.Fatalf("a failed refresh replaced the stale entry: %+v", state)
	}
}

func TestProfileStaleWhileRevalidate(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, LastAt: "2026-10-21T09:00:00Z"},
		"C-art-d": {ConversationID: "C-art-d", State: threadintel.AwaitingOther,
			Reason: threadintel.ReasonOwnerLast, LastAt: "2026-10-06T08:00:00Z"},
	}}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	key := "c:atelier nord"
	first, err := svc.Profile(t.Context(), key, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if priority.callCount() != 1 || len(first.OpenThreads) != 2 {
		t.Fatalf("first profile: calls=%d open=%+v", priority.callCount(), first.OpenThreads)
	}

	// The refresh will resolve C-art-d as closed; the stale read must still
	// show the old two-thread view until the background build completes.
	priority.set(map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, LastAt: "2026-10-21T09:00:00Z"},
		"C-art-d": {ConversationID: "C-art-d", State: threadintel.NoAction,
			Reason: threadintel.ReasonNoReplyExpected, LastAt: "2026-10-06T08:00:00Z"},
	}, nil)
	entered, release := priority.hold()
	clock.Advance(cacheTTL + time.Second)

	type profileResult struct {
		view ProfileView
		err  error
	}
	done := make(chan profileResult, 1)
	go func() {
		view, err := svc.Profile(t.Context(), key, 12, 30)
		done <- profileResult{view: view, err: err}
	}()
	var got profileResult
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a stale profile blocked on its refresh")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.view.OpenThreads) != 2 {
		t.Fatalf("the stale view changed: %+v", got.view.OpenThreads)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the background profile refresh did not start")
	}
	if priority.callCount() != 2 {
		t.Fatalf("refresh calls %d, want 2", priority.callCount())
	}

	// A second stale read while the refresh is blocked must not start a
	// second build.
	again, err := svc.Profile(t.Context(), key, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.OpenThreads) != 2 || priority.callCount() != 2 {
		t.Fatalf("second stale read: open=%d calls=%d", len(again.OpenThreads), priority.callCount())
	}

	close(release)
	var fresh ProfileView
	waitFor(t, "profile refresh", func() bool {
		view, err := svc.Profile(t.Context(), key, 12, 30)
		if err != nil {
			return false
		}
		fresh = view
		return len(view.OpenThreads) == 1
	})
	if fresh.ThreadStates[string(threadintel.NoAction)] != 4 {
		t.Fatalf("the refreshed profile was not installed: open=%+v states=%+v",
			fresh.OpenThreads, fresh.ThreadStates)
	}
	if want := now.Add(cacheTTL + time.Second).UTC().Format(time.RFC3339); fresh.GeneratedAt != want {
		t.Fatalf("refreshed profile generated_at %q, want %q", fresh.GeneratedAt, want)
	}
	if priority.callCount() != 2 {
		t.Fatalf("the fresh read rebuilt: priority called %d times", priority.callCount())
	}
}

func TestProfileRefreshFailureKeepsStale(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-art-b": {ConversationID: "C-art-b", State: threadintel.AwaitingOwner,
			Reason: threadintel.ReasonInboundLast, LastAt: "2026-10-21T09:00:00Z"},
		"C-art-d": {ConversationID: "C-art-d", State: threadintel.AwaitingOther,
			Reason: threadintel.ReasonOwnerLast, LastAt: "2026-10-06T08:00:00Z"},
	}}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	key := "c:atelier nord"
	if _, err := svc.Profile(t.Context(), key, 12, 30); err != nil {
		t.Fatal(err)
	}

	priority.set(nil, errors.New("refresh failed"))
	clock.Advance(cacheTTL + time.Second)
	stale, err := svc.Profile(t.Context(), key, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.OpenThreads) != 2 {
		t.Fatalf("first read after the failure: %+v", stale.OpenThreads)
	}
	waitFor(t, "failed profile refresh", func() bool {
		return priority.callCount() == 2
	})
	again, err := svc.Profile(t.Context(), key, 12, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.OpenThreads) != 2 {
		t.Fatalf("a failed refresh changed the stale view: %+v", again.OpenThreads)
	}

	// The refresh flag was cleared: the next stale read starts one more
	// attempt instead of being stuck.
	waitFor(t, "second failed profile refresh", func() bool {
		view, err := svc.Profile(t.Context(), key, 12, 30)
		return err == nil && len(view.OpenThreads) == 2 && priority.callCount() == 3
	})
}

func TestWorldCachesInvalidateOnGeneration(t *testing.T) {
	st, cfg := openWorldStore(t)
	ids := seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	priority := &fakePriority{}
	svc, err := New(Deps{Store: st, Priority: priority, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	firstList, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	key := "c:atelier nord"
	if _, err := svc.Profile(t.Context(), key, 12, 30); err != nil {
		t.Fatal(err)
	}

	if err := st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{
		{RowID: ids["art-a"], Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch},
	}); err != nil {
		t.Fatal(err)
	}
	gen := st.Generation()
	clock.Advance(time.Second)

	stale, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GeneratedAt != firstList.GeneratedAt {
		t.Fatal("a generation change rebuilt the list synchronously")
	}
	refreshed := now.Add(time.Second)
	waitFor(t, "generation-triggered list refresh", func() bool {
		state := readListCache(svc)
		return state.gen == gen && !state.refreshing && state.at.Equal(refreshed)
	})
	if fresh, err := svc.List(t.Context(), ListQuery{Limit: 10}); err != nil || fresh.GeneratedAt != refreshed.Format(time.RFC3339) {
		t.Fatalf("fresh list after generation change: %+v, %v", fresh, err)
	}

	if _, err := svc.Profile(t.Context(), key, 12, 30); err != nil {
		t.Fatal(err)
	}
	refreshedAt := now.Add(time.Second).UTC().Format(time.RFC3339)
	waitFor(t, "generation-triggered profile refresh", func() bool {
		view, err := svc.Profile(t.Context(), key, 12, 30)
		return err == nil && view.GeneratedAt == refreshedAt
	})
	if priority.callCount() != 2 {
		t.Fatalf("generation change did not rebuild the profile: %d calls", priority.callCount())
	}
	if _, err := svc.Profile(t.Context(), key, 12, 30); err != nil {
		t.Fatal(err)
	}
	if priority.callCount() != 2 {
		t.Fatalf("the generation-fresh profile was rebuilt: %d calls", priority.callCount())
	}
}

func TestWorldCacheContract(t *testing.T) {
	// The cache contract is externally specified: a 30-second TTL, a
	// two-minute bound on background refreshes, at most 16 profiles, and a
	// profile key that names every input.
	if cacheTTL != 30*time.Second {
		t.Fatalf("cache TTL %v, want 30s", cacheTTL)
	}
	if cacheRefreshTimeout != 2*time.Minute {
		t.Fatalf("refresh timeout %v, want 2m", cacheRefreshTimeout)
	}
	if profileCacheSize != 16 {
		t.Fatalf("profile cache bound %d, want 16", profileCacheSize)
	}
	if got := profileCacheKey("c:atelier nord", 12, 30); got != "c:atelier nord|12|30" {
		t.Fatalf("profile cache key %q", got)
	}
}

func TestCompanyBoundsAreOwnedOnce(t *testing.T) {
	// These are aliases, not copies: the store owns the profile window, the
	// mailquery validator owns the company ranking limit, and the profile
	// cache owns the prefetch batch. If any pair drifts, this fails.
	if DefaultCompanyMonths != store.CompanyProfileDefaultMonths ||
		MaxCompanyMonths != store.CompanyProfileMaxMonths ||
		DefaultCompanyEventDays != store.CompanyProfileEventDaysDefault ||
		MaxCompanyEventDays != store.CompanyProfileEventDaysMax {
		t.Fatalf("profile bounds diverged from the store: months %d/%d days %d/%d",
			DefaultCompanyMonths, MaxCompanyMonths, DefaultCompanyEventDays, MaxCompanyEventDays)
	}
	if MaxCompanyLimit != mailquery.MaxCompanyLimit {
		t.Fatalf("company limit %d, mailquery owner %d", MaxCompanyLimit, mailquery.MaxCompanyLimit)
	}
	if MaxProfileBatch != profileCacheSize {
		t.Fatalf("profile batch %d, cache size %d", MaxProfileBatch, profileCacheSize)
	}
}

func TestProfileCacheIsBounded(t *testing.T) {
	st, cfg := openWorldStore(t)
	clk := clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	gen, routeGen := st.MailGeneration(), st.RouteGeneration()
	for i := 0; i <= profileCacheSize; i++ {
		clk.Advance(time.Second) // distinct ages: the oldest is unambiguous
		key := profileCacheKey(fmt.Sprintf("d:x%d.test", i), 12, 30)
		if _, err := svc.profiles.Get(t.Context(), key, func(context.Context) (profileValue, error) {
			return profileValue{gen: gen, routeGen: routeGen}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	probeErr := errors.New("probe")
	present := func(key string) bool {
		built := false
		_, err := svc.profiles.Get(t.Context(), key, func(context.Context) (profileValue, error) {
			built = true
			return profileValue{}, probeErr
		})
		return err == nil && !built
	}
	if present(profileCacheKey("d:x0.test", 12, 30)) {
		t.Fatal("the oldest profile entry was not evicted")
	}
	if !present(profileCacheKey(fmt.Sprintf("d:x%d.test", profileCacheSize), 12, 30)) {
		t.Fatal("the newest profile entry is missing")
	}
}

func TestValidCompanyKey(t *testing.T) {
	cases := map[string]bool{
		"c:atelier nord":                 true,
		"d:delta.co":                     true,
		"c:a":                            true,
		"x:atelier-nord":                 false,
		"atelier-nord":                   false,
		"c:":                             false,
		"c:" + string(rune(1)) + "x":     false,
		"c:" + string(make([]byte, 600)): false,
	}
	for key, want := range cases {
		if got := ValidCompanyKey(key); got != want {
			t.Errorf("ValidCompanyKey(%q) = %v, want %v", key, got, want)
		}
	}
}

// listCache is a lock-free snapshot for assertions.
type listCache struct {
	present    bool
	gen        uint64
	at         time.Time
	refreshing bool
}

func readListCache(svc *Service) listCache {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.list == nil {
		return listCache{}
	}
	return listCache{present: true, gen: svc.list.gen, at: svc.list.at, refreshing: svc.list.refreshing}
}

// warnRecorder counts the warnings one service logs, so a test can observe
// refresh failures without matching their wording.
type warnRecorder struct {
	mu      sync.Mutex
	counted int
}

func (r *warnRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *warnRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		r.mu.Lock()
		r.counted++
		r.mu.Unlock()
	}
	return nil
}

func (r *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *warnRecorder) WithGroup(string) slog.Handler { return r }

func (r *warnRecorder) warnings() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counted
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func worldPerson(list []store.CompanyPerson, address string) (store.CompanyPerson, bool) {
	for _, person := range list {
		if person.Address == address {
			return person, true
		}
	}
	return store.CompanyPerson{}, false
}

// TestListIncludesOneWayCompany is the screen-level guard for direction
// honesty: a company that only ever wrote to the owner is still a real
// organization, not a row to hide until the owner replies.
func TestListIncludesOneWayCompany(t *testing.T) {
	st, cfg := openWorldStore(t)
	ref := store.StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := st.SaveReported(t.Context(), ref, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "gamma", DisplayName: "Gamma News", Company: "Gamma", Emails: []string{"news@gamma.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	msg := worldRecv("gamma-in", "news@gamma.test", "Gamma News", "2026-10-20T09:00:00Z", "C-gamma", "Gamma update")
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

	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Companies {
		if row.Key != "c:gamma" {
			continue
		}
		if row.Messages != 1 || row.Sent != 0 || row.Received != 1 || row.Label != "Gamma" {
			t.Fatalf("one-way row %+v", row)
		}
		return
	}
	t.Fatalf("a received-only company is missing from %+v", result.Companies)
}

// TestEveryProfileListIsAnArray: a profile view's lists are arrays, never
// null, and the value is a copy so a returned view cannot be mutated through
// the store's slice. The six per-type helpers this replaced were character-for
// character the same function.
func TestEveryProfileListIsAnArray(t *testing.T) {
	lists := map[string]any{
		"strings":   nonNil([]string(nil)),
		"people":    nonNil([]store.CompanyPerson(nil)),
		"months":    nonNil([]store.CompanyMonth(nil)),
		"messages":  nonNil([]store.CompanyMessage(nil)),
		"events":    nonNil([]calendar.Event(nil)),
		"deadlines": nonNil([]agenda.Deadline(nil)),
	}
	for name, list := range lists {
		encoded, err := json.Marshal(list)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(encoded) != "[]" {
			t.Errorf("%s list marshals as %s, want []", name, encoded)
		}
	}

	// A non-empty list is copied, not aliased.
	in := []string{"acme.test"}
	out := nonNil(in)
	out[0] = "other.test"
	if in[0] != "acme.test" {
		t.Fatal("nonNil returned an alias of its input")
	}
}
