package store

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

func TestLayer1ModelsOneActivePerQuestion(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := Layer1Model{Question: "q", VersionKey: "v1", Kind: "binary", Classes: 2, Params: []byte{1}, Meta: "{}", Metrics: "null", RunID: 1}
	id1, err := s.SaveLayer1Candidate(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteLayer1(ctx, id1); err != nil {
		t.Fatal(err)
	}
	// the same version again: the active row is kept as it is
	m.Params = []byte{9}
	if id, err := s.SaveLayer1Candidate(ctx, m); err != nil || id != id1 {
		t.Fatalf("%d %v", id, err)
	}
	active, _ := s.ActiveLayer1(ctx)
	if len(active) != 1 || active[0].Params[0] != 1 {
		t.Fatalf("the active row changed: %+v", active)
	}
	for i := range 5 {
		m.VersionKey = string(rune('a' + i))
		id, err := s.SaveLayer1Candidate(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 4 {
			if err := s.PromoteLayer1(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if active, _ := s.ActiveLayer1(ctx); len(active) != 1 || active[0].VersionKey != "e" {
		t.Fatalf("one active model: %+v", active)
	}
	if n, err := s.PruneLayer1(ctx, 2); err != nil || n != 3 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if all, _ := s.Layer1Models(ctx, "q"); len(all) != 3 {
		t.Fatalf("kept %d (active + 2)", len(all))
	}
	if err := s.PromoteLayer1(ctx, 999); err == nil {
		t.Error("promoted an unknown model")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE layer1_models SET state = 'active'"); err == nil {
		t.Error("two active models of one question accepted")
	}
	id, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishLayer1Run(ctx, id, runstate.Succeeded, `{"x":1}`, ""); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.ListLayer1Runs(ctx, 5); len(runs) != 1 || runs[0].State != runstate.Succeeded || runs[0].Inputs != "inputs" {
		t.Fatalf("%+v", runs)
	}
}

// A layer-1 commit that writes route answers bumps the route generation
// (the derived route views' invalidation) and leaves the mailbox generation
// alone (the triage core built from the mail stays valid). A commit that
// writes no route answer, and a rolled-back commit, bump nothing.
func TestCommitLayer1BumpsRouteGeneration(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	gen, routeGen := s.Generation(), s.RouteGeneration()

	commit := func(routes bool, report error) error {
		t.Helper()
		run, err := s.BeginLayer1Run(ctx, "inputs")
		if err != nil {
			t.Fatal(err)
		}
		c := Layer1Commit{RunID: run, Key: key, RouteKey: key.Fingerprint, Report: func() (string, error) { return "{}", report },
			Decisions: decisionRows()}
		if routes {
			c.Routes = routeRows(RouteAnswer{MessageID: ids[0], Revision: 1, Answers: routeJSON})
		}
		return s.CommitLayer1(ctx, c)
	}

	if err := commit(false, nil); err != nil {
		t.Fatal(err)
	}
	if s.RouteGeneration() != routeGen {
		t.Fatalf("a decision-only commit bumped the route generation: %d → %d", routeGen, s.RouteGeneration())
	}
	if err := commit(true, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.RouteGeneration(); got != routeGen+1 {
		t.Fatalf("a route commit did not bump the route generation: %d → %d", routeGen, got)
	}
	if err := commit(true, errors.New("report failed")); err == nil {
		t.Fatal("a failing commit succeeded")
	}
	if got := s.RouteGeneration(); got != routeGen+1 {
		t.Fatalf("a rolled-back commit bumped the route generation: %d → %d", routeGen+1, got)
	}
	if got := s.Generation(); got != gen {
		t.Fatalf("a route commit bumped the mailbox generation: %d → %d", gen, got)
	}
}
