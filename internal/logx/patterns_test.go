package logx

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// TestBuiltInRedactPatternsAlwaysApply: the built-in token and email patterns
// are intrinsic to logx. A user list (including an empty one) only adds
// expressions; it never replaces the built-ins.
func TestBuiltInRedactPatternsAlwaysApply(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		custom   bool
	}{
		{name: "empty user list", patterns: []string{}},
		{name: "non-email user list", patterns: []string{`(?i)\bzzz\b`}, custom: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := setup(t, nil, map[string]any{"log.redact.patterns": tc.patterns})
			For("test.custom").Info(
				"mail alice@example.com bearer Bearer abcdefghijklmnop key sk-ABCDEFGHIJKLMNOPQRST client_secret=abcDEF123456789 custom ZZZ",
				"actor", "bob@example.org",
				"unicode_actor", "李娜@例子.中国",
				"host_actor", "user@host",
				"note", "noreply",
				"at", "@",
				"spaced", "noreply @ example",
			)
			e := last(t, svc)
			raw, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{
				"alice@example.com", "bob@example.org", "李娜@", "user@host",
				"abcdefghijklmnop", "ABCDEFGHIJKLMNOPQRST", "abcDEF123456789",
			} {
				if strings.Contains(string(raw), leak) {
					t.Errorf("built-in pattern did not apply with %s: %q", tc.name, leak)
				}
			}
			for k, want := range map[string]any{
				"note": "noreply", "at": "@", "spaced": "noreply @ example",
			} {
				if e.Attrs[k] != want {
					t.Errorf("edge %s = %#v, want %#v", k, e.Attrs[k], want)
				}
			}
			if tc.custom {
				if strings.Contains(e.Message, "ZZZ") {
					t.Errorf("custom pattern was not additive: %q", e.Message)
				}
			} else if !strings.Contains(e.Message, "ZZZ") {
				t.Errorf("an empty user list must not invent a pattern: %q", e.Message)
			}
			if !strings.Contains(string(raw), config.SecretMask) {
				t.Errorf("no redaction marker in %s", raw)
			}
		})
	}
}

// TestRedactPatternsDefaultIsEmpty: the Go built-in list is the one owner of
// the always-on patterns, so the schema default must stay empty. This contract
// prevents a silent second copy in schema.json.
func TestRedactPatternsDefaultIsEmpty(t *testing.T) {
	if got := defaultValues().Strings("log.redact.patterns"); len(got) != 0 {
		t.Fatalf("log.redact.patterns default has %d entries; built-ins belong to logx", len(got))
	}
	if len(builtinRedactPatterns) == 0 {
		t.Fatal("no built-in redaction patterns")
	}
}

// Redact is the one policy entry point for values that leave through the API
// or a job record instead of a log handler.
func TestRedactAppliesThePolicy(t *testing.T) {
	_, _ = setup(t, nil, nil)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	const address = "bob@example.test"
	got := Redact("provider said " + secret + " for " + address)
	if strings.Contains(got, secret) || strings.Contains(got, address) {
		t.Fatalf("Redact leaked a secret or an address: %q", got)
	}
	if !strings.Contains(got, config.SecretMask) {
		t.Fatalf("Redact did not mark the value: %q", got)
	}
}
