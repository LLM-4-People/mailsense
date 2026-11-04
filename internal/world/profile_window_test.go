package world

import (
	"os"
	"strings"
	"testing"
)

// TestProfileWindowHasOneOwner: the single profile and the batch clamp months
// and event days identically, so a cache key built by one is found by the
// other. A second clamp prologue is how the two windows drift.
func TestProfileWindowHasOneOwner(t *testing.T) {
	data, err := os.ReadFile("world.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if got := strings.Count(source, "mathx.Clamp(months"); got != 1 {
		t.Errorf("world.go clamps months %d times, want once in profileWindow", got)
	}
	if got := strings.Count(source, "mathx.Clamp(eventDays"); got != 1 {
		t.Errorf("world.go clamps event days %d times, want once in profileWindow", got)
	}
	if got := strings.Count(source, "= profileWindow(months, eventDays)"); got != 2 {
		t.Errorf("world.go calls profileWindow %d times, want the single and the batch read", got)
	}
	if months, eventDays := profileWindow(0, -1); months != DefaultCompanyMonths || eventDays != DefaultCompanyEventDays {
		t.Errorf("profileWindow(0, -1) = %d, %d; want the defaults", months, eventDays)
	}
	if months, eventDays := profileWindow(MaxCompanyMonths+5, MaxCompanyEventDays+5); months != MaxCompanyMonths || eventDays != MaxCompanyEventDays {
		t.Errorf("profileWindow over the bounds = %d, %d; want the maxima", months, eventDays)
	}
}
