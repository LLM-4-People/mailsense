package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

var epoch = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func newClient(t *testing.T, cfg *config.Config, opts ...Option) (*http.Client, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(epoch)
	c, err := New(cfg, nil, "test", append([]Option{WithClock(clk), WithRand(func() float64 { return 0 })}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c, clk
}

// script answers with the given statuses in order, then 200.
func script(t *testing.T, hits *atomic.Int32, bodies *[]string, statuses ...int) *httptest.Server {
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1))
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if bodies != nil {
			*bodies = append(*bodies, string(b))
		}
		mu.Unlock()
		w.Header().Set("request-id", "req-"+r.Header.Get("User-Agent"))
		if n <= len(statuses) {
			w.WriteHeader(statuses[n-1])
			_, _ = w.Write([]byte("busy"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRetriesRetryableStatusThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	var bodies []string
	srv := script(t, &hits, &bodies, 503, 429)
	c, s := newClient(t, configtest.New(t, nil))
	resp, err := c.Post(srv.URL+"/v1/x?key=secret", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" || hits.Load() != 3 {
		t.Fatalf("body %q after %d hits", b, hits.Load())
	}
	for _, body := range bodies {
		if body != `{"q":1}` {
			t.Fatalf("body not replayed: %q", bodies)
		}
	}
	if d := s.Sleeps(); len(d) != 2 || d[0] != time.Second || d[1] != 2*time.Second {
		t.Fatalf("sleeps: %v", d)
	}
}

func TestFinalStatusesAreNotRetried(t *testing.T) {
	for _, code := range []int{500, 400, 401, 402} {
		var hits atomic.Int32
		srv := script(t, &hits, nil, code)
		c, _ := newClient(t, configtest.New(t, nil))
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != code || hits.Load() != 1 {
			t.Errorf("%d: status %d after %d hits", code, resp.StatusCode, hits.Load())
		}
	}
}

func TestExhaustedReturnsLastResponse(t *testing.T) {
	var hits atomic.Int32
	srv := script(t, &hits, nil, 503, 503, 503)
	c, _ := newClient(t, configtest.New(t, map[string]any{"http.retry.max_attempts": 2}))
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 503 || string(b) != "busy" || hits.Load() != 2 {
		t.Fatalf("status %d body %q hits %d", resp.StatusCode, b, hits.Load())
	}
}

func TestTimeoutAfterWriteIsTerminal(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	base := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	c, s := newClient(t, configtest.New(t, nil), WithBaseTransport(base))
	_, err := c.Post(srv.URL, "text/plain", strings.NewReader("bill me once"))
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if hits.Load() != 1 || len(s.Sleeps()) != 0 {
		t.Fatalf("a timeout after write must not retry: hits=%d sleeps=%v", hits.Load(), s.Sleeps())
	}
}

func TestUnsentFailureIsRetried(t *testing.T) {
	var dials atomic.Int32
	refuse := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("connection refused")
	}}
	bus := events.New()
	retries, _ := events.Subscribe(bus, TopicRetry, 8)
	clk := clocktest.New(epoch)
	c, err := New(configtest.New(t, map[string]any{"http.retry.max_attempts": 3}), bus, "api",
		WithBaseTransport(refuse), WithClock(clk), WithRand(func() float64 { return 0 }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("http://api.invalid/v1"); err == nil {
		t.Fatal("expected a dial error")
	}
	if d := clk.Sleeps(); dials.Load() != 3 || len(d) != 2 || d[0] != time.Second || d[1] != 2*time.Second {
		t.Fatalf("unsent failures must retry with backoff: dials %d sleeps %v", dials.Load(), d)
	}
	if ev := <-retries.C; ev.Client != "api" || ev.Attempt != 1 || ev.Delay != time.Second || ev.Path != "/v1" {
		t.Fatalf("retry event %+v", ev)
	}
}

func TestHTTPDateRetryAfterUsesTheClock(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", epoch.Add(42*time.Second).Format(http.TimeFormat))
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	c, clk := newClient(t, configtest.New(t, nil))
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d := clk.Sleeps(); len(d) != 1 || d[0] != 42*time.Second {
		t.Fatalf("sleeps %v", d)
	}
}

type onceReader struct{ io.Reader }

func TestNonReplayableBodyIsNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := script(t, &hits, nil, 503)
	c, _ := newClient(t, configtest.New(t, nil))
	req, _ := http.NewRequest(http.MethodPost, srv.URL, onceReader{strings.NewReader("x")})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 || resp.StatusCode != 503 {
		t.Fatalf("hits %d status %d", hits.Load(), resp.StatusCode)
	}
}

func TestCancelDuringBackoff(t *testing.T) {
	var hits atomic.Int32
	srv := script(t, &hits, nil, 503, 503, 503)
	ctx, cancel := context.WithCancel(t.Context())
	clk := clocktest.New(epoch)
	clk.OnSleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() } // the user cancels mid-wait
	c, err := New(configtest.New(t, nil), nil, "test", WithClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := c.Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("cancel must interrupt the backoff (hits %d)", hits.Load())
	}
}

func TestLoggingAttributesAndRedactedBodies(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"log.components": []string{"httpx=debug"}, "http.user_agent": "UA1",
		"http.log.body_max_bytes": 512})
	svc, err := logx.Setup(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var hits atomic.Int32
	srv := script(t, &hits, nil)
	c, _ := newClient(t, cfg)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/p?api_key=zzz", strings.NewReader(`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`))
	req.Header.Set("Authorization", "Bearer header-token-123456")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("body must survive debug peeking: %q", b)
	}
	var sawRequest, sawBodies bool
	for _, e := range svc.Ring().Snapshot() {
		line := e.Message
		for k, v := range e.Attrs {
			line += " " + k + "=" + toString(v)
		}
		if strings.Contains(line, "abcdefghijklmnop") || strings.Contains(line, "header-token") || strings.Contains(line, "zzz") {
			t.Errorf("secret leaked: %s", line)
		}
		switch e.Message {
		case "http request":
			sawRequest = e.Attrs["status"] == int64(200) && e.Attrs["hdr.request-id"] == "req-UA1" && e.Attrs["path"] == "/p" && e.Attrs["attempt"] == int64(1)
		case "http bodies":
			sawBodies = e.Attrs["response_body"] == "ok"
		}
	}
	if !sawRequest || !sawBodies {
		t.Fatalf("missing log entries: request=%v bodies=%v", sawRequest, sawBodies)
	}
}

func TestQuietUnavailableStatusLevels(t *testing.T) {
	cases := []struct {
		name   string
		status int
		quiet  bool
		want   string
	}{
		{"503 quiet", http.StatusServiceUnavailable, true, "DEBUG"},
		{"503 default", http.StatusServiceUnavailable, false, "WARN"},
		{"500 quiet", http.StatusInternalServerError, true, "WARN"},
		{"200 quiet", http.StatusOK, true, "INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configtest.New(t, map[string]any{
				"log.components":          []string{"httpx=debug"},
				"http.retry.max_attempts": 1,
			})
			svc, err := logx.Setup(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			var hits atomic.Int32
			srv := script(t, &hits, nil, tc.status)
			var opts []Option
			if tc.quiet {
				opts = append(opts, WithQuietUnavailable())
			}
			c, _ := newClient(t, cfg, opts...)
			resp, err := c.Get(srv.URL + "/health")
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			var got string
			for _, e := range svc.Ring().Snapshot() {
				if e.Component == "httpx" && e.Message == "http request" && e.Attrs["status"] == int64(tc.status) {
					got = e.Level
				}
			}
			if got != tc.want {
				t.Fatalf("status %d quiet=%v logged at %q, want %q", tc.status, tc.quiet, got, tc.want)
			}
		})
	}
}

func TestQuietSuccessLogsAtDebug(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"log.components": []string{"httpx=debug"}})
	svc, err := logx.Setup(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	var hits atomic.Int32
	srv := script(t, &hits, nil)
	c, _ := newClient(t, cfg, WithQuietSuccess())
	resp, err := c.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	for _, e := range svc.Ring().Snapshot() {
		if e.Message == "http request" {
			if e.Level != "DEBUG" {
				t.Fatalf("a quiet success logged at %s", e.Level)
			}
			return
		}
	}
	t.Fatal("no request entry")
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func TestNewTransportFromConfig(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"http.proxy_url": "http://proxy.local:3128", "http.timeout.dial": "3s"})
	tr, err := NewTransport(cfg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.example/", nil)
	u, _ := tr.Proxy(req)
	if u == nil || u.Host != "proxy.local:3128" || tr.ResponseHeaderTimeout != 120*time.Second {
		t.Fatalf("transport: proxy=%v rht=%v", u, tr.ResponseHeaderTimeout)
	}
}

// A redirect must not move a provider call to another origin: the bearer key
// and the prompt body would leave with it.
func TestRedirectsStayOnTheSameOrigin(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)
	first := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(first.Close)

	c, _ := newClient(t, configtest.New(t, nil))
	if _, err := c.Post(first.URL, "application/json", strings.NewReader(`{"q":1}`)); err == nil {
		t.Fatal("a cross-origin redirect was followed")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("the redirect reached the second origin %d times", targetHits.Load())
	}
}

// A pinned artifact download may follow the CDN's cross-origin redirect: it
// sends no credential and verifies the bytes it receives. A write (a
// provider call) is still refused, even with the option.
func TestCrossOriginRedirectIsAllowedOnlyForReads(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("artifact"))
	}))
	t.Cleanup(target.Close)
	first := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	t.Cleanup(first.Close)

	c, _ := newClient(t, configtest.New(t, nil), WithCrossOriginRedirects())
	req, _ := http.NewRequest(http.MethodGet, first.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("a read did not follow the artifact redirect: %v", err)
	}
	resp.Body.Close()
	if targetHits.Load() != 1 {
		t.Fatalf("the redirect reached the second origin %d times, want 1", targetHits.Load())
	}

	targetHits.Store(0)
	req, _ = http.NewRequest(http.MethodPost, first.URL, strings.NewReader(`{"q":1}`))
	if _, err := c.Do(req); err == nil {
		t.Fatal("a write followed a cross-origin redirect")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("a write reached the second origin %d times", targetHits.Load())
	}
}

// A same-origin redirect is ordinary endpoint behaviour and must still work.
func TestSameOriginRedirectIsFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, _ := newClient(t, configtest.New(t, nil))
	resp, err := c.Post(srv.URL+"/old", "application/json", strings.NewReader(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}
