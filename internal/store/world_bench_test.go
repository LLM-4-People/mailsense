package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// BenchmarkCalendarEvents measures a window read over a calendar that holds
// many more out-of-window occurrences than the window returns. The attendee
// read takes the window's event ids, so its cost is the window and not the
// whole calendar; the benchmark seeds the out-of-window bulk once and only
// times the read.
func BenchmarkCalendarEvents(b *testing.B) {
	cfg := configtest.New(b, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	s, err := Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertSource(b.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		b.Fatal(err)
	}
	ref := StoreRef{SourceID: "src", StoreID: "store-1"}
	key := source.StoreKey("src", "store-1")
	event := func(entry, start string, attendees int) calendar.Event {
		at, err := time.Parse(time.RFC3339, start)
		if err != nil {
			b.Fatal(err)
		}
		e := calendar.Event{SourceID: "src", StoreKey: key, EntryID: entry, Subject: entry,
			Start: at, End: at.Add(time.Hour), Busy: calendar.Busy}
		for i := range attendees {
			e.Attendees = append(e.Attendees, calendar.Attendee{Kind: calendar.Required,
				Name: "Person", Address: fmt.Sprintf("%s-%d@example.test", entry, i)})
		}
		return e
	}
	old := make([]calendar.Event, 0, 2000)
	for i := range 2000 {
		old = append(old, event(fmt.Sprintf("old-%d", i), "2020-01-02T09:00:00Z", 3))
	}
	since, until := worldTime("2020-01-01T00:00:00Z"), worldTime("2021-01-01T00:00:00Z")
	if err := s.ReplaceCalendarWindow(b.Context(), ref, CalendarRef{EntryID: "old"}, since, until, old); err != nil {
		b.Fatal(err)
	}
	current := make([]calendar.Event, 0, 20)
	for i := range 20 {
		current = append(current, event(fmt.Sprintf("now-%d", i), "2026-11-15T09:00:00Z", 2))
	}
	since, until = worldTime("2026-11-01T00:00:00Z"), worldTime("2026-12-01T00:00:00Z")
	if err := s.ReplaceCalendarWindow(b.Context(), ref, CalendarRef{EntryID: "now"}, since, until, current); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		events, err := s.CalendarEvents(b.Context(), since, until)
		if err != nil {
			b.Fatal(err)
		}
		if len(events) != len(current) {
			b.Fatalf("window events %d, want %d", len(events), len(current))
		}
	}
}
