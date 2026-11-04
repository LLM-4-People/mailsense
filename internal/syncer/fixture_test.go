//go:build fixture

package syncer

// Imports a legacy JSONL export end to end (archive → syncer → store →
// scope) and prints counts, timings, DB size and verdicts. Run through
// scripts/fixture-import.sh. Inputs (environment, not MAILSENSE_*):
//
//	FIXTURE_JSONL      path of the export (required; the test skips without it)
//	FIXTURE_DATA_DIR   keep the database here instead of a temp dir
//	FIXTURE_EXPECT     comma list of key=value checks: messages, inbox, sent,
//	                   owner, shared, unattributed, skipped
//	FIXTURE_FTS_PAIRS  comma list of accented/plain word pairs, e.g. réunion/reunion
//	FIXTURE_OWNER      the export's owner addresses (fixtureconf)

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixtureconf"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestFixtureImport(t *testing.T) {
	path := os.Getenv("FIXTURE_JSONL")
	if path == "" {
		t.Skip("FIXTURE_JSONL not set")
	}
	ctx := t.Context()
	cfg, st := openFixtureStore(t)
	conn, err := archive.New(fixtureconf.SourceID, path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE input=%s", path)
	rep := timedRun(t, New(st, cfg, nil, nil), conn, "wall")
	if rep.Error != "" {
		t.Fatalf("import failed: %s", rep.Error)
	}
	got := reportCounts(t, cfg, st, rep)
	checkExpectations(t, got, os.Getenv("FIXTURE_EXPECT"))
	if again := timedRun(t, New(st, cfg, nil, nil), conn, "rerun wall"); again.Error != "" || again.Totals.Seen != 0 {
		t.Errorf("FIXTURE rerun must be a no-op (unchanged file: cursors skip every folder): %s seen=%d", again.Error, again.Totals.Seen)
	}
	for _, pair := range splitList(os.Getenv("FIXTURE_FTS_PAIRS")) {
		accented, plain, _ := strings.Cut(pair, "/")
		a, errA := st.Search(ctx, accented, 3)
		p, errP := st.Search(ctx, plain, 3)
		t.Logf("FIXTURE fts %q=%d %q=%d", accented, a.Total, plain, p.Total)
		if errA != nil || errP != nil || a.Total == 0 || a.Total != p.Total {
			t.Errorf("FIXTURE fts pair %s: %d vs %d (%v %v)", pair, a.Total, p.Total, errA, errP)
		}
	}
}

func openFixtureStore(t *testing.T) (*config.Config, *store.Store) {
	dir := os.Getenv("FIXTURE_DATA_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: abs}})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := fixtureconf.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range settings {
		if err := cfg.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return cfg, st
}

// timedRun measures real wall time on purpose: it is the reported import cost.
func timedRun(t *testing.T, s *Syncer, conn *archive.Connector, label string) Report {
	start := time.Now()
	rep, _ := s.Run(t.Context(), conn, nil)
	t.Logf("FIXTURE %s=%s run=%d status=%s", label, time.Since(start).Round(time.Millisecond), rep.RunID, rep.Status)
	return rep
}

func reportCounts(t *testing.T, cfg *config.Config, st *store.Store, rep Report) map[string]int {
	ctx := t.Context()
	if err := st.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	roles, _ := st.CountByRole(ctx)
	verdicts, _ := st.VerdictCounts(ctx)
	size, _ := st.SizeBytes(ctx)
	fileInfo, _ := os.Stat(store.Path(cfg))
	total := 0
	for _, n := range roles {
		total += n
	}
	for _, f := range rep.Folders {
		t.Logf("FIXTURE folder=%s seen=%d written=%d skipped=%d duplicates=%d", f.Path, f.Totals.Seen, f.Totals.Written, f.Totals.Skipped, f.Totals.Duplicates)
	}
	t.Logf("FIXTURE messages=%d inbox=%d sent=%d", total, roles[mail.RoleInbox], roles[mail.RoleSent])
	t.Logf("FIXTURE owner=%d shared=%d unattributed=%d unscoped=%d", verdicts[scope.Owner], verdicts[scope.Shared], verdicts[scope.Unattributed], verdicts[""])
	for _, st := range rep.Scope.Stores {
		t.Logf("FIXTURE identity store=%q addresses=%d judged=%d blocked=%q", st.Store.StoreID, len(st.Identity.Addresses), st.Judged, st.Blocked)
	}
	reasons, _ := st.VerdictReasons(ctx)
	for _, r := range reasons {
		t.Logf("FIXTURE reason verdict=%s reason=%s n=%d", r.Verdict, r.Reason, r.Count)
	}
	t.Logf("FIXTURE db=%s logical_bytes=%d file_bytes=%d", filepath.Base(store.Path(cfg)), size, fileInfo.Size())
	return map[string]int{"messages": total, "inbox": roles[mail.RoleInbox], "sent": roles[mail.RoleSent],
		"owner": verdicts[scope.Owner], "shared": verdicts[scope.Shared], "unattributed": verdicts[scope.Unattributed],
		"skipped": rep.Totals.Skipped}
}

func checkExpectations(t *testing.T, got map[string]int, spec string) {
	for _, kv := range splitList(spec) {
		k, v, _ := strings.Cut(kv, "=")
		want, err := strconv.Atoi(v)
		if _, known := got[k]; !known || err != nil {
			t.Fatalf("bad FIXTURE_EXPECT entry %q", kv)
		}
		if got[k] != want {
			t.Errorf("FIXTURE %s=%d, want %d", k, got[k], want)
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
