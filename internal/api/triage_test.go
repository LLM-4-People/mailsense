package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// The triage list is a bounded page: defaults and bounds belong to the
// triage contract, misuse is a 400 with the API's one error shape.
func TestTriagePageBounds(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.triage.page = &triage.Page{Key: store.TriageKey{Fingerprint: "fp", Decider: "layer1"}, Total: 3,
		Limit: 50, Offset: 0, Routed: true, Preview: true, Results: map[int64]triage.PageRow{1: {MessageID: 1}}}
	status, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage"})
	mustStatus(t, status, 200, data)
	if w.triage.lastPage.Limit != triage.TriagePageDefault || w.triage.lastPage.Offset != 0 {
		t.Fatalf("default page %+v", w.triage.lastPage)
	}
	body := decodeMap(t, data)
	if body["total"].(float64) != 3 || body["limit"].(float64) != 50 || body["offset"].(float64) != 0 || body["preview"] != true {
		t.Fatalf("page fields %#v", body)
	}
	// The question catalogue has one wire owner (/api/questions); the page
	// carried a second copy of the whole profile on every request.
	if _, ok := body["profile"]; ok {
		t.Fatalf("the triage page still ships the profile: %s", data)
	}
	status, _, data = do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage?limit=200&offset=7"})
	mustStatus(t, status, 200, data)
	if w.triage.lastPage.Limit != 200 || w.triage.lastPage.Offset != 7 {
		t.Fatalf("explicit page %+v", w.triage.lastPage)
	}
	for _, tc := range []struct{ path, code string }{
		{"/api/triage?limit=0", "invalid_limit"},
		{"/api/triage?limit=201", "invalid_limit"},
		{"/api/triage?limit=abc", "invalid_limit"},
		{"/api/triage?offset=-1", "invalid_offset"},
		{"/api/triage?offset=abc", "invalid_offset"},
	} {
		status, _, data = do(t, ts, srv, request{method: http.MethodGet, path: tc.path})
		mustStatus(t, status, 400, data)
		if got := errorCode(t, data); got != tc.code {
			t.Fatalf("%s: code %q, want %q", tc.path, got, tc.code)
		}
	}
}

// The summary carries the coverage, the key and per-question value
// counts, and no message content or results.
func TestTriageSummaryShapeAndGate(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.triage.summary = &triage.Summary{Key: store.TriageKey{Fingerprint: "fp", Decider: "layer1"},
		Routed: true, Preview: true, Total: 2, Coverage: triage.Coverage{Eligible: 4, Fresh: 2, Unjudged: 2}}
	status, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage/summary"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if w.triage.summaryCalls != 1 || w.triage.readCalls != 0 {
		t.Fatalf("summary calls %d, page calls %d", w.triage.summaryCalls, w.triage.readCalls)
	}
	key, _ := body["key"].(map[string]any)
	if key["decider"] != "layer1" || body["total"].(float64) != 2 || body["routed"] != true || body["preview"] != true {
		t.Fatalf("summary header %#v", body)
	}
	coverage, _ := body["coverage"].(map[string]any)
	if coverage["eligible"].(float64) != 4 || coverage["fresh"].(float64) != 2 || coverage["unjudged"].(float64) != 2 {
		t.Fatalf("coverage %#v", coverage)
	}
	if _, ok := body["questions"]; ok {
		t.Fatalf("the summary still ships a per-question distribution: %s", data)
	}
	if body["results"] != nil {
		t.Fatalf("the summary leaks results: %v", body["results"])
	}
	w.triage.summaryErr = lockedGate()
	status, _, data = do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage/summary"})
	mustStatus(t, status, 423, data)
}

// The triage wire carries only the fields the UI reads. Model, Prior and
// Probabilities stay internal (evaluation, priors, the stored answer), so
// neither the page nor the per-message result may encode them.
func TestTriageValueWireOmitsDiagnostics(t *testing.T) {
	srv, ts, w := newTestServer(t)
	prior, confidence := 0.35, 0.8
	value := taxonomy.Value{Type: engine.Choice, Choice: "work", Model: 0.61, Prior: &prior,
		Confidence: &confidence, Probabilities: map[string]float64{"work": 0.8, "finance": 0.2}}
	result := triage.Result{MessageID: 1, Values: map[string]taxonomy.Value{"category": value},
		DecidedAt: "2026-10-26T09:00:00Z"}
	w.triage.page = &triage.Page{Key: store.TriageKey{Fingerprint: "fp", Decider: "layer1"},
		Results: map[int64]triage.PageRow{1: {MessageID: 1, Answered: 1,
			FirstQuestion: "category", FirstValue: value, DecidedAt: "2026-10-26T09:00:00Z"}},
		Routed: true, Total: 1, Limit: 50}
	w.triage.result = result

	status, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage"})
	mustStatus(t, status, 200, data)
	requireNoTriageDiagnostics(t, data)
	body := decodeMap(t, data)
	results, _ := body["results"].(map[string]any)
	entry, _ := results["1"].(map[string]any)
	// The list row is a projection: the full value map is not on the page.
	if _, ok := entry["values"]; ok {
		t.Fatalf("the triage page still ships the full value map: %s", data)
	}
	first, _ := entry["first_value"].(map[string]any)
	assertTriageValueUI(t, map[string]any{"category": first}, value)

	status, _, data = do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage/1"})
	mustStatus(t, status, 200, data)
	requireNoTriageDiagnostics(t, data)
	body = decodeMap(t, data)
	values, _ := body["values"].(map[string]any)
	assertTriageValueUI(t, values, value)
}

// The list row reports usable answers, present abstentions/refusals and
// absent questions apart; the wire carries all three and never omits an
// empty abstained count.
func TestTriagePageCarriesAbstainedCount(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.triage.page = &triage.Page{Key: store.TriageKey{Fingerprint: "fp", Decider: "layer1"},
		Results: map[int64]triage.PageRow{
			1: {MessageID: 1, Answered: 2, Abstained: 3, Missing: 1},
			2: {MessageID: 2},
		}, Routed: true, Total: 2, Limit: 50}

	status, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	results, _ := body["results"].(map[string]any)
	entry, _ := results["1"].(map[string]any)
	if entry["answered"] != float64(2) || entry["abstained"] != float64(3) || entry["missing"] != float64(1) {
		t.Fatalf("row counts on the wire: %#v", entry)
	}
	zero, _ := results["2"].(map[string]any)
	if got, ok := zero["abstained"]; !ok || got != float64(0) {
		t.Fatalf("an empty abstained count is not serialized: %#v", zero)
	}
}

func assertTriageValueUI(t *testing.T, values map[string]any, want taxonomy.Value) {
	t.Helper()
	value, ok := values["category"].(map[string]any)
	if !ok {
		t.Fatalf("the answer is missing: %v", values)
	}
	if value["type"] != string(want.Type) || value["choice"] != want.Choice || value["number"] != want.Number {
		t.Fatalf("the answer fields changed: %v", value)
	}
	confidence, ok := value["confidence"].(float64)
	if !ok || want.Confidence == nil || confidence != *want.Confidence {
		t.Fatalf("confidence %v, want %v", value["confidence"], want.Confidence)
	}
	for _, key := range []string{"model", "prior", "probabilities"} {
		if _, leaked := value[key]; leaked {
			t.Fatalf("the triage wire carries %q: %v", key, value)
		}
	}
}

// requireNoTriageDiagnostics walks the decoded response, so a diagnostic
// nested in a page result, a label task or an answer item cannot hide.
func requireNoTriageDiagnostics(t *testing.T, data []byte) {
	t.Helper()
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("not JSON: %v: %s", err, data)
	}
	var found []string
	findJSONKeys(body, map[string]bool{"model": true, "prior": true, "probabilities": true}, &found)
	if len(found) > 0 {
		t.Fatalf("triage diagnostics on the wire: %v: %s", found, data)
	}
}

func findJSONKeys(value any, banned map[string]bool, found *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if banned[key] {
				*found = append(*found, key)
			}
			findJSONKeys(child, banned, found)
		}
	case []any:
		for _, child := range v {
			findJSONKeys(child, banned, found)
		}
	}
}

// The per-message result carries the honest display flag: true only when
// at least one answer was substituted by the layer-1 preview.
func TestTriageResultCarriesPreview(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.triage.result = triage.Result{MessageID: 1, Preview: true}
	status, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/triage/1"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["preview"] != true || body["message_id"].(float64) != 1 {
		t.Fatalf("result fields %#v", body)
	}
}
