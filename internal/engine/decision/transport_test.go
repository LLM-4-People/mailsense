package decision

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// refusedTransport fails every attempt before writing the request, so the
// HTTP retry policy retries it and gives up still never having written it.
type refusedTransport struct {
	attempts int
}

func (t *refusedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.attempts++
	return nil, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
}

// A transport failure that survives the retry policy is about the provider,
// not about this message: it is transient (the original class is kept).
func TestTransportFailureIsTransient(t *testing.T) {
	srv := newServer(t, reply{200, `{}`})
	rt := &refusedTransport{}
	h := newAdapterWith(t, provider(srv, "systemone"), httpx.WithBaseTransport(rt))
	_, err := h.ad.Decide(t.Context(), "state", taxonomy17()[:1])
	if !errors.Is(err, engine.ErrTransient) || !errors.Is(err, ErrHTTP) {
		t.Fatalf("a persistent transport failure is transient, preserving its class: %v", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInput) {
		t.Fatalf("a transport failure is not a cancellation or an input error: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the original failure is preserved: %v", err)
	}
	if want := h.ad.deps.Config.Int("http.retry.max_attempts"); rt.attempts != want {
		t.Fatalf("%d attempts, want %d: httpx must exhaust its policy first", rt.attempts, want)
	}
	if len(h.sink.recs) != 1 || !h.sink.recs[0].Failed {
		t.Fatalf("the failed call is accounted once: %+v", h.sink.recs)
	}
}

// Cancellation is the run stopping for another reason: it must stay
// context.Canceled/DeadlineExceeded, never be mislabelled transient.
func TestCancelledTransportFailureIsNotTransient(t *testing.T) {
	srv := newServer(t, reply{200, `{}`})
	h := newAdapterWith(t, provider(srv, "systemone"), httpx.WithBaseTransport(&refusedTransport{}))
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, _, err := h.ad.call(ctx, "sk-test-0123456789abcdef", []byte(`{}`), taxonomy17()[:1])
		if !errors.Is(err, context.Canceled) || errors.Is(err, engine.ErrTransient) {
			t.Fatalf("cancelled transport failure: %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		defer cancel()
		_, _, err := h.ad.call(ctx, "sk-test-0123456789abcdef", []byte(`{}`), taxonomy17()[:1])
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, engine.ErrTransient) {
			t.Fatalf("expired transport failure: %v", err)
		}
	})
}
