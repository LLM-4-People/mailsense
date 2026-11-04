package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

func TestPolicyOverrideAndQuotaBodyIsFinal(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		switch r.URL.Path {
		case "/quota":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":"credit_balance_exhausted"}}`))
		case "/overloaded":
			if n == 1 {
				w.WriteHeader(529)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	c, clk := newClient(t, configtest.New(t, nil))
	final := func(_ int, body []byte) bool { return strings.Contains(string(body), "credit_balance_exhausted") }
	ctx := WithPolicy(t.Context(), func(p *Policy) { p.Final = final; p.Statuses = append(p.Statuses, 529) })

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/quota", strings.NewReader("{}"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 429 || hits.Load() != 1 || !strings.Contains(string(body), "credit_balance") {
		t.Fatalf("a quota 429 must not be retried and keep its body: status %d hits %d body %q", resp.StatusCode, hits.Load(), body)
	}

	hits.Store(0)
	req, _ = http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/overloaded", strings.NewReader("{}"))
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || hits.Load() != 2 || len(clk.Sleeps()) != 1 {
		t.Fatalf("an override status is retried: status %d hits %d", resp.StatusCode, hits.Load())
	}

	// Without the override 529 is final.
	hits.Store(0)
	resp, err = c.Post(srv.URL+"/overloaded", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 529 || hits.Load() != 1 {
		t.Fatalf("default policy: status %d hits %d", resp.StatusCode, hits.Load())
	}
}

// Review 2a #6: the global http.retry.statuses could list 500 (and a
// provider override could re-add it); a 500 may have billed. The schema
// now rejects it and the policy never retries the not_in statuses.
func TestNeverRetriedStatusesCannotBeConfiguredRetryable(t *testing.T) {
	if err := configtest.New(t, nil).Set("http.retry.statuses", []any{500, 429}); err == nil {
		t.Fatal("the schema must reject 500 in http.retry.statuses")
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c, _ := newClient(t, configtest.New(t, nil))
	ctx := WithPolicy(t.Context(), func(p *Policy) { p.Statuses = []int{500}; p.Never = nil })
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("x"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 || hits.Load() != 1 {
		t.Fatalf("a 500 must never be retried, even through an override: hits %d", hits.Load())
	}
	if n := NeverRetried(configtest.New(t, nil).Snapshot()); len(n) != 7 {
		t.Fatalf("never list %v", n)
	}
}

func TestFailureAfterWriteIsMarked(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	c, _ := newClient(t, configtest.New(t, nil), WithBaseTransport(&http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}))
	_, err := c.Post(srv.URL, "text/plain", strings.NewReader("bill me once"))
	if !errors.Is(err, ErrAfterWrite) {
		t.Fatalf("a post-write failure must be marked (may be billed): %v", err)
	}
	refuse := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("refused") }}
	c2, _ := newClient(t, configtest.New(t, map[string]any{"http.retry.max_attempts": 1}), WithBaseTransport(refuse))
	if _, err := c2.Get("http://x.invalid/"); err == nil || errors.Is(err, ErrAfterWrite) {
		t.Fatalf("an unsent failure is not marked: %v", err)
	}
}
