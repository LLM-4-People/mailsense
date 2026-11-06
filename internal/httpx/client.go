// Package httpx is the one HTTP client factory: every outgoing request goes
// through its retry policy (billing-aware) and its logging transport.
package httpx

import (
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Option customises a client (tests inject transports, clocks, randomness).
type Option func(*options)

type options struct {
	base             http.RoundTripper
	clock            clock.Clock
	rand             func() float64
	total            *time.Duration
	quietSuccess     bool
	quietUnavailable bool
	crossOrigin      bool
}

// WithBaseTransport replaces the network transport under the logging and retry layers.
func WithBaseTransport(rt http.RoundTripper) Option { return func(o *options) { o.base = rt } }

// WithClock replaces the clock used for retry waits and HTTP-date Retry-After.
func WithClock(c clock.Clock) Option { return func(o *options) { o.clock = c } }

// WithTotalTimeout replaces http.timeout.total for this client (large
// downloads have their own limit).
func WithTotalTimeout(d time.Duration) Option { return func(o *options) { o.total = &d } }

// WithRand replaces the jitter source (uniform in [0,1)).
func WithRand(f func() float64) Option { return func(o *options) { o.rand = f } }

// WithQuietSuccess logs successful requests at debug instead of info. A
// high-frequency internal probe uses it so its healthy poll never crowds
// the log ring; failures and slow retries still log at their own level.
func WithQuietSuccess() Option { return func(o *options) { o.quietSuccess = true } }

// WithQuietUnavailable logs an expected HTTP 503 at debug instead of warn.
// A health probe uses it while its runtime is still loading; every other
// failure status and every transport error keeps its own level.
func WithQuietUnavailable() Option { return func(o *options) { o.quietUnavailable = true } }

// WithCrossOriginRedirects allows a GET or HEAD to follow a redirect to
// another host, but never across schemes (https cannot become http). It
// exists for a pinned artifact download: no credential is sent and the
// bytes are verified against a SHA-256 and size. Provider calls keep the
// same-origin default, so a bearer key or prompt body cannot be forwarded
// to a second host.
func WithCrossOriginRedirects() Option { return func(o *options) { o.crossOrigin = true } }

// New returns a client named name (logged and published as "client").
// Transport timeouts are read once; the retry policy is re-read per request.
// bus may be nil.
func New(cfg *config.Config, bus *events.Bus, name string, opts ...Option) (*http.Client, error) {
	o := options{clock: clock.Real(), rand: rand.Float64}
	for _, opt := range opts {
		opt(&o)
	}
	if o.base == nil {
		base, err := NewTransport(cfg.Snapshot())
		if err != nil {
			return nil, err
		}
		o.base = base
	}
	log := logx.For("httpx").With("client", name)
	logging := &logTransport{next: o.base, cfg: cfg, log: log, clock: o.clock, quietSuccess: o.quietSuccess, quietUnavailable: o.quietUnavailable}
	total := cfg.Duration("http.timeout.total")
	if o.total != nil {
		total = *o.total
	}
	return &http.Client{
		// The URL guard is outermost so a refused destination never reaches
		// the retry policy or the network, and no caller can bypass it.
		Transport: &urlGuard{next: &retryTransport{next: logging, cfg: cfg, bus: bus, name: name, log: log, clock: o.clock, rand: o.rand}},
		Timeout:   total,
		// Follow only same-origin redirects: a provider must not downgrade
		// https to http or forward the bearer key and the prompt body to a
		// second host. The check belongs here so every engine adapter and
		// discovery share it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("httpx: too many redirects")
			}
			origin := via[0].URL
			if req.URL.Scheme != origin.Scheme {
				return errors.New("httpx: cross-origin redirect refused (scheme change)")
			}
			if req.URL.Host != origin.Host {
				if !o.crossOrigin || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
					return errors.New("httpx: cross-origin redirect refused")
				}
			}
			return nil
		},
	}, nil
}

// NewTransport builds the network transport from the http.* settings.
func NewTransport(s *config.Snapshot) (*http.Transport, error) {
	proxy := http.ProxyFromEnvironment
	if raw := s.String("http.proxy_url"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		proxy = http.ProxyURL(u)
	}
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: s.Duration("http.timeout.dial")}).DialContext,
		TLSHandshakeTimeout:   s.Duration("http.timeout.tls_handshake"),
		ResponseHeaderTimeout: s.Duration("http.timeout.response_header"),
		IdleConnTimeout:       s.Duration("http.idle_conn_timeout"),
		MaxIdleConns:          s.Int("http.max_idle_conns"),
		MaxIdleConnsPerHost:   s.Int("http.max_idle_conns_per_host"),
		ForceAttemptHTTP2:     true,
	}, nil
}
