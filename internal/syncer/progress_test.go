package syncer

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// The strip translates a stable stage key, so a producer must keep naming its
// phase instead of leaving the owner with a bare "Working…".
func TestSyncProgressNamesItsStage(t *testing.T) {
	if got := syncProgressStage(Progress{Phase: source.PhaseMessages}); got != "folders" {
		t.Fatalf("message phase stage = %q, want folders", got)
	}
	if got := syncProgressStage(Progress{Phase: source.PhaseDetails}); got != "details" {
		t.Fatalf("details phase stage = %q, want details", got)
	}
}

func TestBackfillProgressNamesItsStage(t *testing.T) {
	var got jobs.Progress
	reportBackfill(func(p jobs.Progress) { got = p }, 2, 10, "2 updated, 0 skipped")
	if got.Stage != "backfill" {
		t.Fatalf("backfill stage = %q, want backfill", got.Stage)
	}
	if got.Done != 2 || got.Total != 10 {
		t.Fatalf("backfill progress = %+v", got)
	}
}
