package triage

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Review 2b (median/percentile): the urgency prior is legacy's again —
// the median (average of the two middles) rounded to 0.1 minute and
// compared with ≤ each bound (rules.json resolution_minutes), where 2a
// had exclusive bounds over the median-low. Failed first: 120 minutes
// gave 2.5 (legacy 3.25).
func TestUrgencyPriorIsLegacyInclusive(t *testing.T) {
	r, err := loadRules(rulesJSON)
	if err != nil {
		t.Fatal(err)
	}
	sc := newScales([]int{60, 240, 1440, 4320}, r.Urgency.BoundsMinutes, r.Urgency.ResolutionMinutes)
	level := func(minutes float64) float64 {
		return r.Urgency.Levels[sc.coarse(threads.BucketIndex(minutes, sc.fine), sc.urgency)]
	}
	for minutes, want := range map[float64]float64{0: 3.25, 119.99: 3.25, 120: 3.25, 120.04: 3.25, 120.06: 2.5, 480: 2.5,
		1440: 1.75, 4320: 1.25, 4320.1: 0.75, 99999: 0.75} {
		if got := level(minutes); got != want {
			t.Errorf("%v minutes: level %v, want %v", minutes, got, want)
		}
	}
	if sc.speedLabels[sc.coarse(threads.BucketIndex(60, sc.fine), sc.speed)] != "1h-4h" {
		t.Fatal("speed bands stay exclusive (legacy: hours < 1)")
	}
}
