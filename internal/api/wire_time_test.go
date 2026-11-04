package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestWireTimeHasOneOwner: every wire timestamp goes through wireTime, and the
// RFC 3339 rule is spelled once in the package.
func TestWireTimeHasOneOwner(t *testing.T) {
	want := map[string]string{
		"routes_companies.go": "wireTime(s.clock.Now())",
		"routes_priority.go":  "wireTime(page.GeneratedAt)",
	}
	for name, call := range want {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if !strings.Contains(source, call) {
			t.Errorf("%s does not format its timestamp through wireTime", name)
		}
		if strings.Contains(source, "Format(time.RFC3339)") {
			t.Errorf("%s formats a timestamp itself; use wireTime", name)
		}
	}
	data, err := os.ReadFile("wire.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "Format(time.RFC3339)"); got != 1 {
		t.Errorf("wire.go spells the RFC 3339 format %d times, want once", got)
	}
}

// TestWireTimeShape: the one form is UTC, RFC 3339, without fractional
// seconds, and a zero instant keeps its own year.
func TestWireTimeShape(t *testing.T) {
	instant := time.Date(2026, 11, 4, 12, 30, 45, 987654321, time.FixedZone("CET", 3600))
	if got, want := wireTime(instant), "2026-11-04T11:30:45Z"; got != want {
		t.Fatalf("wireTime(%v) = %q, want %q", instant, got, want)
	}
	if got, want := wireTime(time.Time{}), "0001-01-01T00:00:00Z"; got != want {
		t.Fatalf("wireTime(zero) = %q, want %q", got, want)
	}
	for _, raw := range []string{wireTime(instant), wireTime(time.Time{})} {
		if _, err := time.Parse(time.RFC3339, raw); err != nil {
			t.Errorf("%q does not parse as RFC 3339: %v", raw, err)
		}
	}
}
