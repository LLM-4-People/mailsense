package httpx

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

func TestURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"loopback IPv4", "http://127.0.0.1:8080/v1", true},
		{"loopback name", "http://localhost:11434/v1", true},
		{"loopback IPv6", "https://[::1]:8443/v1", true},
		{"private IPv4", "http://192.168.1.10:8080/v1", true},
		{"ten network", "http://10.0.0.5/v1", true},
		{"private IPv6", "http://[fd00::1]/v1", true},
		{"public https", "https://api.example.test/v1", true},
		{"query is not refused here", "https://example.test/v1?key=x", true},

		{"empty", "", false},
		{"relative", "/v1", false},
		{"no scheme", "//example.test/v1", false},
		{"file", "file:///etc/passwd", false},
		{"ftp", "ftp://example.test/x", false},
		{"gopher", "gopher://example.test/", false},
		{"data", "data:text/plain,hi", false},
		{"credentials", "https://user:pass@example.test/v1", false},
		{"no host", "http:///v1", false},
		{"metadata", "http://169.254.169.254/latest/meta-data", false},
		{"mapped metadata", "http://[::ffff:169.254.169.254]/", false},
		{"link-local IPv6", "http://[fe80::1]/v1", false},
		{"link-local IPv6 high", "http://[febf::1]/v1", false},
		{"zoned link-local IPv6", "http://[fe80::1%25eth0]/v1", false},
		{"control character", "http://exa\nmple.test/v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateURLString(tc.raw)
			if tc.ok && err != nil {
				t.Fatalf("%q: %v", tc.raw, err)
			}
			if !tc.ok && !errors.Is(err, ErrURLRefused) {
				t.Fatalf("%q: want ErrURLRefused, got %v", tc.raw, err)
			}
		})
	}
	if err := ValidateURL(nil); !errors.Is(err, ErrURLRefused) {
		t.Fatalf("nil URL: %v", err)
	}
}

func TestNewRequestAppliesThePolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"loopback", "http://127.0.0.1:8080/v1/models", true},
		{"public with query", "https://example.test/v1/models?page=2", true},
		{"credentials", "https://user:pass@example.test/v1", false},
		{"metadata", "http://169.254.169.254/latest/meta-data", false},
		{"file", "file:///etc/passwd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := NewRequest(t.Context(), http.MethodGet, tc.raw, nil)
			if tc.ok {
				if err != nil || req == nil {
					t.Fatalf("%q: %v", tc.raw, err)
				}
				return
			}
			if req != nil || !errors.Is(err, ErrURLRefused) {
				t.Fatalf("%q: req %v, want ErrURLRefused, got %v", tc.raw, req, err)
			}
		})
	}
}

type countingTransport struct{ calls atomic.Int32 }

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, errors.New("the network must not be reached")
}

// A refused destination is reported before the retry policy or the network
// sees it, whichever call site built the request.
func TestClientRefusesURLBeforeTheNetwork(t *testing.T) {
	base := &countingTransport{}
	cfg := configtest.New(t, map[string]any{"http.retry.max_attempts": 3})
	client, err := New(cfg, nil, "test", WithBaseTransport(base))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		t.Fatal("a refused URL returned a response")
	}
	if !errors.Is(err, ErrURLRefused) {
		t.Fatalf("want ErrURLRefused, got %v", err)
	}
	if calls := base.calls.Load(); calls != 0 {
		t.Fatalf("the base transport was called %d times", calls)
	}
}
