package feedback

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threads"
)

// TestYearOverYearComparesSharedMonthsOnly: the year comparison splits the
// month bucket key through its owner (mail.SplitMonth) and counts only the
// months present in both years, so an absent month is "no data yet" rather
// than zero. A key that is not a month is ignored instead of panicking a
// slicing caller.
func TestYearOverYearComparesSharedMonthsOnly(t *testing.T) {
	months := map[string]threads.Volume{
		"2024-01": {Received: 10}, // shares month 01 with 2025
		"2025-01": {Received: 20},
		"2025-02": {Received: 30},
		"2026-01": {Received: 40},
		"2026-02": {Received: 60},
		"2026-03": {Received: 70}, // no 2025-03: excluded
		"":        {Received: 99}, // junk: ignored
	}
	v := &Volume{}
	v.yearOverYear(months)
	want := []YearChange{
		{From: "2024", To: "2025", Months: 1, Previous: 10, Current: 20, ChangeRate: ptr(round3(1))},
		{From: "2025", To: "2026", Months: 2, Previous: 50, Current: 100, ChangeRate: ptr(round3(1))},
	}
	if !reflect.DeepEqual(v.YearOverYear, want) {
		t.Fatalf("year over year\n got %+v\nwant %+v", v.YearOverYear, want)
	}
}

// TestVolumeDispersionRoundsAsAStatistic: the monthly dispersion is a count
// statistic and keeps its own rounding, never the reply-latency rule.
func TestVolumeDispersionRoundsAsAStatistic(t *testing.T) {
	v := &Volume{}
	v.fluctuation(map[string]threads.Volume{
		"2026-01": {Received: 10},
		"2026-02": {Received: 20},
		"2026-03": {Received: 30},
	})
	// Population deviation of 10, 20, 30 is 8.1649…, reported at one decimal.
	if v.StdDev == nil || *v.StdDev != 8.2 {
		t.Fatalf("stddev %v, want 8.2", v.StdDev)
	}
	if v.CV == nil || *v.CV != round3(8.16496580927726/20) {
		t.Fatalf("cv %v", v.CV)
	}
}
