package embed

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// The embedder installs the same provider policy hook as the decider and the
// generator: a quota code in a normally retryable response is final, so a
// run stops after one call instead of burning every attempt.
func TestQuotaBodyIsFinalThroughTheSharedPolicyHook(t *testing.T) {
	cfg := configtest.New(t, nil)
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"out of credits"}}`)
	}))
	defer srv.Close()

	p := preset(t, cfg)
	p.Runtime, p.BaseURL, p.APIKeySecret, p.ContextTokens = "", srv.URL, "", 2048
	p.Retry.QuotaCodes = []string{"credit_balance_exhausted"}
	a, err := New(p, engine.Deps{Config: cfg},
		httpx.WithClock(clocktest.New(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Embed(t.Context(), docs(1)); !errors.Is(err, ErrTransient) {
		t.Fatalf("a quota 429 is a provider-wide temporary failure: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("a quota body is final: %d attempts, want 1", hits)
	}
}
