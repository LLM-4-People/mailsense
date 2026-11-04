package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

func setupLogs(t *testing.T, bus *events.Bus) *logx.Service {
	t.Helper()
	cfg := configtest.NewWithBus(t, bus, nil)
	svc, err := logx.Setup(cfg, bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

type panicStatus struct{ value string }

func (p panicStatus) Status(context.Context) (StatusView, error) { panic(p.value) }

// ringLogs serves the real logx ring through the API seam, so the no-echo
// tests read the same redacted entries production serves.
type ringLogs struct{ ring *logx.Ring }

func (r ringLogs) ReadLimit(seq uint64, limit int, newest bool) ([]logx.Entry, uint64, uint64) {
	return r.ring.ReadLimit(seq, limit, newest)
}
func (r ringLogs) Bounds() (uint64, uint64) { return r.ring.Bounds() }

func (r ringLogs) Snapshot(q LogQuery) (LogSnapshot, error) {
	entries, oldest, last := r.ring.ReadLimit(q.Since, q.Limit, !q.HasSince)
	base := q.Since
	if !q.HasSince || q.Since < oldest {
		base = oldest - 1
	}
	truncated := last > 0 && last > base && last-base > uint64(q.Limit)
	return LogSnapshot{Entries: LogEntryViews(entries), Health: logx.Health{}, OldestSeq: oldest, LastSeq: last, Truncated: truncated}, nil
}

func TestNoTokenEcho(t *testing.T) {
	w := newWorld()
	logs := setupLogs(t, w.bus)
	w.services.Logs = ringLogs{ring: logs.Ring()}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	token := srv.token
	port := endpointURL(t, ts).Port()

	w.learning.allowErr = lockedGate()
	w.status.err = errors.New("status failed with " + token)
	cases := []request{
		{method: "GET", path: "/api/status"},
		{method: "GET", path: "/api/learning"},
		{method: "GET", path: "/api/logs"},
		{method: "GET", path: "/api/secrets"},
		noAuth(request{method: "GET", path: "/api/status"}),
		{method: "GET", path: "/api/status", host: "evil.test:" + port},
		{method: "GET", path: "/api/nope"},
		{method: "GET", path: "/api/triage"},
	}
	for _, in := range cases {
		_, _, data := do(t, ts, srv, in)
		if strings.Contains(string(data), token) || strings.Contains(string(data), "#token=") {
			t.Fatalf("%s %s leaks the token: %s", in.method, in.path, data)
		}
	}
	status, header, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/runtime/api.json"}))
	mustStatus(t, status, 404, data)
	if strings.Contains(string(data), token) {
		t.Fatal("the descriptor route leaks the token")
	}
	if strings.Contains(header.Get("Content-Type"), token) {
		t.Fatal("a header leaks the token")
	}

	st := openSSE(t, ts, srv, "/api/events", nil, context.Background())
	frame := st.next(2 * time.Second)
	raw, _ := json.Marshal(frame)
	if strings.Contains(string(raw), token) || strings.Contains(frame.data(), token) {
		t.Fatal("the first SSE frames leak the token")
	}
	if strings.Contains(st.resp.Header.Get("Content-Type"), token) {
		t.Fatal("the SSE content type leaks the token")
	}

	entries := logs.Ring().Snapshot()
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(line), token) {
			t.Fatalf("the log leaks the token: %s", line)
		}
	}
}

func TestReadOnlySecretRemedy(t *testing.T) {
	const sentinel = "SENTINEL-read-only"
	w := newWorld()
	w.secrets.view.ReadOnly = true
	w.secrets.setErr = secrets.ErrReadOnly
	w.secrets.deleteErr = secrets.ErrReadOnly
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	for _, in := range []request{
		{method: "PUT", path: "/api/secrets/DEMO_API_KEY", body: `{"value":"` + sentinel + `"}`},
		{method: "DELETE", path: "/api/secrets/DEMO_API_KEY"},
	} {
		status, _, data := do(t, ts, srv, in)
		mustStatus(t, status, 403, data)
		if got := errorCode(t, data); got != "read_only" {
			t.Fatalf("%s: code %q", in.method, got)
		}
		if !strings.Contains(string(data), config.EnvSecretPrefix+"DEMO_API_KEY") {
			t.Fatalf("%s: no environment remedy: %s", in.method, data)
		}
		if strings.Contains(string(data), sentinel) {
			t.Fatalf("%s: the remedy echoes the value", in.method)
		}
	}
}

func TestNoSecretEcho(t *testing.T) {
	const sentinel = "SENTINEL-do-not-echo-0123456789"
	w := newWorld()
	logs := setupLogs(t, w.bus)
	w.services.Logs = ringLogs{ring: logs.Ring()}
	w.secrets.setHook = func(_, value string) { logx.RegisterSecret(value) }
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, _, data := do(t, ts, srv, request{method: "PUT", path: "/api/secrets/DEMO_API_KEY", body: `{"value":"` + sentinel + `"}`})
	mustStatus(t, status, 204, data)
	if len(data) != 0 {
		t.Fatalf("the secret write returned a body: %s", data)
	}
	if w.secrets.setCalls != 1 {
		t.Fatalf("set calls %d", w.secrets.setCalls)
	}

	status, _, data = do(t, ts, srv, request{method: "PUT", path: "/api/secrets/DEMO_API_KEY", body: `{"value":"` + sentinel})
	mustStatus(t, status, 400, data)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("the malformed secret echo: %s", data)
	}
	if got := errorCode(t, data); got != "invalid_json" {
		t.Fatalf("code %q", got)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/secrets"})
	mustStatus(t, status, 200, data)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("the secret list echoes a value: %s", data)
	}

	w.settings.setErr = &config.ValidationError{Problems: config.Problems{{
		Key: "http.proxy_url", Source: "file", Message: "cannot parse " + sentinel,
	}}}
	status, _, data = do(t, ts, srv, request{method: "PUT", path: "/api/settings/http.proxy_url",
		body: `{"value":"http://user:` + sentinel + `@host:1/"}`})
	mustStatus(t, status, 422, data)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("the validation error echoes a secret setting: %s", data)
	}
	if !strings.Contains(string(data), "invalid value (hidden)") {
		t.Fatalf("the masked validation error: %s", data)
	}

	logx.For("test").Info("using "+sentinel, "value", sentinel)
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/logs"})
	mustStatus(t, status, 200, data)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("the logs endpoint echoes a secret: %s", data)
	}
	for _, e := range logs.Ring().Snapshot() {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(line), sentinel) {
			t.Fatalf("the ring entry leaks the secret: %s", line)
		}
	}
}

func TestPanicRecovery(t *testing.T) {
	const sentinel = "PANIC-VALUE-do-not-echo"
	w := newWorld()
	logs := setupLogs(t, w.bus)
	w.services.Status = panicStatus{value: sentinel}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, header, data := do(t, ts, srv, request{method: "GET", path: "/api/status"})
	mustStatus(t, status, 500, data)
	if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type %q", header.Get("Content-Type"))
	}
	if got := errorCode(t, data); got != "internal" {
		t.Fatalf("code %q", got)
	}
	if strings.Contains(string(data), sentinel) || strings.Contains(string(data), "goroutine") {
		t.Fatalf("the 500 body leaks the panic: %s", data)
	}
	for _, e := range logs.Ring().Snapshot() {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(line), sentinel) {
			t.Fatalf("the panic log leaks the value: %s", line)
		}
	}

	// A panic before any byte becomes a JSON 500; a panic after the headers
	// can only abort the connection, never append a second response.
	panicHandler := srv.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(sentinel)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+srv.token)
	panicHandler.ServeHTTP(rec, req)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("recovered panic: %d %q", rec.Code, rec.Body.String())
	}

	wroteThenPanic := srv.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("already sent"))
		panic(sentinel)
	}))
	aborted := false
	func() {
		defer func() {
			rec := recover()
			aborted = rec == http.ErrAbortHandler
		}()
		wroteThenPanic.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if !aborted {
		t.Fatal("a panic after the response did not abort the connection")
	}
}
