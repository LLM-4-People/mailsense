//go:build fixture

package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestFixtureEvidence runs the evidence engine over the whole fixture
// (scripts/evidence-fixture.sh): coverage and distributions per signal,
// runtime (first and unchanged second run), the database growth, the
// stage; then, when EVIDENCE_GOLD_DIR names a gold sample, the rule table
// against its TRAIN and DEV labels only (the test split is never read).
// Reports are aggregates only (no text, no address): EVIDENCE_REPORT_DIR.
func TestFixtureEvidence(t *testing.T) {
	importStart := time.Now()
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), map[string]any{"embed.batch_size": 32, "http.retry.max_attempts": 1})
	importWall := time.Since(importStart) // the same process and machine load as the evidence runs below
	ix, space := fakeIndex(t, cfg, st)
	if rep, err := ix.Run(t.Context(), index.Options{}, nil); err != nil || rep.After.Indexed != rep.After.Eligible {
		t.Fatalf("index: %v %+v", err, rep.After)
	}
	size := func() int64 {
		if err := st.Checkpoint(t.Context()); err != nil {
			t.Fatal(err)
		}
		n, _ := st.SizeBytes(t.Context())
		return n
	}
	before := size()
	ev, err := New(Deps{Config: cfg, Store: st, Space: space})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	first, err := ev.Run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	firstWall := time.Since(start)
	start = time.Now()
	second, err := ev.Run(t.Context(), nil)
	if err != nil || second.Written != 0 {
		t.Fatalf("an unchanged mailbox writes nothing: %v %+v", err, second)
	}
	secondWall := time.Since(start)
	after := size()
	status, err := ev.Stage().Status(t.Context())
	if err != nil || status.State != learning.Done || status.Progress.Done != int64(first.Messages) {
		t.Fatalf("FIXTURE the evidence stage is complete: %v %+v", err, status)
	}
	cov, err := ev.Coverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE import wall=%s (same process)", importWall.Round(time.Millisecond))
	t.Logf("FIXTURE evidence messages=%d signals=%d first=%s (context %s, values %s) second=%s db_before=%d db_after=%d delta=%d",
		first.Messages, first.Signals, firstWall.Round(time.Millisecond), first.ContextTime.Round(time.Millisecond),
		first.ValuesTime.Round(time.Millisecond), secondWall.Round(time.Millisecond), before, after, after-before)
	writeReport(t, cfg, "evidence-fixture.json", map[string]any{"messages": first.Messages, "signals": first.Signals,
		"import_wall_ms": importWall.Milliseconds(),
		"first_run": map[string]any{"wall_ms": firstWall.Milliseconds(), "context_ms": first.ContextTime.Milliseconds(),
			"values_ms": first.ValuesTime.Milliseconds(), "messages_per_second": first.PerSecond, "written": first.Written},
		"second_run":      map[string]any{"wall_ms": secondWall.Milliseconds(), "computed": second.Computed, "written": second.Written},
		"db_bytes_before": before, "db_bytes_after": after, "db_bytes_delta": after - before,
		"stage": status.State, "vector_space": first.Space != "", "coverage": cov})
	goldRules(t, ev, st)
}

// TestFixtureLiveGold writes the gold labels for the live measurement
// (the live evidence harness).
func TestFixtureLiveGold(t *testing.T) {
	if os.Getenv("EVIDENCE_LIVE_GOLD") == "" || os.Getenv("EVIDENCE_GOLD_DIR") == "" {
		t.Skip("EVIDENCE_LIVE_GOLD and EVIDENCE_GOLD_DIR name the output and the gold sample")
	}
	_, st := fixturetest.Import(t, fixturetest.Path(t), nil)
	liveGold(t, st)
}

// liveGold writes, when EVIDENCE_LIVE_GOLD names a file, the train and
// dev labels keyed by messageKey (the live evidence harness uploads it to
// measure the rules on the live mailbox, where the fixture's ids do not
// exist): labels only, no text, no address.
func liveGold(t *testing.T, st *store.Store) {
	path := os.Getenv("EVIDENCE_LIVE_GOLD")
	if path == "" {
		return
	}
	labels, _ := goldLabels(t, os.Getenv("EVIDENCE_GOLD_DIR"))
	out := map[string]map[string]json.RawMessage{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if answers, ok := labels[goldexport.ID(&r.Message)]; ok {
			if k := messageKey(r.Message.InternetMessageID); k != "" {
				out[k] = answers
			}
		}
		return nil
	})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFileAtomic(path, b, fsx.FilePerm, fsx.Replace{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE live gold: %d of %d labelled messages keyed by their Message-ID", len(out), len(labels))
}

// fakeIndex indexes with the lexical fake embedder (no model on the dev
// box): template_matches then compares lexical vectors.
func fakeIndex(t *testing.T, cfg *config.Config, st *store.Store) (*index.Indexer, func() (string, error)) {
	fake := embedtest.New(t, &embedtest.Fake{})
	embedtest.Install(t, cfg, fake.URL)
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: cfg, Secrets: sec, Locator: embedtest.Locator{URL: fake.URL}})
	return index.New(cfg, st, nil, src, nil), func() (string, error) {
		e, err := src.Embedder()
		if err != nil {
			return "", err
		}
		return e.Space(), nil
	}
}

// goldRules measures the rules on the gold sample's train and dev labels,
// and again on the dev split alone: train+dev is the table the rule
// document is built from, dev is the held-out check a rule has to pass
// before its answer is trusted. Test stays sealed.
func goldRules(t *testing.T, ev *Engine, st *store.Store) {
	dir := os.Getenv("EVIDENCE_GOLD_DIR")
	if dir == "" {
		t.Log("FIXTURE EVIDENCE_GOLD_DIR not set: no rule table")
		return
	}
	set, err := goldset.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	labels, splits := set.Labels, set.Used()
	rows := map[string]int64{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		rows[goldexport.ID(&r.Message)] = r.RowID
		return nil
	})
	byRow := map[int64]map[string]json.RawMessage{}
	devByRow := map[int64]map[string]json.RawMessage{}
	var ids, devIDs []int64
	for id, answers := range labels {
		row, ok := rows[id]
		if !ok {
			t.Fatalf("gold id %s names no message of the fixture", id)
		}
		byRow[row] = answers
		ids = append(ids, row)
		if set.Splits[id] == goldset.Dev {
			devByRow[row] = answers
			devIDs = append(devIDs, row)
		}
	}
	recs, err := ev.Records(t.Context(), ids)
	if err != nil {
		t.Fatal(err)
	}
	snap := ev.cfg.Snapshot()
	z, minPrecision := snap.Float("evidence.rules.z"), snap.Float("evidence.rules.min_precision")
	table := ev.rules.Measure(byRow, recs, z, minPrecision)
	dev := ev.rules.Measure(devByRow, recs, z, minPrecision)
	keys, devKeys := 0, 0
	for _, r := range table {
		if r.Key {
			keys++
		}
	}
	for i := range dev {
		r := &dev[i]
		if r.Key {
			devKeys++
		}
		// The held-out line is the one a shipped rule must survive: fired,
		// correct and the Wilson lower bound, on dev only.
		t.Logf("FIXTURE dev rule %-30s %-14s fired %3d/%3d correct %3d precision %.3f wilson [%.3f, %.3f] key %v",
			r.ID, r.Question, r.Fired, r.Labelled, r.Correct, r.Precision, r.WilsonLow, r.WilsonHi, r.Key)
	}
	writeReport(t, ev.cfg, "evidence-rules.json", map[string]any{"splits_used": splits, "messages": len(labels),
		"dev_messages": len(devIDs), "z": z, "min_precision": minPrecision,
		"keys": keys, "dev_keys": devKeys, "rules": table, "dev_rules": dev})
}

func writeReport(t *testing.T, cfg *config.Config, name string, v any) {
	dir := os.Getenv("EVIDENCE_REPORT_DIR")
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fsx.WriteFileAtomic(filepath.Join(dir, name), append(b, '\n'), fsx.FilePerm, cfg.Snapshot().Replace(nil)); err != nil {
		t.Fatal(err)
	}
	t.Logf("FIXTURE report %s", filepath.Join(dir, name))
}
