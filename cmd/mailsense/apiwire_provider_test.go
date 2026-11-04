package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

func providerInfoJSON(t *testing.T, info any) map[string]any {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The API record embeds engine.Provider, but its JSON must stay exactly the
// record of the old field-by-field copy: same keys, same values, same
// omissions. A field added to either struct changes this test, never the wire
// silently.
func TestProviderInfoJSONIsUnchanged(t *testing.T) {
	input, output := 0.05, 0.0
	attempts := 3
	p := engine.Provider{
		ID: "dec", Label: "Local decider", Protocol: engine.ProtocolSystemOne,
		BaseURL: "http://127.0.0.1:9/v1", Path: "/systemone", Model: "m-1",
		APIKeySecret:  "DECIDER_KEY",
		ContextTokens: 100000, CharsPerToken: 3, ReserveTokensPerByte: 1, ReserveOutputTokens: 1024,
		MaxQuestions: 16, ProbabilityTolerance: 0.02, ResponseFormat: engine.ResponseFormatJSON,
		ReasoningEffort: "low", PriceInput: &input, PriceOutput: &output,
		Retry:    engine.Retry{MaxAttempts: &attempts, Statuses: []int{500}, QuotaCodes: []string{"insufficient_quota"}},
		Envelope: engine.EnvelopeCloudflare, NoulFold: "fold",
		Embedding: &engine.EmbeddingSpec{Dimensions: 8, Matryoshka: []int{4},
			QueryTemplate: "query: {text}", DocumentTemplate: "title: {title} | text: {text}",
			EmptyTitle: "none", NormTolerance: 0.01, MaxBatch: 8, ReservedTokens: 2},
	}
	st := engine.ProviderStatus{Provider: "dec", KeyPresent: true, Healthy: true, Calls: 3,
		Failures: 1, LastStatus: 500, LastError: "boom"}

	got := providerInfoJSON(t, providerInfo(p, st))
	want := map[string]any{
		"id": "dec", "label": "Local decider", "protocol": "systemone", "kind": "decider",
		"base_url": "http://127.0.0.1:9/v1", "path": "/systemone", "model": "m-1",
		"api_key_secret": "DECIDER_KEY",
		"context_tokens": float64(100000), "chars_per_token": float64(3),
		"reserve_tokens_per_byte": float64(1), "reserve_output_tokens": float64(1024),
		"max_questions": float64(16), "probability_tolerance": float64(0.02),
		"response_format": "json_object", "reasoning_effort": "low",
		"price_input_per_mtok": 0.05, "price_output_per_mtok": 0.0,
		"retry": map[string]any{
			"max_attempts": float64(3), "statuses": []any{float64(500)},
			"quota_codes": []any{"insufficient_quota"},
		},
		"envelope": "cloudflare", "noul_fold": "fold",
		"embedding": map[string]any{
			"dimensions": float64(8), "matryoshka": []any{float64(4)},
			"query_template": "query: {text}", "document_template": "title: {title} | text: {text}",
			"empty_title": "none", "norm_tolerance": float64(0.01),
			"max_batch": float64(8), "reserved_tokens": float64(2),
		},
		"key_present": true,
		"status": map[string]any{
			"provider": "dec", "key_present": true, "healthy": true,
			"calls": float64(3), "failures": float64(1), "last_status": float64(500),
			"last_error": "boom",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider JSON:\n got %#v\nwant %#v", got, want)
	}
	for key := range got {
		if strings.Contains(key, "api_key") && key != "api_key_secret" {
			t.Fatalf("a key value field leaked: %q", key)
		}
	}
}

// A record with only the required fields keeps the old omissions: optional
// zero values disappear, the two price keys stay null, and omitting the retry
// policy leaves the key out.
func TestProviderInfoJSONOmitsTheSameEmptyFields(t *testing.T) {
	p := engine.Provider{ID: "gen", Protocol: engine.ProtocolOpenAIChat, Path: "/chat/completions", Model: "g-1"}
	st := engine.ProviderStatus{Provider: "gen"}
	got := providerInfoJSON(t, providerInfo(p, st))
	want := map[string]any{
		"id": "gen", "protocol": "openai_chat", "kind": "generator",
		"base_url": "", "path": "/chat/completions", "model": "g-1",
		"price_input_per_mtok": nil, "price_output_per_mtok": nil,
		"key_present": false,
		"status": map[string]any{
			"provider": "gen", "key_present": false, "healthy": false,
			"calls": float64(0), "failures": float64(0),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider JSON:\n got %#v\nwant %#v", got, want)
	}
}
