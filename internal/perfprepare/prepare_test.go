//go:build fixture

package perfprepare

// Bench support for scripts/bench-perf.sh: import the legacy fixture (or
// reuse an already imported PERF_STORE_DIR), write one synthetic route
// answer per member and question, and latch the selection as learned so
// the gated reads can be measured. Aggregates only; the answers are
// generated from the profile and carry no message content.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixtureconf"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// TestFixturePerfPrepare is run by scripts/bench-perf.sh before it starts
// the app. PERF_STORE_DIR is the data directory to fill; FIXTURE_JSONL is
// the private export.
func TestFixturePerfPrepare(t *testing.T) {
	dir := os.Getenv("PERF_STORE_DIR")
	if dir == "" {
		t.Skip("PERF_STORE_DIR not set (scripts/bench-perf.sh sets it)")
	}
	settings, err := fixtureconf.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
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
	defer st.Close()
	ids, err := st.MessageIDs(t.Context(), store.MessageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		ctx := t.Context()
		conn, err := archive.New(fixtureconf.SourceID, fixturetest.Path(t), cfg)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := syncer.New(st, cfg, nil, nil).Run(ctx, conn, nil)
		if err != nil || rep.Error != "" {
			t.Fatalf("fixture import: %v %s", err, rep.Error)
		}
	}
	profile, err := taxonomy.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The app reads route rows under layer1.RouteKey; a fixture that writes
	// the legacy question-spec fingerprint prepares an empty routed page and
	// the benchmark measures nothing. Compute the key from the same owner.
	routeKey, err := layer1.RouteKey(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var roles []mail.FolderRole
	for _, r := range cfg.Strings("triage.folder_roles") {
		roles = append(roles, mail.FolderRole(r))
	}
	refs, err := st.MessageRefs(t.Context(), store.MessageFilter{Roles: roles, OwnerOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.BeginLayer1Run(t.Context(), "perf")
	if err != nil {
		t.Fatal(err)
	}
	answer := func(row int64) map[string]route.Answer {
		out := make(map[string]route.Answer, len(profile.Questions))
		for i := range profile.Questions {
			q := &profile.Questions[i]
			a := engine.Answer{Type: q.Type}
			switch q.Type {
			case engine.Choice:
				choice := q.Options[0].Name
				confidence := 0.9
				a.Choice, a.Confidence = choice, &confidence
				a.Probabilities = map[string]float64{choice: 0.9}
			case engine.Noul:
				a.Value = 0.9
			case engine.Score:
				a.Value = float64(row % int64(q.Top()+1))
			}
			out[q.Name] = route.Answer{Answer: a}
		}
		return out
	}
	err = st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: profile.Fingerprint, Decider: "layer1"},
		RouteKey:  routeKey,
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes: func(fn func(store.RouteAnswer) error) error {
			for _, ref := range refs {
				body, err := json.Marshal(answer(ref.RowID))
				if err != nil {
					return err
				}
				if err := fn(store.RouteAnswer{MessageID: ref.RowID, Revision: ref.Revision, Answers: string(body)}); err != nil {
					return err
				}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLearnedScope(t.Context(), source.Scope(cfg.Snapshot())); err != nil {
		t.Fatal(err)
	}
	t.Logf("PERF prepared messages=%d routes=%d questions=%d route_key=%s", len(refs), len(refs), len(profile.Questions), routeKey)
}
