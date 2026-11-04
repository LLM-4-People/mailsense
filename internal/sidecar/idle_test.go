package sidecar

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// idleWorld is the fake runtime world with an idle policy short enough to
// test (sidecar.idle_timeout's 1s floor, and http.timeout.total's 1s
// floor, bound the window from below).
func idleWorld(t *testing.T, overrides map[string]any) *world {
	t.Helper()
	set := map[string]any{
		"sidecar.threads":      1,
		"sidecar.idle_timeout": "1s",
		"http.timeout.total":   "1s",
	}
	maps.Copy(set, overrides)
	return newWorld(t, cpuOnly, set)
}

// waitState waits for the one instance to reach state.
func waitState(t *testing.T, m *Manager, state State) Status {
	t.Helper()
	eventually(t, func() bool { return m.Status()[0].State == state })
	return m.Status()[0]
}

func TestIdleTimeoutStopsTheRuntime(t *testing.T) {
	w := idleWorld(t, nil)
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	ready := m.Status()[0]
	if ready.State != StateReady || ready.PID == 0 {
		t.Fatalf("not ready before the idle stop: %+v", ready)
	}
	stopped := waitState(t, m, StateStopped)
	if stopped.Detail == "" || stopped.PID != 0 {
		t.Fatalf("a truthful stop detail and no live pid: %+v", stopped)
	}
	if !processGone(ready.PID) {
		t.Fatalf("the child process %d outlived the idle stop", ready.PID)
	}
}

func TestIdleTimeoutRestartsOnTheNextLocate(t *testing.T) {
	w := idleWorld(t, nil)
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	first := m.Status()[0]
	waitState(t, m, StateStopped)

	ep, err := w.locate(m)
	if err != nil || ep.URL == "" || ep.Key == "" {
		t.Fatalf("the next Locate starts a fresh runtime: %+v %v", ep, err)
	}
	restarted := m.Status()[0]
	if restarted.State != StateReady || restarted.PID == 0 || restarted.PID == first.PID {
		t.Fatalf("a fresh start after the idle stop: first %+v, now %+v", first, restarted)
	}
	if !restarted.Since.After(first.Since) {
		t.Fatalf("the restart is newer: first %s, now %s", first.Since, restarted.Since)
	}
	if _, err := w.embedder(m).Embed(t.Context(), doc("after the idle stop")); err != nil {
		t.Fatalf("the restarted endpoint is usable: %v", err)
	}
}

func TestZeroIdleTimeoutKeepsTheRuntimeAndAnEditApplies(t *testing.T) {
	w := idleWorld(t, map[string]any{"sidecar.idle_timeout": "0s"})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	ready := m.Status()[0]
	time.Sleep(350 * time.Millisecond) // more than three sidecar.health.interval ticks
	if st := m.Status()[0]; st.State != StateReady || st.PID != ready.PID {
		t.Fatalf("0 disables the idle policy: %+v", st)
	}
	// A configured timeout applies to the next health tick, measured from
	// the last Locate that this ready runtime already had.
	if err := w.cfg.Set("sidecar.idle_timeout", "1s"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateStopped)
}

func TestLocateWithinTheIdleTimeoutResetsIt(t *testing.T) {
	w := idleWorld(t, map[string]any{"sidecar.idle_timeout": "2s"})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := w.locate(m); err != nil {
		t.Fatalf("Locate while ready: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if st := m.Status()[0]; st.State != StateReady {
		t.Fatalf("a Locate within the timeout must reset the idle clock: %+v", st)
	}
	waitState(t, m, StateStopped)
}

func TestIdleStopIsNotARestartOrFailure(t *testing.T) {
	w := idleWorld(t, nil)
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	before := m.Status()[0]
	seen := w.states()
	stopped := waitState(t, m, StateStopped)
	if stopped.Restarts != before.Restarts {
		t.Fatalf("an idle stop is not a restart: %d -> %d", before.Restarts, stopped.Restarts)
	}
	if _, _, broken := m.Learned(); len(broken) != 0 {
		t.Fatalf("an idle stop marks no build broken: %+v", broken)
	}
	got := seen()
	if got[StateRestarting] || got[StateFailed] || !got[StateStopped] {
		t.Fatalf("states published around an idle stop: %v", got)
	}
}

func TestLocateAfterAnIdleStopReportsStarting(t *testing.T) {
	// A runtime that answers 503 while loading keeps the restart in
	// progress long enough for a caller that cannot wait to report it.
	slow := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_HEALTH_503": "40"}}}
	w := newWorld(t, slow, map[string]any{
		"sidecar.threads":      1,
		"sidecar.idle_timeout": "1s",
		"http.timeout.total":   "1s",
	})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateStopped)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := m.Locate(ctx, rtName, modelName)
	if !errors.Is(err, ErrStarting) {
		t.Fatalf("a caller that cannot wait for the restart: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, rtName+" "+string(StateStarting)) ||
		strings.Contains(msg, rtName+" "+string(StateStopped)) || strings.Contains(msg, rtName+" "+string(StateIdle)) {
		t.Fatalf("a requested runtime on its way reports starting, not stopped/idle: %v", err)
	}
}

// A touch (Locate) resets the idle clock; the clock is the manager's, so a
// fake clock can prove it without waiting.
func TestTouchResetsTheIdleClock(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"sidecar.idle_timeout": "1m", "http.timeout.total": "1s"})
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	m := &Manager{cfg: cfg, clock: clk}
	in := newInstance(m, rtName, modelName)

	clk.Advance(30 * time.Second)
	in.touch()
	clk.Advance(59 * time.Second)
	if _, due := in.idleDue(); due {
		t.Fatal("the idle clock did not restart at the last touch")
	}
	clk.Advance(time.Second)
	if _, due := in.idleDue(); !due {
		t.Fatal("the idle policy did not end at the timeout after the last touch")
	}
}

// One embeddings call may hold the endpoint for up to its HTTP client
// timeout after the Locate that admitted it (the adapter calls Locate once
// per batch, immediately before the request). The idle window therefore
// never ends before that bound, even when sidecar.idle_timeout is shorter.
func TestIdleWindowIsAtLeastOneRequest(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"sidecar.idle_timeout": "1m", "http.timeout.total": "10m"})
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	m := &Manager{cfg: cfg, clock: clk}
	in := newInstance(m, rtName, modelName)

	clk.Advance(9 * time.Minute)
	if _, due := in.idleDue(); due {
		t.Fatal("the idle policy ended before the request bound http.timeout.total")
	}
	clk.Advance(time.Minute)
	if _, due := in.idleDue(); !due {
		t.Fatal("the idle policy did not end after the request bound")
	}
}

func TestIdleTimeoutSetting(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"sidecar.idle_timeout": "5m"})
	st, ok := cfg.Schema().Lookup("sidecar.idle_timeout")
	if !ok {
		t.Fatal("sidecar.idle_timeout is not in the schema")
	}
	if st.Type != config.TypeDuration || st.Group != "Local runtime" || st.Description == "" {
		t.Fatalf("the setting's type, group or description: %+v", st)
	}
	if string(st.Default) != `"15m"` || string(st.Min) != `"0s"` || string(st.Max) != `"24h"` {
		t.Fatalf("default/min/max = %s/%s/%s", st.Default, st.Min, st.Max)
	}
	if got := cfg.Duration("sidecar.idle_timeout"); got != 5*time.Minute {
		t.Fatalf("configtest resolves the override: %s", got)
	}
	for _, value := range []any{"0s", "24h"} {
		if err := cfg.Set("sidecar.idle_timeout", value); err != nil {
			t.Errorf("Set(%v): %v", value, err)
		}
	}
	for _, value := range []any{"-1s", "25h"} {
		if err := cfg.Set("sidecar.idle_timeout", value); err == nil {
			t.Errorf("Set(%v) must be refused", value)
		}
	}
}
