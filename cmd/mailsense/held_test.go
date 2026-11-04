package main

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// TestHeldAppliesAtFollowsTheSyncPolicy: the held-removal list must render the
// same auto-apply deadline the syncer enforces, and a confirm-only policy
// (0) must render no deadline at all rather than a false "stays" promise.
func TestHeldAppliesAtFollowsTheSyncPolicy(t *testing.T) {
	cases := []struct {
		name   string
		after  string
		since  string
		expect string
	}{
		{"72h policy", "72h", "2026-11-03T12:00:00Z", "2026-11-06T12:00:00Z"},
		{"confirm only", "0s", "2026-11-03T12:00:00Z", ""},
		{"unknown held time", "72h", "", ""},
		{"junk held time", "72h", "not a time", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configtest.New(t, map[string]any{"sync.removal.auto_apply_after": tc.after})
			if got := heldAppliesAt(cfg, tc.since); got != tc.expect {
				t.Fatalf("applies_at = %q, want %q", got, tc.expect)
			}
		})
	}
}
