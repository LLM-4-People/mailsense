package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var baseURLPattern = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/$`)

func newStartedServer(t *testing.T, opts Options, w *world) (*Server, string) {
	t.Helper()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	base, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(ctx)
		w.bus.Close()
	})
	return srv, base
}

func TestDescriptorAndToken(t *testing.T) {
	opts := testOptions(t)
	w := newWorld()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	if srv.URL() != "" || srv.BootstrapURL() != "" {
		t.Fatal("a URL exists before Start")
	}
	if len(srv.token) != 43 {
		t.Fatalf("token length %d", len(srv.token))
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(srv.token) {
		t.Fatalf("token is not base64url: %q", srv.token)
	}
	if len(srv.Launch()) != 16 || strings.Contains(srv.Launch(), "-") {
		t.Fatalf("launch %q", srv.Launch())
	}
	base, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(ctx)
		w.bus.Close()
	})
	if srv.URL() != base || !baseURLPattern.MatchString(base) {
		t.Fatalf("URL %q base %q", srv.URL(), base)
	}
	if want := base + "#token=" + srv.token; srv.BootstrapURL() != want {
		t.Fatalf("BootstrapURL %q", srv.BootstrapURL())
	}

	info, err := os.Stat(opts.DescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("descriptor mode %o", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(opts.DescriptorPath))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && parent.Mode().Perm() != 0700 {
		t.Fatalf("descriptor directory mode %o", parent.Mode().Perm())
	}
	data, err := os.ReadFile(opts.DescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var d descriptor
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Version != 1 || d.Launch != srv.Launch() || d.PID != os.Getpid() || d.URL != base ||
		d.Token != srv.token || d.StartedAt.IsZero() || d.Build == "" {
		t.Fatalf("descriptor %+v", d)
	}

	req, err := http.NewRequest(http.MethodGet, base+"api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+srv.token)
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), srv.token) || strings.Contains(string(body), "#token=") {
		t.Fatalf("status leaks the token: %s", body)
	}
	var view StatusView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Launch != srv.Launch() || view.URL != base || view.PID != os.Getpid() {
		t.Fatalf("status view %+v", view)
	}

	if _, err := srv.Start(); err == nil {
		t.Fatal("a second Start succeeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opts.DescriptorPath); !os.IsNotExist(err) {
		t.Fatalf("descriptor survived shutdown: %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestTokensArePerLaunch(t *testing.T) {
	a := newWorld()
	b := newWorld()
	first, err := New(a.services, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(b.services, testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if first.token == second.token {
		t.Fatal("two launches share a token")
	}
	if first.Launch() == second.Launch() {
		t.Fatal("two launches share a launch id")
	}
	// The hub subscriptions must not leak into the next test.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = first.Shutdown(ctx)
	_ = second.Shutdown(ctx)
	a.bus.Close()
	b.bus.Close()
}

func TestDescriptorIsNotDeletedForAnotherLaunch(t *testing.T) {
	opts := testOptions(t)
	w := newWorld()
	srv, base := newStartedServer(t, opts, w)
	if base == "" {
		t.Fatal("no base URL")
	}
	data, err := os.ReadFile(opts.DescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	var d descriptor
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	d.Launch = "anotherlaunch000"
	data, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.DescriptorPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(opts.DescriptorPath); err != nil {
		t.Fatalf("shutdown deleted a descriptor for another launch: %v", err)
	}
}

func TestStartFailsWhenTheDescriptorCannotBeWritten(t *testing.T) {
	opts := testOptions(t)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.DescriptorPath = filepath.Join(blocker, "api.json")
	w := newWorld()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Start(); err == nil {
		t.Fatal("Start succeeded without a descriptor")
	}
	srv.startMu.Lock()
	done := srv.serveDone
	srv.startMu.Unlock()
	if done == nil {
		t.Fatal("the serve loop was never started")
	}
	select {
	case <-done:
	default:
		t.Fatal("the serve loop is still running after the descriptor failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	w.bus.Close()
}

// A handler that ignores cancellation outlives http.Server.Shutdown. Shutdown
// must report that it did not join the handler, so the process owner keeps the
// store open instead of closing it under the running handler.
func TestShutdownReportsHandlersThatIgnoreCancellation(t *testing.T) {
	opts := testOptions(t)
	w := newWorld()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	left := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	stop := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stop()
		w.bus.Close()
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/block", func(http.ResponseWriter, *http.Request) {
		defer close(left)
		close(entered)
		<-release // deliberately ignores the request context
	})
	srv.handler = srv.middleware(mux)
	base, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, err := noRedirectClient.Get(base + "block")
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = srv.Shutdown(ctx)
	if !errors.Is(err, ErrHandlersUnjoined) {
		t.Fatalf("Shutdown returned %v, want ErrHandlersUnjoined", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown returned %v, want the expired context", err)
	}
	select {
	case <-left:
		t.Fatal("the handler returned before it was released")
	default:
	}

	stop()
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("the released handler did not return")
	}
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not return after the connection closed")
	}
}

// Once Shutdown has closed the handler gate, a request that still reaches the
// router is refused before it can touch a service or the store.
func TestShutdownRefusesNewRequests(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, _, data := do(t, ts, srv, request{method: http.MethodGet, path: "/api/status"})
	mustStatus(t, code, http.StatusServiceUnavailable, data)
	if got := errorCode(t, data); got != "shutting_down" {
		t.Fatalf("code %q", got)
	}

	resp, err := noRedirectClient.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "shutting down") {
		t.Fatalf("static shutdown response %d: %q", resp.StatusCode, body)
	}
}

func TestShutdownBeforeStart(t *testing.T) {
	opts := testOptions(t)
	w := newWorld()
	srv, err := New(w.services, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.bus.Close()
}

func TestShutdownEndpoint(t *testing.T) {
	srv, ts, w := newTestServer(t)
	code, _, data := do(t, ts, srv, request{method: "POST", path: "/api/shutdown", body: `{}`})
	mustStatus(t, code, 202, data)
	if w.shutdown.calls != 1 {
		t.Fatalf("shutdown calls %d", w.shutdown.calls)
	}
	w.shutdown.err = ErrShutdown
	code, _, data = do(t, ts, srv, request{method: "POST", path: "/api/shutdown", body: `{}`})
	mustStatus(t, code, 503, data)
	if got := errorCode(t, data); got != "shutting_down" {
		t.Fatalf("code %q", got)
	}
}
