package outlook

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// The pace math: after a call that took d the next waits so the busy
// share holds, and never starts before the rate's interval.
func TestPaceNextKeepsShareAndRate(t *testing.T) {
	start := t0
	cases := []struct {
		l    paceLimits
		busy time.Duration
		want time.Duration // from start
	}{
		{paceLimits{Share: 0.25, Rate: 1e6, MaxWait: time.Minute}, 4 * time.Millisecond, 16 * time.Millisecond},             // 4 busy + 12 rest
		{paceLimits{Share: 1, Rate: 10, MaxWait: time.Minute}, time.Millisecond, 100 * time.Millisecond},                    // the rate decides
		{paceLimits{Share: 0.5, Rate: 100, MaxWait: time.Minute}, 20 * time.Millisecond, 40 * time.Millisecond},             // the share decides
		{paceLimits{Share: 0.5, Rate: 100, MaxWait: time.Minute}.scaled(0.5), 2 * time.Millisecond, 20 * time.Millisecond},  // 0.25, 50/s: the rate
		{paceLimits{Share: 0.5, Rate: 100, MaxWait: time.Minute}.scaled(0.5), 10 * time.Millisecond, 40 * time.Millisecond}, // 0.25, 50/s: the share
		{paceLimits{Share: 1, Rate: 1e9, MaxWait: time.Minute}, time.Millisecond, time.Millisecond},                         // open: no wait
		{paceLimits{Share: 1, Rate: 0.001, MaxWait: time.Minute}, time.Millisecond, time.Minute + time.Millisecond},         // capped by max_wait
	}
	for i, c := range cases {
		if got := c.l.next(start, start.Add(c.busy)).Sub(start); got != c.want {
			t.Errorf("case %d: next after %s, want %s", i, got, c.want)
		}
	}
}

// measured runs a discover on a fake Outlook whose calls take 1 ms each
// (releases take none) and returns the paced calls made (member calls and
// releases), the fake wall time it took and the connector.
func measured(t *testing.T, o *fakeOutlook, overrides map[string]any) (int, time.Duration, *Connector) {
	t.Helper()
	c, clk := testConnector(t, o, overrides)
	start := clk.Now()
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	return int(c.Stats().Calls), clk.Now().Sub(start), c
}

// busyTime is the time the connector's calls spent inside Outlook.
func busyTime(c *Connector) time.Duration {
	var d time.Duration
	for _, m := range c.Stats().Members {
		d += m.Time
	}
	return d
}

// Under load, the share of wall time spent inside Outlook stays at the
// setting (the first call has no rest before it).
func TestPaceHoldsTheBusyShare(t *testing.T) {
	for _, share := range []float64{0.5, 0.25, 0.1} {
		calls, wall, c := measured(t, profile(), map[string]any{"outlook.pace.busy_share": share,
			"outlook.pace.calls_per_second": 100000, "outlook.pace.active.calls_per_second": 100})
		got := float64(busyTime(c)) / float64(wall)
		if math.Abs(got-share) > share*0.05 {
			t.Errorf("share %v: measured %.3f over %d calls in %s", share, got, calls, wall)
		}
		if c.Stats().Pace.Paused <= 0 || c.Stats().Pace.ActiveCalls != 0 {
			t.Errorf("share %v: stats %+v", share, c.Stats().Pace)
		}
	}
}

func TestPaceHoldsTheCallRate(t *testing.T) {
	calls, wall, _ := measured(t, profile(), map[string]any{"outlook.pace.busy_share": 1, "outlook.pace.calls_per_second": 50})
	if rate := float64(calls-1) / wall.Seconds(); rate > 50 || rate < 49 {
		t.Fatalf("%d calls in %s: %.2f calls/s, want 50", calls, wall, rate)
	}
}

// While the user is active (last input more recent than idle_after) the
// active limits apply; an unreadable idle time counts as active.
func TestPaceSlowsDownWhileTheUserIsActive(t *testing.T) {
	set := map[string]any{"outlook.pace.busy_share": 1, "outlook.pace.calls_per_second": 1000,
		"outlook.pace.active.busy_share": 0.25, "outlook.pace.active.calls_per_second": 10, "outlook.pace.idle_after": "1m"}
	away := profile()
	_, awayWall, _ := measured(t, away, set)
	for name, setup := range map[string]func(*fakeOutlook){
		"active":      func(o *fakeOutlook) { o.idle = 5 * time.Second },
		"unreadable":  func(o *fakeOutlook) { o.idleErr = errors.New("GetLastInputInfo failed") },
		"just active": func(o *fakeOutlook) { o.idle = 59 * time.Second },
	} {
		o := profile()
		setup(o)
		calls, wall, c := measured(t, o, set)
		if wall < 50*awayWall || c.Stats().Pace.ActiveCalls != int64(calls) {
			t.Errorf("%s: %s vs %s away; %d of %d calls active", name, wall, awayWall, c.Stats().Pace.ActiveCalls, calls)
		}
	}
}

// After a lost connection the pace is the breaker's factor of the limits.
func TestPaceIsReducedAfterALoss(t *testing.T) {
	o := profile()
	set := map[string]any{"outlook.pace.busy_share": 1, "outlook.pace.calls_per_second": 100, "outlook.breaker.resume_factor": 0.5,
		"outlook.breaker.min_uptime": "1m", "outlook.breaker.cooldown": "1m"}
	c, clk := testConnector(t, o, set)
	start := clk.Now()
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	full, n := clk.Now().Sub(start), len(o.calls)
	o.crash()
	_, _ = c.Discover(t.Context())
	o.restart()
	clk.Advance(time.Hour)
	o.calls = nil
	start = clk.Now()
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	half := clk.Now().Sub(start)
	if len(o.calls) != n || half < 19*full/10 || half > 21*full/10 {
		t.Fatalf("after a loss: %s for %d calls, at full pace %s for %d", half, len(o.calls), full, n)
	}
}

// A cancelled operation ends its pacing wait at once.
func TestPaceWaitEndsWithTheContext(t *testing.T) {
	o := profile()
	c, clk := testConnector(t, o, map[string]any{"outlook.pace.busy_share": 1, "outlook.pace.calls_per_second": 0.1,
		"outlook.pace.active.calls_per_second": 0.1})
	ctx, cancel := context.WithCancel(t.Context())
	clk.OnSleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	if _, err := c.Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if len(o.calls) != 1 {
		t.Fatalf("%d calls after the cancel", len(o.calls))
	}
}

// Idle time across the 32-bit tick wrap, and a last input "after" now.
func TestIdleSinceWraps(t *testing.T) {
	cases := []struct {
		now, last uint32
		want      time.Duration
	}{
		{10_000, 4_000, 6 * time.Second},
		{1_000, math.MaxUint32 - 999, 2 * time.Second}, // wrapped between the two
		{4_000, 10_000, 0},                             // not incremental: input just now
		{5, 5, 0},
	}
	for _, c := range cases {
		if got := idleSince(c.now, c.last); got != c.want {
			t.Errorf("idleSince(%d, %d) = %s, want %s", c.now, c.last, got, c.want)
		}
	}
}

// Misuse: limits that would stop or disable the protection are refused by
// the schema; the active pace may not exceed the away pace.
func TestPaceAndBreakerSettingsRejectMisuse(t *testing.T) {
	cfg := configtest.New(t, nil)
	bad := map[string][]any{
		"outlook.pace.busy_share":              {0, -0.5, 1.5},
		"outlook.pace.calls_per_second":        {0, -1},
		"outlook.pace.active.busy_share":       {0, -0.1},
		"outlook.pace.active.calls_per_second": {0, -3},
		"outlook.pace.idle_after":              {"0s", "-1m"},
		"outlook.breaker.cooldown":             {"0s", "-5m"},
		"outlook.breaker.cooldown_growth":      {0, 0.5, -2},
		"outlook.breaker.cooldown_max":         {"0s"},
		"outlook.breaker.min_uptime":           {"0s", "30s", "-1m"},
		"outlook.breaker.window":               {"0s", "-1h"},
		"outlook.breaker.max_losses":           {0, -1},
		"outlook.breaker.resume_factor":        {0, -0.5, 2},
		"outlook.breaker.min_factor":           {0, 1e-6, -0.1, 1.5},
		"outlook.pace.max_wait":                {"0s", "-1s", "2h"},
		"outlook.discover.count_unpicked":      {"no", 3},
		"sync.details.enabled":                 {"off", 0},
		"sync.details.max_per_run":             {-1},
		"sync.details.settle_after":            {"-1m"},
	}
	for key, values := range bad {
		for _, v := range values {
			if err := cfg.Set(key, v); err == nil {
				t.Errorf("%s = %v must be rejected", key, v)
			}
		}
	}
	for key, v := range map[string]any{"outlook.pace.active.busy_share": 0.9, "outlook.pace.active.calls_per_second": 1000,
		"outlook.breaker.cooldown": "3h"} {
		if err := cfg.Set(key, v); err == nil {
			t.Errorf("%s = %v breaks a constraint and must be rejected", key, v)
		}
	}
}
