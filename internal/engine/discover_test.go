package engine_test

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

const providerModelsJSON = `{"object":"list","contract_version":"2.0","provider":{"id":"provider.example.test","status_url":"https://provider.example.test/#incidents","support_contact":"mailto:hello@provider.example.test","incident_contact":"mailto:privacy@provider.example.test","regions":["global"],"request_id_header":"x-request-id","error_contract":{"rate_limit_status":429,"overload_status":503,"retry_after_header":"Retry-After","account_quota_error_codes":["insufficient_credits"]}},"data":[{"id":"provider.example.test/deepseek-v4.1-flash-fast","object":"model","owned_by":"provider.example.test","name":"DeepSeek V4.1 Flash Fast","type":"chat","context_length":1048576,"max_output_tokens":393216,"endpoints":["chat/completions"],"input_modalities":["text","image"],"output_modalities":["text"],"capabilities":{"streaming":true,"tools":true,"structured_output":true,"reasoning":true,"prompt_caching":true},"pricing":{"currency":"USD","unit":"per_1m_tokens","input":"0.45","output":"1.80","cached_input":"0.009","cache_write":"0.00","minimum_request":"0"},"lifecycle":{"status":"active","deprecation_at":null,"retirement_at":null,"replacement_model_id":null},"reliability":{"first_token_timeout_seconds":20.0,"completion_timeout_seconds":900.0,"stream_idle_timeout_seconds":300.0,"capacity_scope":"model_region"}},{"id":"provider.example.test/deepseek-v4.1-flash","object":"model","owned_by":"provider.example.test","name":"DeepSeek V4.1 Flash Standard","type":"chat","context_length":1048576,"max_output_tokens":393216,"endpoints":["chat/completions"],"input_modalities":["text","image"],"output_modalities":["text"],"capabilities":{"streaming":true,"tools":true,"structured_output":true,"reasoning":true,"prompt_caching":true},"pricing":{"currency":"USD","unit":"per_1m_tokens","input":"0.19","output":"0.60","cached_input":"0.008","cache_write":"0.00","minimum_request":"0"},"lifecycle":{"status":"active","deprecation_at":null,"retirement_at":null,"replacement_model_id":null},"reliability":{"first_token_timeout_seconds":20.0,"completion_timeout_seconds":900.0,"stream_idle_timeout_seconds":300.0,"capacity_scope":"model_region"}}]}`

type discoverySecrets struct {
	secrets.Store
	values map[string]string
}

func (s discoverySecrets) Get(name string) (string, error) {
	if value, ok := s.values[name]; ok {
		return value, nil
	}
	return "", secrets.ErrNotFound
}

func discoveryDeps(t *testing.T, values map[string]string) engine.Deps {
	t.Helper()
	return engine.Deps{
		Config:  configtest.New(t, map[string]any{"http.retry.max_attempts": 1}),
		Secrets: discoverySecrets{values: values},
	}
}

func modelServer(t *testing.T, status int, body string, check func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func modelByID(t *testing.T, models []engine.ProviderModel, id string) engine.ProviderModel {
	t.Helper()
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("model %q not found in %+v", id, models)
	return engine.ProviderModel{}
}

func priceEqual(a *float64, b float64) bool {
	return a != nil && math.Abs(*a-b) < 1e-12
}

func TestDiscoverModelsProviderPayload(t *testing.T) {
	srv := modelServer(t, http.StatusOK, providerModelsJSON, func(r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("request %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
	})
	models, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{
		BaseURL: srv.URL + "/v1/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "provider.example.test/deepseek-v4.1-flash" || models[1].ID != "provider.example.test/deepseek-v4.1-flash-fast" {
		t.Fatalf("not sorted by id: %+v", models)
	}
	standard := modelByID(t, models, "provider.example.test/deepseek-v4.1-flash")
	fast := modelByID(t, models, "provider.example.test/deepseek-v4.1-flash-fast")
	for _, model := range []engine.ProviderModel{standard, fast} {
		if model.Name == "" || model.Type != "chat" || model.ContextTokens != 1048576 || model.MaxOutputTokens != 393216 {
			t.Fatalf("metadata: %+v", model)
		}
		if !model.StructuredOutput || !model.Reasoning || !model.Tools || !model.Streaming {
			t.Fatalf("capabilities: %+v", model)
		}
		if model.Path != "/chat/completions" || len(model.Endpoints) != 1 || model.Endpoints[0] != "chat/completions" {
			t.Fatalf("endpoint: %+v", model)
		}
	}
	if standard.Name != "DeepSeek V4.1 Flash Standard" || !priceEqual(standard.PriceInput, 0.19) || !priceEqual(standard.PriceOutput, 0.60) {
		t.Fatalf("standard model: %+v", standard)
	}
	if fast.Name != "DeepSeek V4.1 Flash Fast" || !priceEqual(fast.PriceInput, 0.45) || !priceEqual(fast.PriceOutput, 1.80) {
		t.Fatalf("fast model: %+v", fast)
	}
}

func TestDiscoverModelsPlainOpenAIPayloadDoesNotInventValues(t *testing.T) {
	const body = `{"object":"list","data":[{"id":"gpt-4o-mini","object":"model","owned_by":"openai"}]}`
	for _, tc := range []struct {
		name     string
		protocol string
		path     string
		typ      string
	}{
		{"chat default", "", "/chat/completions", "chat"},
		{"chat explicit", engine.ProtocolOpenAIChat, "/chat/completions", "chat"},
		{"embeddings", engine.ProtocolOpenAIEmbeddings, "/embeddings", "embedding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := modelServer(t, http.StatusOK, body, nil)
			models, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{
				BaseURL: srv.URL, Protocol: tc.protocol,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 1 {
				t.Fatalf("models = %+v", models)
			}
			model := models[0]
			if model.ID != "gpt-4o-mini" || model.Name != "" || model.Path != tc.path || model.Type != tc.typ {
				t.Fatalf("model = %+v", model)
			}
			if model.ContextTokens != 0 || model.MaxOutputTokens != 0 || model.PriceInput != nil || model.PriceOutput != nil ||
				model.StructuredOutput || model.Reasoning || model.Tools || model.Streaming || len(model.Endpoints) != 0 {
				t.Fatalf("invented values: %+v", model)
			}
		})
	}
}

func TestDiscoverModelsInfersTypePathAndLimits(t *testing.T) {
	const body = `{"object":"list","data":[
		{"id":"chat-model","context_length":0,"context_tokens":100,"context_window":200,
		 "max_output_tokens":0,"max_completion_tokens":50,"endpoints":["chat/completions"]},
		{"id":"embed-model","endpoints":["/v1/embeddings"]}]}`
	srv := modelServer(t, http.StatusOK, body, nil)
	models, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	chat := modelByID(t, models, "chat-model")
	if chat.Type != "chat" || chat.Path != "/chat/completions" || chat.ContextTokens != 100 || chat.MaxOutputTokens != 50 {
		t.Fatalf("chat model: %+v", chat)
	}
	embed := modelByID(t, models, "embed-model")
	if embed.Type != "embedding" || embed.Path != "/embeddings" {
		t.Fatalf("embedding model: %+v", embed)
	}
}

func TestDiscoverModelsAuthorizationHeader(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    engine.DiscoverRequest
		values map[string]string
		want   string
	}{
		{"form key", engine.DiscoverRequest{APIKey: "form-key", APIKeySecret: "STORED"}, map[string]string{"STORED": "stored-key"}, "Bearer form-key"},
		{"stored secret", engine.DiscoverRequest{APIKeySecret: "STORED"}, map[string]string{"STORED": "stored-key"}, "Bearer stored-key"},
		{"no key", engine.DiscoverRequest{}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := modelServer(t, http.StatusOK, `{"object":"list","data":[{"id":"m"}]}`, func(r *http.Request) {
				got = r.Header.Get("Authorization")
				if r.Header.Get("Accept") != "application/json" {
					t.Errorf("Accept = %q", r.Header.Get("Accept"))
				}
			})
			tc.req.BaseURL = srv.URL
			if _, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, tc.values), tc.req); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiscoverModelsMissingSecretIsExplicit(t *testing.T) {
	_, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{
		BaseURL: "http://127.0.0.1:1/v1", APIKeySecret: "MISSING_KEY",
	})
	if !errors.Is(err, engine.ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("the error contains a key: %v", err)
	}
}

func TestDiscoverModelsErrorsDoNotLeakTheKeyOrBody(t *testing.T) {
	const marker = "upstream-body-marker"
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"401", http.StatusUnauthorized, `{"error":{"message":"` + marker + `"}}`, engine.ErrDiscoveryAuth},
		{"403", http.StatusForbidden, `{"error":{"message":"` + marker + `"}}`, engine.ErrDiscoveryAuth},
		{"404", http.StatusNotFound, marker, engine.ErrDiscoveryNotFound},
		{"500", http.StatusInternalServerError, marker, engine.ErrDiscoveryFailed},
		{"429", http.StatusTooManyRequests, marker, engine.ErrDiscoveryFailed},
		{"invalid json", http.StatusOK, `{"data":[`, engine.ErrDiscoveryInvalid},
		{"empty data", http.StatusOK, `{"object":"list","data":[]}`, engine.ErrDiscoveryEmpty},
		{"no ids", http.StatusOK, `{"object":"list","data":[{"object":"model"}]}`, engine.ErrDiscoveryEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := modelServer(t, tc.status, tc.body, nil)
			_, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{
				BaseURL: srv.URL, APIKey: "super-secret-value",
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			for _, secret := range []string{"super-secret-value", marker} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("the error contains %q: %v", secret, err)
				}
			}
			if tc.status != http.StatusOK && !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(tc.status)) {
				t.Fatalf("the error does not name the status: %v", err)
			}
		})
	}
}

func TestDiscoverModelsRejectsOversizedBody(t *testing.T) {
	body := `{"object":"list","data":[],"pad":"` + strings.Repeat("a", engine.DiscoveryMaxBodyBytes) + `"}`
	srv := modelServer(t, http.StatusOK, body, nil)
	_, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{BaseURL: srv.URL})
	if !errors.Is(err, engine.ErrDiscoveryFailed) {
		t.Fatalf("want ErrDiscoveryFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "2 MiB") {
		t.Fatalf("the error does not name the cap: %v", err)
	}
}

func TestDiscoverModelsConvertsPriceUnits(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		in   float64
		out  float64
	}{
		{"per 1k", `{"object":"list","data":[{"id":"m","pricing":{"unit":"per_1k_tokens","input":"0.00019","output":0.0006}}]}`, 0.19, 0.60},
		{"per token", `{"object":"list","data":[{"id":"m","pricing":{"unit":"per_token","input":0.00000019,"output":"0.0000006"}}]}`, 0.19, 0.60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := modelServer(t, http.StatusOK, tc.body, nil)
			models, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{BaseURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 1 || !priceEqual(models[0].PriceInput, tc.in) || !priceEqual(models[0].PriceOutput, tc.out) {
				t.Fatalf("price conversion: %+v", models)
			}
		})
	}
}

func TestDiscoverModelsUnknownPriceUnitOmitPrices(t *testing.T) {
	const body = `{"object":"list","data":[{"id":"m","pricing":{"unit":"per_hour","input":1,"output":2}}]}`
	srv := modelServer(t, http.StatusOK, body, nil)
	models, err := engine.DiscoverModels(t.Context(), discoveryDeps(t, nil), engine.DiscoverRequest{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].PriceInput != nil || models[0].PriceOutput != nil {
		t.Fatalf("unknown unit: %+v", models)
	}
}

type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("the network must not be reached")
}

// A provider request whose URL the policy refuses is stopped by the client,
// not written to the network, whatever built the request.
func TestProviderRequestToRefusedURLNeverReachesTheNetwork(t *testing.T) {
	base := &countingTransport{}
	cfg := configtest.New(t, map[string]any{"http.retry.max_attempts": 3})
	client, err := httpx.New(cfg, nil, "provider.test", httpx.WithBaseTransport(base))
	if err != nil {
		t.Fatal(err)
	}
	inputErr, httpErr := errors.New("input"), errors.New("http")
	policy := engine.SentinelPolicy("provider.test", inputErr, httpErr)

	_, _, err = engine.PostJSON(t.Context(), cfg, client, &engine.Provider{ID: "provider.test", Protocol: engine.ProtocolOpenAIChat},
		engine.PostSpec{URL: "http://169.254.169.254/v1/chat/completions", Body: []byte(`{}`)}, policy)
	if !errors.Is(err, httpx.ErrURLRefused) {
		t.Fatalf("want ErrURLRefused, got %v", err)
	}
	if calls := base.calls.Load(); calls != 0 {
		t.Fatalf("the base transport was called %d times", calls)
	}
}

func TestNormalizeDiscoverRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  engine.DiscoverRequest
		ok   bool
	}{
		{"trimmed", engine.DiscoverRequest{BaseURL: " https://provider.example.test/v1/ "}, true},
		{"http", engine.DiscoverRequest{BaseURL: "http://127.0.0.1:9/v1"}, true},
		{"empty", engine.DiscoverRequest{}, false},
		{"relative", engine.DiscoverRequest{BaseURL: "/v1"}, false},
		{"other scheme", engine.DiscoverRequest{BaseURL: "ftp://example.test/v1"}, false},
		{"query", engine.DiscoverRequest{BaseURL: "https://example.test/v1?key=x"}, false},
		{"credentials", engine.DiscoverRequest{BaseURL: "https://user:pass@example.test/v1"}, false},
		{"metadata", engine.DiscoverRequest{BaseURL: "http://169.254.169.254"}, false},
		{"link-local IPv6", engine.DiscoverRequest{BaseURL: "http://[fe80::1]/v1"}, false},
		{"control character", engine.DiscoverRequest{BaseURL: "http://exa\nmple.test/v1"}, false},
		{"local runtime", engine.DiscoverRequest{BaseURL: "http://192.168.1.10:8080/v1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := engine.NormalizeDiscoverRequest(tc.req)
			if tc.ok {
				if err != nil || req.Protocol != engine.DiscoveryDefaultProtocol || strings.HasSuffix(req.BaseURL, "/") {
					t.Fatalf("normalized %+v %v", req, err)
				}
				return
			}
			if !errors.Is(err, engine.ErrDiscoveryInvalidRequest) {
				t.Fatalf("want ErrDiscoveryInvalidRequest, got %v", err)
			}
		})
	}
}
