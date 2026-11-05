package triage

import "testing"

// The view budgets admit the maximum screens: a page at TriagePageMax rows
// with its id order, and a summary, both stay under their bounds.
func TestViewBudgetsAdmitMaxViews(t *testing.T) {
	page := &Page{
		Results: make(map[int64]PageRow, TriagePageMax),
		IDs:     make([]int64, TriagePageMax),
	}
	for i := range TriagePageMax {
		page.Results[int64(i)] = PageRow{MessageID: int64(i)}
	}
	if got := weighPage(page); got > pageCacheMaxBytes {
		t.Fatalf("a %d-row page weighs %d bytes, over the %d-byte budget", TriagePageMax, got, pageCacheMaxBytes)
	}
	if got := weighSummary(&Summary{}); got > summaryCacheMaxBytes {
		t.Fatalf("a summary weighs %d bytes, over the %d-byte budget", got, summaryCacheMaxBytes)
	}
}
