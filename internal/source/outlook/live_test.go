//go:build windows && outlooklive

package outlook

// Live verification against the real Outlook profile of this machine
// (docs/DEVELOPMENT.md, "Live Outlook test"). Read-only: it picks the
// -live.pick stores (default: the profile's default store, picked as a user
// would), syncs them into a throw-away database,
// runs an incremental sync, then one with the snapshot backstop forced on
// every folder, cross-checks counts and read states against Outlook, and
// writes an AGGREGATE report (counts, timings, HRESULTs, hashes): no
// subject, body or address ever leaves the machine.
//
// It stops on its own before -test.timeout and when the -live.stop file
// appears (a test-owned stop file), so its deferred cleanup always removes
// the database.
//
//	outlook-live.exe -test.run TestLive -test.v -test.timeout 14400s -live.report=report.json -live.stop=job.stop
//	    [-live.details=1000 -live.rate=100]   (a short verification run: livetest.Limit; the
//	                                          1,000 detail reads are the whole test's, over its three syncs and the probe)

import (
	"context"
	"encoding/json"
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

var (
	liveReport = flag.String("live.report", "outlook-live-report.json", "aggregate report path")
	liveStop   = flag.String("live.stop", "", "stop (cancel, clean up) when this file appears")
	liveKeep   = flag.Bool("live.keep", false, "keep the temporary database (debugging only)")
)

// liveContext ends before the test deadline or when the stop file appears.
func liveContext(t *testing.T) context.Context { return livetest.Context(t, *liveStop) }

func TestLive(t *testing.T) {
	ctx := liveContext(t)
	dir, err := filepath.Abs("live-data")
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	policy := fsx.Replace{Attempts: cfg.Int("fs.replace_attempts"), Delay: cfg.Duration("fs.replace_retry_delay")}
	_ = fsx.RemoveAll(dir, policy)
	if !*liveKeep {
		defer func() {
			if err := fsx.RemoveAll(dir, policy); err != nil {
				t.Errorf("LIVE could not remove the temporary database: %v", err)
			}
		}()
	}
	logs, err := logx.Setup(cfg, bus)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	st, err := store.Open(ctx, cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conn, err := New("outlook", cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Keep(ctx, st); err != nil { // the breaker's state in the store, as the app keeps it
		t.Fatal(err)
	}
	s := syncer.New(st, cfg, bus, nil)
	budget := openLive(t, ctx, conn, cfg)
	// The cross-check reads whole folders through the read-only runner.
	if err := cfg.Set("query.max_rows", 100000); err != nil {
		t.Fatal(err)
	}
	rep := liveReportData{Started: time.Now().UTC().Format(time.RFC3339)}
	if rep.Health, err = conn.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	full := timedRun(t, ctx, s, conn, budget, "full")
	rep.Full, rep.FullStats = summarize(full), conn.Stats()
	rep.UnreadProbe = unreadProbe(t, ctx, conn, cfg, budget)
	before := conn.Stats()
	inc := timedRun(t, ctx, s, conn, budget, "incremental")
	rep.Incremental, rep.IncrementalCost = summarize(inc), costDelta(before, conn.Stats(), inc)
	// The snapshot backstop, forced on every folder (outlook.sync.recheck_after = 0).
	if err := cfg.Set("outlook.sync.recheck_after", "0s"); err != nil {
		t.Fatal(err)
	}
	before = conn.Stats()
	back := timedRun(t, ctx, s, conn, budget, "backstop")
	rep.Backstop, rep.BackstopCost = summarize(back), costDelta(before, conn.Stats(), back)
	rep.Folders, rep.Checks = verify(t, ctx, conn, st, cfg)
	rep.Stores = storeScopes(back)
	for _, in := range conn.Stores() {
		in.Name, in.StoreID, in.Key, in.Addresses = hash(in.Name), "", hash(in.Key), nil
		rep.Inventory = append(rep.Inventory, in)
	}
	rep.Timestamps = timestampSanity(t, ctx, cfg)
	rep.Finished = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := fsx.WriteFileAtomic(*liveReport, b, fsx.FilePerm, policy); err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE report written: %s (%d bytes)", *liveReport, len(b))
}
