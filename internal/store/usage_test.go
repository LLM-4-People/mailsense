package store

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// usageWorld opens a store on a manual clock so the day boundary of the
// usage window is deterministic.
func usageWorld(t *testing.T) (*Store, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	s, err := Open(t.Context(), configtest.New(t, nil), nil, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clk
}

// recordUsageAt moves the clock to day and records one call there.
func recordUsageAt(t *testing.T, s *Store, clk *clocktest.Fake, day time.Time, provider, model string, usd float64) {
	t.Helper()
	clk.Advance(day.Sub(clk.Now()))
	if err := s.AddUsage(t.Context(), engine.UsageRecord{Provider: provider, Model: model,
		Usage: engine.Usage{Calls: 1, InputTokens: 10, OutputTokens: 2, EstimatedUSD: usd}}); err != nil {
		t.Fatal(err)
	}
}

// The usage window includes the first day and today, excludes the day before
// the first, and orders the rows newest day first.
func TestListUsageWindowBoundaries(t *testing.T) {
	s, clk := usageWorld(t)
	recordUsageAt(t, s, clk, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), "old", "m-old", 0.25)
	recordUsageAt(t, s, clk, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), "first", "m-first", 0.5)
	recordUsageAt(t, s, clk, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), "today", "m-today", 1)
	recordUsageAt(t, s, clk, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC), "future", "m-future", 2)
	// The query's upper bound is today, not whatever the clock wrote last.
	clk.Advance(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Sub(clk.Now()))

	rows, err := s.ListUsage(t.Context(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %+v, want the first and today only", rows)
	}
	if rows[0].Day != "2026-11-04" || rows[0].Provider != "today" {
		t.Fatalf("first row %+v, want today first", rows[0])
	}
	if rows[1].Day != "2026-11-02" || rows[1].Provider != "first" {
		t.Fatalf("second row %+v, want the inclusive first day", rows[1])
	}

	// One day is today only: the same window rule at its lower bound.
	rows, err = s.ListUsage(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Day != "2026-11-04" {
		t.Fatalf("one-day window %+v", rows)
	}
}

// An empty usage table is an empty list, never null and never an error.
func TestListUsageEmpty(t *testing.T) {
	s, _ := usageWorld(t)
	rows, err := s.ListUsage(t.Context(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("empty store returned %+v", rows)
	}
}
