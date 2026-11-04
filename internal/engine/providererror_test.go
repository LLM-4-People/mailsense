package engine

import (
	"errors"
	"net/http"
	"testing"
)

func TestClassifyProviderFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		quota  []string
		want   error
	}{
		{"408 request timeout", http.StatusRequestTimeout, `timeout`, nil, ErrTransient},
		{"429 rate limit", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`, nil, ErrTransient},
		{"502 bad gateway", http.StatusBadGateway, `bad gateway`, nil, ErrTransient},
		{"503 service unavailable", http.StatusServiceUnavailable, `unavailable`, nil, ErrTransient},
		{"504 gateway timeout", http.StatusGatewayTimeout, `timeout`, nil, ErrTransient},
		{"402 credits", http.StatusPaymentRequired, `{"detail":{"error_type":"insufficient_credits","message":"Add credits"}}`, nil, ErrInsufficientCredits},
		{"401 auth", http.StatusUnauthorized, `{"detail":"bad key"}`, nil, ErrAuth},
		{"403 auth", http.StatusForbidden, `{}`, nil, ErrAuth},
		{"400 quota code", http.StatusBadRequest, `{"error":{"code":"billing_hard_limit_reached"}}`, []string{"billing_hard_limit_reached"}, ErrQuota},
		{"429 quota code wins over transient", http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`, []string{"insufficient_quota"}, ErrQuota},
		{"400 rejected", http.StatusBadRequest, `not json`, nil, ErrRejected},
		{"404 rejected", http.StatusNotFound, `missing`, nil, ErrRejected},
		{"422 rejected", http.StatusUnprocessableEntity, `{"detail":[{"msg":"bad"}]}`, nil, ErrRejected},
		{"500 is provider-wide", http.StatusInternalServerError, `oops`, nil, ErrTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Classify("fake", c.status, []byte(c.body), c.quota, 200)
			if e.Status != c.status || e.Class != c.want || !errors.Is(e, c.want) {
				t.Fatalf("status %d: class %v, want %v (%v)", e.Status, e.Class, c.want, e)
			}
			if c.want != ErrTransient && errors.Is(e, ErrTransient) {
				t.Fatalf("status %d: %v is also transient", e.Status, c.want)
			}
		})
	}
}
