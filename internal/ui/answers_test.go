package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// The answers detail is one read surface with the list: it follows label
// writes, which the live events invalidate, not the repaint timer. It must go
// through the shared cache so a background route repaint does not refetch it.
func TestAnswersDetailUsesTheSharedCache(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	if !strings.Contains(js, `requestCached("/api/answers?message=`) {
		t.Fatal("the answers detail bypasses the shared cache")
	}
	if strings.Contains(js, `request("/api/answers?message=`) {
		t.Fatal("the answers detail still issues an uncached read")
	}
}

// The answers TTL only bounds a missed live event, so it must outlive the
// background revalidation window. Expressing it from that window keeps the
// relationship in one place instead of two literals that can drift apart.
func TestAnswersTTLOutlivesTheRouteRevalidationWindow(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	table := js[strings.Index(js, "const CACHE_TTL = {"):]
	table = table[:strings.Index(table, "};")]
	entry := table[strings.Index(table, "answers:"):]
	entry = strings.TrimSpace(entry[:strings.Index(entry, ",")])
	if !strings.Contains(entry, "ROUTE_FRESH_MS") {
		t.Fatalf("the answers TTL %q is an independent literal, not derived from ROUTE_FRESH_MS", entry)
	}
}
