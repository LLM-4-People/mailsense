package sidecar

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// TestNoKeyBeforeTheChildOwnsThePort is the regression for the readiness
// order: the health probe carries the per-launch bearer key, so until
// ownsPort confirms the child itself holds its port the key must not be
// sent. The fake runtime here stays alive without binding (FAKE_NO_BIND)
// while another listener holds the port and answers every /health with
// 200. The child never owns that port, so the readiness probe must never
// run and the foreign listener must never see a bearer.
func TestNoKeyBeforeTheChildOwnsThePort(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skipf("ownsPort cannot verify ownership on %s", runtime.GOOS)
	}
	var mu sync.Mutex
	var headers []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer fake.Close()
	u, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	restore := choosePort
	choosePort = func() (int, error) { return port, nil }
	t.Cleanup(func() { choosePort = restore })

	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := launch(launchSpec{exe: self, model: model, args: []string{"--model", "{model}", "--host", "{host}"},
		env:    map[string]string{"MAILSENSE_FAKE_LLAMA": "1", "FAKE_NO_BIND": "1"},
		keyEnv: "LLAMA_API_KEY", key: "the-launch-key", threads: 1}, logx.For("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.stop(time.Second, clock.Real()) })

	cfg := configtest.New(t, map[string]any{
		"sidecar.start_poll":      "10ms",
		"sidecar.start_timeout":   "1s",
		"sidecar.health.interval": "100ms",
	})
	health, err := httpx.New(cfg, nil, "sidecar.health", httpx.WithQuietSuccess(), httpx.WithQuietUnavailable())
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: cfg, clock: clock.Real(), log: logx.For("test"), health: health}
	if err := m.waitReady(t.Context(), c, Build{RuntimeSpec: Runtime{HealthPath: "/health"}}); err == nil {
		t.Fatal("the runtime was reported ready while another process held its port")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, got := range headers {
		if got != "" {
			t.Fatalf("the foreign listener received the launch key before ownership: %q", got)
		}
	}
}
