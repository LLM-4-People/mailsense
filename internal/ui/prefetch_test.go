package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// Every prefetched message renders and ships its body. The detail warm-up is
// therefore one row (the likely click), not the whole home list; the list
// size and the prefetch size are separate facts.
func TestPriorityDetailPrefetchIsBounded(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function prefetchPriorityDetails(items) {")
	if start < 0 {
		t.Fatal("prefetchPriorityDetails not found")
	}
	end := strings.Index(js[start:], "\n  }")
	if end < 0 {
		t.Fatal("prefetchPriorityDetails has no end")
	}
	body := js[start : start+end]
	if !strings.Contains(body, "PRIORITY_PREFETCH_SIZE") {
		t.Fatalf("the detail prefetch is not bounded by its own size:\n%s", body)
	}
	if strings.Contains(body, "PRIORITY_HOME_SIZE") {
		t.Fatal("the detail prefetch reuses the list size")
	}
}

// A row entering the viewport and a pointer/focus on a row share one queue,
// so a pointer moving down the list sends one batch request, not one request
// per row.
func TestCompanyPrefetchUsesOneQueue(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "// company-prefetch-queue:start")
	end := strings.Index(js, "// company-prefetch-queue:end")
	if start < 0 || end <= start {
		t.Fatal("the company prefetch queue block is missing")
	}
	block := js[start:end]
	if got := strings.Count(block, "setTimeout("); got != 1 {
		t.Fatalf("the prefetch queue owns %d timers, want 1", got)
	}
	if !strings.Contains(block, "companyPrefetchPending") {
		t.Fatal("the prefetch queue does not coalesce keys")
	}
	if strings.Count(js, "prefetchCompanyProfiles([key])") != 0 {
		t.Fatal("a row still fires its own one-key profile request")
	}
	if !strings.Contains(js, "queueCompanyPrefetch([key])") {
		t.Fatal("the row does not feed the shared prefetch queue")
	}
	if !strings.Contains(js, "queueCompanyPrefetch(keys)") {
		t.Fatal("the visibility observer does not feed the shared prefetch queue")
	}
	start = strings.Index(js, "function prefetchCompanyProfiles(keys) {")
	if start < 0 {
		t.Fatal("prefetchCompanyProfiles not found")
	}
	end = strings.Index(js[start:], "\n  }")
	if end <= 0 {
		t.Fatal("prefetchCompanyProfiles has no end")
	}
	body := js[start : start+end]
	if !strings.Contains(body, "COMPANY_PREFETCH_BATCH") {
		t.Fatal("the profile prefetch is not bounded by the shared batch size")
	}
	if strings.Contains(body, ">= 16") {
		t.Fatal("the profile prefetch still warms every visible row")
	}
}
