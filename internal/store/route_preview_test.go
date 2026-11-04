package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// previewRows is a Preview stream of the given rows.
func previewRows(rows ...PreviewAnswer) func(func(PreviewAnswer) error) error {
	return func(fn func(PreviewAnswer) error) error {
		for _, r := range rows {
			if err := fn(r); err != nil {
				return err
			}
		}
		return nil
	}
}

const previewJSON = `{"q":{"type":"noul","value":0.75}}`

func TestPreviewAnswersCommitReplaceAndRead(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":{"type":"noul","value":0.5}}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: routeJSON}),
		Preview: previewRows(
			PreviewAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: previewJSON},
			PreviewAnswer{MessageID: ids[1], Revision: 2, RunID: run, Answers: previewJSON},
		)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPreviewRecords(ctx, "fp1", []int64{ids[0], ids[1], 999999})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[ids[0]].Answers["q"].Value != 0.75 || got[ids[1]].Answers["q"].Value != 0.75 {
		t.Fatalf("loaded %+v", got)
	}
	if r := got[ids[0]]; r.Revision != 1 || r.RunID != run || r.DecidedAt == "" {
		t.Fatalf("the record's metadata: %+v", r)
	}
	if r := got[ids[1]]; r.Revision != 2 {
		t.Fatalf("the record's revision: %+v", r)
	}
	if _, ok := got[999999]; ok {
		t.Fatal("an unknown id must be absent, never zero-filled")
	}
	if none, err := s.LoadPreviewRecords(ctx, "fp1", nil); err != nil || len(none) != 0 {
		t.Fatalf("no ids: %v %v", none, err)
	}
	if other, err := s.LoadPreviewRecords(ctx, "other", []int64{ids[0]}); err != nil || len(other) != 0 {
		t.Fatalf("another fingerprint sees nothing: %v %v", other, err)
	}
	// The route table is a different source: it holds exactly its own rows.
	var routed int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_answers WHERE fingerprint = 'fp1'").Scan(&routed); err != nil || routed != 1 {
		t.Fatalf("route rows %d: %v", routed, err)
	}
	var previewed int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_preview_answers WHERE fingerprint = 'fp1'").Scan(&previewed); err != nil || previewed != 2 {
		t.Fatalf("preview rows %d: %v", previewed, err)
	}

	// A later preview commit replaces the fingerprint's rows as a whole,
	// and a commit without a preview stream leaves them untouched.
	run, _ = s.BeginLayer1Run(ctx, "inputs")
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 2, Answers: `{"q":{"type":"noul","value":0.4}}`}),
		Preview:   previewRows(PreviewAnswer{MessageID: ids[2], Revision: 3, RunID: run, Answers: previewJSON})}); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadPreviewRecords(ctx, "fp1", ids)
	if err != nil || len(got) != 1 || got[ids[2]].Revision != 3 {
		t.Fatalf("replacement %+v: %v", got, err)
	}
	if kept, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0]}); err != nil || kept[ids[0]]["q"].Value != 0.75 {
		t.Fatalf("a commit without routes changed them: %v %v", kept, err)
	}
	run, _ = s.BeginLayer1Run(ctx, "inputs")
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 3, Answers: `{"q":{"type":"noul","value":0.3}}`})}); err != nil {
		t.Fatal(err)
	}
	if kept, err := s.LoadPreviewRecords(ctx, "fp1", ids); err != nil || len(kept) != 1 || kept[ids[2]].Revision != 3 {
		t.Fatalf("a commit without previews changed them: %v %v", kept, err)
	}

	// A malformed stored row fails the read: preview data is never
	// silently an empty answer.
	if _, err := s.db.ExecContext(ctx, "UPDATE route_preview_answers SET answers = '{' WHERE message_id = ? AND fingerprint = 'fp1'", ids[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPreviewRecords(ctx, "fp1", ids); err == nil {
		t.Fatal("malformed preview answers must fail")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE route_preview_answers SET answers = 'null' WHERE message_id = ? AND fingerprint = 'fp1'", ids[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPreviewRecords(ctx, "fp1", []int64{ids[2]}); err == nil {
		t.Fatal("a null answers value must fail, never become an empty map")
	}
}

func TestPreviewAnswersPruneAtomicityAndSeparation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_preview_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
		VALUES (?, 'old-profile', 0, ?, 0, '')`, ids[1], previewJSON); err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, _ := s.BeginLayer1Run(ctx, "inputs")
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":{"type":"noul","value":0.5}}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[1], Revision: 1, RunID: run, Answers: routeJSON}),
		Preview:   previewRows(PreviewAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: previewJSON})}); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_preview_answers WHERE fingerprint = 'old-profile'").Scan(&old); err != nil || old != 0 {
		t.Fatalf("the commit prunes every other profile: %d %v", old, err)
	}
	previews, err := s.LoadPreviewRecords(ctx, "fp1", ids)
	if err != nil || len(previews) != 1 || previews[ids[0]].MessageID != ids[0] {
		t.Fatalf("previews %+v: %v", previews, err)
	}
	routes, err := s.LoadRouteAnswers(ctx, "fp1", ids)
	if err != nil || len(routes) != 1 || routes[ids[1]]["q"].Value != 0.75 {
		t.Fatalf("routes %+v: %v", routes, err)
	}
	if _, ok := routes[ids[0]]; ok {
		t.Fatal("a preview answer filled a route question")
	}
	if _, ok := previews[ids[1]]; ok {
		t.Fatal("a route answer filled a preview question")
	}

	// A failure after the preview stream (the report) rolls everything
	// back: the previous previews stay and no other profile is pruned.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_preview_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
		VALUES (?, 'old-profile', 0, ?, 0, '')`, ids[1], previewJSON); err != nil {
		t.Fatal(err)
	}
	before := previews[ids[0]]
	run, _ = s.BeginLayer1Run(ctx, "inputs")
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "", errors.New("boom") }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 2, Answers: `{"q":{"type":"noul","value":0.25}}`}),
		Preview:   previewRows(PreviewAnswer{MessageID: ids[1], Revision: 2, RunID: run, Answers: previewJSON})})
	if err == nil {
		t.Fatal("a failing commit must fail")
	}
	after, err := s.LoadPreviewRecords(ctx, "fp1", ids)
	if err != nil || len(after) != 1 || !reflect.DeepEqual(after[ids[0]], before) {
		t.Fatalf("a failed commit changed the stored previews:\nbefore %+v\nafter  %+v (%v)", before, after, err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_preview_answers WHERE fingerprint = 'old-profile'").Scan(&old); err != nil || old != 1 {
		t.Fatalf("the rollback pruned the other profile: %d %v", old, err)
	}
	if n, err := s.PrunePreviewAnswers(ctx, "fp1"); err != nil || n != 1 {
		t.Fatalf("prune removed %d: %v", n, err)
	}
	var routeRows int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_answers WHERE fingerprint = 'fp1'").Scan(&routeRows); err != nil || routeRows != 1 {
		t.Fatalf("pruning previews changed the route table: %d %v", routeRows, err)
	}
}

// A profile can leave preview answers behind while the explicit test mode is
// on; opening the same store with the mode off must clear the unreachable
// copy instead of keeping it for the life of the mailbox.
func TestOpenClearsPreviewAnswersWhenTestModeOff(t *testing.T) {
	s, cfg := open(t, map[string]any{config.SettingPreview: true})
	ctx := t.Context()
	ids := seed(t, s, 1)
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":{"type":"noul","value":0.5}}`}),
		Preview:   previewRows(PreviewAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: previewJSON})}); err != nil {
		t.Fatal(err)
	}
	count := func(st *Store) int {
		t.Helper()
		var n int
		if err := st.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_preview_answers").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(s); got != 1 {
		t.Fatalf("wrote %d preview rows, want 1", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(config.SettingPreview, false); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if got := count(again); got != 0 {
		t.Fatalf("test mode off kept %d preview rows", got)
	}
}
