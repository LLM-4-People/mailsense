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

// mkDate parses the civil (wall-clock) form the recurrence engine uses.
func mkDate(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v.UTC()
}

func mkDateList(t *testing.T, ss []string) []time.Time {
	t.Helper()
	out := make([]time.Time, len(ss))
	for i, s := range ss {
		out[i] = mkDate(t, s)
	}
	return out
}

// wallClockOf is an instant in the civil form the recurrence engine and
// GetOccurrence use: the owner's local wall clock carried in a UTC time.Time.
func wallClockOf(t *testing.T, s string) time.Time {
	t.Helper()
	return wallClock(mkDate(t, s))
}

// TestRecurrencePatternDates covers every recurrence type the connector
// expands, plus the window, end-date, occurrence-count and month-end rules.
func TestRecurrencePatternDates(t *testing.T) {
	cases := []struct {
		name         string
		p            recurrencePattern
		since, until string
		want         []string
	}{
		{
			name:  "daily",
			p:     recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-11-01T09:00:00Z")},
			since: "2026-11-03T00:00:00Z", until: "2026-11-07T00:00:00Z",
			want: []string{"2026-11-03T09:00:00Z", "2026-11-04T09:00:00Z", "2026-11-05T09:00:00Z", "2026-11-06T09:00:00Z"},
		},
		{
			name:  "daily every third day",
			p:     recurrencePattern{kind: recurDaily, interval: 3, start: mkDate(t, "2026-11-01T09:00:00Z")},
			since: "2026-11-03T00:00:00Z", until: "2026-11-13T00:00:00Z",
			want: []string{"2026-11-04T09:00:00Z", "2026-11-07T09:00:00Z", "2026-11-10T09:00:00Z"},
		},
		{
			name: "daily every weekday",
			p: recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-11-06T09:00:00Z"),
				mask: dayMonday | dayTuesday | dayWednesday | dayThursday | dayFriday},
			since: "2026-11-06T00:00:00Z", until: "2026-11-17T00:00:00Z",
			want: []string{"2026-11-06T09:00:00Z", "2026-11-09T09:00:00Z", "2026-11-10T09:00:00Z",
				"2026-11-11T09:00:00Z", "2026-11-12T09:00:00Z", "2026-11-13T09:00:00Z",
				"2026-11-16T09:00:00Z"},
		},
		{
			name: "daily every weekday counts weekends out of the occurrence limit",
			p: recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-11-06T09:00:00Z"),
				mask: dayMonday | dayTuesday | dayWednesday | dayThursday | dayFriday, count: 3},
			since: "2026-11-09T00:00:00Z", until: "2026-11-30T00:00:00Z",
			want: []string{"2026-11-09T09:00:00Z", "2026-11-10T09:00:00Z"}, // Nov 6, 9, 10 are the series
		},
		{
			name: "weekly monday wednesday friday",
			p: recurrencePattern{kind: recurWeekly, interval: 1, start: mkDate(t, "2026-11-02T14:00:00Z"),
				mask: dayMonday | dayWednesday | dayFriday},
			since: "2026-11-04T00:00:00Z", until: "2026-11-12T00:00:00Z",
			want: []string{"2026-11-04T14:00:00Z", "2026-11-06T14:00:00Z", "2026-11-09T14:00:00Z", "2026-11-11T14:00:00Z"},
		},
		{
			name: "weekly every second week on tuesday and thursday",
			p: recurrencePattern{kind: recurWeekly, interval: 2, start: mkDate(t, "2026-11-03T10:00:00Z"),
				mask: dayTuesday | dayThursday},
			since: "2026-11-01T00:00:00Z", until: "2026-12-05T00:00:00Z",
			want: []string{"2026-11-03T10:00:00Z", "2026-11-05T10:00:00Z", "2026-11-17T10:00:00Z",
				"2026-11-19T10:00:00Z", "2026-12-01T10:00:00Z", "2026-12-03T10:00:00Z"},
		},
		{
			name: "weekly start on saturday with a sunday mask",
			p: recurrencePattern{kind: recurWeekly, interval: 1, start: mkDate(t, "2026-11-07T09:00:00Z"),
				mask: daySunday, count: 2},
			since: "2026-11-09T00:00:00Z", until: "2026-11-30T00:00:00Z",
			want: []string{"2026-11-15T09:00:00Z"}, // Nov 8 is the first occurrence, before the window
		},
		{
			name:  "monthly by day of month",
			p:     recurrencePattern{kind: recurMonthly, interval: 1, start: mkDate(t, "2026-01-15T10:00:00Z"), day: 15},
			since: "2026-03-01T00:00:00Z", until: "2026-06-01T00:00:00Z",
			want: []string{"2026-03-15T10:00:00Z", "2026-04-15T10:00:00Z", "2026-05-15T10:00:00Z"},
		},
		{
			name:  "monthly day 31 clamps to the month end",
			p:     recurrencePattern{kind: recurMonthly, interval: 1, start: mkDate(t, "2026-01-31T10:00:00Z"), day: 31},
			since: "2026-02-01T00:00:00Z", until: "2026-05-01T00:00:00Z",
			want: []string{"2026-02-28T10:00:00Z", "2026-03-31T10:00:00Z", "2026-04-30T10:00:00Z"},
		},
		{
			name: "monthly second tuesday",
			p: recurrencePattern{kind: recurMonthNth, interval: 1, start: mkDate(t, "2026-01-13T10:00:00Z"),
				instance: 2, mask: dayTuesday},
			since: "2026-02-01T00:00:00Z", until: "2026-05-01T00:00:00Z",
			want: []string{"2026-02-10T10:00:00Z", "2026-03-10T10:00:00Z", "2026-04-14T10:00:00Z"},
		},
		{
			name: "monthly last friday",
			p: recurrencePattern{kind: recurMonthNth, interval: 1, start: mkDate(t, "2026-01-30T10:00:00Z"),
				instance: 5, mask: dayFriday},
			since: "2026-02-01T00:00:00Z", until: "2026-04-01T00:00:00Z",
			want: []string{"2026-02-27T10:00:00Z", "2026-03-27T10:00:00Z"},
		},
		{
			name: "yearly by date",
			p: recurrencePattern{kind: recurYearly, interval: 1, start: mkDate(t, "2026-03-15T09:00:00Z"),
				month: 3, day: 15},
			since: "2027-01-01T00:00:00Z", until: "2029-01-01T00:00:00Z",
			want: []string{"2027-03-15T09:00:00Z", "2028-03-15T09:00:00Z"},
		},
		{
			name: "yearly february 29 clamps in common years",
			p: recurrencePattern{kind: recurYearly, interval: 1, start: mkDate(t, "2024-02-29T09:00:00Z"),
				month: 2, day: 29},
			since: "2025-01-01T00:00:00Z", until: "2029-01-01T00:00:00Z",
			want: []string{"2025-02-28T09:00:00Z", "2026-02-28T09:00:00Z", "2027-02-28T09:00:00Z",
				"2028-02-29T09:00:00Z"},
		},
		{
			name: "yearly second sunday of may",
			p: recurrencePattern{kind: recurYearNth, interval: 1, start: mkDate(t, "2026-05-10T09:00:00Z"),
				month: 5, instance: 2, mask: daySunday},
			since: "2027-01-01T00:00:00Z", until: "2030-01-01T00:00:00Z",
			want: []string{"2027-05-09T09:00:00Z", "2028-05-14T09:00:00Z", "2029-05-13T09:00:00Z"},
		},
		{
			name: "pattern end date bounds the series",
			p: recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-11-01T09:00:00Z"),
				end: mkDate(t, "2026-11-03T00:00:00Z")},
			since: "2026-10-30T00:00:00Z", until: "2026-11-10T00:00:00Z",
			want: []string{"2026-11-01T09:00:00Z", "2026-11-02T09:00:00Z", "2026-11-03T09:00:00Z"},
		},
		{
			name: "occurrence count bounds the series",
			p: recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-11-01T09:00:00Z"),
				count: 2},
			since: "2026-10-30T00:00:00Z", until: "2026-11-10T00:00:00Z",
			want: []string{"2026-11-01T09:00:00Z", "2026-11-02T09:00:00Z"},
		},
		{
			name: "occurrences before the window count toward the limit",
			p: recurrencePattern{kind: recurWeekly, interval: 1, start: mkDate(t, "2026-11-02T09:00:00Z"),
				mask: dayMonday | dayWednesday, count: 3},
			since: "2026-11-10T00:00:00Z", until: "2026-11-30T00:00:00Z",
			want: nil, // Nov 2, 4 and 9 are the whole series
		},
		{
			name:  "window before the series",
			p:     recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2026-12-01T09:00:00Z")},
			since: "2026-11-01T00:00:00Z", until: "2026-11-10T00:00:00Z",
			want: nil,
		},
		{
			name:  "a series years old reaches the window without scanning it",
			p:     recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2020-01-01T09:00:00Z")},
			since: "2026-06-01T00:00:00Z", until: "2026-06-04T00:00:00Z",
			want: []string{"2026-06-01T09:00:00Z", "2026-06-02T09:00:00Z", "2026-06-03T09:00:00Z"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.p.dates(mkDate(t, c.since), mkDate(t, c.until))
			if err != nil {
				t.Fatal(err)
			}
			want := mkDateList(t, c.want)
			if len(got) != len(want) {
				t.Fatalf("got %d occurrences %v, want %d %v", len(got), got, len(want), want)
			}
			for i := range want {
				if !got[i].Equal(want[i]) {
					t.Errorf("occurrence %d = %s, want %s", i, got[i], want[i])
				}
			}
		})
	}
}

// TestRecurrencePatternRefusesWhatItCannotEnumerate: one bounded refusal per
// pattern, never a guessed series and never an unbounded loop.
func TestRecurrencePatternRefusesWhatItCannotEnumerate(t *testing.T) {
	cases := []struct {
		name string
		p    recurrencePattern
	}{
		{"unknown recurrence type", recurrencePattern{kind: 4, interval: 1, start: t0}},
		{"zero interval", recurrencePattern{kind: recurDaily, interval: 0, start: t0}},
		{"daily weekday mask with a multi-day interval", recurrencePattern{kind: recurDaily, interval: 2, start: t0,
			mask: dayMonday}},
		{"weekly without a weekday", recurrencePattern{kind: recurWeekly, interval: 1, start: t0}},
		{"weekly with an undefined day bit", recurrencePattern{kind: recurWeekly, interval: 1, start: t0, mask: 1 << 7}},
		{"monthly without a day", recurrencePattern{kind: recurMonthly, interval: 1, start: t0}},
		{"monthly nth with several weekdays", recurrencePattern{kind: recurMonthNth, interval: 1, start: t0,
			instance: 1, mask: dayMonday | dayTuesday}},
		{"monthly nth with an invalid instance", recurrencePattern{kind: recurMonthNth, interval: 1, start: t0,
			instance: 6, mask: dayMonday}},
		{"yearly without a month", recurrencePattern{kind: recurYearly, interval: 1, start: t0, day: 1}},
		{"yearly nth without a month", recurrencePattern{kind: recurYearNth, interval: 1, start: t0,
			instance: 1, mask: dayMonday}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.p.dates(mkDate(t, "2026-01-01T00:00:00Z"), mkDate(t, "2027-01-01T00:00:00Z")); err == nil {
				t.Fatal("the pattern must be refused, not guessed")
			}
		})
	}

	// The cap: a daily series over ten years is 3,653 candidates, more than
	// maxOccurrencesPerSeries. The series is refused with one error.
	p := recurrencePattern{kind: recurDaily, interval: 1, start: mkDate(t, "2020-01-01T09:00:00Z")}
	_, err := p.dates(mkDate(t, "2020-01-01T00:00:00Z"), mkDate(t, "2030-01-01T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("cap: %v", err)
	}
}

// TestRecurrenceWindowStartsMidSeries reaches a weekly series that began ten
// months earlier without walking it, and keeps the Occurrences count exact.
func TestRecurrenceWindowStartsMidSeries(t *testing.T) {
	p := recurrencePattern{kind: recurWeekly, interval: 1, start: mkDate(t, "2026-01-05T09:00:00Z"),
		mask: dayMonday, count: 60}
	got, err := p.dates(mkDate(t, "2026-11-01T00:00:00Z"), mkDate(t, "2026-12-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	want := mkDateList(t, []string{
		"2026-11-02T09:00:00Z", "2026-11-09T09:00:00Z", "2026-11-16T09:00:00Z",
		"2026-11-23T09:00:00Z", "2026-11-30T09:00:00Z",
	})
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("occurrence %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// recurringItem builds a recurring master: eventItem plus its RecurrencePattern
// properties and the duration Outlook returns on each occurrence.
func recurringItem(id, subject, start, end string, props map[string]any) *fakeItem {
	it := eventItem(id, subject, start, end)
	its, _ := time.Parse(time.RFC3339, start)
	ite, _ := time.Parse(time.RFC3339, end)
	wall := wallClock(its) // StartTime is local wall clock, like every pattern date
	it.props["IsRecurring"] = true
	it.props["Duration"] = int64(ite.Sub(its) / time.Minute)
	it.props["StartTime"] = DateOf(time.Date(1899, 12, 30, wall.Hour(), wall.Minute(), 0, 0, time.UTC))
	for k, v := range props {
		it.props[k] = v
	}
	return it
}

// patternProps is the rule of a test series. PatternStartDate is the local day
// of its Start, with the time-of-day carried by StartTime (as Outlook does).
func patternProps(kind int64, start string, extra map[string]any) map[string]any {
	s, _ := time.Parse(time.RFC3339, start)
	wall := wallClock(s)
	props := map[string]any{
		"RecurrenceType":   kind,
		"PatternStartDate": DateOf(time.Date(wall.Year(), wall.Month(), wall.Day(), 0, 0, 0, 0, time.UTC)),
		"NoEndDate":        true,
		"Interval":         int64(1),
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

// localDay is a window bound in the owner's local wall clock, the form the
// connector compares recurrence candidates against.
func localDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// syncOneSeries runs a calendar with a single master and returns the events,
// the fake (for call assertions) and the log output. An incomplete read is a
// test failure; tests that expect one use syncOneSeriesRead.
func syncOneSeries(t *testing.T, master *fakeItem, since, until time.Time) (*fakeOutlook, []calendar.Event, string) {
	t.Helper()
	o, events, logs, err := syncOneSeriesRead(t, master, since, until)
	if err != nil {
		t.Fatalf("calendar read: %v\n%s", err, logs)
	}
	return o, events, logs
}

// syncOneSeriesRead is syncOneSeries with the connector's completeness signal.
func syncOneSeriesRead(t *testing.T, master *fakeItem, since, until time.Time) (*fakeOutlook, []calendar.Event, string, error) {
	t.Helper()
	o := profile()
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal-primary"), class: "IPF.Appointment", itemType: 1}
	cal.items = []*fakeItem{master}
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

func eventStarts(events []calendar.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Start.Format(time.RFC3339)
	}
	return out
}

// TestSyncCalendarExpandsEveryRecurrenceType drives the real COM path (the
// fake object model) for each OlRecurrenceType.
func TestSyncCalendarExpandsEveryRecurrenceType(t *testing.T) {
	cases := []struct {
		name  string
		kind  int64
		start string
		end   string
		extra map[string]any
		since time.Time
		until time.Time
		want  []string
	}{
		{
			name: "daily", kind: recurDaily, start: "2026-11-02T14:00:00Z", end: "2026-11-02T15:00:00Z",
			since: localDay(2026, 11, 1), until: localDay(2026, 11, 8),
			want: []string{"2026-11-02T14:00:00Z", "2026-11-03T14:00:00Z", "2026-11-04T14:00:00Z",
				"2026-11-05T14:00:00Z", "2026-11-06T14:00:00Z", "2026-11-07T14:00:00Z"},
		},
		{
			name: "daily weekdays", kind: recurDaily, start: "2026-11-06T14:00:00Z", end: "2026-11-06T15:00:00Z",
			extra: map[string]any{"DayOfWeekMask": int64(dayMonday | dayTuesday | dayWednesday | dayThursday | dayFriday)},
			since: localDay(2026, 11, 6), until: localDay(2026, 11, 17),
			want: []string{"2026-11-06T14:00:00Z", "2026-11-09T14:00:00Z", "2026-11-10T14:00:00Z",
				"2026-11-11T14:00:00Z", "2026-11-12T14:00:00Z", "2026-11-13T14:00:00Z",
				"2026-11-16T14:00:00Z"},
		},
		{
			name: "weekly", kind: recurWeekly, start: "2026-11-02T14:00:00Z", end: "2026-11-02T15:00:00Z",
			extra: map[string]any{"DayOfWeekMask": int64(dayMonday | dayWednesday | dayFriday)},
			since: localDay(2026, 11, 1), until: localDay(2026, 11, 8),
			want: []string{"2026-11-02T14:00:00Z", "2026-11-04T14:00:00Z", "2026-11-06T14:00:00Z"},
		},
		{
			name: "monthly", kind: recurMonthly, start: "2026-10-15T09:00:00Z", end: "2026-10-15T10:00:00Z",
			extra: map[string]any{"DayOfMonth": int64(15)},
			since: localDay(2026, 11, 1), until: localDay(2026, 12, 20),
			want: []string{"2026-11-15T09:00:00Z", "2026-12-15T09:00:00Z"},
		},
		{
			name: "monthly nth", kind: recurMonthNth, start: "2026-10-13T10:00:00Z", end: "2026-10-13T11:00:00Z",
			extra: map[string]any{"Instance": int64(2), "DayOfWeekMask": int64(dayTuesday)},
			since: localDay(2026, 11, 1), until: localDay(2026, 12, 20),
			want: []string{"2026-11-10T10:00:00Z", "2026-12-08T10:00:00Z"},
		},
		{
			name: "yearly", kind: recurYearly, start: "2025-03-15T09:00:00Z", end: "2025-03-15T10:00:00Z",
			extra: map[string]any{"MonthOfYear": int64(3), "DayOfMonth": int64(15)},
			since: localDay(2026, 1, 1), until: localDay(2026, 12, 31),
			want: []string{"2026-03-15T09:00:00Z"},
		},
		{
			name: "yearly nth", kind: recurYearNth, start: "2025-05-11T09:00:00Z", end: "2025-05-11T10:00:00Z",
			extra: map[string]any{"MonthOfYear": int64(5), "Instance": int64(2), "DayOfWeekMask": int64(daySunday)},
			since: localDay(2026, 1, 1), until: localDay(2026, 12, 31),
			want: []string{"2026-05-10T09:00:00Z"},
		},
		{
			name: "weekly with an end date", kind: recurWeekly, start: "2026-11-02T14:00:00Z", end: "2026-11-02T15:00:00Z",
			extra: map[string]any{
				"DayOfWeekMask": int64(dayMonday), "NoEndDate": false,
				"PatternEndDate": DateOf(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)),
			},
			since: localDay(2026, 11, 1), until: localDay(2026, 12, 1),
			want: []string{"2026-11-02T14:00:00Z", "2026-11-09T14:00:00Z", "2026-11-16T14:00:00Z"},
		},
		{
			name: "daily with an occurrence count", kind: recurDaily, start: "2026-11-02T14:00:00Z", end: "2026-11-02T15:00:00Z",
			extra: map[string]any{
				"NoEndDate": false, "Occurrences": int64(2),
				"PatternEndDate": DateOf(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)),
			},
			since: localDay(2026, 11, 1), until: localDay(2026, 12, 1),
			want: []string{"2026-11-02T14:00:00Z", "2026-11-03T14:00:00Z"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			master := recurringItem("series-"+c.name, c.name, c.start, c.end, patternProps(c.kind, c.start, c.extra))
			o, events, logs := syncOneSeries(t, master, c.since, c.until)
			if got := eventStarts(events); !equalStrings(got, c.want) {
				t.Fatalf("events %v, want %v\n%s", got, c.want, logs)
			}
			if callsTo(o, "RecurrencePattern.GetOccurrence") != len(c.want) {
				t.Fatalf("GetOccurrence calls %d, want %d", callsTo(o, "RecurrencePattern.GetOccurrence"), len(c.want))
			}
		})
	}
}

// TestSyncCalendarHonoursMovedAndCanceledExceptions: GetOccurrence returns the
// exception Outlook keeps for the instance, so the emitted event carries the
// moved time (or the canceled state) instead of the pattern's.
func TestSyncCalendarHonoursMovedAndCanceledExceptions(t *testing.T) {
	base := patternProps(recurDaily, "2026-11-02T14:00:00Z", nil)
	since, until := localDay(2026, 11, 1), localDay(2026, 11, 7)

	t.Run("moved", func(t *testing.T) {
		master := recurringItem("series-moved", "Daily", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z", base)
		moved := eventItem("series-moved/20261104", "Daily", "2026-11-04T10:00:00Z", "2026-11-04T11:00:00Z")
		moved.props["IsRecurring"] = true
		moved.props["CleanGlobalObjectId"] = "G-series-moved"
		moved.props["GlobalAppointmentID"] = "G-series-moved"
		master.exceptions = []fakeException{{original: wallClockOf(t, "2026-11-04T14:00:00Z"), item: moved}}
		_, events, logs := syncOneSeries(t, master, since, until)
		want := []string{"2026-11-02T14:00:00Z", "2026-11-03T14:00:00Z", "2026-11-04T10:00:00Z",
			"2026-11-05T14:00:00Z", "2026-11-06T14:00:00Z"}
		if got := eventStarts(events); !equalStrings(got, want) {
			t.Fatalf("events %v, want %v\n%s", got, want, logs)
		}
		if events[2].EntryID != "series-moved/20261104" {
			t.Fatalf("moved occurrence entry id %q", events[2].EntryID)
		}
		if events[2].SeriesID != "G-series-moved" || !events[2].Recurring {
			t.Fatalf("moved occurrence lost its series identity: %+v", events[2])
		}
	})

	t.Run("canceled", func(t *testing.T) {
		master := recurringItem("series-cancel", "Daily", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z", base)
		canceled := eventItem("series-cancel/20261103", "Daily", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z")
		canceled.props["IsRecurring"] = true
		canceled.props["CleanGlobalObjectId"] = "G-series-cancel"
		canceled.props["GlobalAppointmentID"] = "G-series-cancel"
		canceled.props["MeetingStatus"] = int64(calendar.MeetingCanceled)
		master.exceptions = []fakeException{{original: wallClockOf(t, "2026-11-03T14:00:00Z"), item: canceled}}
		_, events, logs := syncOneSeries(t, master, since, until)
		if len(events) != 5 {
			t.Fatalf("events %v\n%s", eventStarts(events), logs)
		}
		if events[1].Meeting != calendar.MeetingCanceled {
			t.Fatalf("canceled occurrence kept as %+v", events[1].Meeting)
		}
		if events[1].Active() {
			t.Fatal("a canceled occurrence must not stay active")
		}
	})
}

// TestSyncCalendarSkipsDeletedOccurrences: a deleted instance is a documented
// GetOccurrence answer (E_FAIL, or Nothing), never a series failure and never
// a warning.
func TestSyncCalendarSkipsDeletedOccurrences(t *testing.T) {
	base := patternProps(recurDaily, "2026-11-02T14:00:00Z", nil)
	since, until := localDay(2026, 11, 1), localDay(2026, 11, 7)
	want := []string{"2026-11-02T14:00:00Z", "2026-11-03T14:00:00Z", "2026-11-05T14:00:00Z",
		"2026-11-06T14:00:00Z"}
	for _, c := range []struct {
		name string
		ex   fakeException
	}{
		{"typed error", fakeException{deleted: true}},
		{"nothing", fakeException{deleted: true, nothing: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			master := recurringItem("series-deleted", "Daily", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z", base)
			c.ex.original = wallClockOf(t, "2026-11-04T14:00:00Z")
			master.exceptions = []fakeException{c.ex}
			_, events, logs := syncOneSeries(t, master, since, until)
			if got := eventStarts(events); !equalStrings(got, want) {
				t.Fatalf("events %v, want %v\n%s", got, want, logs)
			}
			if strings.Contains(logs, "series skipped") {
				t.Fatalf("a deleted occurrence is not a series failure: %s", logs)
			}
		})
	}
}

// TestSyncCalendarSkipsAnUnsupportedSeriesOnce: one warning with the pattern
// type, no GetOccurrence call, and never one warning per occurrence.
func TestSyncCalendarSkipsAnUnsupportedSeriesOnce(t *testing.T) {
	master := recurringItem("series-odd", "Odd series", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(4, "2026-11-02T14:00:00Z", nil))
	o, events, logs, err := syncOneSeriesRead(t, master, localDay(2026, 11, 1), localDay(2026, 12, 1))
	var incomplete *IncompleteCalendarError
	if !errors.As(err, &incomplete) || incomplete.SkippedSeries != 1 || incomplete.SkippedEvents != 0 {
		t.Fatalf("incomplete signal = %v, want one skipped series", err)
	}
	if len(events) != 0 {
		t.Fatalf("an unsupported series produced %v", eventStarts(events))
	}
	if n := callsTo(o, "RecurrencePattern.GetOccurrence"); n != 0 {
		t.Fatalf("GetOccurrence calls %d for an unsupported pattern", n)
	}
	if n := strings.Count(logs, "calendar: series skipped"); n != 1 {
		t.Fatalf("%d warnings, want exactly 1: %s", n, logs)
	}
	if !strings.Contains(logs, `"pattern":"type 4"`) {
		t.Fatalf("the warning must name the pattern type: %s", logs)
	}
}

// TestSyncCalendarBoundsCandidateExpansion: a pattern denser than the hard cap
// is skipped with one warning instead of flooding the COM thread.
func TestSyncCalendarBoundsCandidateExpansion(t *testing.T) {
	master := recurringItem("series-dense", "Dense", "2015-01-01T09:00:00Z", "2015-01-01T10:00:00Z",
		patternProps(recurDaily, "2015-01-01T09:00:00Z", nil))
	o, events, logs, err := syncOneSeriesRead(t, master, localDay(2020, 1, 1), localDay(2030, 1, 1))
	var incomplete *IncompleteCalendarError
	if !errors.As(err, &incomplete) || incomplete.SkippedSeries != 1 || incomplete.SkippedEvents != 0 {
		t.Fatalf("incomplete signal = %v, want one skipped series", err)
	}
	if len(events) != 0 {
		t.Fatalf("a capped series produced %v", eventStarts(events))
	}
	if n := callsTo(o, "RecurrencePattern.GetOccurrence"); n != 0 {
		t.Fatalf("GetOccurrence calls %d before the cap", n)
	}
	if n := strings.Count(logs, "series skipped"); n != 1 || !strings.Contains(logs, "2000") {
		t.Fatalf("%d warnings, want one naming the cap: %s", n, logs)
	}
}

// TestRecurringSeriesUseOnlyGetOccurrence is the regression guard for the
// precision bug: Outlook has GetOccurrence(DateTime), never GetOccurrences.
// Reintroducing the wrong member makes the guard refuse it, so the series
// emits nothing and this test fails.
func TestRecurringSeriesUseOnlyGetOccurrence(t *testing.T) {
	if _, ok := model[classRecurrence]["GetOccurrences"]; ok {
		t.Fatal("RecurrencePattern.GetOccurrences does not exist in the Outlook object model")
	}
	if _, ok := model[classRecurrence]["GetOccurrence"]; !ok {
		t.Fatal("RecurrencePattern.GetOccurrence is missing from the allowlist")
	}
	master := recurringItem("series-regression", "Weekly", "2026-11-02T14:00:00Z", "2026-11-02T15:00:00Z",
		patternProps(recurWeekly, "2026-11-02T14:00:00Z",
			map[string]any{"DayOfWeekMask": int64(dayMonday)}))
	o, events, logs := syncOneSeries(t, master, localDay(2026, 11, 1), localDay(2026, 12, 1))
	if len(events) == 0 {
		t.Fatalf("the recurring series emitted nothing: %s", logs)
	}
	if callsTo(o, "RecurrencePattern.GetOccurrence") == 0 {
		t.Fatalf("GetOccurrence was never called: %s", logs)
	}
	if o.callsContain("RecurrencePattern.GetOccurrences") {
		t.Fatal("GetOccurrences reached the object model")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
