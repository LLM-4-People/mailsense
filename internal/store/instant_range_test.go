package store

import (
	"os"
	"strings"
	"testing"
)

// TestInstantRangeDeclaresUndatedMessages pins the one time-window shape and
// which side of the undated contract each caller speaks: search excludes an
// undated message from a date window, a ranked mail query includes it because
// the company profile counts it in Messages.
func TestInstantRangeDeclaresUndatedMessages(t *testing.T) {
	where, args := instantRange([]string{"vis"}, []any{"v"}, "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", false)
	if len(args) != 3 || args[0] != "v" {
		t.Fatalf("args = %v", args)
	}
	joined := strings.Join(where, " AND ")
	if got := strings.Count(joined, instantSQL+" != ''"); got != 2 {
		t.Errorf("search window guards undated messages %d times, want 2: %s", got, joined)
	}
	where, _ = instantRange(nil, nil, "", "2026-02-01T00:00:00Z", false)
	if !strings.Contains(strings.Join(where, " AND "), "!= ''") {
		t.Errorf("an upper-only search window lost its undated guard: %v", where)
	}

	where, _ = instantRange(nil, nil, "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", true)
	if strings.Contains(strings.Join(where, " AND "), "!= ''") {
		t.Errorf("a mail window excludes undated messages, which the profile counts: %v", where)
	}

	where, args = instantRange(nil, nil, "", "", true)
	if len(where) != 0 || len(args) != 0 {
		t.Errorf("an open window added %v with args %v", where, args)
	}
}

// TestInstantRangeCallersDeclareTheRule: the two call sites name their side of
// the contract, so flipping one is a visible change.
func TestInstantRangeCallersDeclareTheRule(t *testing.T) {
	for _, c := range []struct {
		file string
		call string
	}{
		{"search.go", "instantRange(parts, args, since, until, false)"},
		{"mailquery.go", "instantRange(where, args, plan.Since, plan.Until, true)"},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), c.call) {
			t.Errorf("%s does not declare %q", c.file, c.call)
		}
	}
}
