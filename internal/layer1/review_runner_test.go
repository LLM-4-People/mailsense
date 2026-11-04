package layer1

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Regressions of the layer-1 review round (review round L1), at the runner's
// level.

func reportOf(rep Report, q string) (QuestionReport, Verdict) {
	var qr QuestionReport
	var v Verdict
	for _, r := range rep.Questions {
		if r.Question == q {
			qr = r
		}
	}
	for _, x := range rep.Verdicts {
		if x.Question == q {
			v = x
		}
	}
	return qr, v
}

// writeProfile adds an option to the category question (a profile change).
func writeProfile(t *testing.T, w *world) {
	t.Helper()
	var doc struct {
		Questions map[string]map[string]any `json:"questions"`
	}
	raw, _ := os.ReadFile("../triage/taxonomy/default_profile.json")
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	cat := doc.Questions["category"]
	cat["criteria"].(map[string]any)["travel"] = "Bookings and itineraries."
	over, _ := json.Marshal(map[string]any{"questions": map[string]any{"category": cat}})
	if err := os.WriteFile(w.cfg.Paths().Resolve(w.cfg.String(taxonomy.ProfileFileSetting)), over, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Finding (bugs review #2): an option added to a question while the old
// 7-class model was kept panicked in answer() (index out of range) and
// left the run "running" forever. A model answers only the spec it was
// trained on.
func TestAProfileChangeNeverUsesAModelOfAnotherShape(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, map[string]any{SettingMaxNLLRegression: 0})
	w.run()
	// the active model looks far better than any candidate: the old code kept it
	active0, _ := w.st.ActiveLayer1(t.Context())
	a0 := active0[0]
	var m Metrics
	_ = json.Unmarshal([]byte(a0.Metrics), &m)
	m.NLL = 0
	raw0, _ := json.Marshal(m)
	a0.VersionKey, a0.Metrics = "better", string(raw0)
	id0, err := w.st.SaveLayer1Candidate(t.Context(), a0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.PromoteLayer1(t.Context(), id0); err != nil {
		t.Fatal(err)
	}
	writeProfile(t, w)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := p.Question("category")
	if len(cq.Options) != 8 {
		t.Fatalf("the override did not add an option: %d", len(cq.Options))
	}
	// a model of the old spec is never usable
	active, _ := w.r.active(t.Context())
	if a := active["category"]; a == nil || a.model.Spec == SpecOf(cq) {
		t.Fatal("the stored model has no spec, or the spec did not change")
	}
	rep, err := w.r.Run(t.Context(), nil)
	if err != nil || rep.State != runstate.Succeeded {
		t.Fatalf("%+v %v", rep, err)
	}
	active, _ = w.r.active(t.Context())
	if a := active["category"]; a == nil || a.model.Classes != 8 || a.meta.Spec != SpecOf(cq) {
		t.Fatalf("the active model after the change: %+v", a)
	}
}

// Finding (bugs review #3): per-question teacher and requirement
// overrides were not in the inputs fingerprint (json of an unexported
// map), so a run removing a leaky teacher for one question kept the old
// model answering under the same version key.
func TestPerQuestionOverridesAreInputs(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	first := w.run()
	if err := writeTeachers(w.cfg, `{"questions": {"category": {"decider": null}}}`); err != nil {
		t.Fatal(err)
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != stale {
		t.Fatal("a per-question teacher override left the run current")
	}
	rep := w.run()
	qr, _ := reportOf(rep, "category")
	if qr.Train != 20 || !slices.Contains(rep.Promoted, "category") || rep.Inputs == first.Inputs {
		t.Fatalf("the override did not retrain: train %d promoted %v", qr.Train, rep.Promoted)
	}
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 6, "family_wise": false}, "questions": {"category": {"target_accuracy": 0.9}}}`); err != nil {
		t.Fatal(err)
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != stale {
		t.Fatal("a per-question requirement override left the run current")
	}
}

// Findings (bugs review #4, rules D): the verdicts came from the
// candidate even when the active model was kept, and a question now
// untrainable kept its old model answering.
func TestVerdictsComeFromTheModelThatAnswers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, map[string]any{SettingMaxNLLRegression: 0})
	permissive := `{"defaults": {"min_effective_train": 10, "min_eval_labels": 6, "target_accuracy": 0.6, "min_coverage": 0.1, "z": 1.0, "family_wise": false}}`
	if err := writeRequirements(w.cfg, permissive); err != nil {
		t.Fatal(err)
	}
	if _, v := reportOf(w.run(), "category"); !v.Meets {
		t.Fatalf("the permissive requirement is not met: %+v", v)
	}
	// the active model "looks" far better (NLL 0) but has no holdout: it
	// would abstain on everything
	active, _ := w.st.ActiveLayer1(t.Context())
	a := active[0]
	var m Metrics
	_ = json.Unmarshal([]byte(a.Metrics), &m)
	m.NLL = 0
	var meta modelMeta
	_ = json.Unmarshal([]byte(a.Meta), &meta)
	meta.Holdout, meta.Threshold = nil, selective.NoThreshold
	mraw, _ := json.Marshal(m)
	metaRaw, _ := json.Marshal(meta)
	kept := a
	kept.VersionKey, kept.Metrics, kept.Meta = "kept", string(mraw), string(metaRaw)
	id, err := w.st.SaveLayer1Candidate(t.Context(), kept)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.PromoteLayer1(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	rep := w.run()
	_, v := reportOf(rep, "category")
	if rep.Kept["category"] == "" || v.Meets || v.Certification.Certified {
		t.Fatalf("kept %v, verdict %+v: the verdict is the candidate's", rep.Kept, v)
	}
	for _, ans := range w.answers() {
		if ans["category"].Abstain == "" {
			t.Fatal("the kept model answered on its own")
		}
	}
	// no teacher any more: the old model retires and stops answering
	if err := writeTeachers(w.cfg, `{"questions": {"category": {"decider": null, "judge": null}}}`); err != nil {
		t.Fatal(err)
	}
	rep = w.run()
	if !slices.Contains(rep.Retired, "category") {
		t.Fatalf("retired %v", rep.Retired)
	}
	if act, _ := w.st.ActiveLayer1(t.Context()); len(act) != 0 {
		t.Fatalf("an untrainable question kept an active model: %+v", act)
	}
	if len(w.answers()) != 0 {
		t.Fatal("the retired model still answers")
	}
}

// Finding (bugs review #5): a run failing after promotion had already
// replaced the active model while the answers stayed the old model's;
// promotion and answers are one transaction now.
func TestAFailedAnswerWritePromotesNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 40, nil)
	w.run()
	before, _ := w.st.ActiveLayer1(t.Context())
	answers := w.answers()
	db, err := sql.Open("sqlite", w.cfg.Paths().Resolve(w.cfg.String("store.file")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_layer1 BEFORE INSERT ON triage_decisions WHEN NEW.decider = 'layer1' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	rep, err := w.r.Run(t.Context(), nil)
	if err == nil || rep.State != runstate.Failed {
		t.Fatalf("%+v %v", rep, err)
	}
	after, _ := w.st.ActiveLayer1(t.Context())
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("active model %d → %d", before[0].ID, after[0].ID)
	}
	if got := w.answers(); len(got) != len(answers) {
		t.Fatal("the answers changed")
	}
	if _, err := db.Exec(`DROP TRIGGER fail_layer1`); err != nil {
		t.Fatal(err)
	}
	// a cancel once every question is trained (before the commit) promotes nothing either
	ctx, cancel := context.WithCancel(t.Context())
	rep, err = w.r.Run(ctx, func(p jobs.Progress) {
		if p.Done == p.Total {
			cancel()
		}
	})
	if err == nil || rep.State != runstate.Cancelled {
		t.Fatalf("%+v %v", rep, err)
	}
	if after, _ := w.st.ActiveLayer1(t.Context()); after[0].ID != before[0].ID {
		t.Fatal("a cancelled run promoted")
	}
}

// Findings (bugs review #7, rules #4): a teachers or requirements file
// that breaks after start failed before the run was recorded: no blocker,
// "running" forever, and at startup it aborted the app. Now it is a
// recorded failed run with a blocker naming the remedy, recorded once per
// distinct problem.
func TestABrokenConfigurationIsABlocker(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	w.run()
	if err := writeTeachers(w.cfg, `{"sources": {"judge": {"weight": -1}}}`); err != nil {
		t.Fatal(err)
	}
	rep, err := w.r.Run(t.Context(), nil)
	if err == nil || rep.Code != BlockConfig || rep.Broken != SettingTeachersFile || rep.RunID == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	st, err := w.r.Stage().Status(t.Context())
	if err != nil || st.State != learning.Blocked || len(st.Blockers) != 1 || st.Blockers[0].Code != BlockConfig ||
		st.Blockers[0].Actions[0].ID != learning.ActionChooseTeacher || !strings.Contains(st.Blockers[0].Detail, "weight") {
		t.Fatalf("%+v %v", st, err)
	}
	if err := writeRequirements(w.cfg, `{"defaults": {"z": -1}}`); err != nil {
		t.Fatal(err)
	}
	if err := writeTeachers(w.cfg, `{}`); err != nil {
		t.Fatal(err)
	}
	st, _ = w.r.Stage().Status(t.Context())
	if st.State != learning.Blocked || st.Blockers[0].Actions[0].ID != learning.ActionReviewRequirements {
		t.Fatalf("requirements: %+v", st)
	}
}

// Findings (rules review #1, #13): the gate asked for 381 labels (it
// needs ⌈381 / min_coverage⌉ in the certification part, a fraction of
// the labels), the actions said the total instead of what remains, the
// detail had no "need", and the paid action no cost.
func TestBlockersSayWhatRemainsInPlainLanguage(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	rep := w.run()
	_, v := reportOf(rep, "category")
	if v.Requested != 1524 || v.Remaining != 1524-20 || v.CertifiedOn[0] != "manual" {
		t.Fatalf("verdict %+v", v)
	}
	st, _ := w.r.Stage().Status(t.Context())
	for _, b := range st.Blockers {
		if strings.Contains(b.Detail, "layer1_") {
			t.Errorf("a code in the user text: %q", b.Detail)
		}
		for _, a := range b.Actions {
			if a.ID == learning.ActionRunJudge && (a.Params["estimated_cost_usd"] == "" || a.Params["cost_note"] == "") {
				t.Errorf("a paid action without its cost: %+v", a)
			}
		}
		if b.Subject == "category" {
			if b.Measure == nil || b.Measure.Need != 200 || b.Measure.Have != v.Effective {
				t.Fatalf("category blocker %+v", b)
			}
		}
	}
	w.r.judgeCost = func(n int) (float64, bool) { return float64(n) * 0.001, true }
	a := w.r.judgeAction("category", v.Remaining)
	if a.Params["count"] != "1504" || a.Params["estimated_cost_usd"] != "1.50" || a.Params["cost_note"] != "" {
		t.Fatalf("%+v", a)
	}
	qs, _ := w.r.QuestionsStage().Status(t.Context())
	for _, b := range qs.Blockers {
		if b.Subject == "category" && !strings.Contains(b.Detail, "certification labels") && !strings.Contains(b.Detail, "certified") {
			t.Errorf("the verdict does not say what it certifies: %q", b.Detail)
		}
	}
}

// Finding (rules review #2): fewer than min_new_labels corrections kept a
// certified question "meets"; any evaluation-label change re-scores the
// verdict at once from the stored holdout predictions, and a correction
// that reveals errors closes the gate.
func TestALabelCorrectionReScoresTheVerdict(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissive := `{"defaults": {"min_effective_train": 10, "min_eval_labels": 6, "target_accuracy": 0.6, "min_coverage": 0.1, "z": 1.0, "family_wise": false}}`
	if err := writeRequirements(w.cfg, permissive); err != nil {
		t.Fatal(err)
	}
	_, v := reportOf(w.run(), "category")
	if !v.Meets {
		t.Fatalf("%+v", v)
	}
	// relabel every certification label of category to its wrong class
	active, _ := w.r.active(t.Context())
	h := active["category"].meta.Holdout
	if len(h.Certify) < 6 {
		t.Fatalf("certification part %d", len(h.Certify))
	}
	for _, it := range h.Certify[:6] {
		for i, id := range w.ids {
			if id == it.ID {
				wrong := []string{"work", "finance"}[w.kind(i)]
				if err := w.st.PutLabel(t.Context(), id, "category", `"`+wrong+`"`, "judge"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != relabelled {
		t.Fatalf("corrections left the verdicts current: %+v", lr)
	}
	rep, err := w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, v := reportOf(rep, "category"); v.Meets || v.Certification.Certified {
		t.Fatalf("after the corrections: %+v", v)
	}
	for _, a := range w.answers() {
		if a["category"].Abstain == "" {
			t.Fatal("an answer of a de-certified model is still given on its own")
		}
	}
}
