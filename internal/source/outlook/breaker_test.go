package outlook

import (
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Incident 2026-10-15: after Outlook crashed under a sync, the next folder
// reconnected at once (and a rerun hit an Outlook 90 s into its restart).
// The breaker refuses to reconnect for a cooldown that grows per loss,
// then until Outlook has been up outlook.breaker.min_uptime, resumes at a
// pace reduced per loss, and stops after outlook.breaker.max_losses.
func TestBreakerCooldownUptimeAndStop(t *testing.T) {
	o := profile()
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicBreaker, 64)
	clk := fakeClock(o)
	c, err := newConnector("outlook", configtest.New(t, bothStores(nil)), bus, clk, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	discover := func() error { _, err := c.Discover(t.Context()); return err }
	if err := discover(); err != nil {
		t.Fatal(err)
	}
	cooldowns := []time.Duration{5 * time.Minute, 10 * time.Minute} // default cooldown, growth 2
	for i, cool := range cooldowns {
		o.crash()
		if err := discover(); !errors.Is(err, ErrSessionLost) || !errors.Is(err, source.ErrUnavailable) {
			t.Fatalf("loss %d: %v", i+1, err)
		}
		lostAt := clk.Now()
		o.restart() // Windows restarts Outlook right away
		calls := len(o.calls)
		if err := discover(); !errors.Is(err, ErrCoolingDown) {
			t.Fatalf("loss %d: reconnect during the cooldown: %v", i+1, err)
		}
		if b := c.Breaker(); b.State != BreakerCooling || b.Losses != i+1 || !b.Until.Equal(lostAt.Add(cool)) {
			t.Fatalf("loss %d: %+v, want cooling until +%s", i+1, b, cool)
		}
		clk.Advance(cool)
		if err := discover(); !errors.Is(err, ErrOutlookWarmingUp) {
			t.Fatalf("loss %d: Outlook up %s must not be read yet: %v", i+1, clk.Now().Sub(o.startedAt), err)
		}
		if b := c.Breaker(); b.State != BreakerWarming || !b.Until.Equal(o.startedAt.Add(15*time.Minute)) {
			t.Fatalf("loss %d: %+v", i+1, b)
		}
		if len(o.calls) != calls {
			t.Fatalf("loss %d: %d Outlook calls while the breaker was open", i+1, len(o.calls)-calls)
		}
		clk.Advance(15*time.Minute - cool)
		if err := discover(); err != nil {
			t.Fatalf("loss %d: resume: %v", i+1, err)
		}
		if f := c.Breaker().Factor; f != []float64{0.5, 0.25}[i] {
			t.Fatalf("loss %d: pace factor %v", i+1, f)
		}
	}
	o.crash()
	if err := discover(); !errors.Is(err, ErrSessionLost) {
		t.Fatal(err)
	}
	o.restart()
	clk.Advance(23 * time.Hour) // within the 24 h window: still stopped
	if err := discover(); !errors.Is(err, ErrStopped) {
		t.Fatalf("third loss must stop the source: %v", err)
	}
	b := c.Breaker()
	if b.State != BreakerStopped || b.Losses != 3 || len(b.Action) == 0 || b.Error == "" {
		t.Fatalf("stopped state: %+v", b)
	}
	if err := c.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := discover(); err != nil || c.Breaker().Factor != 1 || c.Breaker().State != BreakerClosed {
		t.Fatalf("after the user's resume: %v %+v", err, c.Breaker())
	}
	var states []string
	for len(sub.C) > 0 {
		states = append(states, (<-sub.C).State)
	}
	if len(states) < 6 || states[len(states)-1] != BreakerClosed || !contains(states, BreakerStopped) || !contains(states, BreakerWarming) {
		t.Fatalf("published states %v", states)
	}
	if o.starts != 0 {
		t.Fatal("the breaker must never start Outlook")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Losses leave the window: the cooldown and the pace recover, and a
// loss long ago does not count toward the stop.
func TestBreakerWindowForgetsOldLosses(t *testing.T) {
	o := profile()
	c, clk := testConnector(t, o, map[string]any{"outlook.breaker.window": "1h", "outlook.breaker.max_losses": 2})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.crash()
	_, _ = c.Discover(t.Context())
	o.restart()
	clk.Advance(2 * time.Hour)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("a loss outside the window: %v", err)
	}
	if b := c.Breaker(); b.Losses != 0 || b.Factor != 1 {
		t.Fatalf("%+v", b)
	}
	o.crash()
	_, _ = c.Discover(t.Context())
	if b := c.Breaker(); b.State == BreakerStopped {
		t.Fatalf("one loss in the window must not stop: %+v", b)
	}
}

// After the cooldown, a gone Outlook is reported and never started; a
// start time that cannot be read refuses too (never assumed old).
func TestBreakerAfterCooldownNeedsARunningOutlook(t *testing.T) {
	o := profile()
	c, clk := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.crash()
	_, _ = c.Discover(t.Context())
	clk.Advance(time.Hour)
	if _, err := c.Discover(t.Context()); !errors.Is(err, ErrNotRunning) || o.starts != 0 {
		t.Fatalf("gone Outlook: %v (starts %d)", err, o.starts)
	}
}

// The cooldown formula: cooldown × growth^(n−1), capped.
func TestCooldownGrowthIsCapped(t *testing.T) {
	s := breakerSettings{cooldown: time.Minute, growth: 3, cooldownMax: 20 * time.Minute}
	for n, want := range map[int]time.Duration{1: time.Minute, 2: 3 * time.Minute, 3: 9 * time.Minute, 4: 20 * time.Minute, 60: 20 * time.Minute} {
		if got := s.cooldownAfter(n); got != want {
			t.Errorf("n=%d: %s, want %s", n, got, want)
		}
	}
}
