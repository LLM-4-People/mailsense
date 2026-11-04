//go:build windows && evidencelive

package evidence

// Live evidence on the real Outlook profile of the Windows machine
// (the live evidence harness): the -live.pick stores are synced read-only
// into a throw-away database (full transport headers: Outlook keeps up to
// mail.headers.max_chars), the evidence engine runs twice, and the rules
// are measured on the gold sample's TRAIN and DEV labels found in the
// mailbox by Message-ID (-live.gold, from the live evidence harness). The
// reports hold aggregates only: no subject, body or address leaves the
// machine; the database is removed.
//
//	evidence-live.exe -test.run TestEvidenceLive -test.v -test.timeout 10800s -live.zone=<IANA> -live.gold=gold.json
//	    -live.report=evidence-live.json -live.rules=evidence-live-rules.json -live.stop=job.stop
//	    [-live.details=1000 -live.rate=100]   (a short verification run: livetest.Limit)

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/source/outlook"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

var (
	liveReport = flag.String("live.report", "evidence-live.json", "aggregate report path")
	liveRules  = flag.String("live.rules", "evidence-live-rules.json", "rule table path")
	liveGold   = flag.String("live.gold", "", "train+dev labels keyed by messageKey (none: no rule table)")
	liveZone   = flag.String("live.zone", "", "the owner's IANA time zone (required: never assumed)")
	livePick   = flag.String("live.pick", "default", `mailboxes to pick: "default" or comma-separated store keys`)
	liveStop   = flag.String("live.stop", "", "stop (cancel, clean up) when this file appears")
)

func TestEvidenceLive(t *testing.T) {
	ctx := livetest.Context(t, *liveStop)
	if *liveZone == "" {
		t.Fatal("-live.zone is required: the owner's zone is never assumed")
	}
	dir, _ := filepath.Abs("live-evidence-data")
	bus := events.New()
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	policy := fsx.Replace{Attempts: cfg.Int("fs.replace_attempts"), Delay: cfg.Duration("fs.replace_retry_delay")}
	_ = fsx.RemoveAll(dir, policy)
	defer func() {
		if err := fsx.RemoveAll(dir, policy); err != nil {
			t.Errorf("LIVE could not remove the temporary database: %v", err)
		}
	}()
	if err := cfg.Set("time.zone", *liveZone); err != nil {
		t.Fatal(err)
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
	conn, err := outlook.New("outlook", cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Keep(ctx, st); err != nil { // the breaker's state in the store, as the app keeps it
		t.Fatal(err)
	}
	livetest.Open(t, ctx, cfg, conn, *livePick) // the limits before the first call; one sync: its run cap is the budget
	start := time.Now()
	sync, err := syncer.New(st, cfg, bus, nil).Run(ctx, conn, nil)
	if err != nil || sync.Error != "" {
		t.Fatalf("sync: %v %s", err, sync.Error)
	}
	syncWall := time.Since(start)
	t.Logf("LIVE sync seen=%d wall=%s details=%+v calls=%d paused=%s", sync.Totals.Seen, syncWall.Round(time.Second),
		detailCounts(sync), conn.Stats().Calls, conn.Stats().Pace.Paused.Round(time.Second))
	liveEvidence(t, ctx, cfg, st, syncWall, sync)
}

// liveEvidence runs the engine twice and writes the reports.
func liveEvidence(t *testing.T, ctx context.Context, cfg *config.Config, st *store.Store, syncWall time.Duration, sync syncer.Report) {
	ev, err := New(Deps{Config: cfg, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	first, err := ev.Run(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstWall := time.Since(start)
	start = time.Now()
	second, err := ev.Run(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondWall := time.Since(start)
	status, err := ev.Stage().Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cov, err := ev.Coverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE evidence messages=%d first=%s second=%s stage=%s", first.Messages, firstWall.Round(time.Millisecond),
		secondWall.Round(time.Millisecond), status.State)
	report(t, *liveReport, map[string]any{"messages": first.Messages, "signals": first.Signals, "sync_wall_ms": syncWall.Milliseconds(),
		"sync_altered": sync.Totals.Altered, "details_altered": detailsAltered(sync),
		"first_run": map[string]any{"wall_ms": firstWall.Milliseconds(), "context_ms": first.ContextTime.Milliseconds(),
			"values_ms": first.ValuesTime.Milliseconds(), "written": first.Written},
		"second_run": map[string]any{"wall_ms": secondWall.Milliseconds(), "computed": second.Computed, "written": second.Written},
		"stage":      status.State, "coverage": cov})
	liveRuleTable(t, ev, st)
}

func detailCounts(r syncer.Report) map[string]any {
	if r.Details == nil {
		return nil
	}
	return map[string]any{"completed": r.Details.Completed, "failed": r.Details.Failed, "pending": r.Details.Pending, "capped": r.Details.Capped}
}

func detailsAltered(r syncer.Report) map[string]int {
	if r.Details == nil {
		return nil
	}
	return r.Details.Altered
}

// liveRuleTable measures the rules on the gold labels whose message is
// in the synced mailbox.
func liveRuleTable(t *testing.T, ev *Engine, st *store.Store) {
	if *liveGold == "" {
		t.Log("LIVE no -live.gold: no rule table")
		return
	}
	raw, err := os.ReadFile(*liveGold)
	if err != nil {
		t.Fatal(err)
	}
	var byKey map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byKey); err != nil {
		t.Fatal(err)
	}
	labels := map[int64]map[string]json.RawMessage{}
	var ids []int64
	found := map[string]bool{} // a message filed twice (a copy in another folder) counts once
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		key := messageKey(r.Message.InternetMessageID)
		if answers, ok := byKey[key]; ok && !found[key] {
			found[key], labels[r.RowID] = true, answers
			ids = append(ids, r.RowID)
		}
		return nil
	})
	recs, err := ev.Records(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	snap := ev.cfg.Snapshot()
	table := ev.rules.Measure(labels, recs, snap.Float("evidence.rules.z"), snap.Float("evidence.rules.min_precision"))
	t.Logf("LIVE gold labels %d, found in the mailbox %d", len(byKey), len(labels))
	report(t, *liveRules, map[string]any{"gold": len(byKey), "matched": len(labels), "splits": "train+dev",
		"z": snap.Float("evidence.rules.z"), "min_precision": snap.Float("evidence.rules.min_precision"), "rules": table})
}

func report(t *testing.T, path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFileAtomic(path, append(b, '\n'), fsx.FilePerm, fsx.Replace{}); err != nil {
		t.Fatal(err)
	}
}
