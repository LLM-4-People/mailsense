package outlook

import (
	"testing"
	"time"
)

// TestCalendarEventInstantsUseTheHostZone is the regression for the old
// d.UTC() conversion: AppointmentItem.Start/End are built-in date members,
// so Outlook reports the owner's local wall clock. The emitted Event must
// carry the UTC instant of that wall clock, not the wall-clock fields read as
// if they were UTC.
func TestCalendarEventInstantsUseTheHostZone(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("UTC-5", -5*3600)
	defer func() { time.Local = old }()

	// Built-in date members: 09:00 and 10:30 on the owner's wall clock.
	item := &fakeItem{props: map[string]any{
		"EntryID": "e-local", "Subject": "Planning",
		"Start": DateOf(time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)),
		"End":   DateOf(time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)),
	}}
	since := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	_, events, logs := syncOneSeries(t, item, since, until)
	if len(events) != 1 {
		t.Fatalf("events %d, want 1\n%s", len(events), logs)
	}
	wantStart := time.Date(2026, 1, 15, 14, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 1, 15, 15, 30, 0, 0, time.UTC)
	if !events[0].Start.Equal(wantStart) || !events[0].End.Equal(wantEnd) {
		t.Fatalf("event window %s..%s, want %s..%s", events[0].Start, events[0].End, wantStart, wantEnd)
	}
}

// TestRecurringSeriesKeepWallClockAcrossDST: recurrence pattern dates stay on
// the owner's local wall clock. A daily 09:00 series keeps starting at 09:00
// after the fall change, so its UTC instant moves from 13:00Z to 14:00Z; only
// the emitted instants are converted, never the pattern's civil dates.
func TestRecurringSeriesKeepWallClockAcrossDST(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	toronto, err := time.LoadLocation("America/Toronto")
	if err != nil {
		t.Skipf("America/Toronto tzdata unavailable: %v", err)
	}
	time.Local = toronto

	master := recurringItem("series-dst", "Standup", "2026-10-30T13:00:00Z", "2026-10-30T14:00:00Z",
		patternProps(recurDaily, "2026-10-30T13:00:00Z", nil))
	since := time.Date(2026, 10, 30, 0, 0, 0, 0, toronto)
	until := time.Date(2026, 11, 3, 0, 0, 0, 0, toronto)
	_, events, logs := syncOneSeries(t, master, since, until)
	want := []string{
		"2026-10-30T13:00:00Z", // 09:00 EDT
		"2026-10-31T13:00:00Z", // 09:00 EDT
		"2026-11-01T14:00:00Z", // 09:00 EST, after the fall change
		"2026-11-02T14:00:00Z", // 09:00 EST
	}
	if got := eventStarts(events); !equalStrings(got, want) {
		t.Fatalf("events %v, want %v\n%s", got, want, logs)
	}
}
