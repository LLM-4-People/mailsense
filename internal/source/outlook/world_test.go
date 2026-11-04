package outlook

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/source"
)

type testContactSink struct {
	list     []contacts.Contact
	progress []source.Progress
}

func (s *testContactSink) Contact(_ context.Context, c contacts.Contact) error {
	s.list = append(s.list, c)
	return nil
}

func (s *testContactSink) Progress(p source.Progress) { s.progress = append(s.progress, p) }

// calDate is the built-in date value Outlook reports for an instant: the
// owner's local wall clock (Date.LocalInstant reverses it in calTime), not
// the UTC fields of the instant.
func calDate(s string) Date {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return DateOf(t.In(time.Local))
}

func eventItem(id, subject, start, end string) *fakeItem {
	return &fakeItem{props: map[string]any{
		"EntryID": id, "Subject": subject, "Start": calDate(start), "End": calDate(end),
		"CleanGlobalObjectId": "G-" + id, "GlobalAppointmentID": "G-" + id,
		"BusyStatus": int64(2), "MeetingStatus": int64(1), "ResponseStatus": int64(3),
		"Location": "Room A", "Organizer": "Client",
	}}
}

func TestCalendarCollectionsAndWindow(t *testing.T) {
	o := profile()
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal-primary"), class: "IPF.Appointment", itemType: 1}
	cal.items = []*fakeItem{
		eventItem("e1", "Kickoff", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z"),
		eventItem("e2", "Too late", "2027-03-03T14:00:00Z", "2027-03-03T15:00:00Z"),
	}
	o.stores[1].defaults[9] = cal
	o.stores[1].root.children = append(o.stores[1].root.children, cal)
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, err := c.CalendarCollections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].StoreID != "S-PRIMARY" || cols[0].Name != "Calendrier" {
		t.Fatalf("collections %+v", cols)
	}
	sink := &calendarSink{}
	since, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	until, _ := time.Parse(time.RFC3339, "2026-12-01T00:00:00Z")
	if err := c.SyncCalendar(t.Context(), cols[0], since, until, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.list) != 1 || sink.list[0].EntryID != "e1" || sink.list[0].Subject != "Kickoff" {
		t.Fatalf("events %+v", sink.list)
	}
	if !sink.list[0].Start.Equal(time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC)) || sink.list[0].Busy != calendar.Busy ||
		sink.list[0].Response != calendar.Accepted {
		t.Fatalf("event %+v", sink.list[0])
	}
	if !o.callsContain("Items.Restrict") {
		t.Fatal("the calendar was not restricted to the sync window")
	}
	if o.callsContain("Items.Put:IncludeRecurrences") {
		t.Fatal("the unbounded recurrence expansion must not be used")
	}
}

type calendarSink struct{ list []calendar.Event }

func (s *calendarSink) Event(_ context.Context, e calendar.Event) error {
	s.list = append(s.list, e)
	return nil
}

func (s *calendarSink) Progress(source.Progress) {}

func TestContactCollectionsAndRead(t *testing.T) {
	o := profile()
	book := &fakeFolder{name: "Contacts", eid: hexID("contacts-primary"), class: "IPF.Contact", itemType: 2}
	book.items = []*fakeItem{
		{props: map[string]any{"EntryID": "c1", propDisplayName: "Grace Hopper",
			"Email1Address": "grace@example.test", propCompanyName: "Navy",
			propBusinessPhone: "+1 555 010 0100", propMobilePhone: "+1 555 010 0199",
			propLastModified: DateOf(t0)}},
		{props: map[string]any{"EntryID": "c2", propDisplayName: "No address", propMobilePhone: "+1 555 010 0200"}},
	}
	o.stores[1].defaults[10] = book
	o.stores[1].root.children = append(o.stores[1].root.children, book)
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, err := c.ContactCollections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].EntryID != hexID("contacts-primary") {
		t.Fatalf("collections %+v", cols)
	}
	sink := &testContactSink{}
	if err := c.SyncContacts(t.Context(), cols[0], sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.list) != 2 {
		t.Fatalf("contacts %+v", sink.list)
	}
	if sink.list[0].DisplayName != "Grace Hopper" || len(sink.list[0].Emails) != 1 ||
		sink.list[0].Emails[0] != "grace@example.test" || sink.list[0].Phone != "+1 555 010 0100" {
		t.Fatalf("contact %+v", sink.list[0])
	}
	if sink.list[1].Phone != "+1 555 010 0200" {
		t.Fatalf("mobile fallback %+v", sink.list[1])
	}
}

// callsContain reports whether the fake recorded a member call.
func (o *fakeOutlook) callsContain(want string) bool {
	for _, c := range o.calls {
		if c == want {
			return true
		}
	}
	return false
}
