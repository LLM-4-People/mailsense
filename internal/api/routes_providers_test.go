package api

import (
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// GET /api/providers describes the provider record itself: the editor reads
// the field schema from here instead of keeping a second copy of the bounds
// and enums. The payload stays additive: providers, roles and protocols keep
// their shape.
func TestProvidersViewCarriesTheRecordSchema(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/providers"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	rawFields, ok := body["fields"].([]any)
	if !ok {
		t.Fatalf("fields is not an array: %s", data)
	}
	if len(rawFields) != len(engine.ProviderFields()) {
		t.Fatalf("%d schema fields, want %d", len(rawFields), len(engine.ProviderFields()))
	}
	byJSON := make(map[string]map[string]any, len(rawFields))
	for _, raw := range rawFields {
		field, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("schema entry %#v", raw)
		}
		for _, key := range []string{"name", "json", "type", "kinds", "required", "advanced"} {
			if _, ok := field[key]; !ok {
				t.Fatalf("schema entry lacks %q: %#v", key, field)
			}
		}
		byJSON[field["json"].(string)] = field
	}
	tolerance, ok := byJSON["probability_tolerance"]
	if !ok {
		t.Fatal("the schema lacks probability_tolerance")
	}
	bounds, ok := tolerance["bounds"].(map[string]any)
	if !ok {
		t.Fatalf("probability_tolerance bounds %#v", tolerance["bounds"])
	}
	wantBounds := map[string]any{"min": float64(0), "min_exclusive": true, "max": float64(1), "max_exclusive": true}
	if !reflect.DeepEqual(bounds, wantBounds) {
		t.Fatalf("probability_tolerance bounds %#v, want %#v", bounds, wantBounds)
	}
	if !reflect.DeepEqual(tolerance["kinds"], []any{"decider"}) {
		t.Fatalf("probability_tolerance kinds %#v", tolerance["kinds"])
	}
	if enum, ok := byJSON["reasoning_effort"]["enum"].([]any); !ok || len(enum) != 8 {
		t.Fatalf("reasoning_effort enum %#v", byJSON["reasoning_effort"]["enum"])
	}
	if _, ok := body["providers"]; !ok {
		t.Fatal("providers disappeared from the view")
	}
	if _, ok := body["roles"]; !ok {
		t.Fatal("roles disappeared from the view")
	}
	if protocols, ok := body["protocols"].([]any); !ok || len(protocols) != len(engine.Protocols()) {
		t.Fatalf("protocols %#v", body["protocols"])
	}
}
