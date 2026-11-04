package engine

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// Tokens counts code points, not bytes, and rounds up: a multi-byte
// character must never be charged as several characters.
func TestTokensCountsRunesAndRoundsUp(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		chars float64
		want  int64
	}{
		{"empty", "", 3, 0},
		{"exact ratio", "abc", 3, 1},
		{"rounds up", "abcd", 3, 2},
		{"multibyte runes are one character each", "éééé", 3, 2}, // 4 runes, 8 bytes
		{"emoji runes are one character each", "🙂🙂🙂", 3, 1},      // 3 runes, 12 bytes
		{"non-integral ratio", "abcdef", 4, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Provider{CharsPerToken: tc.chars}
			if got := p.Tokens([]byte(tc.body)); got != tc.want {
				t.Fatalf("Tokens(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

// Ceiling reserves tokens per UTF-8 byte (not per character) and prices the
// input plus the output reserve.
func TestCeilingUsesBytesAndPrices(t *testing.T) {
	in, out := 0.5, 2.0
	p := Provider{ReserveTokensPerByte: 1.5, ReserveOutputTokens: 100, PriceInput: &in, PriceOutput: &out}
	got := p.Ceiling([]byte("é🙂")) // 6 bytes, 2 runes
	const tokens = int64(9)
	wantUSD := float64(tokens)*in/1e6 + float64(p.ReserveOutputTokens)*out/1e6
	if got != (Estimate{Calls: 1, InputTokens: tokens, EstimatedUSD: wantUSD, PriceKnown: true}) {
		t.Fatalf("Ceiling = %+v, want 1 call, %d tokens, %v USD", got, tokens, wantUSD)
	}

	unpriced := Provider{ReserveTokensPerByte: 2, ReserveOutputTokens: 10, PriceOutput: &out}
	got = unpriced.Ceiling([]byte("abc")) // 6 bytes
	if got.InputTokens != 6 || got.PriceKnown || got.EstimatedUSD != float64(10)*out/1e6 {
		t.Fatalf("an unknown input price keeps the output charge and reports the price unknown: %+v", got)
	}
}

// Declined is the billing rule: a status below 500 is a decline, and a 5xx
// is one only while the configured policy (with the record's override)
// lists it as retryable.
func TestDeclinedStatusRule(t *testing.T) {
	cfg := configtest.New(t, nil)
	cases := []struct {
		name   string
		retry  []int
		status int
		want   bool
	}{
		{"200", nil, 200, true},
		{"400", nil, 400, true},
		{"404", nil, 404, true},
		{"408 below 500", nil, 408, true},
		{"422", nil, 422, true},
		{"429 default retryable", nil, 429, true},
		{"500 may have been billed", nil, 500, false},
		{"502 default retryable", nil, 502, true},
		{"503 default retryable", nil, 503, true},
		{"504 default retryable", nil, 504, true},
		{"529 unconfigured", nil, 529, false},
		{"529 configured", []int{429, 529}, 529, true},
		{"503 replaced by the override", []int{429, 529}, 503, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Provider{Retry: Retry{Statuses: tc.retry}}
			if got := p.Declined(tc.status, cfg); got != tc.want {
				t.Fatalf("Declined(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

// PolicyHook installs the record's overrides and its quota finality on the
// live configured policy, and ErrorTextChars is the setting's one reader.
func TestPolicyHookAppliesOverridesAndQuotaFinality(t *testing.T) {
	const quotaBody = `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"out of credits"}}`
	cfg := configtest.New(t, map[string]any{"http.error_text_chars": 20})
	attempts := 7
	p := Provider{Retry: Retry{
		MaxAttempts: &attempts,
		Statuses:    []int{429, 529},
		QuotaCodes:  []string{"credit_balance_exhausted"},
	}}
	base := httpx.Policy{MaxAttempts: 2, Statuses: []int{500}}
	hooked := base
	p.PolicyHook(cfg)(&hooked)
	if hooked.MaxAttempts != attempts {
		t.Fatalf("max attempts = %d, want %d", hooked.MaxAttempts, attempts)
	}
	if !slices.Equal(hooked.Statuses, p.Retry.Statuses) {
		t.Fatalf("statuses = %v, want %v", hooked.Statuses, p.Retry.Statuses)
	}
	if hooked.Final == nil || !hooked.Final(429, []byte(quotaBody)) {
		t.Fatalf("a quota body on a retryable status must be final: %+v", hooked)
	}
	if hooked.Final(429, []byte(`{"error":{"code":"rate_limit_exceeded"}}`)) {
		t.Fatal("an ordinary rate limit must stay retryable")
	}
	if got := (Deps{Config: cfg}).ErrorTextChars(); got != 20 {
		t.Fatalf("ErrorTextChars = %d, want 20", got)
	}
}
