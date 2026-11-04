package api

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// The usage route defaults to thirty days, forwards the requested window and
// returns the totals and every breakdown row.
func TestUsageWindowAndBreakdown(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.usage.rows = []store.UsageRow{
		{Day: "2026-11-04", Provider: "decider", Model: "deepseek", Calls: 3, Failures: 1, InputTokens: 300, OutputTokens: 90, CostUSD: 0.25},
		{Day: "2026-11-03", Provider: "embedder", Model: "gemma", Calls: 2, InputTokens: 200, OutputTokens: 40, CostUSD: 0.05},
	}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/usage"})
	mustStatus(t, status, 200, data)
	if w.usage.lastDays != usageDefaultDays {
		t.Fatalf("default days %d", w.usage.lastDays)
	}
	body := decodeMap(t, data)
	if body["days"] != float64(usageDefaultDays) {
		t.Fatalf("days on the wire %v", body["days"])
	}
	totals, _ := body["totals"].(map[string]any)
	if totals["calls"] != float64(5) || totals["failures"] != float64(1) ||
		totals["input_tokens"] != float64(500) || totals["output_tokens"] != float64(130) || totals["cost_usd"] != 0.3 {
		t.Fatalf("totals %#v", totals)
	}
	rows, _ := body["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %#v", body["rows"])
	}
	first, _ := rows[0].(map[string]any)
	if first["day"] != "2026-11-04" || first["provider"] != "decider" || first["model"] != "deepseek" ||
		first["calls"] != float64(3) || first["failures"] != float64(1) ||
		first["input_tokens"] != float64(300) || first["output_tokens"] != float64(90) || first["cost_usd"] != 0.25 {
		t.Fatalf("first row %#v", first)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/usage?days=1"})
	mustStatus(t, status, 200, data)
	if w.usage.lastDays != 1 {
		t.Fatalf("requested days %d", w.usage.lastDays)
	}
}

// The window is bounded at both ends and every misuse is the one invalid_days
// shape.
func TestUsageWindowIsBounded(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/usage?days=0", 400, "invalid_days"},
		{"/api/usage?days=366", 400, "invalid_days"},
		{"/api/usage?days=nope", 400, "invalid_days"},
		{"/api/usage?days=", 400, "invalid_days"},
	}
	for _, tc := range cases {
		status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
		mustStatus(t, status, tc.status, data)
		if code := errorCode(t, data); code != tc.code {
			t.Fatalf("%s: code %q, want %q", tc.path, code, tc.code)
		}
	}
	if w.usage.calls != 0 {
		t.Fatalf("an invalid window reached the service %d time(s)", w.usage.calls)
	}
	for _, days := range []string{"1", "365"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/usage?days=" + days})
		mustStatus(t, status, 200, data)
	}
}

// An empty usage database is an empty array and zero totals, never null.
func TestUsageEmptyResult(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.usage.rows = nil
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/usage?days=30"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	rows, ok := body["rows"].([]any)
	if !ok || len(rows) != 0 {
		t.Fatalf("rows %#v", body["rows"])
	}
	totals, _ := body["totals"].(map[string]any)
	if totals["calls"] != float64(0) || totals["input_tokens"] != float64(0) ||
		totals["output_tokens"] != float64(0) || totals["cost_usd"] != float64(0) {
		t.Fatalf("totals %#v", totals)
	}
	if _, present := totals["failures"]; present {
		t.Fatalf("an empty window carries failures: %#v", totals)
	}
}

// The store's error keeps the API's one envelope.
func TestUsageStoreErrorIsInternal(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.usage.err = errors.New("usage read failed")
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/usage"})
	mustStatus(t, status, 500, data)
	if code := errorCode(t, data); code != "internal" {
		t.Fatalf("code %q", code)
	}
}
