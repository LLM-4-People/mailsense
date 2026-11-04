package outlook

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
)

// TestCalendarReadCompletenessSignal is the table of what makes a calendar
// read incomplete: a bad appointment or series does, a deleted occurrence
// does not.
func TestCalendarReadCompletenessSignal(t *testing.T) {
	since, until := localDay(2026, 11, 1), localDay(2026, 11, 6)
	valid := func(id string) *fakeItem {
		return eventItem(id, "Kickoff", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z")
	}
	malformed := &fakeItem{props: map[string]any{"EntryID": "e-bad", "Subject": "Bad"}}
	unsupported := recurringItem("series-bad", "Bad series", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(4, "2026-11-02T14:00:00Z", nil))
	lookupFailure := recurringItem("series-lookup", "Lookup", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(recurDaily, "2026-11-02T14:00:00Z", nil))
	lookupFailure.failOccurrence = hrInvalidArg
	deleted := recurringItem("series-deleted", "Deleted", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(recurDaily, "2026-11-02T14:00:00Z", map[string]any{
			"NoEndDate": false, "Occurrences": int64(2),
			"PatternEndDate": DateOf(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		}))
	deleted.exceptions = []fakeException{{original: wallClockOf(t, "2026-11-03T14:00:00Z"), deleted: true}}

	cases := []struct {
		name           string
		items          []*fakeItem
		wantEvents     int
		wantSkipped    int
		wantSeries     int
		wantIncomplete bool
	}{
		{"complete ordinary", []*fakeItem{valid("e-complete")}, 1, 0, 0, false},
		{"deleted occurrence is complete", []*fakeItem{deleted}, 1, 0, 0, false},
		{"unsupported series", []*fakeItem{unsupported, valid("e-with-series")}, 1, 0, 1, true},
		{"occurrence lookup failure", []*fakeItem{lookupFailure}, 0, 0, 1, true},
		{"appointment conversion failure", []*fakeItem{malformed, valid("e-with-bad")}, 1, 1, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, events, logs, err := readCalendarItems(t, c.items, since, until)
			var incomplete *IncompleteCalendarError
			gotIncomplete := errors.As(err, &incomplete)
			if gotIncomplete != c.wantIncomplete {
				t.Fatalf("incomplete = %v (%v), want %v\n%s", gotIncomplete, err, c.wantIncomplete, logs)
			}
			if gotIncomplete && (incomplete.SkippedEvents != c.wantSkipped || incomplete.SkippedSeries != c.wantSeries) {
				t.Fatalf("skips = %d events / %d series, want %d / %d\n%s",
					incomplete.SkippedEvents, incomplete.SkippedSeries, c.wantSkipped, c.wantSeries, logs)
			}
			if !gotIncomplete && err != nil {
				t.Fatalf("complete read failed: %v\n%s", err, logs)
			}
			if len(events) != c.wantEvents {
				t.Fatalf("events %v, want %d\n%s", eventStarts(events), c.wantEvents, logs)
			}
			if c.wantSeries > 0 && strings.Count(logs, "calendar: series skipped") != c.wantSeries {
				t.Fatalf("%d series warnings, want %d\n%s", strings.Count(logs, "calendar: series skipped"), c.wantSeries, logs)
			}
		})
	}
}

// addCalendar installs one default calendar in the primary store.
func addCalendar(o *fakeOutlook, items ...*fakeItem) *fakeFolder {
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal-primary"), class: "IPF.Appointment", itemType: 1, items: items}
	o.stores[1].defaults[9] = cal
	o.stores[1].root.children = append(o.stores[1].root.children, cal)
	return cal
}

// TestIncompleteCalendarReadKeepsStoredEvents: an unreadable series must not
// delete the occurrences already stored for it, while the readable event is
// upserted and the sync report says the window is incomplete.
func TestIncompleteCalendarReadKeepsStoredEvents(t *testing.T) {
	o := profile()
	master := recurringItem("series-keep", "Standup", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(recurDaily, "2026-11-02T14:00:00Z", map[string]any{
			"NoEndDate": false, "Occurrences": int64(2),
			"PatternEndDate": DateOf(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		}))
	valid := eventItem("e-valid", "Kickoff", "2026-11-03T16:00:00Z", "2026-11-03T17:00:00Z")
	addCalendar(o, master, valid)
	c, s, st := syncRig(t, o)

	first := run(t, s, c)
	if first.World == nil || first.World.CalendarCollections != 1 || first.World.CalendarIncomplete != 0 {
		t.Fatalf("complete read report %+v", first.World)
	}
	window := func() []calendar.Event {
		t.Helper()
		events, err := st.CalendarEvents(t.Context(), localDay(2026, 11, 1), localDay(2026, 11, 5))
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	if got := window(); len(got) != 3 {
		t.Fatalf("first read stored %d events, want 3", len(got))
	}

	master.props["RecurrenceType"] = int64(4) // unsupported: the series is skipped
	valid.props["Subject"] = "Kickoff updated"
	second := run(t, s, c)
	if second.World == nil || second.World.CalendarCollections != 0 || second.World.CalendarIncomplete != 1 ||
		second.World.Events != 1 {
		t.Fatalf("incomplete read report %+v", second.World)
	}
	var found bool
	for _, cr := range second.World.Collections {
		if cr.Kind != "calendar" {
			continue
		}
		found = true
		if !cr.Incomplete || cr.SkippedSeries != 1 || cr.SkippedEvents != 0 || cr.Items != 1 || cr.Error != "" {
			t.Fatalf("calendar collection report %+v", cr)
		}
	}
	if !found {
		t.Fatalf("no calendar report: %+v", second.World.Collections)
	}
	got := window()
	if len(got) != 3 {
		t.Fatalf("incomplete read left %d events, want the 3 stored occurrences", len(got))
	}
	series, updated := 0, false
	for _, e := range got {
		if e.SeriesID == "G-series-keep" {
			series++
		}
		if e.EntryID == "e-valid" {
			updated = e.Subject == "Kickoff updated"
		}
	}
	if series != 2 || !updated {
		t.Fatalf("stored events after the partial read: %+v", got)
	}

	// The series is gone from the source now: the next complete read is
	// allowed to replace the window and remove its stale occurrences.
	cal := o.stores[1].defaults[9]
	cal.items = []*fakeItem{valid}
	third := run(t, s, c)
	if third.World == nil || third.World.CalendarCollections != 1 || third.World.CalendarIncomplete != 0 {
		t.Fatalf("recovery read report %+v", third.World)
	}
	if got := window(); len(got) != 1 || got[0].EntryID != "e-valid" {
		t.Fatalf("after the complete recovery read: %+v", got)
	}
}

// TestIncompleteCalendarReadFromAppointmentFailure: one item that cannot be
// converted is a partial read too, and it preserves the other old events.
func TestIncompleteCalendarReadFromAppointmentFailure(t *testing.T) {
	o := profile()
	cal := addCalendar(o, eventItem("e-old", "Old", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z"))
	c, s, st := syncRig(t, o)
	run(t, s, c)

	bad := &fakeItem{props: map[string]any{"EntryID": "e-bad", "Subject": "Bad"}}
	fresh := eventItem("e-new", "New", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z")
	cal.items = []*fakeItem{bad, fresh}
	rep := run(t, s, c)
	if rep.World == nil || rep.World.CalendarIncomplete != 1 || rep.World.CalendarCollections != 0 || rep.World.Events != 1 {
		t.Fatalf("incomplete read report %+v", rep.World)
	}
	for _, cr := range rep.World.Collections {
		if cr.Kind == "calendar" && (!cr.Incomplete || cr.SkippedEvents != 1 || cr.SkippedSeries != 0) {
			t.Fatalf("calendar collection report %+v", cr)
		}
	}
	got, err := st.CalendarEvents(t.Context(), localDay(2026, 11, 1), localDay(2026, 11, 5))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.EntryID] = true
	}
	if len(got) != 2 || !ids["e-old"] || !ids["e-new"] || ids["e-bad"] {
		t.Fatalf("stored events after a conversion failure: %+v", got)
	}
}

// TestCompleteCalendarReadRemovesVanishedEvents: the next complete read still
// replaces the window, so an event that truly vanished is deleted.
func TestCompleteCalendarReadRemovesVanishedEvents(t *testing.T) {
	o := profile()
	keep := eventItem("e-keep", "Keep", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z")
	gone := eventItem("e-gone", "Gone", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z")
	cal := addCalendar(o, keep, gone)
	c, s, st := syncRig(t, o)
	run(t, s, c)

	cal.items = []*fakeItem{keep}
	rep := run(t, s, c)
	if rep.World == nil || rep.World.CalendarCollections != 1 || rep.World.CalendarIncomplete != 0 {
		t.Fatalf("complete read report %+v", rep.World)
	}
	got, err := st.CalendarEvents(t.Context(), localDay(2026, 11, 1), localDay(2026, 11, 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EntryID != "e-keep" {
		t.Fatalf("after a complete read: %+v", got)
	}
}

// TestDeletedOccurrenceDoesNotMakeCalendarReadIncomplete: an absence Outlook
// reports through GetOccurrence is a complete answer, not a skipped series.
func TestDeletedOccurrenceDoesNotMakeCalendarReadIncomplete(t *testing.T) {
	o := profile()
	master := recurringItem("series-deleted", "Deleted", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(recurDaily, "2026-11-02T14:00:00Z", map[string]any{
			"NoEndDate": false, "Occurrences": int64(2),
			"PatternEndDate": DateOf(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
		}))
	master.exceptions = []fakeException{{original: wallClockOf(t, "2026-11-03T14:00:00Z"), deleted: true}}
	addCalendar(o, master)
	c, s, st := syncRig(t, o)
	rep := run(t, s, c)
	if rep.World == nil || rep.World.CalendarCollections != 1 || rep.World.CalendarIncomplete != 0 {
		t.Fatalf("deleted occurrence report %+v", rep.World)
	}
	got, err := st.CalendarEvents(t.Context(), localDay(2026, 11, 1), localDay(2026, 11, 5))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored deleted-occurrence series: %+v", got)
	}
}

// readCalendarItems runs one calendar folder with several items, like
// syncOneSeriesRead but without the single-master restriction.
func readCalendarItems(t *testing.T, items []*fakeItem, since, until time.Time) (*fakeOutlook, []calendar.Event, string, error) {
	t.Helper()
	o := profile()
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal-many"), class: "IPF.Appointment", itemType: 1}
	cal.items = items
	o.stores[1].defaults[9] = cal
	o.stores[1].root.children = append(o.stores[1].root.children, cal)
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, err := c.CalendarCollections(t.Context())
	if err != nil || len(cols) != 1 {
		t.Fatalf("collections %+v: %v", cols, err)
	}
	var buf bytes.Buffer
	c.log = slog.New(slog.NewJSONHandler(&buf, nil))
	sink := &calendarSink{}
	err = c.SyncCalendar(t.Context(), cols[0], since, until, sink)
	return o, sink.list, buf.String(), err
}
