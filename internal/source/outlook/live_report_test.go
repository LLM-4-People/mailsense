//go:build windows && outlooklive

package outlook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// Report shapes: aggregates only.

type liveReportData struct {
	Started         string         `json:"started"`
	Finished        string         `json:"finished"`
	Health          Health         `json:"health"`
	Full            runSummary     `json:"full"`
	FullStats       *Stats         `json:"full_stats"`
	UnreadProbe     unreadResult   `json:"unread_probe"`
	Incremental     runSummary     `json:"incremental"`
	IncrementalCost costSummary    `json:"incremental_cost"`
	Backstop        runSummary     `json:"backstop"` // every folder re-snapshotted (recheck_after = 0)
	BackstopCost    costSummary    `json:"backstop_cost"`
	Checks          checkTotals    `json:"checks"`
	Stores          []storeSummary `json:"stores"`
	Inventory       []StoreInfo    `json:"inventory"` // every store; Name hashed, StoreID blanked
	Timestamps      map[string]int `json:"timestamps"`
	Folders         []folderCheck  `json:"folders"`
}

type runSummary struct {
	Status        string                `json:"status"`
	ErrorCodes    []string              `json:"error_codes,omitempty"`
	WallSeconds   float64               `json:"wall_seconds"`
	Folders       int                   `json:"folders"`
	Unchanged     int                   `json:"unchanged"`
	FolderErrors  int                   `json:"folder_errors"`
	Seen          int                   `json:"seen"`
	Written       int                   `json:"written"`
	Skipped       int                   `json:"skipped"`
	Duplicates    int                   `json:"duplicates"`
	Deleted       int                   `json:"deleted"`
	Moved         int                   `json:"moved"`
	Held          int                   `json:"held"`
	Details       *syncer.DetailReport  `json:"details,omitempty"`
	VerdictCounts map[scope.Verdict]int `json:"verdict_counts,omitempty"`
}

type costSummary struct {
	Calls            int64   `json:"calls"`
	Folders          int     `json:"folders"`
	Unchanged        int     `json:"unchanged"`
	CallsPerFolder   float64 `json:"calls_per_folder"`
	DiscoverSeconds  float64 `json:"discover_seconds"`
	SnapshotSeconds  float64 `json:"snapshot_seconds"`
	NewestReads      int     `json:"newest_reads"`
	SnapshotRows     int     `json:"snapshot_rows"`
	Emitted          int     `json:"emitted"`
	DetailsCompleted int     `json:"details_completed"`
}

type storeSummary struct {
	Kind       mail.StoreKind        `json:"kind"`
	Name       string                `json:"name"` // hash of the display name
	OwnerKnown bool                  `json:"owner_known"`
	OwnerHash  string                `json:"owner_hash,omitempty"`
	Addresses  int                   `json:"addresses"` // owner addresses (reported + declared)
	Judged     int                   `json:"judged"`
	Blocked    bool                  `json:"blocked"`
	Verdicts   map[scope.Verdict]int `json:"verdicts"`
}

type checkTotals struct {
	Folders      int   `json:"folders"`
	CountMatches int   `json:"count_matches"` // stored == Items.Count
	ItemsCount   int64 `json:"items_count"`
	Stored       int   `json:"stored"`
	Missing      int   `json:"missing"`
	Extra        int   `json:"extra"`
	ReadMismatch int   `json:"read_mismatch"`
	StampDiffers int   `json:"stamp_differs"`
	Partial      int   `json:"partial"`
	Errors       int   `json:"errors"`
}

func (c *checkTotals) add(f folderCheck) {
	c.Folders++
	c.ItemsCount += f.ItemsCount
	c.Stored += f.Stored
	c.Missing += f.Missing
	c.Extra += f.Extra
	c.ReadMismatch += f.ReadMismatch
	c.StampDiffers += f.StampDiffers
	c.Partial += f.Partial
	if f.Error != "" {
		c.Errors++
	}
	if int64(f.Stored) == f.ItemsCount {
		c.CountMatches++
	}
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// timedRun runs one sync under what is left of the test's detail budget;
// a cancelled test or an unavailable Outlook (lost, busy, breaker) ends
// the test (the deferred cleanup runs): the next run would only call an
// Outlook that just failed.
func timedRun(t *testing.T, ctx context.Context, s *syncer.Syncer, c *Connector, budget *livetest.Budget, name string) syncer.Report {
	budget.Before(t)
	start, last := time.Now(), time.Now()
	rep, err := s.Run(ctx, c, func(p syncer.Progress) {
		if time.Since(last) < 30*time.Second {
			return
		}
		last = time.Now()
		t.Logf("LIVE %s %s phase=%s folder=%d done=%d total=%d written=%d calls=%d", name, time.Since(start).Round(time.Second),
			p.Phase, p.FolderID, p.Position.Done, p.Position.Total, p.Totals.Written, c.Stats().Calls)
	})
	t.Logf("LIVE %s finished in %s status=%s err=%v", name, time.Since(start).Round(time.Millisecond), rep.Status, err != nil)
	if rep.Details != nil {
		budget.Spent(t, rep.Details.Completed+rep.Details.Failed)
	}
	switch {
	case err != nil && ctx.Err() != nil:
		t.Fatalf("LIVE %s stopped: %v", name, ctx.Err())
	case errors.Is(err, source.ErrUnavailable):
		t.Fatalf("LIVE %s: Outlook is unavailable, the test stops here (codes %v): %v", name, hexCodes(err.Error()), err)
	}
	rep.Duration = time.Since(start)
	return rep
}

func summarize(r syncer.Report) runSummary {
	s := runSummary{Status: string(r.Status), WallSeconds: r.Duration.Seconds(), Folders: len(r.Folders), Seen: r.Totals.Seen,
		Written: r.Totals.Written, Skipped: r.Totals.Skipped, Duplicates: r.Totals.Duplicates, Deleted: r.Removed.Deleted,
		Moved: r.Removed.Moved, Held: r.Removed.Held, Details: r.Details}
	for _, f := range r.Folders {
		if f.Unchanged {
			s.Unchanged++
		}
		if f.Error != "" {
			s.FolderErrors++
			s.ErrorCodes = append(s.ErrorCodes, hexCodes(f.Error)...)
		}
	}
	if r.Scope != nil {
		s.VerdictCounts = r.Scope.Counts
	}
	return s
}

var hrRE = regexp.MustCompile(`0x[0-9A-F]{8}`)

// hexCodes keeps only HRESULTs of an error text (no names leak).
func hexCodes(msg string) []string {
	if codes := hrRE.FindAllString(msg, -1); len(codes) > 0 {
		return codes
	}
	return []string{"no-hresult"}
}

func costDelta(a, b *Stats, r syncer.Report) costSummary {
	c := costSummary{Calls: b.Calls - a.Calls, Folders: b.Sync.Folders - a.Sync.Folders, Unchanged: b.Sync.Unchanged - a.Sync.Unchanged,
		DiscoverSeconds: (b.Discover.Time - a.Discover.Time).Seconds(), SnapshotSeconds: (b.Sync.SnapshotTime - a.Sync.SnapshotTime).Seconds(),
		NewestReads: b.Discover.NewestReads - a.Discover.NewestReads, SnapshotRows: b.Sync.SnapshotRows - a.Sync.SnapshotRows,
		Emitted: b.Sync.Emitted - a.Sync.Emitted}
	if r.Details != nil {
		c.DetailsCompleted = r.Details.Completed
	}
	if c.Folders > 0 {
		c.CallsPerFolder = float64(c.Calls) / float64(c.Folders)
	}
	return c
}

func storeScopes(r syncer.Report) []storeSummary {
	if r.Scope == nil {
		return nil
	}
	var out []storeSummary
	for _, st := range r.Scope.Stores {
		s := storeSummary{Kind: st.Store.Kind, Name: hash(st.Store.Name), OwnerKnown: st.Identity.Known(),
			Addresses: len(st.Identity.Addresses), Judged: st.Judged, Blocked: st.Blocked != "", Verdicts: st.Counts}
		if s.OwnerKnown {
			s.OwnerHash = hash(st.Identity.Primary())
		}
		out = append(out, s)
	}
	return out
}
