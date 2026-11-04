package triage

import (
	"os"
	"strings"
	"testing"
)

// TestPageAndSummaryShareOneCacheKey: a routed page and its summary are cached
// under one identity. A second viewKey literal is how a new field reaches one
// path and not the other, and the page then serves a summary's cache entry.
func TestPageAndSummaryShareOneCacheKey(t *testing.T) {
	data, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if got := strings.Count(source, "viewKey{"); got != 1 {
		t.Errorf("page.go builds a viewKey %d times, want once in cacheKey", got)
	}
	if !strings.Contains(source, "func (t *Triager) cacheKey(") {
		t.Fatal("cacheKey is gone")
	}
	for _, call := range []string{
		"key := t.cacheKey(ctx, id, limit, q.Offset)",
		"key := t.cacheKey(ctx, id, 0, 0)",
	} {
		if !strings.Contains(source, call) {
			t.Errorf("page.go lacks %q", call)
		}
	}
}

// TestDeciderKeyComesFromTheEngine: the triage cache and the provider record
// name the paid cache entry the same way. Re-spelling the identity in triage
// is how a model change reaches one caller and not the other.
func TestDeciderKeyComesFromTheEngine(t *testing.T) {
	data, err := os.ReadFile("triager.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if strings.Contains(source, `Name() + "/" +`) {
		t.Error("triager.go re-spells the decider identity; use engine.DeciderKey")
	}
	if got := strings.Count(source, "engine.DeciderKey("); got != 3 {
		t.Errorf("triager.go calls engine.DeciderKey %d times, want three", got)
	}
}
