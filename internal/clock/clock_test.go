package clock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

func TestRealSleepHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := clock.Real().Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep on a cancelled context: %v", err)
	}
	if err := clock.Real().Sleep(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if clock.Or(nil) == nil || clock.Real().Now().IsZero() {
		t.Fatal("Or/Now")
	}
}

func TestFake(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := clocktest.New(start)
	if clock.Or(f) != clock.Clock(f) {
		t.Fatal("Or must keep a given clock")
	}
	_ = f.Sleep(t.Context(), time.Minute)
	f.Advance(time.Second)
	if f.Now() != start.Add(61*time.Second) || len(f.Sleeps()) != 1 {
		t.Fatalf("fake: %v %v", f.Now(), f.Sleeps())
	}
	f.OnSleep = func(context.Context, time.Duration) error { return errors.New("stop") }
	if err := f.Sleep(t.Context(), time.Hour); err == nil || f.Now() != start.Add(61*time.Second) {
		t.Fatal("OnSleep replaces the advance")
	}
}

func TestZone(t *testing.T) {
	// #12: an empty setting is "not confirmed", never the machine's zone.
	if z, err := clock.Zone(""); !errors.Is(err, clock.ErrZoneUnconfirmed) || z != nil {
		t.Fatal("empty is unconfirmed, never silently the system zone")
	}
	if d := clock.Detected(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)); len(d.Offset) != 6 || d.Abbrev == "" {
		t.Fatalf("the proposal names its offset: %+v", d)
	}
	z, err := clock.Zone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(z).Hour(); got != 8 {
		t.Fatalf("EDT hour %d", got)
	}
	// Review L0 rules §1.6 (failed first: "Local" resolved to the
	// machine's zone): the machine's zone is never a setting value.
	for _, bad := range []string{"Mars/Base", "../etc/passwd", "Local ", "Local", "local"} {
		if _, err := clock.Zone(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// A read-only view must still answer when the owner's zone is unset or
// invalid; the explicit stages keep using Zone so they can report a blocker.
func TestZoneOrUTC(t *testing.T) {
	if got := clock.ZoneOrUTC("").String(); got != "UTC" {
		t.Fatalf("an unset zone resolved to %q", got)
	}
	if got := clock.ZoneOrUTC("Mars/Base").String(); got != "UTC" {
		t.Fatalf("an invalid zone resolved to %q", got)
	}
	if got := clock.ZoneOrUTC("America/New_York").String(); got != "America/New_York" {
		t.Fatalf("a valid zone resolved to %q", got)
	}
}

// The same configured zone is one parsed location, not a zone-file parse per
// view.
func TestZoneCachesTheLocation(t *testing.T) {
	first, err := clock.Zone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	second, err := clock.Zone("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Zone parsed the same zone twice")
	}
	if _, err := clock.Zone("   "); !errors.Is(err, clock.ErrZoneUnconfirmed) {
		t.Fatalf("a blank zone is %v, want ErrZoneUnconfirmed", err)
	}
}
