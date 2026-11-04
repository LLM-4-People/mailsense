package layer1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// labelValue is a valid stored value of a question for variant n.
func labelValue(t *testing.T, q *engine.Question, n int) string {
	t.Helper()
	var raw any
	switch q.Type {
	case engine.Choice:
		raw = q.Options[n%len(q.Options)].Name
	case engine.Noul:
		raw = n%2 == 1
	default:
		raw = float64(n % len(q.Levels))
	}
	label, err := taxonomy.ParseLabel(q, raw)
	if err != nil {
		t.Fatal(err)
	}
	return label.JSON(q.Type)
}

// labelAll stores a valid label of every profile question on one message.
func labelAll(t *testing.T, w *world, id int64, source string, variant int) {
	t.Helper()
	for i := range w.p.Questions {
		q := &w.p.Questions[i]
		if err := w.st.PutLabel(t.Context(), id, q.Name, labelValue(t, q, variant), source); err != nil {
			t.Fatal(err)
		}
	}
}

// labelRequirements asks for exactly request labels of certify sources.
func labelRequirements(t *testing.T, w *world, request int, certify []string) {
	t.Helper()
	raw, err := json.Marshal(certify)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRequirements(w.cfg, fmt.Sprintf(`{"defaults": {"min_effective_train": 10, "min_eval_labels": 2,
		"target_accuracy": 0.8, "min_coverage": 0.5, "z": 1.2, "family_wise": false, "request_labels": %d, "certify_sources": %s}}`, request, raw)); err != nil {
		t.Fatal(err)
	}
}

// blockerOf is one question's blocker in a stage status (ok false: none).
func blockerOf(st learning.StageStatus, question string) (learning.Blocker, bool) {
	for _, b := range st.Blockers {
		if b.Subject == question {
			return b, true
		}
	}
	return learning.Blocker{}, false
}

// The stage asks for the requirement's Requested labels (1,524 with the
// defaults), counts the certification sources only, and every short
// question carries the remaining count on its manual and paid actions.
func TestLabelsStageCountsAndBlocks(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != learning.Blocked || st.Progress.Unit != "evaluation labels" || st.Progress.Total != int64(17*1524) || st.Progress.Done != 10 {
		t.Fatalf("status %+v", st)
	}
	b, ok := blockerOf(st, "category")
	if !ok || b.Code != BlockTooFewEval || b.Measure == nil || b.Measure.Unit != "evaluation labels" ||
		b.Measure.Have != 10 || b.Measure.Need != 1524 || strings.Contains(b.Detail, "layer1_") {
		t.Fatalf("category %+v", b)
	}
	if len(b.Actions) != 3 || b.Actions[0].ID != learning.ActionLabelMessages || b.Actions[0].Params["question"] != "category" ||
		b.Actions[0].Params["count"] != "1514" {
		t.Fatalf("label action %+v", b.Actions)
	}
	if b.Actions[1].ID != learning.ActionRunJudge || b.Actions[1].Params["count"] != "1514" ||
		b.Actions[1].Params["estimated_cost_usd"] != "unknown" || b.Actions[1].Params["cost_note"] == "" {
		t.Fatalf("judge action %+v", b.Actions[1])
	}
	if b.Actions[2].ID != learning.ActionReviewRequirements || b.Actions[2].Params["question"] != "category" {
		t.Fatalf("requirements action %+v", b.Actions[2])
	}
	// The decider answered every message; its targets are not labels.
	if u, ok := blockerOf(st, "urgency"); !ok || u.Measure == nil || u.Measure.Have != 0 {
		t.Fatalf("a decider answer counted as a label: %+v", u)
	}
}

// A message counts once (the first evaluation source with a valid value),
// only from certify_sources, and a label whose value does not parse under
// the profile does not count.
func TestLabelsStageCountsEvaluationSourcesOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	labelRequirements(t, w, 3, []string{"manual"})
	// The two judge labels of the world do not certify against manual.
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, "judge"); err != nil {
		t.Fatal(err)
	}
	st, _ := w.r.LabelsStage().Status(t.Context())
	if b, ok := blockerOf(st, "category"); !ok || b.Measure.Have != 0 {
		t.Fatalf("judge labels counted for a manual-only requirement: %+v", b)
	}
	// A stale value under the current profile is not a label.
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"not_an_option"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	st, _ = w.r.LabelsStage().Status(t.Context())
	if b, ok := blockerOf(st, "category"); !ok || b.Measure.Have != 0 {
		t.Fatalf("an invalid value counted: %+v", b)
	}
	// The manual value replaces it and counts once; a judge label on the
	// same message does not make it two.
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"finance"`, "judge"); err != nil {
		t.Fatal(err)
	}
	st, _ = w.r.LabelsStage().Status(t.Context())
	if b, ok := blockerOf(st, "category"); !ok || b.Measure.Have != 1 || b.Actions[0].Params["count"] != "2" {
		t.Fatalf("one message counted more than once: %+v", b)
	}
	// Two more manual messages complete the question's requirement.
	for _, id := range w.ids[2:4] {
		if err := w.st.PutLabel(t.Context(), id, "category", `"work"`, store.LabelManual); err != nil {
			t.Fatal(err)
		}
	}
	st, _ = w.r.LabelsStage().Status(t.Context())
	if b, ok := blockerOf(st, "category"); ok {
		t.Fatalf("category still blocked: %+v", b)
	}
}

// A label source with eval false may teach but never counts, even when its
// name is in certify_sources (checked through the plan).
func TestLabelsStageExcludesNonEvaluationSources(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	labelRequirements(t, w, 2, []string{"manual"})
	if err := writeTeachers(w.cfg, `{"sources": {"judge": {"from": "labels", "label_source": "judge", "precedence": 2, "weight": 2, "eval": false}}}`); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], "category", `"work"`, "judge"); err != nil {
		t.Fatal(err)
	}
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); !ok || b.Measure.Have != 0 {
		t.Fatalf("a non-evaluating source counted: %+v", b)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	st, _ = w.r.LabelsStage().Status(t.Context())
	if b, ok := blockerOf(st, "category"); !ok || b.Measure.Have != 1 {
		t.Fatalf("the manual label did not count: %+v", b)
	}
}

// Progress is min(have, need) per question; done is every question at its
// requirement, blocked is each short question with its remaining count.
func TestLabelsStageDoneAndProgress(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	labelRequirements(t, w, 5, []string{"manual", "judge"})
	for _, id := range w.ids[:2] {
		labelAll(t, w, id, store.LabelManual, 0)
	}
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil || st.State != learning.Done {
		t.Fatalf("%+v %v", st, err)
	}
	// category has 10 judge labels already, so it is satisfied; the 16
	// other questions keep their warnings without closing the gate.
	if st.Progress.Done != 5+16*2 || st.Progress.Total != 17*5 || len(st.Blockers) != 16 {
		t.Fatalf("progress %+v, blockers %d", st.Progress, len(st.Blockers))
	}
	b, ok := blockerOf(st, "urgency")
	if !ok || b.Measure == nil || b.Measure.Have != 2 || b.Measure.Need != 5 || b.Actions[0].Params["count"] != "3" {
		t.Fatalf("urgency blocker %+v", b)
	}
	for _, id := range w.ids[2:5] {
		labelAll(t, w, id, store.LabelManual, 1)
	}
	st, err = w.r.LabelsStage().Status(t.Context())
	if err != nil || st.State != learning.Done || len(st.Blockers) != 0 || st.Progress.Done != 17*5 || st.Progress.Total != 17*5 {
		t.Fatalf("done %+v %v", st, err)
	}
}

// The stage waits for its inputs; a broken teachers or requirements file is
// the shared config blocker with that file's remedy.
func TestLabelsStageWaitsAndReportsConfigProblems(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	st, err := w.r.LabelsStage(&fixedStage{id: learning.StageSync, state: learning.Running}).Status(t.Context())
	if err != nil || st.State != learning.Pending || st.Note != "waits for the sync stage" {
		t.Fatalf("waiting %+v %v", st, err)
	}
	if err := writeTeachers(w.cfg, `{"sources": {"judge": {"weight": -1}}}`); err != nil {
		t.Fatal(err)
	}
	st, err = w.r.LabelsStage().Status(t.Context())
	if err != nil || st.State != learning.Blocked || len(st.Blockers) != 1 || st.Blockers[0].Code != BlockConfig ||
		st.Blockers[0].Subject != SettingTeachersFile || st.Blockers[0].Actions[0].ID != learning.ActionChooseTeacher {
		t.Fatalf("teachers %+v %v", st, err)
	}
	if err := writeTeachers(w.cfg, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := writeRequirements(w.cfg, `{"defaults": {"z": -1}}`); err != nil {
		t.Fatal(err)
	}
	st, _ = w.r.LabelsStage().Status(t.Context())
	if st.State != learning.Blocked || st.Blockers[0].Code != BlockConfig || st.Blockers[0].Actions[0].ID != learning.ActionReviewRequirements {
		t.Fatalf("requirements %+v", st)
	}
}

// A manual correction through the label writer (one event for the card)
// voids the certified verdicts; the re-score then de-certifies the route
// and the questions stage blocks it.
func TestALabelCorrectionVoidsTheCertifiedVerdict(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 4, "target_accuracy": 0.6, "min_coverage": 0.3, "z": 1.2, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	first := w.run()
	if r, _ := routeReportOf(first, "category"); !r.Certification.Certified {
		t.Fatalf("category route did not certify: %+v", r.Certification)
	}
	if _, v := reportOf(first, "category"); !v.Meets {
		t.Fatalf("category verdict %+v", v)
	}
	active, err := w.r.active(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	holdout := active["category"].meta.Holdout
	if holdout == nil || len(holdout.Certify) < 6 {
		t.Fatalf("certification part %+v", holdout)
	}
	lb := feedback.NewLabeler(w.st, nil)
	wrong := map[int64]string{}
	for _, id := range w.ids {
		wrong[id] = w.wrongClass(t, id)
	}
	for _, item := range holdout.Certify[:6] {
		value, err := json.Marshal(wrong[item.ID])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lb.PutMessage(t.Context(), w.p, item.ID, map[string]json.RawMessage{"category": value}); err != nil {
			t.Fatal(err)
		}
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != relabelled {
		t.Fatalf("the corrections left the verdicts current: %+v", lr)
	}
	qs, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil || qs.State != learning.Running {
		t.Fatalf("the verdicts are not void: %+v %v", qs, err)
	}
	labels, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(labels, "category"); ok {
		t.Fatalf("the corrections lost labels: %+v", b)
	}
	rep, err := w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v %v", rep, err)
	}
	if _, v := reportOf(rep, "category"); v.Meets || v.Certification.Certified {
		t.Fatalf("after the corrections: %+v", v)
	}
	if r, _ := routeReportOf(rep, "category"); r.Certification.Certified {
		t.Fatalf("the corrected route is still certified: %+v", r.Certification)
	}
	qs, err = w.r.QuestionsStage().Status(t.Context())
	if err != nil || qs.State != learning.Blocked {
		t.Fatalf("the questions stage after the re-score: %+v %v", qs, err)
	}
	if b, ok := blockerOf(qs, "category"); !ok || b.Code != BlockRequirement {
		t.Fatalf("category is not blocked after the correction: %+v", b)
	}
}

// idsOnlyInputs serves the member ids and labels only; Members reports the
// thread-graph rebuild the label count must not do.
type idsOnlyInputs struct{ ids []int64 }

func (i idsOnlyInputs) Members(context.Context) ([]Member, error) {
	return nil, errors.New("Members rebuilt the thread graph")
}
func (i idsOnlyInputs) MemberIDs(context.Context) ([]int64, error) { return i.ids, nil }
func (i idsOnlyInputs) Vectors(context.Context) (string, *vector.Set, error) {
	return "", nil, nil
}
func (i idsOnlyInputs) Cells(context.Context) (Layout, map[int64][]int32, error) {
	return Layout{}, nil, nil
}
func (i idsOnlyInputs) Labels(context.Context) ([]store.LabelRow, error) { return nil, nil }
func (i idsOnlyInputs) Decisions(context.Context, store.TriageKey, func(int64, map[string]engine.Answer) error) error {
	return nil
}
func (i idsOnlyInputs) Rules(context.Context, map[int64]map[string]json.RawMessage) ([]RuleInfo, map[int64][]int, error) {
	return nil, nil, nil
}

// The stage counts ids: it never calls Members, whose thread-graph rebuild
// was 94–96 % of /api/status and /api/learning.
func TestLabelsStageReadsMemberIDsOnly(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, map[string]any{SettingWorkers: 1})
	r := &Runner{cfg: cfg, in: idsOnlyInputs{ids: []int64{1, 2, 3}}}
	st, err := r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != learning.Blocked || st.Progress.Total == 0 {
		t.Fatalf("status %+v", st)
	}
}

// The stage's count and the label queue describe one population: every
// member has at most one evaluation label, and the first evaluation source
// with a valid value counts once. With every evaluation source certifying,
// stage-have + queue-missing equals the member count for every question.
func TestLabelsStageCountMatchesQueuePopulation(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	labelAll(t, w, w.ids[0], store.LabelManual, 0)
	labelAll(t, w, w.ids[1], store.LabelManual, 1)
	labelAll(t, w, w.ids[2], store.LabelJudge, 1)
	labelAll(t, w, w.ids[3], store.LabelManual, 2)

	memberIDs, err := w.r.in.MemberIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	members, err := w.r.in.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(memberIDs) != len(members) {
		t.Fatalf("MemberIDs %d, Members %d", len(memberIDs), len(members))
	}
	for i := range members {
		if memberIDs[i] != members[i].ID {
			t.Fatalf("member %d differs: %d vs %d", i, memberIDs[i], members[i].ID)
		}
	}
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		b, ok := blockerOf(st, q.Name)
		if !ok || b.Measure == nil {
			t.Fatalf("%s: no blocker with a measure: %+v", q.Name, st)
		}
		queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: q.Name, Limit: LabelQueueMax})
		if err != nil {
			t.Fatal(err)
		}
		have := int(b.Measure.Have)
		if have+len(queue.Tasks) != len(memberIDs) {
			t.Fatalf("%s: have %d + queued %d != members %d", q.Name, have, len(queue.Tasks), len(memberIDs))
		}
	}
}

// BenchmarkLabelsStageStatus fails if the count rebuilds the thread graph:
// the fake Members returns an error and the stage must not ask for it.
func BenchmarkLabelsStageStatus(b *testing.B) {
	ids := make([]int64, 33842)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	cfg := configtest.New(b, map[string]any{SettingWorkers: 1})
	r := &Runner{cfg: cfg, in: idsOnlyInputs{ids: ids}}
	stage := r.LabelsStage()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		st, err := stage.Status(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if st.State == "" {
			b.Fatal("empty status")
		}
	}
}
