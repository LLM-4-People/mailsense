//go:build fixture

package layer1

// The layer-1 dev harness (scripts/layer1-fixture.sh run <experiment>):
// the legacy fixture imported into a fresh store; its real EmbeddingGemma
// vectors from the private cache (never embedded here); the evidence
// engine run over it; the legacy Ondelle answers imported as the "legacy"
// teacher EXCEPT for every gold dev and test message; the gold TRAIN
// labels stored as judge labels (the in-app judge's place); layer 1
// trained through the production pass (Train); then measured on the gold
// DEV split only, against the legacy Ondelle answers of the same messages
// and the agreement ceiling. The test split is sealed and never read.
// Writes aggregates only (out/layer1-<experiment>.json, a line of
// out/layer1-dev-ledger.jsonl); the per-item correctness of the run (for
// paired comparisons) and the per-item error list stay under the private
// cache directory (~/.cache/mailsense-layer1/runs).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixtureconf"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// harnessDecider is the key the dev items' legacy answers are stored
// under for the comparison (never a teacher source).
const harnessDecider = "legacy:harness-dev"

// legacyTeacher is the harness's teachers override: the legacy Ondelle
// answers of the fixture as a teacher (the shipped teachers know no such
// source: it exists in the developer's fixture only). An experiment's own
// override merges over it.
const legacyTeacher = `{"sources": {"legacy": {"from": "decisions", "decider": "legacy:ondelle-snap-preview", "provider": "ondelle", "precedence": 4, "weight": 1, "temperature": 1}}}`

func TestLayer1Fixture(t *testing.T) {
	t.Parallel()
	name := os.Getenv("LAYER1_EXPERIMENT")
	if name == "" {
		name = "default"
	}
	exp := experimentNamed(t, name)
	run := os.Getenv("LAYER1_RUN")
	if run == "" {
		run = name
	}
	gold := goldOf(t)
	ctx := harnessContext(t, "run")
	cacheDir := harnessEnv(t, "LAYER1_CACHE_DIR")
	reportDir := harnessEnv(t, "LAYER1_REPORT_DIR")
	overrides := providerOverrides(map[string]any{"sidecar.dir": harnessEnv(t, "EMBEDLOCAL_RUNTIME_DIR")})
	for k, v := range exp.settings {
		overrides[k] = v
	}
	start := time.Now()
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), overrides)
	teachers := []byte(legacyTeacher)
	if exp.teachers != "" {
		var err error
		if teachers, err = jsonx.Overlay(teachers, []byte(exp.teachers), "sources", "questions"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cfg.Paths().Resolve(cfg.String(SettingTeachersFile)), teachers, 0o600); err != nil {
		t.Fatal(err)
	}
	importWall := time.Since(start)
	rows := rowsByGoldID(t, st)
	owner := slices.Collect(func(yield func(int64) bool) {
		for _, r := range rows {
			if !yield(r) {
				return
			}
		}
	})
	slices.Sort(owner)

	// vectors: the cache only (scripts/layer1-fixture.sh embed fills it)
	sc, err := sidecar.New(sidecar.Options{Config: cfg, Jobs: jobs.NewManager(cfg, nil, nil), Probe: embed.Probe(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Stop()
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	deps := engine.Deps{Config: cfg, Secrets: sec, Locator: sc}
	cache, err := fixturetest.OpenVectorCache(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	docE := purposeNamed(t, "document").embedder(t, deps)
	docSet, docMissing := cachedVectors(t, cache, purposeNamed(t, "document"), docE, st, owner, true)
	t.Logf("LAYER1 vectors document: %d cached, %d not cached (pending)", docSet.Len(), docMissing)

	// layer 0 over the real vectors
	evStart := time.Now()
	ev, err := evidence.New(evidence.Deps{Config: cfg, Store: st, Space: func() (string, error) { return docE.Space(), nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	evWall := time.Since(evStart)

	// exclusions: every gold dev and test message (and, E6, their conversation-mates)
	base := &StoreInputs{Config: cfg, Store: st, Evidence: ev}
	members, err := base.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	exclude := map[int64]bool{}
	for _, split := range []string{goldset.Dev, goldset.Test} {
		for _, id := range gold.IDs(split) {
			exclude[rows[id]] = true
		}
	}
	held := len(exclude)
	if exp.mates {
		groups := map[string]bool{}
		for _, m := range members {
			if exclude[m.ID] {
				groups[m.Group] = true
			}
		}
		for _, m := range members {
			if groups[m.Group] {
				exclude[m.ID] = true
			}
		}
	}
	t.Logf("LAYER1 excluded from every target: %d gold dev/test messages, %d with their conversation-mates", held, len(exclude))
	excludedIDs := map[string]bool{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if exclude[r.RowID] {
			excludedIDs[r.Message.ID] = true
		}
		return nil
	})
	profile, err := taxonomy.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ondelleProv, _ := cat.Get("ondelle")
	legacyPath := harnessEnv(t, "LAYER1_LEGACY")
	imp, err := archive.ImportLegacyAnswers(t.Context(), st, archive.LegacyAnswers{Path: legacyPath, SourceID: fixtureconf.SourceID,
		Fingerprint: profile.Fingerprint, Decider: "legacy:ondelle-snap-preview", Questions: profile.Questions,
		Tolerance: ondelleProv.ProbabilityTolerance, Skip: func(id string) bool { return excludedIDs[id] }, MaxLine: 1 << 26})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LAYER1 legacy teacher: %d imported, %d skipped (excluded), %d without a message, %d of another fingerprint, %d bad lines",
		imp.Imported, imp.Skipped, imp.NoMessage, imp.OtherFingerprint, imp.BadLines)
	devIDs := map[string]bool{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if _, ok := gold.Of(goldset.Dev)[goldexport.ID(&r.Message)]; ok {
			devIDs[r.Message.ID] = true
		}
		return nil
	})
	if _, err := archive.ImportLegacyAnswers(t.Context(), st, archive.LegacyAnswers{Path: legacyPath, SourceID: fixtureconf.SourceID,
		Fingerprint: profile.Fingerprint, Decider: harnessDecider, Questions: profile.Questions, Tolerance: ondelleProv.ProbabilityTolerance,
		Skip: func(id string) bool { return !devIDs[id] }, MaxLine: 1 << 26}); err != nil {
		t.Fatal(err)
	}
	judge := putJudgeLabels(t, st, profile, gold, rows)
	t.Logf("LAYER1 judge labels (gold train): %d", judge)

	// the experiment's vectors and population
	set, space := docSet, docE.Space()
	if exp.purpose != "" && exp.purpose != "document" {
		p := purposeNamed(t, exp.purpose)
		e := p.embedder(t, deps)
		var missing int
		set, missing = cachedVectors(t, cache, p, e, st, owner, false)
		space = e.Space()
		t.Logf("LAYER1 vectors %s: %d cached, %d not cached", p.name, set.Len(), missing)
	}
	var only map[int64]bool
	switch os.Getenv("LAYER1_POPULATION") {
	case "sample":
		exp.sample = true
	case "all":
		exp.sample = false
	case "":
	default:
		t.Fatalf("LAYER1_POPULATION is sample or all, not %q", os.Getenv("LAYER1_POPULATION"))
	}
	if exp.sample {
		n := 8000
		only = map[int64]bool{}
		for _, id := range experimentSample(t, st, gold, n) {
			only[id] = true
		}
	}
	in := &harnessInputs{StoreInputs: base, exclude: exclude, only: only, space: space, set: set}
	plan, err := NewPlan(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if exp.plan != nil {
		exp.plan(plan)
	}
	plan.Progress = func(done, total int, r QuestionReport) {
		t.Logf("LAYER1 trained %d/%d %s: train %d eval %d λ %g iterations %d converged %v fits %d evaluations %d %s in %s", done, total, r.Question,
			r.Train, r.Eval, r.Lambda, r.Iterations, r.Converged, r.Fits, r.Evaluations, r.Code, r.Wall.Round(time.Millisecond))
	}
	plan.BeforeFit = guard(t, exclude) // fails before any fit
	runtime.GC()
	before := memoryNow(0) // the fixture store, evidence and harness data before layer 1
	tr, err := Train(ctx, plan, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	mem := memoryNow(tr.Design.Len())
	runtime.GC()
	mem["heap_inuse_before_bytes"] = before["heap_inuse_bytes"]
	mem["heap_inuse_trained_after_gc_bytes"] = memoryNow(0)["heap_inuse_bytes"] // the pass (design, targets, models) still held
	t.Logf("LAYER1 memory: heap in use before training %d MB, after %d MB (%d MB after a GC, the pass still held), peak RSS %d MB (%d rows)",
		mem["heap_inuse_before_bytes"]>>20, mem["heap_inuse_bytes"]>>20, mem["heap_inuse_trained_after_gc_bytes"]>>20, mem["peak_rss_bytes"]>>20, tr.Design.Len())
	t.Logf("LAYER1 trained %d questions in %s (design %d rows × %d+%d columns, %d without a vector)", len(tr.Models),
		tr.Wall.Round(time.Millisecond), tr.Design.Len(), tr.Design.Dims, tr.Design.Cells, tr.NoVector)

	recs, err := ev.Records(t.Context(), devRows(gold, rows))
	if err != nil {
		t.Fatal(err)
	}
	dev := devItems(t, gold, rows, recs, tr.Members)
	ondelle := legacyOf(t, st, store.TriageKey{Fingerprint: profile.Fingerprint, Decider: harnessDecider})
	plan.Tolerance["legacy"] = ondelleProv.ProbabilityTolerance
	trainLabels := map[int64]map[string]json.RawMessage{}
	for id, a := range gold.Of(goldset.Train) {
		if !selective.InCertifyPart(tr.Members[rows[id]].Group, plan.Config.CertifyFraction) {
			trainLabels[rows[id]] = map[string]json.RawMessage(a)
		}
	}
	rinfo, fires, err := base.Rules(ctx, trainLabels) // measured on the selection part's labels, as in the pass
	if err != nil {
		t.Fatal(err)
	}
	qs := evaluateDev(t, plan, tr, dev, ondelle, readCeilings(t, gold.Dir, profile), &gathered{rules: rinfo, fires: fires})
	kinds := agreementKinds(t, gold)
	report99 := plan.Requirements.Report
	errorsAt := map[string][]ErrorItem{}
	for i := range qs {
		q, _ := profile.Question(qs[i].Question)
		req := plan.Requirements.For(q.Name)
		var labels map[int64]evalLabel
		if set := tr.sets[q.Name]; set != nil {
			labels = set.labels()
		}
		var err error
		qs[i].Select, err = selectiveOf(q, &qs[i], tr.Models[q.Name], labels, plan.Profile.Cutoff(q.Name), req, report99, plan.Config.CertifyFraction, kinds[q.Name])
		if err != nil {
			t.Fatal(err)
		}
		errorsAt[q.Name] = qs[i].Select.errors
		if k := slices.Index(report99, req.TargetAccuracy); k >= 0 {
			// the gate judges the route (the layer-1 verdict is diagnostic)
			qs[i].Meets = qs[i].N >= req.MinEvalLabels && qs[i].Select.Certified["stack"][k].Coverage >= req.MinCoverage
		}
	}
	sel := stackSummary(qs, report99)
	at := func(pts []selective.TargetPoint, t float64) selective.TargetPoint {
		for _, p := range pts {
			if p.Target == t {
				return p
			}
		}
		return selective.TargetPoint{}
	}
	k99 := slices.Index(report99, 0.99)
	for _, q := range qs {
		s := q.Select
		op := s.StackAt[k99]
		g, cg := s.Given[k99], s.CertifiedGiven[k99]
		t.Logf("LAYER1 select %-17s dev-certified@0.99 layer1 %.2f ondelle %.2f stack %.2f | dev curve layer1 %.2f ondelle %.2f stack %.2f | point op 0.99: %d answered, %d wrong; certified op: %d answered, %d wrong; given key %d layer1 %d ondelle %d (app %d/%d/%d); %d keys",
			q.Question, s.Certified["layer1"][k99].Coverage, s.Certified["ondelle"][k99].Coverage, s.Certified["stack"][k99].Coverage,
			at(s.Layer1, 0.99).Coverage, at(s.Ondelle, 0.99).Coverage, at(s.Stack, 0.99).Coverage, op.Covered, op.Errors, op.CoveredCertified, op.ErrorsCertified,
			g[route.LayerKey], g[route.Layer1], g[route.LayerOndelle], cg[route.LayerKey], cg[route.Layer1], cg[route.LayerOndelle], s.Keys)
	}
	for _, m := range sel {
		t.Logf("LAYER1 select mean@%.2f: dev-certified coverage layer1 %.3f ondelle %.3f stack %.3f (questions certified %v/%v/%v); dev curve %.3f / %.3f / %.3f; stack point op %.3f (%v answers, %v wrong), certified op %.3f (%v answers, %v wrong); given key %.2f layer1 %.2f ondelle %.2f (app %.2f/%.2f/%.2f)",
			m["target"], m["layer1_certified_coverage"], m["ondelle_certified_coverage"], m["stack_certified_coverage"], m["layer1_certified_questions"],
			m["ondelle_certified_questions"], m["stack_certified_questions"], m["layer1_coverage"], m["ondelle_coverage"], m["stack_coverage"],
			m["stack_point_coverage"], m["stack_point_answers"], m["stack_point_errors"], m["stack_certified_op_coverage"], m["stack_certified_op_answers"], m["stack_certified_op_errors"],
			m["given_key"], m["given_layer1"], m["given_ondelle"], m["certified_given_key"], m["certified_given_layer1"], m["certified_given_ondelle"])
	}
	for _, q := range qs {
		t.Logf("LAYER1 dev %-17s n=%3d layer1 %.3f ondelle %.3f major %.3f ceiling %.2f meets %-5v λ=%g eval=%d (select %d, certify %d, unscored %d) wall=%s %s",
			q.Question, q.N, q.Layer1.Accuracy, q.Ondelle.Accuracy, q.Majority, q.Ceiling, q.Meets, q.Train.Lambda, q.Train.Eval, q.Train.Select,
			q.Train.Certify, q.Train.Unscored, q.Train.Wall.Round(time.Millisecond), q.Train.Code)
	}
	l1, sq := meanAcc(qs, false), meanAcc(qs, true)
	t.Logf("LAYER1 dev mean accuracy over %d questions: layer1 %.4f ondelle %.4f", len(qs), l1, sq)

	// private per-item results; the paired comparison with a baseline run
	runs := filepath.Join(filepath.Dir(cacheDir), "runs")
	if err := fsx.EnsureDir(runs); err != nil {
		t.Fatal(err)
	}
	res := resultsOf(qs)
	writeJSON(t, filepath.Join(runs, run+".json"), res)
	writeJSON(t, filepath.Join(runs, run+"-errors.json"), errorsAt) // per item: private, never out/
	var compare map[string]any
	if b := os.Getenv("LAYER1_BASELINE"); b != "" {
		raw, err := os.ReadFile(filepath.Join(runs, b+".json"))
		if err != nil {
			t.Fatalf("baseline %s: %v", b, err)
		}
		var base itemResults
		if err := json.Unmarshal(raw, &base); err != nil {
			t.Fatal(err)
		}
		d, lo, hi := pairedBootstrap(base, res, 2000, accuracyOf)
		c, clo, chi := pairedBootstrap(base, res, 2000, coverageAt(0.99))
		compare = map[string]any{"baseline": b, "mean_diff": d, "ci95": []float64{lo, hi},
			"coverage_0_99_diff": c, "coverage_0_99_ci95": []float64{clo, chi}}
		t.Logf("LAYER1 vs %s: mean dev accuracy %+.4f (95%% paired bootstrap [%+.4f, %+.4f]); stack coverage at 0.99 %+.4f [%+.4f, %+.4f]",
			b, d, lo, hi, c, clo, chi)
	}
	snap := cfg.Snapshot()
	settings := map[string]any{}
	for _, k := range settingKeys {
		settings[k], _ = snap.Value(k)
	}
	report := map[string]any{"experiment": name, "run": run, "population": map[bool]string{true: "sample", false: "all"}[exp.sample], "at": time.Now().UTC().Format(time.RFC3339), "settings": settings,
		"teachers_override": exp.teachers != "", "purpose": exp.purpose, "sample": exp.sample, "mates": exp.mates,
		"space": space, "excluded": len(exclude), "legacy_import": imp, "judge_labels": judge,
		"wall_ms":       map[string]int64{"import": importWall.Milliseconds(), "evidence": evWall.Milliseconds(), "train": tr.Wall.Milliseconds()},
		"design":        map[string]int{"rows": tr.Design.Len(), "dims": tr.Design.Dims, "cells": tr.Design.Cells, "no_vector": tr.NoVector},
		"mean_accuracy": map[string]float64{"layer1": l1, "ondelle": sq}, "compare": compare, "questions": qs, "selective": sel,
		"model_bytes": modelBytes(tr), "memory": mem}
	writeJSON(t, filepath.Join(reportDir, "layer1-"+run+".json"), report)
	ledger := map[string]any{"at": report["at"], "experiment": name, "run": run, "population": report["population"], "settings_digest": settingsDigest(settings, exp.teachers),
		"mean_layer1": l1, "mean_ondelle": sq, "dev_items": len(dev), "compare": compare}
	per := map[string]float64{}
	for _, q := range qs {
		per[q.Question] = q.Layer1.Accuracy
	}
	ledger["accuracy"] = per
	ledger["selective"] = sel
	appendLedger(t, filepath.Join(reportDir, "layer1-dev-ledger.jsonl"), ledger)
}

// putJudgeLabels stores the gold TRAIN labels as judge labels.
func putJudgeLabels(t *testing.T, st *store.Store, p *taxonomy.Profile, gold *goldset.Set, rows map[string]int64) int {
	t.Helper()
	n := 0
	for id, answers := range gold.Of(goldset.Train) {
		for q, raw := range answers {
			question, ok := p.Question(q)
			if !ok {
				t.Fatalf("gold question %q is not in the profile", q)
			}
			v, err := jsonx.DecodeOne(raw)
			if err != nil {
				t.Fatal(err)
			}
			l, err := taxonomy.ParseLabel(question, v)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.PutLabel(t.Context(), rows[id], q, l.JSON(question.Type), "judge"); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	return n
}

func devRows(gold *goldset.Set, rows map[string]int64) []int64 {
	var out []int64
	for _, id := range gold.IDs(goldset.Dev) {
		out = append(out, rows[id])
	}
	return out
}

// memoryNow is the process's heap in use and peak resident size (Linux
// VmHWM; 0 elsewhere), with the pass's rows (the extrapolation base).
func memoryNow(rows int) map[string]int64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out := map[string]int64{"heap_inuse_bytes": int64(ms.HeapInuse), "rows": int64(rows)}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(l); len(f) == 3 && f[0] == "VmHWM:" {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				out["peak_rss_bytes"] = kb << 10
			}
		}
	}
	return out
}

func modelBytes(tr *Trained) int {
	n := 0
	for _, m := range tr.Models {
		n += len(m.Encode())
	}
	return n
}

func settingsDigest(settings map[string]any, teachers string) string {
	b, _ := json.Marshal(settings)
	return digest.Hex(append(b, teachers...), 12)
}

func appendLedger(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f, err := fsx.OpenAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}
