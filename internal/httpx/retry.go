package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
)

type attemptKey struct{}

// attemptOf returns the 1-based attempt number carried by a request context.
func attemptOf(ctx context.Context) int {
	n, _ := ctx.Value(attemptKey{}).(int)
	return n
}

// Retry is published before every retry wait (the UI shows back-off and
// server-requested waits).
type Retry struct {
	Client  string        `json:"client"`
	Host    string        `json:"host"`
	Path    string        `json:"path"`
	Attempt int           `json:"attempt"` // the attempt that failed (1-based)
	Delay   time.Duration `json:"delay"`
	Reason  string        `json:"reason"`
}

// TopicRetry carries every retry decision.
var TopicRetry = events.NewTopic[Retry]("http.retry")

// retryTransport applies the Policy around next (the logging transport).
type retryTransport struct {
	next  http.RoundTripper
	cfg   *config.Config
	bus   *events.Bus
	name  string
	log   *slog.Logger
	clock clock.Clock
	rand  func() float64
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	policy := adjusted(req.Context(), PolicyFromConfig(t.cfg.Snapshot()))
	ua := t.cfg.String("http.user_agent")
	for attempt := 0; ; attempt++ {
		r, err := prepare(req, attempt, ua)
		if err != nil {
			return nil, err
		}
		var wrote atomic.Bool
		trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) }}
		ctx := context.WithValue(httptrace.WithClientTrace(r.Context(), trace), attemptKey{}, attempt+1)
		resp, err := t.next.RoundTrip(r.WithContext(ctx))
		if req.Context().Err() != nil {
			return resp, err // cancelled by the caller or the total timeout: never retried
		}
		d := finalByBody(policy, resp, policy.Decide(attempt, wrote.Load(), resp, err, t.clock.Now(), t.rand()))
		if d.Retry && !replayable(req) {
			d = Decision{Reason: d.Reason + "; body cannot be replayed"}
		}
		if !d.Retry {
			if attempt > 0 || err != nil {
				t.log.Info("http request settled", "host", req.URL.Host, "path", req.URL.Path, "attempts", attempt+1, "reason", d.Reason)
			}
			if err != nil && wrote.Load() {
				err = &writtenError{err}
			}
			return resp, err
		}
		discard(resp)
		t.log.Warn("http retry", "host", req.URL.Host, "path", req.URL.Path, "attempt", attempt+1, "delay", d.Delay, "reason", d.Reason)
		if t.bus != nil {
			events.Publish(t.bus, TopicRetry, Retry{Client: t.name, Host: req.URL.Host, Path: req.URL.Path, Attempt: attempt + 1, Delay: d.Delay, Reason: d.Reason})
		}
		if err := t.clock.Sleep(req.Context(), d.Delay); err != nil {
			return nil, err
		}
	}
}

// ErrAfterWrite marks a failure that happened after the request was
// written: the server may have done (and billed) the work.
var ErrAfterWrite = errors.New("httpx: failed after the request was written")

type writtenError struct{ err error }

func (e *writtenError) Error() string   { return e.err.Error() + " (after the request was written)" }
func (e *writtenError) Unwrap() []error { return []error{e.err, ErrAfterWrite} }

// prepare returns the request for one attempt: a clone with a fresh body
// (after the first attempt) and the default User-Agent when none is set.
func prepare(req *http.Request, attempt int, userAgent string) (*http.Request, error) {
	r := req.Clone(req.Context())
	if attempt > 0 && req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		r.Body = body
	}
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", userAgent)
	}
	return r, nil
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func discard(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_ = resp.Body.Close()
}
