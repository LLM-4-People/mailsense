package qa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

// A missing provider key is a typed unavailable answer, not a panic and not
// a call: the service turns it into retrieval mode.
func TestSourceMissingKeyIsUnavailable(t *testing.T) {
	cfg := configtest.New(t, nil)
	sec, err := secrets.Open(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	src := NewSource(engine.Deps{Config: cfg, Secrets: sec})
	if _, err := src.Generator(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing key error %v", err)
	}
}

type staticSecrets map[string]string

func (s staticSecrets) Get(name string) (string, error) {
	if value, ok := s[name]; ok {
		return value, nil
	}
	return "", secrets.ErrNotFound
}
func (staticSecrets) Set(string, string) error { return secrets.ErrReadOnly }
func (staticSecrets) Delete(string) error      { return secrets.ErrReadOnly }
func (staticSecrets) List() ([]string, error)  { return nil, nil }
func (staticSecrets) Kind() string             { return "fake" }
func (staticSecrets) ReadOnly() bool           { return true }

// The Q&A factory is the owner that applies the answer budget. A reasoning
// provider must receive the answer plus its effort's allowance as max_tokens,
// otherwise it can spend the whole cap thinking and return a null content.
func TestSourceOutputCapCoversReasoning(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	cfg := configtest.New(t, map[string]any{"qa.provider": "gen", "qa.max_answer_tokens": 350})
	zero := 0.0
	provider := engine.Provider{Protocol: engine.ProtocolOpenAIChat, BaseURL: srv.URL + "/v1",
		Path: "/chat/completions", Model: "reasoner-1", APIKeySecret: "GEN_KEY",
		ContextTokens: 100000, CharsPerToken: 3, ReserveTokensPerByte: 1,
		ReserveOutputTokens: 16384, ResponseFormat: engine.ResponseFormatJSON, ReasoningEffort: "low",
		PriceInput: &zero, PriceOutput: &zero}
	raw, err := json.Marshal(map[string]engine.Provider{"gen": provider})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewSource(engine.Deps{Config: cfg, Secrets: staticSecrets{"GEN_KEY": "k"}})
	gen, err := src.Generator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gen.Generate(t.Context(), "system", "user"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if body["max_tokens"] != float64(4446) {
		t.Fatalf("max_tokens %v, want 4446", body["max_tokens"])
	}
	if rf, _ := body["response_format"].(map[string]any); rf["type"] != engine.ResponseFormatJSON {
		t.Fatalf("the planner contract must keep JSON mode: %v", body["response_format"])
	}
}
