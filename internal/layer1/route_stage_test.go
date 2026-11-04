package layer1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// routeReportOf is one question's route report from a pass (ok false:
// none).
func routeReportOf(rep Report, question string) (RouteReport, bool) {
	for _, r := range rep.Routes {
		if r.Question == question {
			return r, true
		}
	}
	return RouteReport{}, false
}

// groupOf is a message's conversation as layer 1 saw it (the split).
func (w *world) groupOf(t *testing.T) map[int64]string {
	t.Helper()
	members, err := w.r.in.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[int64]string, len(members))
	for _, m := range members {
		out[m.ID] = m.Group
	}
	return out
}

// certifyLabel reports whether a label row of a source is one a
// question's requirement certifies against.
func certifyLabel(plan *Plan, question, labelSource string) bool {
	for _, s := range plan.Teachers.For(question) {
		if s.Eval && s.LabelSource == labelSource && slices.Contains(plan.Requirements.For(question).CertifySources, s.Name) {
			return true
		}
	}
	return false
}

// wrongClass is a category label other than the message's true one.
func (w *world) wrongClass(t *testing.T, id int64) string {
	t.Helper()
	i := slices.Index(w.ids, id)
	if i < 0 {
		t.Fatalf("message %d is not in the world", id)
	}
	return []string{"work", "finance"}[w.kind(i)]
}

// A re-score with no label change rewrites byte-identical route answers
// and an equal route report: both paths read the same inputs (the stored
// layer-1 and decider answers, the rules on the selection labels, the
// current labels).
func TestRescoreReproducesTheRouteAnswers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	first := w.run()
	beforeRaw := w.rawRouteAnswers(t)
	before := w.routeAnswersByID(t)
	rep, err := w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v %v", rep, err)
	}
	if after := w.rawRouteAnswers(t); !reflect.DeepEqual(beforeRaw, after) {
		t.Fatalf("a no-op re-score changed the route answer bytes:\n%+v\n%+v", beforeRaw, after)
	}
	if after := w.routeAnswersByID(t); !reflect.DeepEqual(before, after) {
		t.Fatal("a no-op re-score changed the route answers")
	}
	if !reflect.DeepEqual(first.Routes, rep.Routes) {
		t.Fatalf("a no-op re-score changed the route report:\n%+v\n%+v", first.Routes, rep.Routes)
	}
}

// Flipping every certification label of a certified question makes the
// route uncertified on the re-score, and every answer of it abstains
// not_certified: no participant may answer an uncertified route.
func TestAFlippedCertificationLabelUncertifiesTheRoute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	rep := w.run()
	r, ok := routeReportOf(rep, "category")
	if !ok || !r.Certification.Certified {
		t.Fatalf("category route %+v", r)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	groups := w.groupOf(t)
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	flipped := 0
	for _, l := range rows {
		if l.Question != "category" || !certifyLabel(plan, "category", l.Source) {
			continue
		}
		if !selective.InCertifyPart(groups[l.MessageID], plan.Config.CertifyFraction) {
			continue
		}
		if err := w.st.PutLabel(t.Context(), l.MessageID, "category", fmt.Sprintf("%q", w.wrongClass(t, l.MessageID)), l.Source); err != nil {
			t.Fatal(err)
		}
		flipped++
	}
	if flipped == 0 {
		t.Fatal("no certification label to flip")
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != relabelled {
		t.Fatalf("the corrections left the verdicts current: %+v", lr)
	}
	rep, err = w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v %v", rep, err)
	}
	r, _ = routeReportOf(rep, "category")
	if r.Certification.Certified || r.Certification.Answered == 0 {
		t.Fatalf("the flipped route is certified: %+v", r.Certification)
	}
	answers := w.routeAnswersByID(t)
	if len(answers) != len(w.ids) {
		t.Fatalf("%d route rows for %d messages", len(answers), len(w.ids))
	}
	for id, a := range answers {
		if got := a["category"]; got.Abstain != route.AbstainNotCertified {
			t.Fatalf("message %d answered an uncertified route: %+v", id, got)
		}
	}
	// The questions stage judges the route: the de-certified question is
	// blocked requirement_unmet with the route's numbers.
	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var blocked bool
	for _, b := range st.Blockers {
		if b.Subject == "category" {
			blocked = b.Code == BlockRequirement && b.Measure != nil
		}
	}
	if !blocked || st.State != learning.Blocked {
		t.Fatalf("the questions stage did not block the de-certified route: %+v", st)
	}
}

// A manual label edit is a re-score's input like any other: new manual
// labels on the certification part take precedence over the judge's,
// uncertify the route at once and make every answer of it abstain.
func TestAManualLabelEditChangesTheRouteVerdict(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	if r, _ := routeReportOf(w.run(), "category"); !r.Certification.Certified {
		t.Fatalf("category route did not certify: %+v", r.Certification)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	groups := w.groupOf(t)
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	edited := 0
	for _, l := range rows {
		if l.Question != "category" {
			continue
		}
		if !selective.InCertifyPart(groups[l.MessageID], plan.Config.CertifyFraction) {
			continue
		}
		if err := w.st.PutLabel(t.Context(), l.MessageID, "category", fmt.Sprintf("%q", w.wrongClass(t, l.MessageID)), "manual"); err != nil {
			t.Fatal(err)
		}
		edited++
	}
	if edited == 0 {
		t.Fatal("no certification label to edit")
	}
	if lr, _ := w.r.lastRun(t.Context()); lr.freshness != relabelled {
		t.Fatalf("a manual label edit left the verdicts current: %+v", lr)
	}
	rep, err := w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v %v", rep, err)
	}
	r, _ := routeReportOf(rep, "category")
	if r.Certification.Certified {
		t.Fatalf("the manual edit left the route certified: %+v", r.Certification)
	}
	for id, a := range w.routeAnswersByID(t) {
		if got := a["category"]; got.Abstain != route.AbstainNotCertified {
			t.Fatalf("message %d answered an uncertified route: %+v", id, got)
		}
	}
}

// rewriteLastRun replaces the newest run's recorded report (a pass that
// wrote no routes, or fewer rows than answers).
func rewriteLastRun(t *testing.T, w *world, edit func(*Report)) Report {
	t.Helper()
	runs, err := w.st.ListLayer1Runs(t.Context(), 1)
	if err != nil || len(runs) == 0 {
		t.Fatalf("runs %v %v", runs, err)
	}
	var rep Report
	if err := json.Unmarshal([]byte(runs[0].Report), &rep); err != nil {
		t.Fatal(err)
	}
	edit(&rep)
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.FinishLayer1Run(t.Context(), runs[0].ID, runstate.Succeeded, string(raw), ""); err != nil {
		t.Fatal(err)
	}
	return rep
}

// The calibration stage waits for its inputs and then for a succeeded
// pass.
func TestRouteStageWaitsForLayer1(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	st, err := w.r.RouteStage(&fixedStage{id: "layer1", state: learning.Running}).Status(t.Context())
	if err != nil || st.State != learning.Pending || st.Note != "waits for the layer1 stage" {
		t.Fatalf("an earlier stage not done: %+v %v", st, err)
	}
	st, err = w.r.RouteStage().Status(t.Context())
	if err != nil || st.State != learning.Pending || st.Note != "not routed yet" {
		t.Fatalf("no succeeded pass: %+v %v", st, err)
	}
}

// A pass that wrote no route record, or fewer route rows than answers, is
// the mechanism's blocker with its two remedies; certification is not.
func TestRouteStageBlocksWithoutRoutes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	rep := w.run()
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	answerable := int64(0)
	for _, r := range rep.Routes {
		if r.Answering {
			answerable++
		}
	}
	check := func(st learning.StageStatus) {
		t.Helper()
		if st.State != learning.Blocked || len(st.Blockers) == 0 {
			t.Fatalf("not blocked: %+v", st)
		}
		for _, b := range st.Blockers {
			if b.Code != BlockRouteNotRouted || len(b.Actions) != 2 || b.Actions[0].ID != "run_layer1" ||
				b.Actions[1].ID != "review_failures" || b.Actions[1].Params["log"] != "layer1" {
				t.Fatalf("blocker %+v", b)
			}
		}
		if st.Progress.Total != int64(len(plan.Profile.Questions)) {
			t.Fatalf("progress %+v", st.Progress)
		}
	}
	rewriteLastRun(t, w, func(r *Report) { r.Routes = nil })
	st, err := w.r.RouteStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check(st)
	if st.Progress.Done != 0 || len(st.Blockers) != len(plan.Profile.Questions) {
		t.Fatalf("missing records: %+v", st)
	}
	rewriteLastRun(t, w, func(r *Report) {
		r.Routes, r.Routed = rep.Routes, rep.Answered-1
	})
	st, err = w.r.RouteStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check(st)
	if st.Progress.Done != answerable || len(st.Blockers) != 1 || st.Blockers[0].Subject != "" {
		t.Fatalf("routed mismatch: %+v", st)
	}
}

// A succeeded pass that wrote every route record and row is done; the
// note says how many routes certified and never blocks.
func TestRouteStageDoneWhenRouted(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	rep := w.run()
	st, err := w.r.RouteStage().Status(t.Context())
	if err != nil || st.State != learning.Done || len(st.Blockers) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	answering := 0
	for _, r := range rep.Routes {
		if r.Answering {
			answering++
		}
	}
	if st.Progress.Total != 17 || st.Progress.Done != int64(answering) ||
		st.Note != fmt.Sprintf("%d of 17 questions answerable at the target", answering) {
		t.Fatalf("note %q progress %+v", st.Note, st.Progress)
	}
}

// A question whose layer-1 model alone fails its own certification is
// `meets` when its route certifies: the gate judges what answers. The
// active model's holdout is corrupted (its predictions, and so the
// stored route offers, stay good); the model is kept by the regression
// guard, its verdict fails, and keys + Ondelle + layer 1 certify the route.
func TestQuestionsStageJudgesTheRoute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, map[string]any{SettingMaxNLLRegression: 0})
	permissiveRequirements(t, w)
	w.run()
	active, err := w.st.ActiveLayer1(t.Context())
	if err != nil || len(active) != 1 {
		t.Fatalf("active %v %v", active, err)
	}
	a := active[0]
	var m Metrics
	if err := json.Unmarshal([]byte(a.Metrics), &m); err != nil {
		t.Fatal(err)
	}
	m.NLL = -100 // the candidate is worse: the corrupted model is kept
	var meta modelMeta
	if err := json.Unmarshal([]byte(a.Meta), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Holdout == nil || len(meta.Holdout.Certify) == 0 {
		t.Fatal("the model has no certification holdout to corrupt")
	}
	probs := make([]float64, len(meta.Holdout.Certify[0].Probs))
	probs[0] = 1 // class 0: never the world's finance/work label
	for i := range meta.Holdout.Certify {
		meta.Holdout.Certify[i].Probs = probs
	}
	mraw, _ := json.Marshal(m)
	metaraw, _ := json.Marshal(meta)
	kept := a
	kept.VersionKey, kept.Metrics, kept.Meta = "corrupt-holdout", string(mraw), string(metaraw)
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
	if rep.Kept["category"] == "" {
		t.Fatalf("the corrupted active model was not kept: %+v", rep.Kept)
	}
	_, v := reportOf(rep, "category")
	if v.Meets || v.Certification.Certified {
		t.Fatalf("the layer-1 model alone passes: %+v", v)
	}
	r, ok := routeReportOf(rep, "category")
	if !ok || !r.Certification.Certified {
		t.Fatalf("the route did not certify: %+v", r)
	}
	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range st.Blockers {
		if b.Subject == "category" {
			t.Fatalf("a certified route is blocked: %+v", b)
		}
	}
	if st.Progress.Done != 1 {
		t.Fatalf("progress %+v", st.Progress)
	}
}

// A route that is not certified blocks its question with the design's
// plain-language case, the route's own measure and the label remedy; no
// detail carries a code. A question with no evaluation label at all gets
// the label-first case with the paid judge action.
func TestQuestionsStageBlocksAnUncertifiedRoute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	rep := w.run()
	r, ok := routeReportOf(rep, "category")
	if !ok || r.Certification.Certified {
		t.Fatalf("category route %+v", r)
	}
	_, v := reportOf(rep, "category")
	_, uv := reportOf(rep, "urgency")
	if v.Eval == 0 {
		t.Fatalf("verdict %+v", v)
	}
	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The stage status carries the route report itself (the surface the
	// learning view publishes), not a second verdict.
	seen := map[string]bool{}
	for _, c := range st.Certifications {
		seen[c.Question] = true
	}
	if !seen["category"] || !seen["urgency"] {
		t.Fatalf("questions stage certifications %+v", st.Certifications)
	}
	var category, urgency *learning.Blocker
	for i := range st.Blockers {
		switch st.Blockers[i].Subject {
		case "category":
			category = &st.Blockers[i]
		case "urgency":
			urgency = &st.Blockers[i]
		}
		if strings.Contains(st.Blockers[i].Detail, "requirement_unmet") || strings.Contains(st.Blockers[i].Detail, "layer1_") ||
			strings.Contains(st.Blockers[i].Detail, "route_not_routed") {
			t.Errorf("a code in the user text: %q", st.Blockers[i].Detail)
		}
	}
	if category == nil || category.Code != BlockRequirement || category.Measure == nil {
		t.Fatalf("category blocker %+v", category)
	}
	if category.Measure.Unit != "certification labels" || category.Measure.Have != float64(r.Certification.Eligible) ||
		category.Measure.Need != float64(selective.Needed(v.Target, v.Z)) {
		t.Fatalf("category measure %+v (route %+v)", category.Measure, r.Certification)
	}
	if len(category.Actions) != 2 || category.Actions[0].ID != "label_messages" ||
		category.Actions[0].Params["count"] != fmt.Sprint(v.Remaining) || category.Actions[1].ID != "review_requirements" {
		t.Fatalf("category actions %+v", category.Actions)
	}
	if urgency == nil || urgency.Measure == nil || urgency.Measure.Unit != "evaluation labels" ||
		urgency.Measure.Have != 0 || urgency.Measure.Need != float64(uv.Requested) {
		t.Fatalf("urgency blocker %+v", urgency)
	}
	var judge, labels bool
	for _, a := range urgency.Actions {
		switch a.ID {
		case "run_judge":
			judge = a.Params["estimated_cost_usd"] != "" && a.Params["count"] == fmt.Sprint(uv.Remaining)
		case "label_messages":
			labels = a.Params["count"] == fmt.Sprint(uv.Remaining)
		}
	}
	if !judge || !labels {
		t.Fatalf("urgency actions %+v", urgency.Actions)
	}
	// A missing route record is the calibration stage's blocker; the gate
	// still judges what it has (no certification item), under the same
	// requirement_unmet code and plain-language cases.
	rewriteLastRun(t, w, func(rep *Report) { rep.Routes = nil })
	st, err = w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var missing *learning.Blocker
	for i := range st.Blockers {
		if st.Blockers[i].Subject == "category" {
			missing = &st.Blockers[i]
		}
	}
	if missing == nil || missing.Code != BlockRequirement || missing.Measure == nil ||
		missing.Measure.Unit != "certification labels" || missing.Measure.Have != 0 {
		t.Fatalf("a missing route record is not the gate's requirement_unmet: %+v", missing)
	}
}

// A route whose observed precision is below the target must not be handed a
// larger label request: more labels cannot raise the precision. The stage
// names the real remedies (re-run layer 1 or review the requirement), and
// the blocker is distinct from a too-small certification sample.
func TestBelowTargetRouteGetsAModelQualityBlocker(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	rep := w.run()
	r, ok := routeReportOf(rep, "category")
	if !ok || !r.Certification.Certified || r.Certification.Answered == 0 {
		t.Fatalf("category route %+v", r)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	groups := w.groupOf(t)
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	flipped := 0
	for _, l := range rows {
		if flipped >= 5 {
			break
		}
		if l.Question != "category" || !certifyLabel(plan, "category", l.Source) {
			continue
		}
		if !selective.InCertifyPart(groups[l.MessageID], plan.Config.CertifyFraction) {
			continue
		}
		if err := w.st.PutLabel(t.Context(), l.MessageID, "category", fmt.Sprintf("%q", w.wrongClass(t, l.MessageID)), l.Source); err != nil {
			t.Fatal(err)
		}
		flipped++
	}
	if flipped < 5 {
		t.Fatalf("only %d certification labels to flip", flipped)
	}
	if _, err := w.r.Rescore(t.Context()); err != nil {
		t.Fatal(err)
	}
	last, err := w.r.lastSucceeded(t.Context())
	if err != nil || last == nil {
		t.Fatalf("the re-scored report: %v %v", last, err)
	}
	r, _ = routeReportOf(*last, "category")
	if r.Certification.Certified || r.Certification.Answered == 0 {
		t.Fatalf("the below-target route still certified: %+v", r.Certification)
	}
	if precision := 1 - float64(r.Certification.Errors)/float64(r.Certification.Answered); precision >= plan.Requirements.For("category").TargetAccuracy {
		t.Fatalf("the fixture precision %.3f is not below the target", precision)
	}
	st, err := w.r.QuestionsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, ok := blockerOf(st, "category")
	if !ok || b.Code != BlockRequirement || b.Measure == nil || b.Measure.Unit != "observed precision" {
		t.Fatalf("the below-target blocker %+v", b)
	}
	if len(b.Actions) != 2 || b.Actions[0].ID != learning.ActionRunLayer1 ||
		b.Actions[1].ID != learning.ActionReviewRequirements {
		t.Fatalf("the below-target remedies %+v", b.Actions)
	}
	for _, action := range b.Actions {
		if action.ID == learning.ActionLabelMessages || action.ID == learning.ActionRunJudge {
			t.Fatalf("a below-target route still asks for labels: %+v", b.Actions)
		}
	}
}

// A report written under another route key cannot describe the stored
// route: the stage blocks before reading or counting rows from another
// configuration (an old report has no key at all and blocks too).
func TestRouteStageBlocksAReportOfAnotherRouteKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginLayer1Run(t.Context(), "inputs")
	if err != nil {
		t.Fatal(err)
	}
	rep := Report{RunID: run, State: runstate.Succeeded, Inputs: "inputs", RouteKey: "rk1-other"}
	body, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.CommitLayer1(t.Context(), store.Layer1Commit{RunID: run,
		Report:   func() (string, error) { return string(body), nil },
		Key:      store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: Decider},
		RouteKey: plan.RouteKey()}); err != nil {
		t.Fatal(err)
	}
	st, err := w.r.RouteStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != learning.Blocked {
		t.Fatalf("the stage state is %q, want blocked", st.State)
	}
	if len(st.Blockers) != 1 || st.Blockers[0].Code != BlockRouteNotRouted {
		t.Fatalf("the stale-report blockers %+v", st.Blockers)
	}
}
