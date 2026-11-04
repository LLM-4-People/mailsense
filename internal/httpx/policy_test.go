package httpx

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

func testPolicy(t *testing.T) Policy {
	return PolicyFromConfig(configtest.New(t, map[string]any{"http.retry.jitter": 0}).Snapshot())
}

func status(code int, hdr ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Set(hdr[i], hdr[i+1])
	}
	return &http.Response{StatusCode: code, Header: h}
}

func TestDecide(t *testing.T) {
	p := testPolicy(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	dialErr := errors.New("connection refused")
	cases := []struct {
		name    string
		attempt int
		wrote   bool
		resp    *http.Response
		err     error
		retry   bool
		delay   time.Duration
	}{
		{"unsent error retried with unsent cap", 0, false, nil, dialErr, true, time.Second},
		{"unsent backoff capped at 8s", 4, false, nil, dialErr, false, 0}, // 5th attempt is the last
		{"unsent backoff 2^3", 3, false, nil, dialErr, true, 8 * time.Second},
		{"written error terminal", 0, true, nil, errors.New("timeout awaiting response headers"), false, 0},
		{"200 final", 0, true, status(200), nil, false, 0},
		{"500 never retried", 0, true, status(500), nil, false, 0},
		{"400 final", 0, true, status(400), nil, false, 0},
		{"503 retried even though written", 1, true, status(503), nil, true, 2 * time.Second},
		{"429 honors Retry-After seconds", 0, true, status(429, "Retry-After", "7"), nil, true, 7 * time.Second},
		{"retry-after-ms wins", 0, true, status(429, "Retry-After", "7", "retry-after-ms", "1500"), nil, true, 1500 * time.Millisecond},
		{"HTTP-date Retry-After", 0, true, status(503, "Retry-After", now.Add(30*time.Second).Format(http.TimeFormat)), nil, true, 30 * time.Second},
		{"past HTTP-date is zero", 0, true, status(503, "Retry-After", now.Add(-time.Hour).Format(http.TimeFormat)), nil, true, 0},
		{"junk Retry-After falls back to backoff", 0, true, status(503, "Retry-After", "soon", "retry-after-ms", "-5"), nil, true, time.Second},
		{"Retry-After beyond cap is terminal", 0, true, status(429, "Retry-After", "3600"), nil, false, 0},
		{"exhausted", 4, true, status(503), nil, false, 0},
		{"nil response", 0, true, nil, nil, false, 0},
	}
	for _, c := range cases {
		d := p.Decide(c.attempt, c.wrote, c.resp, c.err, now, 0)
		if d.Retry != c.retry || (c.retry && d.Delay != c.delay) || d.Reason == "" {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
}

func TestBackoffCapsJitterAndOverflow(t *testing.T) {
	p := Policy{BaseDelay: time.Second, Jitter: 0.5}
	if got := p.backoff(2, time.Minute, 0); got != 4*time.Second {
		t.Fatalf("2^2: %v", got)
	}
	if got := p.backoff(2, time.Minute, 0.999); got <= 2*time.Second || got >= 4*time.Second {
		t.Fatalf("jitter removes up to half: %v", got)
	}
	for _, attempt := range []int{10, 40, 62, 63, 1000} {
		if got := p.backoff(attempt, time.Minute, 0); got != time.Minute {
			t.Fatalf("attempt %d must cap at the limit, got %v", attempt, got)
		}
	}
}

func TestHonorRetryAfterOff(t *testing.T) {
	p := testPolicy(t)
	p.HonorRetryAfter = false
	d := p.Decide(0, true, status(429, "Retry-After", "3600"), nil, time.Now(), 0)
	if !d.Retry || d.Delay != time.Second {
		t.Fatalf("ignored Retry-After must use backoff: %+v", d)
	}
}
