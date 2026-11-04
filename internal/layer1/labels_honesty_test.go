package layer1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// storeSucceededReport writes a current last succeeded run's report
// directly: its inputs and label digests match the store, so the questions
// stage judges the fixture instead of calling it stale. The labels stage
// reads only the certifications and label counts, not a model.
func storeSucceededReport(t *testing.T, w *world, verdicts []Verdict, routes []RouteReport) {
	t.Helper()
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	inputs, labels, err := w.r.fingerprint(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Report{State: runstate.Succeeded, Inputs: inputs, Labels: labels, Verdicts: verdicts, Routes: routes})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.st.BeginLayer1Run(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.FinishLayer1Run(t.Context(), id, runstate.Succeeded, string(raw), ""); err != nil {
		t.Fatal(err)
	}
}

// TestBelowTargetModelGetsNoLabelRequest is the live-shaped regression: a
// model that agrees with its certification labels on 85.9 % of 894 answers
// cannot reach 0.95, so the labels stage must not ask for the static 292
// (or the old 2,000 sentinel), the verdict must report zero remaining, and
// the auto-label loop must buy nothing.
func TestBelowTargetModelGetsNoLabelRequest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, map[string]any{
		"labels.auto": true, "labels.auto.batch": 2, "labels.auto.max": 4,
		"labels.auto.budget_usd": 1.0, "layer1.after_runs": false,
	})
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 2, "target_accuracy": 0.95, "min_coverage": 0.5, "z": 1.96, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	req := plan.Requirements.For("category")
	fraction := plan.Config.CertifyFraction
	static := req.Requested(fraction)
	if static <= 0 {
		t.Fatalf("the static measure is %d, want a positive design count", static)
	}
	cert := selective.Certification{Answered: 894, Errors: 126, Coverage: 0.5, Tested: true}

	// The plan keeps the design's static count as the measure, but marks it
	// unfixable: no label count raises an 85.9 % model to 95 %.
	if got := req.LabelNeed(fraction, cert); got != (LabelNeed{Need: static, Fixable: false}) {
		t.Fatalf("below-target plan %+v, want the static measure %d marked unfixable", got, static)
	}

	q, ok := plan.Profile.Question("category")
	if !ok {
		t.Fatal("category is not a profile question")
	}
	v := verdicts(plan, []*settled{{q: q, model: &Model{}, cert: cert,
		report: QuestionReport{Eval: static - 72, Effective: 100}}})[0]
	if v.Requested != static || v.Remaining != 0 {
		t.Fatalf("below-target verdict requests %d labels with %d remaining, want %d and 0", v.Requested, v.Remaining, static)
	}

	var stored []Verdict
	for i := range plan.Profile.Questions {
		stored = append(stored, Verdict{Question: plan.Profile.Questions[i].Name, Certification: cert, Eval: static - 72})
	}
	storeSucceededReport(t, w, stored, nil)

	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); ok {
		t.Fatalf("below-target question got a labels-stage blocker: %+v", b)
	}
	if st.Progress.Total != 0 || st.Progress.Done != 0 || st.State != learning.Done {
		t.Fatalf("below-target questions are in the labels progress: state %s, progress %+v", st.State, st.Progress)
	}
	if !strings.Contains(st.Note, "questions stage") {
		t.Fatalf("the labels note does not name the questions stage: %q", st.Note)
	}
	if !strings.Contains(st.Note, "layer-1 diagnostic") {
		t.Fatalf("the labels note does not name the route-report fallback: %q", st.Note)
	}

	// A below-target question keeps its measure but gets no label or judge
	// action, whichever layer-1 blocker names it.
	for _, code := range []string{BlockTooFewEval, BlockTooFewLabels} {
		b, ok := (&stage{r: w.r}).blocker(Verdict{Question: "category", Code: code,
			Eval: 2, Effective: 1, RequiredTrain: 10, Requested: static, Remaining: 0})
		if !ok || b.Measure == nil {
			t.Fatalf("%s blocker without a measure: %+v", code, b)
		}
		recheck := map[string]bool{}
		for _, a := range b.Actions {
			if a.ID == learning.ActionLabelMessages || a.ID == learning.ActionRunJudge {
				t.Fatalf("%s still offers a label/judge action: %+v", code, b.Actions)
			}
			recheck[a.ID] = true
		}
		if !recheck[learning.ActionRunLayer1] || !recheck[learning.ActionReviewRequirements] {
			t.Fatalf("%s has no re-run/review remedy: %+v", code, b.Actions)
		}
	}

	// The paid loop reads the labels stage: with no blocker it starts no
	// judge job and records no purchase.
	judgeCalls := 0
	w.r.judge = func(string) (judge.Judge, error) {
		judgeCalls++
		return newFakeJudge(), nil
	}
	w.r.state = &fakeStates{fail: map[int64]bool{}}
	bus := events.New()
	t.Cleanup(bus.Close)
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	if err := w.r.autoLabelOnce(m, "test"); err != nil {
		t.Fatal(err)
	}
	if judgeCalls != 0 || len(m.List()) != 0 {
		t.Fatalf("auto-label bought for an unfixable model: %d judge calls, %d jobs", judgeCalls, len(m.List()))
	}
	if bought, err := w.st.AutoLabelsBought(t.Context()); err != nil || bought != 0 {
		t.Fatalf("bought %d labels for an unfixable model (%v)", bought, err)
	}
}

// TestLabelsStageStillAsksForAFixableShortfall: a model at or above the
// target still gets the honest, uncapped request and its manual/judge
// actions. A below-target question in the same report is excluded, so every
// blocker the paid loop sees belongs to a LabelNeed.Fixable subject.
func TestLabelsStageStillAsksForAFixableShortfall(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 2, "target_accuracy": 0.99, "min_coverage": 0.5, "z": 1.96, "family_wise": true}}`); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	fraction := plan.Config.CertifyFraction
	below := selective.Certification{Answered: 894, Errors: 126, Coverage: 0.5, Tested: true}
	at := selective.Certification{Answered: 1000, Errors: 0, Coverage: 0.5, Tested: true, Certified: true,
		Precision: 1, Lower: 1, Eligible: 1000}
	if need := plan.Requirements.For("urgency").LabelNeed(fraction, at); need != (LabelNeed{Need: 3504, Fixable: true}) {
		t.Fatalf("the fixable plan is %+v, want the honest 3504", need)
	}
	var stored []Verdict
	for i := range plan.Profile.Questions {
		cert := at
		if plan.Profile.Questions[i].Name == "category" {
			cert = below
		}
		stored = append(stored, Verdict{Question: plan.Profile.Questions[i].Name, Certification: cert})
	}
	storeSucceededReport(t, w, stored, nil)

	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); ok {
		t.Fatalf("the below-target question is still asked for labels: %+v", b)
	}
	b, ok := blockerOf(st, "urgency")
	if !ok || b.Code != BlockTooFewEval || b.Measure == nil || b.Measure.Unit != learning.UnitEvalLabels ||
		b.Measure.Have != 0 || b.Measure.Need != 3504 {
		t.Fatalf("the fixable shortfall %+v", b)
	}
	if len(b.Actions) != 3 || b.Actions[0].ID != learning.ActionLabelMessages ||
		b.Actions[0].Params["count"] != "3504" ||
		b.Actions[1].ID != learning.ActionRunJudge || b.Actions[1].Params["count"] != "3504" ||
		b.Actions[2].ID != learning.ActionReviewRequirements {
		t.Fatalf("the fixable remedies %+v", b.Actions)
	}
	fixable := len(plan.Profile.Questions) - 1
	if st.State != learning.Blocked || st.Progress.Total != int64(fixable*3504) || st.Progress.Done != 0 {
		t.Fatalf("the labels stage counts the unfixable question: state %s, progress %+v", st.State, st.Progress)
	}
	// The invariant the paid loop relies on: every subject it can select
	// has a fixable labels-stage plan.
	for _, blocker := range st.Blockers {
		if blocker.Code != BlockTooFewEval {
			continue
		}
		cert := at
		if blocker.Subject == "category" {
			cert = below
		}
		if need := plan.Requirements.For(blocker.Subject).LabelNeed(fraction, cert); !need.Fixable {
			t.Errorf("the labels stage asks for %s but its plan is unfixable: %+v", blocker.Subject, need)
		}
	}
}

// TestLabelsStageUsesRouteCertification is the live contradiction: the
// layer-1 answer certification of urgency is above target (96.3 %, 486
// answered), but the route certification that answers is below it (85.9 %,
// 894 answered). The labels stage must follow the route: no blocker, no
// progress and no label/judge action for urgency, while a question whose
// route is at target but short of labels still gets the honest request.
func TestLabelsStageUsesRouteCertification(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 2, "target_accuracy": 0.95, "min_coverage": 0.5, "z": 1.96, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	fraction := plan.Config.CertifyFraction
	urgencyVerdict := selective.Certification{Answered: 486, Errors: 18, Precision: 1 - 18.0/486,
		Lower: 0.9281, Tested: true}
	urgencyRoute := selective.Certification{Answered: 894, Errors: 126, Precision: 1 - 126.0/894,
		Lower: 0.8209, CoverageLower: 0.8795, Tested: true}
	categoryVerdict := urgencyRoute // below target: the old source, now compatibility only
	categoryRoute := selective.Certification{Answered: 894, Errors: 0, Precision: 1, Lower: 1,
		Eligible: 894, Coverage: 0.5, Certified: true, Tested: true}
	// The two certifications disagree for urgency exactly as the live report.
	if need := plan.Requirements.For("urgency").LabelNeed(fraction, urgencyVerdict); !need.Fixable {
		t.Fatalf("the layer-1 diagnostic is not fixable, the fixture cannot prove the source: %+v", need)
	}
	if need := plan.Requirements.For("urgency").LabelNeed(fraction, urgencyRoute); need.Fixable {
		t.Fatalf("the route certification is fixable, the fixture cannot prove the source: %+v", need)
	}
	var verdicts []Verdict
	var routes []RouteReport
	for i := range plan.Profile.Questions {
		name := plan.Profile.Questions[i].Name
		vcert, rcert := urgencyVerdict, urgencyRoute
		if name == "category" {
			vcert, rcert = categoryVerdict, categoryRoute
		}
		verdicts = append(verdicts, Verdict{Question: name, Certification: vcert, Target: 0.95, Z: 1.96, Required: 0.5})
		routes = append(routes, RouteReport{Question: name, Certification: rcert})
	}
	storeSucceededReport(t, w, verdicts, routes)

	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "urgency"); ok {
		t.Fatalf("the labels stage followed the verdict certification, not the route: %+v", b)
	}
	if st.Progress.Total != 292 || st.Progress.Done != 10 {
		t.Fatalf("the labels progress counts urgency (or misses category): %+v", st.Progress)
	}
	if st.State != learning.Blocked {
		t.Fatalf("the labels state %s, want the one fixable blocker to keep it blocked", st.State)
	}
	if !strings.Contains(st.Note, "questions stage") || !strings.Contains(st.Note, "16 cannot be certified by more labels") {
		t.Fatalf("the labels note %q does not name the questions stage and the 16 route-unfixable questions", st.Note)
	}
	if strings.Contains(st.Note, "layer-1 diagnostic") {
		t.Fatalf("a report with routes used the compatibility fallback: %q", st.Note)
	}
	b, ok := blockerOf(st, "category")
	if !ok || b.Measure == nil || b.Measure.Need != 292 || b.Measure.Have != 10 {
		t.Fatalf("the fixable route shortfall %+v", b)
	}
	if len(b.Actions) != 3 || b.Actions[0].ID != learning.ActionLabelMessages || b.Actions[0].Params["count"] != "282" ||
		b.Actions[1].ID != learning.ActionRunJudge || b.Actions[1].Params["count"] != "282" ||
		b.Actions[2].ID != learning.ActionReviewRequirements {
		t.Fatalf("the fixable route remedies %+v", b.Actions)
	}
}

// TestQuestionsStageUsesRouteLabelNeed: the questions stage counts labels
// from the route's LabelNeed, not the layer-1 verdict's diagnostic
// Requested/Remaining. A below-target route offers no purchase; an
// at-target route short of certified answers gets the route's count.
func TestQuestionsStageUsesRouteLabelNeed(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 2, "target_accuracy": 0.95, "min_coverage": 0.5, "z": 1.96, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	below := selective.Certification{Answered: 894, Errors: 126, Precision: 1 - 126.0/894,
		Lower: 0.8209, CoverageLower: 0.8795, Tested: true}
	short := selective.Certification{Answered: 50, Eligible: 50, N: 50, Precision: 1, Lower: 1, Tested: true}
	verdicts := []Verdict{
		// The layer-1 diagnostics say 9,764 requested / 7,836 remaining and
		// 50 requested / 0 remaining; neither may drive the route actions.
		{Question: "urgency", Eval: 1928, Requested: 9764, Remaining: 7836, Target: 0.95, Z: 1.96, Required: 0.5},
		{Question: "category", Eval: 10, Requested: 50, Remaining: 0, Target: 0.95, Z: 1.96, Required: 0.5},
	}
	routes := []RouteReport{
		{Question: "urgency", Certification: below},
		{Question: "category", Certification: short},
	}
	storeSucceededReport(t, w, verdicts, routes)

	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != learning.Blocked || st.Progress.Total != 2 || st.Progress.Done != 0 {
		t.Fatalf("the questions status %+v", st)
	}
	urgency, ok := blockerOf(st, "urgency")
	if !ok || urgency.Code != BlockRequirement || urgency.Measure == nil || urgency.Measure.Unit != "observed precision" {
		t.Fatalf("the below-target route blocker %+v", urgency)
	}
	for _, action := range urgency.Actions {
		if action.ID == learning.ActionLabelMessages || action.ID == learning.ActionRunJudge {
			t.Fatalf("a below-target route still asks for labels: %+v", urgency.Actions)
		}
	}
	category, ok := blockerOf(st, "category")
	if !ok || category.Code != BlockRequirement || category.Measure == nil || category.Measure.Unit != "certification labels" {
		t.Fatalf("the route shortfall blocker %+v", category)
	}
	if len(category.Actions) != 2 || category.Actions[0].ID != learning.ActionLabelMessages ||
		category.Actions[0].Params["count"] != "282" || category.Actions[1].ID != learning.ActionReviewRequirements {
		t.Fatalf("the route label count is not LabelNeed(route cert) - eval: %+v", category.Actions)
	}
}

// TestLabelsStageStaticRequestIsNotCapped: with no certification yet the
// labels stage asks the shipped family-wise request, 3,504, never the old
// 2,000 sentinel.
func TestLabelsStageStaticRequestIsNotCapped(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 200, "min_eval_labels": 50, "target_accuracy": 0.99, "min_coverage": 0.5, "z": 1.96, "family_wise": true}}`); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	const want = 3504
	if got := plan.Requirements.For("category").LabelNeed(plan.Config.CertifyFraction, selective.Certification{}); got != (LabelNeed{Need: want, Fixable: true}) {
		t.Fatalf("the static plan is %+v, want %d fixable", got, want)
	}
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, ok := blockerOf(st, "category")
	if !ok || b.Measure == nil || b.Measure.Need != want || b.Measure.Have != 10 {
		t.Fatalf("the static blocker %+v", b)
	}
	if strings.Contains(b.Detail, "2000") || !strings.Contains(b.Detail, "3504") {
		t.Fatalf("the static request is capped: %q", b.Detail)
	}
	if st.Progress.Total != int64(len(plan.Profile.Questions)*want) || st.Progress.Done != 10 {
		t.Fatalf("the static progress %+v", st.Progress)
	}
}
