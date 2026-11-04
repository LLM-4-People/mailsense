package store

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func worldTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestContactsReplaceAndRead(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	list := []contacts.Contact{
		{EntryID: "c1", DisplayName: "Grace Hopper", Company: "Navy",
			Phone: "  +1 (555) 010-0100  ", Emails: []string{"Grace@Example.test", "mailto:grace@example.test", "bad"}},
		{EntryID: "c2", DisplayName: "Linus", Emails: []string{"linus@example.test"}},
	}
	if err := s.ReplaceContacts(t.Context(), ref, "", list); err != nil {
		t.Fatal(err)
	}
	got, err := s.Contacts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].DisplayName != "Grace Hopper" || len(got[0].Emails) != 1 ||
		got[0].Emails[0] != "grace@example.test" || got[0].Phone != "+1 (555) 010-0100" {
		t.Fatalf("contacts %+v", got)
	}
	// A second complete read replaces the address book: no stale contact.
	if err := s.ReplaceContacts(t.Context(), ref, "", list[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Contacts(t.Context())
	if len(got) != 1 || got[0].EntryID != "c1" {
		t.Fatalf("after replace %+v", got)
	}
	// A second address-book folder in the SAME store must not wipe the
	// default one (the live profile has an Imported folder).
	if err := s.ReplaceContacts(t.Context(), ref, "Imported",
		[]contacts.Contact{{EntryID: "c9", DisplayName: "Imported", FolderPath: "Imported",
			Emails: []string{"imported@example.test"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Contacts(t.Context())
	if len(got) != 2 {
		t.Fatalf("two folders of one store: %+v", got)
	}
	// Replacing one folder leaves the other intact.
	if err := s.ReplaceContacts(t.Context(), ref, "Imported", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Contacts(t.Context())
	if len(got) != 1 || got[0].EntryID != "c1" {
		t.Fatalf("after clearing Imported: %+v", got)
	}
	// Another store's contacts are visible only when it is picked.
	if err := s.ReplaceContacts(t.Context(), StoreRef{SourceID: "src", StoreID: "store-2"}, "",
		[]contacts.Contact{{EntryID: "c3", DisplayName: "Ada", Emails: []string{"ada@example.test"}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Contacts(t.Context())
	if len(got) != 2 {
		t.Fatalf("two picked stores: %+v", got)
	}
}

func TestCalendarWindowReplaceAndRead(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	cal := CalendarRef{EntryID: "cal1", Name: "Calendar", Default: true}
	window := func(a, b string) (time.Time, time.Time) { return worldTime(a), worldTime(b) }
	since, until := window("2026-11-01T00:00:00Z", "2026-12-01T00:00:00Z")
	events := []calendar.Event{
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "e1", Subject: "Kickoff",
			Start: worldTime("2026-11-03T14:00:00Z"), End: worldTime("2026-11-03T15:00:00Z"),
			Busy: calendar.Busy, Meeting: calendar.Meeting,
			Attendees: []calendar.Attendee{{Kind: calendar.Required, Name: "Client", Address: "client@example.test"}}},
		{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "e2", Subject: "Earlier",
			Start: worldTime("2026-11-02T09:00:00Z"), End: worldTime("2026-11-02T10:00:00Z"), Busy: calendar.Busy},
	}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, cal, since, until, events); err != nil {
		t.Fatal(err)
	}
	got, err := s.CalendarEvents(t.Context(), worldTime("2026-11-01T00:00:00Z"), until)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].EntryID != "e2" || got[1].EntryID != "e1" {
		t.Fatalf("events %+v", got)
	}
	if len(got[1].Attendees) != 1 || got[1].Attendees[0].Address != "client@example.test" {
		t.Fatalf("attendees %+v", got[1].Attendees)
	}
	// Re-reading the window replaces it: an occurrence that disappeared from
	// the source is gone.
	if err := s.ReplaceCalendarWindow(t.Context(), ref, cal, since, until, events[1:]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.CalendarEvents(t.Context(), since, until)
	if len(got) != 1 || got[0].EntryID != "e2" {
		t.Fatalf("after window replace %+v", got)
	}
	// The window slides forward with the clock: an occurrence wholly before
	// it is no longer covered by the source and must not be retained.
	later, laterUntil := window("2026-12-01T00:00:00Z", "2027-01-01T00:00:00Z")
	next := []calendar.Event{{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "e3",
		Subject: "Next", Start: worldTime("2026-12-02T09:00:00Z"), End: worldTime("2026-12-02T10:00:00Z"), Busy: calendar.Busy}}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, cal, later, laterUntil, next); err != nil {
		t.Fatal(err)
	}
	got, _ = s.CalendarEvents(t.Context(), since, laterUntil)
	if len(got) != 1 || got[0].EntryID != "e3" {
		t.Fatalf("after the window slid %+v", got)
	}
}

func TestDueDeadlinesAndThreadSummaries(t *testing.T) {
	s, _ := open(t, nil)
	m := mailtest.Message("src", "due1")
	m.SenderName, m.SenderAddress = "Client", "client@example.test"
	m.ConversationID = "C1"
	m.TaskDueDate = "2026-11-03T16:00:00Z"
	m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner},
		{Kind: mail.Cc, Name: "Colleague", Address: "colleague@example.test"}}
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	id := answerMessage(t, s, m)
	mailQueryVerdict(t, s, id, scope.Owner, scope.ReasonRecipientMatch)
	// A picked shared store can mark its own mailbox owner's mail as
	// verdict=owner; its subject and participants must not become a deadline.
	seedSharedStoreMessage(t, s)

	due, err := s.DueDeadlines(t.Context(), worldTime("2026-11-01T00:00:00Z"), worldTime("2026-11-05T00:00:00Z"))
	if err != nil || len(due) != 1 {
		t.Fatalf("deadlines %+v %v", due, err)
	}
	if due[0].MessageID != id || due[0].Subject != m.Subject || due[0].SenderName != "Client" {
		t.Fatalf("deadline %+v", due[0])
	}
	if len(due[0].Participants) != 2 {
		t.Fatalf("participants %+v", due[0].Participants)
	}

	sums, err := s.ThreadSummaries(t.Context(), []string{"C1"})
	if err != nil {
		t.Fatal(err)
	}
	sum := sums["C1"]
	if sum.Subject != m.Subject || sum.MessageID != id || len(sum.Participants) == 0 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestAgendaServiceOverStore(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	cal := CalendarRef{EntryID: "cal1", Name: "Calendar"}
	since, until := worldTime("2026-11-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z")
	ev := calendar.Event{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: "e1", Subject: "Kickoff",
		Start: worldTime("2026-11-03T14:00:00Z"), End: worldTime("2026-11-03T15:00:00Z"), Busy: calendar.Busy,
		Attendees: []calendar.Attendee{{Kind: calendar.Required, Address: "client@example.test"}}}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, cal, since, until, []calendar.Event{ev}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CalendarEvents(t.Context(), worldTime("2026-11-03T00:00:00Z"), worldTime("2026-11-04T00:00:00Z"))
	if err != nil || len(got) != 1 {
		t.Fatalf("agenda event %+v %v", got, err)
	}
}

func TestThreadSummariesPinsTheConversationIndex(t *testing.T) {
	s, _ := open(t, nil)
	plan := priorityQueryPlan(t, s, threadSummariesQuery(visibleSQL), jsonList([]string{"c1"}))
	if !strings.Contains(plan, "messages_conversation_instant") {
		t.Fatalf("the summary read does not follow the conversation instant index:\n%s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("the summary read sorts the conversation batch:\n%s", plan)
	}
	assertIndexOrderedPlan(t, "thread summaries", plan, "messages_conversation_instant")
}

// threadSummaryOldQuery is the previous statement shape, kept as the parity
// oracle: it read the plain conversation index and sorted every batch, and
// its Go fold kept the last row, which was the newest. The new read takes the
// first row of the newest-first index instead; both must name the same
// message, subject, instant and participant set.
func threadSummaryOldQuery(vis string) string {
	return `SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
		m.instant, ` + recipientsSQL + `
		FROM messages m INDEXED BY messages_conversation
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + vis + ` AND ` + ownerContentWhere + ` AND m.conversation_id IN (SELECT value FROM json_each(?))
			AND ` + threadContentRoleWhere + `
		ORDER BY m.conversation_id, m.instant, m.id`
}

// TestThreadSummariesKeepTheNewestMember seeds several messages per
// conversation, including equal instants and a later-inserted older message,
// and compares the summary read with the old ordered statement. The newest
// member must name the subject and instant, and the participants are the
// union of every eligible member regardless of row order.
func TestThreadSummariesKeepTheNewestMember(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if err := s.SaveReported(ctx, StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, conv, when, subject, sender string, recips ...mail.Recipient) int64 {
		m := mailtest.Message("src", id)
		m.ConversationID = conv
		m.Subject = subject
		m.SenderName, m.SenderAddress = sender, strings.ToLower(strings.ReplaceAll(sender, " ", "."))+"@example.org"
		m.ReceivedAt, m.SentOn = when, when
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
		m.Recipients = append(m.Recipients, recips...)
		row := answerMessage(t, s, m)
		mailQueryVerdict(t, s, row, scope.Owner, scope.ReasonRecipientMatch)
		return row
	}
	seed("one", "C1", "2026-10-01T09:00:00Z", "One", "Alice", mail.Recipient{Kind: mail.Cc, Address: "bob@example.org"})
	seed("two", "C1", "2026-10-03T09:00:00Z", "Two", "Carol", mail.Recipient{Kind: mail.Cc, Address: "dave@example.org"})
	tie := seed("three", "C1", "2026-10-03T09:00:00Z", "Three", "Erin", mail.Recipient{Kind: mail.Cc, Address: "frank@example.org"})
	// Inserted last, but older: a last-row-wins fold that ignored the order
	// would pick this one; the newest-first index and the seen guard do not.
	seed("four", "C1", "2026-10-02T09:00:00Z", "Four", "Gina", mail.Recipient{Kind: mail.Cc, Address: "henry@example.org"})
	seed("other", "C2", "2026-10-04T09:00:00Z", "Other", "Ivan")
	// A later shared-store member (C1) and a later draft (C1) must neither
	// name the summary nor add a participant.
	seedSharedStoreMessage(t, s)
	draft := mailtest.Message("src", "draft")
	draft.ConversationID = "C1"
	draft.FolderRole, draft.FolderPath = mail.RoleDrafts, "Drafts"
	draft.Subject, draft.SenderName, draft.SenderAddress = "Draft", "Zoe", "zoe@example.org"
	draft.ReceivedAt, draft.SentOn = "2026-10-30T09:00:00Z", "2026-10-30T09:00:00Z"
	draftID := answerMessage(t, s, draft)
	mailQueryVerdict(t, s, draftID, scope.Owner, scope.ReasonSentByOwner)

	ids := []string{"C1", "C2"}
	got, err := s.ThreadSummaries(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	oracle := threadSummariesLegacy(t, s, ids)
	if len(got) != len(oracle) {
		t.Fatalf("summaries %d, oracle %d", len(got), len(oracle))
	}
	for conv, want := range oracle {
		have := got[conv]
		if have.Subject != want.Subject || have.MessageID != want.MessageID || have.LastAt != want.LastAt ||
			strings.Join(have.Participants, ",") != strings.Join(want.Participants, ",") {
			t.Fatalf("%s summary %+v, oracle %+v", conv, have, want)
		}
	}
	if got["C1"].MessageID != tie || got["C1"].Subject != "Three" || got["C1"].LastAt != "2026-10-03T09:00:00Z" {
		t.Fatalf("C1 did not keep the newest member: %+v", got["C1"])
	}
	wantPeople := "alice@example.org,bob@example.org,carol@example.org,dave@example.org," +
		"erin@example.org,frank@example.org,gina@example.org,henry@example.org"
	if people := strings.Join(got["C1"].Participants, ","); people != wantPeople {
		t.Fatalf("C1 participants %q, want %q", people, wantPeople)
	}
	if _, ok := got["C3"]; ok {
		t.Fatalf("a conversation without owner-facing mail must be absent: %+v", got)
	}
}

// threadSummariesLegacy folds the old ordered statement with its last-row
// rule: the final row of the conversation names the summary. It is a test
// oracle only.
func threadSummariesLegacy(t *testing.T, s *Store, conversationIDs []string) map[string]agenda.ThreadSummary {
	t.Helper()
	rows, err := s.db.Query(threadSummaryOldQuery(visibleSQL), jsonList(conversationIDs))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]agenda.ThreadSummary{}
	people := map[string]map[string]bool{}
	owner, err := s.OwnerSet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var conv, subject, name, address, instant, recips string
		if err := rows.Scan(&id, &conv, &subject, &name, &address, &instant, &recips); err != nil {
			t.Fatal(err)
		}
		if conv == "" {
			continue
		}
		out[conv] = agenda.ThreadSummary{ConversationID: conv, Subject: subject, MessageID: id, LastAt: instant}
		if people[conv] == nil {
			people[conv] = map[string]bool{}
		}
		if a := contacts.Normalize(address); a != "" && !owner[a] {
			people[conv][a] = true
		}
		parsed, err := parseRecipients(recips)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range parsed {
			if a := contacts.Normalize(r.Address); a != "" && !owner[a] {
				people[conv][a] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for conv, set := range people {
		list := make([]string, 0, len(set))
		for a := range set {
			list = append(list, a)
		}
		sort.Strings(list)
		summary := out[conv]
		summary.Participants = list
		out[conv] = summary
	}
	return out
}

func TestDueDeadlinesPinsTheTaskDueIndex(t *testing.T) {
	s, _ := open(t, nil)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+dueDeadlinesQuery(visibleSQL), "2026-01-01T00:00:00Z", "2026-12-31T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if !strings.Contains(plan.String(), "messages_task_due") {
		t.Fatalf("the due read does not follow the task-due index:\n%s", plan.String())
	}
	if strings.Contains(plan.String(), "SCAN m") {
		t.Fatalf("the due read scans the mailbox:\n%s", plan.String())
	}
}

// One store may hold several calendars. Replacing one calendar's window
// removes only that calendar's stale occurrences; a sibling calendar's
// events stay.
func TestCalendarWindowReplaceKeepsSiblingCalendars(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	since, until := worldTime("2026-11-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z")
	event := func(entry, subject, start string) calendar.Event {
		at := worldTime(start)
		return calendar.Event{SourceID: "src", StoreKey: source.StoreKey("src", "store-1"), EntryID: entry, Subject: subject,
			Start: at, End: at.Add(time.Hour), Busy: calendar.Busy}
	}
	if err := s.ReplaceCalendarWindow(ctx, ref, CalendarRef{EntryID: "cal1", Name: "One"}, since, until,
		[]calendar.Event{event("e1", "One", "2026-11-03T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCalendarWindow(ctx, ref, CalendarRef{EntryID: "cal2", Name: "Two"}, since, until,
		[]calendar.Event{event("e2", "Two", "2026-11-04T10:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	// A later, empty window of the first calendar must not touch the second.
	if err := s.ReplaceCalendarWindow(ctx, ref, CalendarRef{EntryID: "cal1", Name: "One"}, since, until, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.CalendarEvents(ctx, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EntryID != "e2" {
		t.Fatalf("a sibling calendar's events were removed: %+v", got)
	}
}

// The unique key does not include calendar_id: syncing one calendar can
// conflict with a row another calendar owns. The upsert must return that
// row's id (RETURNING id), not the connection's previous LastInsertId, or the
// wrong event loses its attendees.
func TestCalendarConflictUpsertKeepsAttendees(t *testing.T) {
	s, _ := open(t, nil)
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	since, until := worldTime("2026-11-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z")
	key := source.StoreKey("src", "store-1")
	sharedStart := worldTime("2026-11-05T14:00:00Z")
	attendee := func(name string) []calendar.Attendee {
		return []calendar.Attendee{{Kind: calendar.Required, Name: name, Address: name + "@example.test"}}
	}
	// Calendar A owns the row the later upsert will target.
	target := calendar.Event{SourceID: "src", StoreKey: key, EntryID: "shared", Subject: "Target",
		Start: sharedStart, End: sharedStart.Add(time.Hour), Busy: calendar.Busy, Attendees: attendee("target")}
	if err := s.ReplaceCalendarWindow(t.Context(), ref, CalendarRef{EntryID: "calA", Name: "A"}, since, until,
		[]calendar.Event{target}); err != nil {
		t.Fatal(err)
	}
	// Calendar B's window does not delete A's row. Inserting "first" before
	// the conflicting "second" makes the old LastInsertId point at "first".
	first := calendar.Event{SourceID: "src", StoreKey: key, EntryID: "first", Subject: "First",
		Start: worldTime("2026-11-04T09:00:00Z"), End: worldTime("2026-11-04T10:00:00Z"),
		Busy: calendar.Busy, Attendees: attendee("first")}
	second := target
	second.Subject = "Second"
	second.Attendees = attendee("second")
	if err := s.ReplaceCalendarWindow(t.Context(), ref, CalendarRef{EntryID: "calB", Name: "B"}, since, until,
		[]calendar.Event{first, second}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CalendarEvents(t.Context(), since, until)
	if err != nil {
		t.Fatal(err)
	}
	bySubject := map[string]calendar.Event{}
	for _, event := range got {
		bySubject[event.Subject] = event
	}
	check := func(subject, address string) {
		t.Helper()
		event, ok := bySubject[subject]
		if !ok {
			t.Fatalf("%q missing from %+v", subject, got)
		}
		if len(event.Attendees) != 1 || event.Attendees[0].Address != address+"@example.test" {
			t.Fatalf("%q attendees %+v", subject, event.Attendees)
		}
	}
	check("First", "first")
	check("Second", "second")
}

// TestCalendarAttendeesReadOnlyTheWindow seeds an out-of-window calendar and
// an in-window one. Reading the window must not visit the out-of-window
// event's attendee rows: the sentinel attendee would be read by a query that
// joined every visible calendar event.
func TestCalendarAttendeesReadOnlyTheWindow(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	key := source.StoreKey("src", "store-1")
	event := func(entry, subject, start string, addresses ...string) calendar.Event {
		at := worldTime(start)
		e := calendar.Event{SourceID: "src", StoreKey: key, EntryID: entry, Subject: subject,
			Start: at, End: at.Add(time.Hour), Busy: calendar.Busy}
		for _, address := range addresses {
			e.Attendees = append(e.Attendees, calendar.Attendee{Kind: calendar.Required,
				Name: address, Address: address})
		}
		return e
	}
	outOfWindow := event("old", "Out of the window", "2020-01-02T09:00:00Z", "sentinel-out@example.test")
	if err := s.ReplaceCalendarWindow(ctx, ref, CalendarRef{EntryID: "old", Name: "Old"},
		worldTime("2020-01-01T00:00:00Z"), worldTime("2021-01-01T00:00:00Z"),
		[]calendar.Event{outOfWindow}); err != nil {
		t.Fatal(err)
	}
	inWindow := event("now", "In the window", "2026-11-15T09:00:00Z", "sentinel-in@example.test")
	since, until := worldTime("2026-11-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z")
	if err := s.ReplaceCalendarWindow(ctx, ref, CalendarRef{EntryID: "now", Name: "Now"}, since, until,
		[]calendar.Event{inWindow}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CalendarEvents(ctx, since, until)
	if err != nil || len(got) != 1 || got[0].EntryID != "now" || len(got[0].Attendees) != 1 ||
		got[0].Attendees[0].Address != "sentinel-in@example.test" {
		t.Fatalf("window events %+v: %v", got, err)
	}
	var inID, outID int64
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM calendar_events WHERE entry_id = 'now'").Scan(&inID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM calendar_events WHERE entry_id = 'old'").Scan(&outID); err != nil {
		t.Fatal(err)
	}
	// The read takes the window's ids. The out-of-window event's attendee is
	// never a key of the result, so it was not read.
	atts, err := s.attendees(ctx, []int64{inID})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 || len(atts[inID]) != 1 || atts[inID][0].Address != "sentinel-in@example.test" {
		t.Fatalf("attendees %+v", atts)
	}
	if _, ok := atts[outID]; ok {
		t.Fatalf("the out-of-window event's attendees were read: %+v", atts)
	}
	plan := priorityQueryPlan(t, s, attendeesQuery, idSet([]int64{inID}))
	if !strings.Contains(plan, "event_attendees_event (event_id=?)") {
		t.Fatalf("the attendee read is not an event_attendees_event range:\n%s", plan)
	}
	if strings.Contains(plan, "calendar_events") || strings.Contains(plan, "SCAN a ") {
		t.Fatalf("the attendee read walks the calendar or the attendee table:\n%s", plan)
	}
}

// TestParticipantProjectionsAgree pins the one per-message participant rule:
// the thread page, the agenda's thread summary and its deadline list must name
// the same external addresses for the same conversation, with duplicate and
// differently-cased recipients collapsed and the owner's own identities
// removed. They used to derive the set separately, so one view could leak an
// owner address or miss a Cc.
func TestParticipantProjectionsAgree(t *testing.T) {
	s, _ := open(t, nil)
	first := mailtest.Message("src", "agree-1")
	first.SenderName, first.SenderAddress = "Client", "Client@Example.test"
	first.ConversationID = "C-agree"
	first.TaskDueDate = "2026-11-03T16:00:00Z"
	first.Recipients = []mail.Recipient{
		{Kind: mail.To, Name: "Owner", Address: mailtest.Owner},
		{Kind: mail.To, Name: "Colleague", Address: "colleague@example.test"},
		{Kind: mail.Cc, Name: "Colleague again", Address: "COLLEAGUE@example.test"},
		{Kind: mail.Cc, Name: "Empty", Address: "   "},
	}
	second := mailtest.Message("src", "agree-2")
	second.SenderName, second.SenderAddress = "Colleague", "colleague@example.test"
	second.ConversationID = "C-agree"
	second.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
	// A conversation whose only external participant is a recipient: the
	// summary must name it even though the owner sent the message.
	recipientOnly := mailtest.Message("src", "agree-3")
	recipientOnly.SenderName, recipientOnly.SenderAddress = "Owner", mailtest.Owner
	recipientOnly.ConversationID = "C-agree-cc"
	recipientOnly.Recipients = []mail.Recipient{{Kind: mail.Cc, Name: "Dana", Address: "Dana@Example.test"}}
	if err := s.SaveReported(t.Context(), StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []mail.Message{first, second} {
		mailQueryVerdict(t, s, answerMessage(t, s, m), scope.Owner, scope.ReasonRecipientMatch)
	}
	mailQueryVerdict(t, s, answerMessage(t, s, recipientOnly), scope.Owner, scope.ReasonSentByOwner)
	want := []string{"client@example.test", "colleague@example.test"}

	thread, err := s.ThreadMessages(t.Context(), "C-agree", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	threadSet := make([]string, 0, len(thread.Participants))
	for _, p := range thread.Participants {
		threadSet = append(threadSet, p.Address)
	}
	sort.Strings(threadSet)
	if strings.Join(threadSet, ",") != strings.Join(want, ",") {
		t.Fatalf("thread participants %v, want %v", threadSet, want)
	}

	sums, err := s.ThreadSummaries(t.Context(), []string{"C-agree", "C-agree-cc"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sums["C-agree"].Participants; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("summary participants %v, want %v", got, want)
	}
	if got := sums["C-agree-cc"].Participants; strings.Join(got, ",") != "dana@example.test" {
		t.Fatalf("recipient-only summary participants %v, want dana@example.test", got)
	}

	due, err := s.DueDeadlines(t.Context(), worldTime("2026-11-01T00:00:00Z"), worldTime("2026-11-05T00:00:00Z"))
	if err != nil || len(due) != 1 {
		t.Fatalf("deadlines %+v %v", due, err)
	}
	if got := due[0].Participants; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("deadline participants %v, want %v", got, want)
	}
}
