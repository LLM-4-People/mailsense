package httpx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
)

// logTransport records every attempt: method, host, path (never the query),
// status, duration, sizes and request-id headers. Bodies are logged only at
// debug level when http.log.body_max_bytes is positive (off by default: a
// request can carry prompts and mail content, which redaction of secrets
// does not remove), truncated to that limit.
type logTransport struct {
	next             http.RoundTripper
	cfg              *config.Config
	log              *slog.Logger
	clock            clock.Clock
	quietSuccess     bool
	quietUnavailable bool
}

func (t *logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	limit := t.cfg.Int("http.log.body_max_bytes")
	bodies := limit > 0 && t.log.Enabled(ctx, slog.LevelDebug)
	start := t.clock.Now()
	resp, err := t.next.RoundTrip(req)
	attrs := []any{"method", req.Method, "host", req.URL.Host, "path", req.URL.Path, "attempt", attemptOf(ctx),
		"duration_ms", t.clock.Now().Sub(start).Milliseconds(), "req_bytes", req.ContentLength}
	if err != nil {
		t.log.Warn("http request failed", append(attrs, "err", err)...)
		return nil, err
	}
	attrs = append(attrs, "status", resp.StatusCode, "resp_bytes", resp.ContentLength)
	for _, h := range t.cfg.Strings("http.log.request_id_headers") {
		if v := resp.Header.Get(h); v != "" {
			attrs = append(attrs, "hdr."+h, v)
		}
	}
	level := slog.LevelInfo
	if t.quietUnavailable && resp.StatusCode == http.StatusServiceUnavailable {
		level = slog.LevelDebug // an expected "still loading" health answer
	} else if resp.StatusCode >= 400 {
		level = slog.LevelWarn
	} else if t.quietSuccess {
		level = slog.LevelDebug
	}
	t.log.Log(ctx, level, "http request", attrs...)
	if bodies {
		t.logBodies(ctx, req, resp, limit)
	}
	return resp, nil
}

// logBodies logs the request body now and the response body lazily: the
// response is teed while the caller reads it and logged at Close, so
// streamed responses are never held back.
func (t *logTransport) logBodies(ctx context.Context, req *http.Request, resp *http.Response, limit int) {
	var reqBody string
	if req.GetBody != nil {
		if rc, err := req.GetBody(); err == nil {
			b, _ := io.ReadAll(io.LimitReader(rc, int64(limit)))
			_ = rc.Close()
			reqBody = string(b)
		}
	}
	if resp.Body == nil {
		t.log.DebugContext(ctx, "http bodies", "path", req.URL.Path, "request_body", reqBody, "response_body", "")
		return
	}
	resp.Body = &teeBody{rc: resp.Body, limit: limit, done: func(head string) {
		t.log.DebugContext(ctx, "http bodies", "path", req.URL.Path, "request_body", reqBody, "response_body", head)
	}}
}

// teeBody keeps the first limit bytes the caller reads and reports them once at Close.
type teeBody struct {
	rc    io.ReadCloser
	limit int
	head  bytes.Buffer
	once  sync.Once
	done  func(head string)
}

func (b *teeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if room := b.limit - b.head.Len(); room > 0 && n > 0 {
		b.head.Write(p[:min(n, room)])
	}
	return n, err
}

func (b *teeBody) Close() error {
	b.once.Do(func() { b.done(b.head.String()) })
	return b.rc.Close()
}
