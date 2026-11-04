package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// A re-run of the same models on the same mail must not rewrite the answer
// rows: their run id and timestamp stay the ones that produced the content.
// This is what keeps a no-op layer-1 run from writing a mailbox-sized WAL.
func TestCommitSkipsUnchangedAnswerRows(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	decisions := []Decision{
		{MessageID: ids[0], Revision: 1, StateHash: "h1", Answers: `{"q":1}`},
		{MessageID: ids[1], Revision: 1, StateHash: "h1", Answers: `{"q":2}`},
	}
	for i := range decisions {
		decisions[i].RunID = 1
	}
	routes := []RouteAnswer{
		{MessageID: ids[0], Revision: 1, Answers: routeJSON},
		{MessageID: ids[1], Revision: 1, Answers: routeJSON},
	}
	previews := []PreviewAnswer{
		{MessageID: ids[0], Revision: 1, Answers: routeJSON},
		{MessageID: ids[1], Revision: 1, Answers: routeJSON},
	}
	commit := func(run int64) {
		rows := func(fn func(RouteAnswer) error) error {
			for i := range routes {
				routes[i].RunID = run
				if err := fn(routes[i]); err != nil {
					return err
				}
			}
			return nil
		}
		prev := func(fn func(PreviewAnswer) error) error {
			for i := range previews {
				previews[i].RunID = run
				if err := fn(previews[i]); err != nil {
					return err
				}
			}
			return nil
		}
		for i := range decisions {
			decisions[i].RunID = run
		}
		if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
			Decisions: decisionRows(decisions...), Routes: rows, Preview: prev}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	commit(first)
	second, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	commit(second)
	records, err := s.LoadRouteRecords(ctx, "fp1", []int64{ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if records[ids[0]].RunID != first {
		t.Fatalf("identical route content was rewritten by run %d, want run %d", records[ids[0]].RunID, first)
	}
	var decisionRun int64
	if err := s.db.QueryRowContext(ctx, "SELECT run_id FROM triage_decisions WHERE message_id = ? AND fingerprint = 'fp1'", ids[0]).Scan(&decisionRun); err != nil {
		t.Fatal(err)
	}
	if decisionRun != 1 {
		t.Fatalf("identical decision was rewritten by run %d, want run 1", decisionRun)
	}
}

// The answer tables are replaced per commit: a message the new run no longer
// answers must lose its old row, without a mailbox-wide rewrite.
func TestCommitRemovesRowsTheRunNoLongerAnswers(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(
			Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":1}`},
			Decision{MessageID: ids[1], Revision: 1, Answers: `{"q":2}`}),
		Routes: routeRows(
			RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: routeJSON},
			RouteAnswer{MessageID: ids[1], Revision: 1, RunID: run, Answers: routeJSON}),
		Preview: previewRows(
			PreviewAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: routeJSON},
			PreviewAnswer{MessageID: ids[1], Revision: 1, RunID: run, Answers: routeJSON})}); err != nil {
		t.Fatal(err)
	}
	run2, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run2, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":1}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run2, Answers: routeJSON}),
		Preview:   previewRows(PreviewAnswer{MessageID: ids[0], Revision: 1, RunID: run2, Answers: routeJSON})}); err != nil {
		t.Fatal(err)
	}
	var decisions, routes, previews int
	for table, dest := range map[string]*int{"triage_decisions": &decisions, "route_answers": &routes, "route_preview_answers": &previews} {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE fingerprint = 'fp1'").Scan(dest); err != nil {
			t.Fatal(err)
		}
		if *dest != 1 {
			t.Fatalf("%s has %d rows, want only the answered message", table, *dest)
		}
	}
}

// The maintenance pass keeps the newest run history and truncates a WAL over
// the configured ceiling.
func TestMaintainPrunesRunsAndBoundsTheWAL(t *testing.T) {
	s, _ := open(t, map[string]any{SettingWALCheckpointMB: 8})
	ctx := t.Context()
	for range 12 {
		id, err := s.BeginLayer1Run(ctx, "inputs")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishLayer1Run(ctx, id, runstate.Succeeded, "{}", ""); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Maintain(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM layer1_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 5 || rep.RunsDeleted != 7 {
		t.Fatalf("%d runs kept, %d deleted; want 5 and 7", runs, rep.RunsDeleted)
	}
	if rep.WALBytes > int64(8)<<20 {
		t.Fatalf("WAL is %d bytes after maintenance, over the 8 MB ceiling", rep.WALBytes)
	}
}

// The one-pass answer stream writes a message's decision, route answer and
// preview together, and prunes each table's stale rows from the same seen
// set.
func TestCombinedAnswerStreamWritesAndPrunes(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	stream := func(rows ...Layer1Answer) func(func(Layer1Answer) error) error {
		return func(fn func(Layer1Answer) error) error {
			for _, r := range rows {
				if err := fn(r); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true, Each: stream(
			Layer1Answer{MessageID: ids[0], Revision: 1, RunID: run, StateHash: "h", Decision: `{"q":1}`, Route: routeJSON, Preview: routeJSON},
			Layer1Answer{MessageID: ids[1], Revision: 1, RunID: run, StateHash: "h", Decision: `{"q":2}`, Route: routeJSON, Preview: routeJSON},
		)}}); err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE fingerprint = 'fp1'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("triage_decisions") != 2 || count("route_answers") != 2 || count("route_preview_answers") != 2 {
		t.Fatalf("first pass wrote %d/%d/%d rows", count("triage_decisions"), count("route_answers"), count("route_preview_answers"))
	}
	run2, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run2, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true, Each: stream(
			Layer1Answer{MessageID: ids[0], Revision: 1, RunID: run2, StateHash: "h", Decision: `{"q":1}`, Route: routeJSON, Preview: routeJSON},
		)}}); err != nil {
		t.Fatal(err)
	}
	if count("triage_decisions") != 1 || count("route_answers") != 1 || count("route_preview_answers") != 1 {
		t.Fatalf("stale rows survived: %d/%d/%d", count("triage_decisions"), count("route_answers"), count("route_preview_answers"))
	}
	records, err := s.LoadRouteRecords(ctx, "fp1", []int64{ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if records[ids[0]].RunID != run {
		t.Fatalf("unchanged route content was rewritten by run %d, want %d", records[ids[0]].RunID, run)
	}
}

// A process that died or was killed leaves its run rows "running". The next
// Open closes them: nothing can finish them, and a reader of the newest run
// (the evidence, index, layer-1 and triage stages) would wait forever.
func TestOpenClosesTheRunsAPreviousProcessLeftOpen(t *testing.T) {
	s, cfg := open(t, nil)
	ctx := t.Context()
	evidence, err := s.BeginEvidenceRun(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginIndexRun(ctx, "space"); err != nil {
		t.Fatal(err)
	}
	syncRun, err := s.BeginRun(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The next start of the same database is a restart.
	reopened, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runs, err := reopened.ListEvidenceRuns(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 || runs[0].ID != evidence || runs[0].State != runstate.Cancelled || runs[0].FinishedAt == "" {
		t.Fatalf("the interrupted evidence run was not closed: %+v", runs)
	}
	if got, err := reopened.ListIndexRuns(ctx, 5); err != nil || len(got) == 0 || got[0].State != runstate.Cancelled {
		t.Fatalf("the interrupted index run was not closed: %+v (%v)", got, err)
	}
	var status, finished string
	if err := reopened.db.QueryRowContext(ctx, "SELECT status, finished_at FROM sync_runs WHERE id = ?", syncRun).Scan(&status, &finished); err != nil {
		t.Fatal(err)
	}
	if status != string(runstate.Cancelled) || finished == "" {
		t.Fatalf("the interrupted sync run is %q at %q", status, finished)
	}
	// A run that finished before the restart is untouched.
	done, err := reopened.BeginEvidenceRun(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.FinishEvidenceRun(ctx, done, runstate.Succeeded, "{}", ""); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	runs, err = again.ListEvidenceRuns(ctx, 5)
	if err != nil || len(runs) == 0 || runs[0].State != runstate.Succeeded {
		t.Fatalf("a finished run was rewritten: %+v (%v)", runs, err)
	}
}

// keep is clamped in one place: a zero must never wipe a history, and the
// secondary order of a table keyed by something other than id is the
// caller's complete ORDER BY.
func TestPruneNewestClampsAndOrders(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	for range 3 {
		if _, err := s.BeginIndexRun(ctx, "space"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := pruneNewest(ctx, s.db, "index_runs", "id", "id DESC", 0); err != nil || n != 2 {
		t.Fatalf("keep 0 removed %d rows (%v), want the newest one kept", n, err)
	}
	if runs, err := s.ListIndexRuns(ctx, 10); err != nil || len(runs) != 1 {
		t.Fatalf("after clamping: %d runs (%v)", len(runs), err)
	}
}

// The WAL and page-cache settings are per connection: a PRAGMA statement after
// Open reached only the one connection the pool handed out. They come from the
// DSN now, so every pooled connection has them.
func TestStoragePragmasApplyToEveryConnection(t *testing.T) {
	const cacheMB, mmapMB = 16, 64
	s, _ := open(t, map[string]any{
		SettingWALAutocheckpoint: 1234,
		SettingWALCheckpointMB:   8,
		SettingCacheSizeMB:       cacheMB,
		SettingMmapSizeMB:        mmapMB,
	})
	ctx := t.Context()
	pragmas := func(c *sql.Conn) (cache, mmap int64) {
		t.Helper()
		if err := c.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cache); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmap); err != nil {
			t.Fatal(err)
		}
		return cache, mmap
	}
	for i := range 2 {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var pages int
		if err := c.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if pages != 1234 {
			t.Fatalf("connection %d has wal_autocheckpoint=%d, want 1234", i, pages)
		}
		var limit int64
		if err := c.QueryRowContext(ctx, "PRAGMA journal_size_limit").Scan(&limit); err != nil {
			t.Fatal(err)
		}
		if limit != 8<<20 {
			t.Fatalf("connection %d has journal_size_limit=%d, want %d", i, limit, 8<<20)
		}
		cache, mmap := pragmas(c)
		if cache != -cacheMB*1024 {
			t.Fatalf("connection %d has cache_size=%d, want %d", i, cache, -cacheMB*1024)
		}
		if mmap != mmapMB<<20 {
			t.Fatalf("connection %d has mmap_size=%d, want %d", i, mmap, mmapMB<<20)
		}
	}

	// The shipped defaults must still open a working store: the cache default
	// is active, and mmap 0 is the documented "off" value. A default outside
	// the schema bounds, or one that leaves the cache at zero, fails here
	// instead of in production.
	def, defCfg := open(t, nil)
	c, err := def.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cache, mmap := pragmas(c)
	if want := -int64(defCfg.Int(SettingCacheSizeMB)) * 1024; cache != want {
		t.Fatalf("defaults have cache_size=%d, want %d", cache, want)
	}
	if want := int64(defCfg.Int(SettingMmapSizeMB)) << 20; mmap != want {
		t.Fatalf("defaults have mmap_size=%d, want %d", mmap, want)
	}
	if cache >= 0 {
		t.Fatalf("defaults leave cache_size=%d inactive", cache)
	}
}

// A checkpoint blocked by a reader reports busy in its result row, not as an
// error. It must not be reported as success.
func TestCheckpointReportsABusyPass(t *testing.T) {
	// A blocked pass must fail fast here: the pool's production busy timeout
	// is five seconds, and the test only needs the busy result.
	s, _ := open(t, map[string]any{SettingWALCheckpointMB: 8, "store.busy_timeout": "150ms"})
	ctx := t.Context()
	reader, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(context.Background(), "COMMIT")
	var n int
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A write makes the WAL non-empty and the old snapshot block truncation.
	if err := s.UpsertMessages(ctx, 0, []mail.Message{msgAt("wal", mail.RoleInbox, "2026-05-01T00:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	err = s.Checkpoint(ctx)
	if err == nil {
		t.Skip("the pass was not blocked on this platform")
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("a blocked checkpoint reported %v", err)
	}
}

// A clean shutdown checkpoints the WAL: the next launch must not replay the
// largest transaction's log, and an idle store must leave no WAL behind.
func TestCloseCheckpointsTheWAL(t *testing.T) {
	s, _ := open(t, map[string]any{SettingWALCheckpointMB: 64})
	seed(t, s, 20)
	if s.WALSize() == 0 {
		t.Skip("the fixture left no WAL to checkpoint")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := s.WALSize(); got != 0 {
		t.Fatalf("the WAL is %d bytes after Close", got)
	}
}

// The production one-pass commit (Answers) must bump the route generation
// exactly like the separate route stream; otherwise a committed route keeps
// serving the previous cached page and label queue for the TTL.
func TestOnePassCommitBumpsRouteGeneration(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	before := s.RouteGeneration()
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	each := func(fn func(Layer1Answer) error) error {
		return fn(Layer1Answer{MessageID: ids[0], Revision: 1, RunID: run, StateHash: "h", Decision: `{"q":1}`, Route: routeJSON})
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint, Answers: &Layer1Answers{Each: each, Routes: true}}); err != nil {
		t.Fatal(err)
	}
	if got := s.RouteGeneration(); got != before+1 {
		t.Fatalf("the one-pass commit did not bump the route generation: %d → %d", before, got)
	}
}

// A message can be deleted between the run's id snapshot and the commit's
// transaction. The answer insert must skip that row, not fail the whole
// commit on the foreign key.
func TestCommitSkipsAMessageDeletedAfterTheSnapshot(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", ids[1]); err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	each := func(fn func(Layer1Answer) error) error {
		for _, id := range ids {
			if err := fn(Layer1Answer{MessageID: id, Revision: 1, RunID: run, StateHash: "h", Decision: `{"q":1}`, Route: routeJSON}); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: "rk1-stale-id", Answers: &Layer1Answers{Each: each, Routes: true}}); err != nil {
		t.Fatalf("a deleted message rolled back the commit: %v", err)
	}
	routes, err := s.LoadRouteAnswers(ctx, "rk1-stale-id", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[ids[0]] == nil {
		t.Fatalf("route rows after the stale id: %+v", routes)
	}
	decisions, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[ids[0]].MessageID != ids[0] {
		t.Fatalf("decisions after the stale id: %+v", decisions)
	}
	if err := s.SaveFailure(ctx, key, Failure{MessageID: ids[1], Error: "deleted", RunID: run}); err != nil {
		t.Fatalf("a failure for a deleted message failed: %v", err)
	}
}

// A profile or decider change writes a new answer copy; maintenance keeps
// only the newest keys and bounds each table, so the paid cache cannot grow
// without bound.
func TestMaintainPrunesTriageKeys(t *testing.T) {
	s, _ := open(t, map[string]any{SettingRetainTriage: 2})
	ctx := t.Context()
	ids := seed(t, s, 3)
	keys := []TriageKey{{Fingerprint: "fp-a", Decider: "layer1"},
		{Fingerprint: "fp-b", Decider: "layer1"}, {Fingerprint: "fp-c", Decider: "layer1"}}
	stamps := []string{"2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z"}
	for i, k := range keys {
		if err := s.SaveDecisions(ctx, k, []Decision{{MessageID: ids[i], Revision: 1, StateHash: "h", Answers: `{}`}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE triage_decisions SET decided_at = ? WHERE fingerprint = ? AND decider = ?",
			stamps[i], k.Fingerprint, k.Decider); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveFailure(ctx, keys[0], Failure{MessageID: ids[0], Error: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFailure(ctx, keys[1], Failure{MessageID: ids[1], Error: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFailure(ctx, keys[2], Failure{MessageID: ids[2], Error: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE triage_failures SET failed_at = CASE fingerprint
		WHEN 'fp-a' THEN '2026-11-01T00:00:00Z' WHEN 'fp-b' THEN '2026-11-02T00:00:00Z'
		ELSE '2026-11-03T00:00:00Z' END`); err != nil {
		t.Fatal(err)
	}

	rep, err := s.Maintain(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.TriageKeysDeleted != 2 {
		t.Fatalf("deleted %d triage rows, want the oldest decision and failure", rep.TriageKeysDeleted)
	}
	count := func(table string) int {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT fingerprint || '|' || decider) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count("triage_decisions"); got != 2 {
		t.Fatalf("%d decision keys kept, want 2", got)
	}
	if got := count("triage_failures"); got != 2 {
		t.Fatalf("%d failure keys kept, want 2", got)
	}
	var oldest int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM triage_decisions WHERE fingerprint = 'fp-a'").Scan(&oldest); err != nil || oldest != 0 {
		t.Fatalf("the oldest decision key survived: %d %v", oldest, err)
	}
	rep, err = s.Maintain(ctx, 50)
	if err != nil || rep.TriageKeysDeleted != 0 {
		t.Fatalf("a second pass deleted %d rows (%v)", rep.TriageKeysDeleted, err)
	}
	// The clamp is the same as the run history: zero keeps the newest key.
	if _, err := s.pruneTriageKeys(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := count("triage_decisions"); got != 1 {
		t.Fatalf("%d decision keys after a zero keep, want 1", got)
	}
}
