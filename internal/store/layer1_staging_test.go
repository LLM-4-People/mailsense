package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// layer1AnswerStream is an Answers stream of the given rows.
func layer1AnswerStream(rows ...Layer1Answer) func(func(Layer1Answer) error) error {
	return func(fn func(Layer1Answer) error) error {
		for _, row := range rows {
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	}
}

// layer1AnswerRows are the rows of one run for the given messages.
func layer1AnswerRows(ids []int64, run int64, revision int64) []Layer1Answer {
	rows := make([]Layer1Answer, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, Layer1Answer{MessageID: id, Revision: revision, RunID: run, StateHash: "h",
			Decision: `{"q":{"type":"noul","value":0.5}}`, Route: routeJSON, Preview: routeJSON})
	}
	return rows
}

// answerSnapshot is every answer table a commit owns.
type answerSnapshot struct {
	decisions map[int64]Decision
	routes    map[int64]RouteRecord
	previews  map[int64]PreviewRecord
}

func snapshotAnswers(t *testing.T, s *Store, key TriageKey, routeKey string, ids []int64) answerSnapshot {
	t.Helper()
	ctx := t.Context()
	decisions, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := s.LoadRouteRecords(ctx, routeKey, ids)
	if err != nil {
		t.Fatal(err)
	}
	previews, err := s.LoadPreviewRecords(ctx, routeKey, ids)
	if err != nil {
		t.Fatal(err)
	}
	return answerSnapshot{decisions: decisions, routes: routes, previews: previews}
}

func (a answerSnapshot) requireUnchanged(t *testing.T, s *Store, key TriageKey, routeKey string, ids []int64) {
	t.Helper()
	after := snapshotAnswers(t, s, key, routeKey, ids)
	if !reflect.DeepEqual(a, after) {
		t.Fatalf("a failed commit changed the stored answers:\nbefore %+v\nafter  %+v", a, after)
	}
}

// runRow is the run columns a commit must leave untouched on failure.
type runRow struct {
	state    runstate.State
	report   string
	finished string
}

// runOutcome reads the columns a commit must leave untouched on failure.
func runOutcome(t *testing.T, s *Store, id int64) runRow {
	t.Helper()
	var state, report, finished string
	if err := s.db.QueryRowContext(t.Context(),
		"SELECT status, report, finished_at FROM layer1_runs WHERE id = ?", id).Scan(&state, &report, &finished); err != nil {
		t.Fatal(err)
	}
	return runRow{state: runstate.State(state), report: report, finished: finished}
}

// The answer phase is the long part of a layer-1 commit (inference over the
// whole mailbox). It must run on a connection-private staging table, outside
// the main database's write transaction: a concurrent mailbox write and a
// label write must complete while the answer stream is blocked.
func TestLayer1CommitDoesNotHoldTheWriteLockDuringAnswers(t *testing.T) {
	s, _ := open(t, map[string]any{"store.max_open_conns": 2, "store.busy_timeout": "1s"})
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	rows := layer1AnswerRows(ids, run, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	each := func(fn func(Layer1Answer) error) error {
		if err := fn(rows[0]); err != nil {
			return err
		}
		close(started)
		<-release
		for _, row := range rows[1:] {
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	}
	commit := make(chan error, 1)
	go func() {
		commit <- s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return `{"answers":3}`, nil },
			Key: key, RouteKey: key.Fingerprint,
			Answers: &Layer1Answers{Each: each, Routes: true, Preview: true}})
	}()
	select {
	case <-started:
	case err := <-commit:
		t.Fatalf("the commit failed before the answer stream: %v", err)
	}
	// The commit has staged one row and is blocked inside the answer
	// stream. Both writes below must finish before the stream is released;
	// with the old whole-commit transaction they waited out the busy timeout
	// and failed SQLITE_BUSY.
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	upsertErr := s.UpsertMessages(writeCtx, 0, []mail.Message{mailtest.Message("src", "concurrent")})
	labelErr := s.PutLabel(writeCtx, ids[0], "q", `"work"`, LabelManual)
	close(release)
	if commitErr := <-commit; commitErr != nil {
		t.Fatalf("the commit failed: %v", commitErr)
	}
	if upsertErr != nil {
		t.Fatalf("a mailbox write was blocked by the answer phase: %v", upsertErr)
	}
	if labelErr != nil {
		t.Fatalf("a label write was blocked by the answer phase: %v", labelErr)
	}
	decisions, err := s.LoadDecisions(ctx, key)
	if err != nil || len(decisions) != 3 {
		t.Fatalf("decisions after the commit: %d %v", len(decisions), err)
	}
	routes, err := s.LoadRouteRecords(ctx, key.Fingerprint, ids)
	if err != nil || len(routes) != 3 || routes[ids[2]].RunID != run {
		t.Fatalf("route answers after the commit: %d %v", len(routes), err)
	}
	previews, err := s.LoadPreviewRecords(ctx, key.Fingerprint, ids)
	if err != nil || len(previews) != 3 {
		t.Fatalf("preview answers after the commit: %d %v", len(previews), err)
	}
	if got := runOutcome(t, s, run); got.state != runstate.Succeeded || got.report != `{"answers":3}` || got.finished == "" {
		t.Fatalf("the run row: %+v", got)
	}
	labels, err := s.ListLabels(ctx)
	if err != nil || len(labels) != 1 || labels[0].MessageID != ids[0] || labels[0].Question != "q" {
		t.Fatalf("the label written during the answer phase: %+v %v", labels, err)
	}
}

// A failure while the answers are streamed into the staging table happens
// before the main transaction: decisions, routes, previews, the run row and
// any promotion must all stay as they were.
func TestLayer1CommitStagingFailureLeavesTheDatabaseUntouched(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 200)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run1, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run1, Report: func() (string, error) { return `{"answers":2}`, nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: layer1AnswerStream(layer1AnswerRows(ids[:2], run1, 1)...), Routes: true, Preview: true}}); err != nil {
		t.Fatal(err)
	}
	before := snapshotAnswers(t, s, key, key.Fingerprint, ids)
	beforeRun := runOutcome(t, s, run1)
	gen, routeGen := s.Generation(), s.RouteGeneration()

	active, err := s.SaveLayer1Candidate(ctx, Layer1Model{Question: "q", VersionKey: "v1", Kind: "binary", Classes: 2,
		Params: []byte{1}, Meta: "{}", Metrics: "null", RunID: run1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteLayer1(ctx, active); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.SaveLayer1Candidate(ctx, Layer1Model{Question: "q", VersionKey: "v2", Kind: "binary", Classes: 2,
		Params: []byte{2}, Meta: "{}", Metrics: "null", RunID: run1})
	if err != nil {
		t.Fatal(err)
	}

	run2, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	pending := runOutcome(t, s, run2)
	// At least one batch reaches the staging table before the stream fails,
	// so the untouched check covers a partially staged commit.
	staged := layer1AnswerRows(ids[2:2+layer1StageBatch], run2, 2)
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run2, Report: func() (string, error) { return "{}", nil },
		Promote: []int64{candidate}, Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true, Each: func(fn func(Layer1Answer) error) error {
			for _, row := range staged {
				if err := fn(row); err != nil {
					return err
				}
			}
			return errors.New("answer stream failed")
		}}})
	if err == nil {
		t.Fatal("a failing answer stream committed")
	}
	before.requireUnchanged(t, s, key, key.Fingerprint, ids)
	if got := runOutcome(t, s, run2); !reflect.DeepEqual(pending, got) {
		t.Fatalf("a failed staging phase changed its run row: %+v → %+v", pending, got)
	}
	if after := runOutcome(t, s, run1); !reflect.DeepEqual(beforeRun, after) {
		t.Fatalf("a failed staging phase changed an older run: %+v → %+v", beforeRun, after)
	}
	if got := s.Generation(); got != gen {
		t.Fatalf("a failed staging phase bumped the mailbox generation: %d → %d", gen, got)
	}
	if got := s.RouteGeneration(); got != routeGen {
		t.Fatalf("a failed staging phase bumped the route generation: %d → %d", routeGen, got)
	}
	models, err := s.ActiveLayer1(ctx)
	if err != nil || len(models) != 1 || models[0].ID != active {
		t.Fatalf("a failed staging phase promoted its candidate: %+v %v", models, err)
	}

	// The staging table is disposable: the next commit on that connection
	// must start clean and succeed.
	run3, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run3, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: layer1AnswerStream(layer1AnswerRows(ids[2:3], run3, 2)...), Routes: true, Preview: true}}); err != nil {
		t.Fatalf("the commit after a failed staging phase: %v", err)
	}
	if decisions, err := s.LoadDecisions(ctx, key); err != nil || len(decisions) != 1 || decisions[ids[2]].RunID != run3 {
		t.Fatalf("the recovered commit wrote %d decisions: %v", len(decisions), err)
	}
}

// A failure after the staging phase, inside the short transaction, rolls the
// promotion, every answer table and the run row back together.
func TestLayer1CommitShortTransactionFailureRollsBack(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run1, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run1, Report: func() (string, error) { return `{"answers":2}`, nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: layer1AnswerStream(layer1AnswerRows(ids[:2], run1, 1)...), Routes: true, Preview: true}}); err != nil {
		t.Fatal(err)
	}
	before := snapshotAnswers(t, s, key, key.Fingerprint, ids)
	gen, routeGen := s.Generation(), s.RouteGeneration()
	rowsFor := func(run int64) []Layer1Answer { return layer1AnswerRows(ids, run, 2) }

	// The report is called after the staged answers are written: its
	// failure is the last step of the short transaction.
	run2, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	pending := runOutcome(t, s, run2)
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run2, Report: func() (string, error) { return "", errors.New("report failed") },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: layer1AnswerStream(rowsFor(run2)...), Routes: true, Preview: true}})
	if err == nil {
		t.Fatal("a commit with a failing report succeeded")
	}
	before.requireUnchanged(t, s, key, key.Fingerprint, ids)
	if got := runOutcome(t, s, run2); !reflect.DeepEqual(pending, got) {
		t.Fatalf("a failed report changed its run row: %+v → %+v", pending, got)
	}

	// A promotion failure is earlier in the same transaction; it must roll
	// back the staged answers that were already written too.
	run3, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	pending3 := runOutcome(t, s, run3)
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run3, Report: func() (string, error) { return "{}", nil },
		Promote: []int64{999999}, Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: layer1AnswerStream(rowsFor(run3)...), Routes: true, Preview: true}})
	if err == nil {
		t.Fatal("a commit promoting an unknown model succeeded")
	}
	before.requireUnchanged(t, s, key, key.Fingerprint, ids)
	if got := runOutcome(t, s, run3); !reflect.DeepEqual(pending3, got) {
		t.Fatalf("a failed promotion changed its run row: %+v → %+v", pending3, got)
	}
	if got := s.Generation(); got != gen {
		t.Fatalf("failed short transactions bumped the mailbox generation: %d → %d", gen, got)
	}
	if got := s.RouteGeneration(); got != routeGen {
		t.Fatalf("failed short transactions bumped the route generation: %d → %d", routeGen, got)
	}
}

// The commit's main transaction must not span the answer phase. A slow
// answer stream (three milliseconds per message, a stand-in for inference) is
// measured against the short transaction the commit hook reports.
func TestLayer1CommitTransactionSpanExcludesAnswers(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 200)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	var txSpan, answerSpan time.Duration
	layer1CommitTxSpan = func(d time.Duration) { txSpan = d }
	t.Cleanup(func() { layer1CommitTxSpan = nil })
	rows := layer1AnswerRows(ids, run, 1)
	each := func(fn func(Layer1Answer) error) error {
		started := time.Now()
		defer func() { answerSpan = time.Since(started) }()
		for _, row := range rows {
			time.Sleep(3 * time.Millisecond)
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: each, Routes: true, Preview: true}}); err != nil {
		t.Fatal(err)
	}
	if txSpan == 0 || answerSpan == 0 {
		t.Fatalf("the span hook did not observe the commit: tx %s answers %s", txSpan, answerSpan)
	}
	if txSpan > answerSpan/2 {
		t.Fatalf("the commit transaction spans the answer phase: tx %s, answers %s", txSpan, answerSpan)
	}
	t.Logf("messages=%d answer_phase=%s commit_transaction=%s", len(ids), answerSpan, txSpan)
}

// The compact one-pass commit stages the decision only: Derive rebuilds the
// route and preview answers during the write transaction, so the staging
// payload is the decision bytes, never a second copy of all three tables.
func TestLayer1CompactCommitStagesOnlyDecisions(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	decision := `{"q":"aaaaaaaaaaaaaaaaaaaaaaaaaa"}` // the length of routeJSON, the conservative case
	decisions := []string{decision, decision, decision}
	routes := []string{routeJSON, routeJSON, routeJSON}
	previews := []string{routeJSON, routeJSON, routeJSON}
	var staged int64
	layer1StagePayload = func(n int64) { staged = n }
	t.Cleanup(func() { layer1StagePayload = nil })
	var derived []int64
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true,
			Each: func(fn func(Layer1Answer) error) error {
				for i, id := range ids {
					if err := fn(Layer1Answer{MessageID: id, Revision: 1, RunID: run, StateHash: "h", Decision: decisions[i]}); err != nil {
						return err
					}
				}
				return nil
			},
			Derive: func(row *Layer1Answer) error {
				derived = append(derived, row.MessageID)
				for i, id := range ids {
					if id == row.MessageID {
						row.Route, row.Preview = routes[i], previews[i]
					}
				}
				return nil
			}}})
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, d := range decisions {
		want += int64(len(d))
	}
	if staged != want {
		t.Fatalf("staged payload = %d bytes, want the decision bytes only (%d)", staged, want)
	}
	// Three same-shaped JSON columns are the conservative case: the compact
	// form keeps one of three (preview on) or one of two (preview off).
	full := want + int64(len(ids))*int64(2*len(routeJSON))
	t.Logf("staged decision=%d bytes; the full form would stage %d bytes (%.0f%% kept)", staged, full, 100*float64(staged)/float64(full))
	decisionsByID, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if got := decisionsByID[id].Answers; got != decisions[i] {
			t.Fatalf("decision of %d = %q, want %q", id, got, decisions[i])
		}
	}
	if len(derived) != len(ids) {
		t.Fatalf("Derive ran %d times, want %d", len(derived), len(ids))
	}
	for i := 1; i < len(derived); i++ {
		if derived[i] <= derived[i-1] {
			t.Fatalf("Derive ran out of message order: %v", derived)
		}
	}
	stored, err := s.LoadRouteAnswers(ctx, key.Fingerprint, ids)
	if err != nil || len(stored) != len(ids) {
		t.Fatalf("route answers: %d %v", len(stored), err)
	}
	previewsByID, err := s.LoadPreviewRecords(ctx, key.Fingerprint, ids)
	if err != nil || len(previewsByID) != len(ids) {
		t.Fatalf("preview answers: %d %v", len(previewsByID), err)
	}
	for _, id := range ids {
		if got := stored[id]["q"].Value; got != 0.75 {
			t.Fatalf("route of %d = %+v", id, stored[id])
		}
		if got := previewsByID[id].Answers["q"].Value; got != 0.75 {
			t.Fatalf("preview of %d = %+v", id, previewsByID[id].Answers)
		}
	}
}

// The compact and full forms are two spellings of one commit: the same
// decision, route and preview rows, written in the same order, with only
// the staging representation differing.
func TestLayer1CompactAndFullCommitsMatch(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	rows := layer1AnswerRows(ids, 0, 1)
	full := make([]Layer1Answer, len(rows))
	for i, row := range rows {
		row.RunID = 0
		full[i] = row
	}
	commit := func(compact bool) {
		t.Helper()
		run, err := s.BeginLayer1Run(ctx, "inputs")
		if err != nil {
			t.Fatal(err)
		}
		answers := &Layer1Answers{Routes: true, Preview: true,
			Each: func(fn func(Layer1Answer) error) error {
				for _, row := range full {
					row.RunID = run
					if compact {
						row.Route, row.Preview = "", ""
					}
					if err := fn(row); err != nil {
						return err
					}
				}
				return nil
			}}
		if compact {
			answers.Derive = func(row *Layer1Answer) error {
				row.Route, row.Preview = routeJSON, routeJSON
				return nil
			}
		}
		if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
			Key: key, RouteKey: key.Fingerprint, Answers: answers}); err != nil {
			t.Fatal(err)
		}
	}
	commit(false)
	fullDecisions, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	fullRoutes, err := s.LoadRouteAnswers(ctx, key.Fingerprint, ids)
	if err != nil {
		t.Fatal(err)
	}
	fullPreviews, err := s.LoadPreviewRecords(ctx, key.Fingerprint, ids)
	if err != nil {
		t.Fatal(err)
	}
	commit(true)
	compactDecisions, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	compactRoutes, err := s.LoadRouteAnswers(ctx, key.Fingerprint, ids)
	if err != nil {
		t.Fatal(err)
	}
	compactPreviews, err := s.LoadPreviewRecords(ctx, key.Fingerprint, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if fullDecisions[id].Answers != compactDecisions[id].Answers {
			t.Fatalf("decision of %d differs: %q vs %q", id, fullDecisions[id].Answers, compactDecisions[id].Answers)
		}
		f, _ := json.Marshal(fullRoutes[id])
		c, _ := json.Marshal(compactRoutes[id])
		if string(f) != string(c) {
			t.Fatalf("route of %d differs: %s vs %s", id, f, c)
		}
		f, _ = json.Marshal(fullPreviews[id].Answers)
		c, _ = json.Marshal(compactPreviews[id].Answers)
		if string(f) != string(c) {
			t.Fatalf("preview of %d differs: %s vs %s", id, f, c)
		}
	}
}

// A Derive failure happens inside the short transaction: every earlier
// answer row, the promotion and the run row roll back together.
func TestLayer1CompactDeriveFailureRollsBack(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run1, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run1, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true,
			Each: layer1AnswerStream(layer1AnswerRows(ids, run1, 1)...)}}); err != nil {
		t.Fatal(err)
	}
	before := snapshotAnswers(t, s, key, key.Fingerprint, ids)
	gen, routeGen := s.Generation(), s.RouteGeneration()
	active := int64(0)
	if models, err := s.ActiveLayer1(ctx); err == nil && len(models) > 0 {
		active = models[0].ID
	}
	candidate, err := s.SaveLayer1Candidate(ctx, Layer1Model{Question: "q", VersionKey: "v2", Kind: "binary", Classes: 2,
		Params: []byte{2}, Meta: "{}", Metrics: "null", RunID: run1})
	if err != nil {
		t.Fatal(err)
	}
	run2, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	pending := runOutcome(t, s, run2)
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run2, Report: func() (string, error) { return "{}", nil },
		Promote: []int64{candidate}, Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Routes: true, Preview: true,
			Each: func(fn func(Layer1Answer) error) error {
				for _, row := range layer1AnswerRows(ids, run2, 2) {
					row.Route, row.Preview = "", ""
					if err := fn(row); err != nil {
						return err
					}
				}
				return nil
			},
			Derive: func(row *Layer1Answer) error {
				if row.MessageID == ids[1] {
					return errors.New("derive failed")
				}
				row.Route, row.Preview = routeJSON, routeJSON
				return nil
			}}})
	if err == nil {
		t.Fatal("a compact commit whose Derive failed succeeded")
	}
	before.requireUnchanged(t, s, key, key.Fingerprint, ids)
	if got := runOutcome(t, s, run2); !reflect.DeepEqual(pending, got) {
		t.Fatalf("a failed Derive changed its run row: %+v → %+v", pending, got)
	}
	models, err := s.ActiveLayer1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active == 0 {
		if len(models) != 0 {
			t.Fatalf("a failed Derive promoted a candidate: %+v", models)
		}
	} else if len(models) != 1 || models[0].ID != active {
		t.Fatalf("a failed Derive promoted its candidate: %+v", models)
	}
	if got := s.Generation(); got != gen {
		t.Fatalf("a failed Derive bumped the mailbox generation: %d → %d", gen, got)
	}
	if got := s.RouteGeneration(); got != routeGen {
		t.Fatalf("a failed Derive bumped the route generation: %d → %d", routeGen, got)
	}
}

// The compact commit derives route and preview answers inside the short
// write transaction, not during the staging phase: the staged decision is
// the only inference output, and the derivation is part of the atomic unit
// that can still roll back. The span hook sees the derivation in txSpan and
// not in answerSpan.
func TestLayer1CompactCommitDerivesInsideTheTransaction(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 20)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	var txSpan, answerSpan time.Duration
	layer1CommitTxSpan = func(d time.Duration) { txSpan = d }
	t.Cleanup(func() { layer1CommitTxSpan = nil })
	rows := layer1AnswerRows(ids, run, 1)
	each := func(fn func(Layer1Answer) error) error {
		started := time.Now()
		defer func() { answerSpan = time.Since(started) }()
		for _, row := range rows {
			row.Route, row.Preview = "", ""
			if err := fn(row); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: key, RouteKey: key.Fingerprint,
		Answers: &Layer1Answers{Each: each, Routes: true, Preview: true,
			Derive: func(row *Layer1Answer) error {
				time.Sleep(2 * time.Millisecond) // the derivation's CPU, a stand-in
				row.Route, row.Preview = routeJSON, routeJSON
				return nil
			}}}); err != nil {
		t.Fatal(err)
	}
	if txSpan < 20*time.Millisecond {
		t.Fatalf("the write transaction did not include the derivation: tx %s, answers %s", txSpan, answerSpan)
	}
	if answerSpan > 10*time.Millisecond {
		t.Fatalf("the staging phase carried the derivation: answers %s", answerSpan)
	}
	t.Logf("messages=%d staging=%s commit_transaction=%s", len(ids), answerSpan, txSpan)
}
