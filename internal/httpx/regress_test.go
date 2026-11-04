package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Review 1a (low/rules 5): debug body logging read the response before
// RoundTrip returned, stalling streamed responses.
func TestDebugBodyLoggingDoesNotStallStreaming(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"log.components": []string{"httpx=debug"}})
	svc, err := logx.Setup(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first-chunk"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("-rest"))
	}))
	defer srv.Close()
	defer close(release)
	c, _ := newClient(t, cfg)
	got := make(chan *http.Response, 1)
	go func() {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Error(err)
		}
		got <- resp
	}()
	select {
	case resp := <-got:
		buf := make([]byte, len("first-chunk"))
		if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "first-chunk" {
			t.Fatalf("stream read: %q %v", buf, err)
		}
		resp.Body.Close()
	case <-t.Context().Done():
		t.Fatal("RoundTrip blocked until the whole body arrived")
	}
}
