package layer1

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// Stage is the learning stage "layer1": pending until the stages it
// learns from (after: evidence and embeddings) are done; blocked with
// BlockConfig while the teachers or requirements cannot be read; running
// while the inputs differ from the last run's (or enough labels changed,
// or evaluation labels changed: a re-score); blocked when the last run
// failed or a question cannot be trained (no teacher, too few labels, a
// single class, too few evaluation labels), each blocker with its
// question as subject, its plain-language detail, its measure and its
// actions; done otherwise.
func (r *Runner) Stage(after ...learning.Stage) learning.Stage {
	return &stage{r: r, id: learning.StageLayer1, after: after}
}

// QuestionsStage is the learning stage "questions": done when every
// question's route is certified at its target and covers at least its
// requirement's minimum (the route that answers is what the gate judges;
// the layer-1 model's own verdict is kept in the report for diagnostics);
// blocked with requirement_unmet otherwise. The gate never relaxes on its
// own; any change of an evaluation label voids the verdicts until they
// are re-scored.
func (r *Runner) QuestionsStage(after ...learning.Stage) learning.Stage {
	return &stage{r: r, id: learning.StageQuestions, after: after}
}

type stage struct {
	r     *Runner
	id    string
	after []learning.Stage
}

func (s *stage) ID() string { return s.id }

func (s *stage) Status(ctx context.Context) (learning.StageStatus, error) {
	out := learning.StageStatus{State: learning.Running, Progress: learning.Progress{Unit: learning.UnitQuestions}}
	// A run in progress answers from its live progress, before anything
	// else: it is the truth even when the stages it learns from are not
	// done yet (a manual or triggered run), and a progress refresh must not
	// re-read the last report and re-fingerprint the whole store per step.
	if live, ok := s.r.liveProgress(); ok {
		out.State = learning.Running
		out.Note = "training"
		out.Progress = learning.Progress{Done: live.Done, Total: live.Total, Unit: live.Unit,
			Stage: live.Stage, Detail: live.Detail, StepDone: live.StepDone, StepTotal: live.StepTotal}
		return out, nil
	}
	if wait, ready, err := learning.WaitFor(ctx, s.after...); err != nil {
		return out, err
	} else if !ready {
		return wait, nil
	}
	lr, err := s.r.lastRun(ctx)
	if err != nil {
		return out, err
	}
	last := lr.report
	switch {
	case lr.planErr != nil:
		out.State, out.Blockers = learning.Blocked, []learning.Blocker{configBlocker(lr.planErr)}
		return out, nil
	case last == nil:
		out.Note = "not trained yet"
		return out, nil
	case last.State == runstate.Running:
		out.Note = "training"
		// The run row is still open: the live progress the runner keeps is
		// the only true position (the stored report is written at the end).
		if live, ok := s.r.liveProgress(); ok {
			out.Progress = learning.Progress{Done: live.Done, Total: live.Total, Unit: live.Unit,
				Stage: live.Stage, Detail: live.Detail, StepDone: live.StepDone, StepTotal: live.StepTotal}
		}
		return out, nil
	case lr.freshness == relabelled:
		out.Note = "evaluation labels changed: re-scoring the verdicts"
		return out, nil
	case lr.freshness == stale:
		out.Note = "its inputs changed since the last run"
		return out, nil
	case last.State == runstate.Cancelled:
		out.Note = "the last run was cancelled"
		return out, nil
	case last.State != runstate.Succeeded:
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{{Code: BlockRunFailed, Detail: "The last training run failed: " + last.Error,
			Actions: []learning.Action{learning.Do(learning.ActionReviewFailures, "log", "layer1"), learning.Do(learning.ActionRunLayer1)}}}
		return out, nil
	}
	if s.id == learning.StageQuestions {
		return s.questionsStatus(ctx, last, out)
	}
	out.Progress.Total = int64(len(last.Verdicts))
	for _, v := range last.Verdicts {
		b, ok := s.blocker(v)
		if !ok {
			out.Progress.Done++
			continue
		}
		out.Blockers = append(out.Blockers, b)
	}
	// One trained question makes the stage done: the remaining questions
	// stay as warnings, and the questions stage certifies each answer on
	// its own. Blocking the whole app on a weak or under-labelled
	// question is what kept the gate shut at 2 of 17 certified.
	out.Note = fmt.Sprintf("%d of %d questions trained", out.Progress.Done, out.Progress.Total)
	if out.Progress.Done > 0 {
		out.State = learning.Done
		return out, nil
	}
	out.State = learning.Blocked
	return out, nil
}

// questionsStatus judges the route: every question's route certification
// (from the last succeeded pass) decides whether it meets its requirement.
// The layer-1 verdict supplies the evaluation-label count and diagnostic
// fields, never the gate.
func (s *stage) questionsStatus(ctx context.Context, last *Report, out learning.StageStatus) (learning.StageStatus, error) {
	plan, err := s.r.plan(ctx)
	if err != nil {
		out.State, out.Blockers = learning.Blocked, []learning.Blocker{configBlocker(err)}
		return out, nil
	}
	certified, err := s.r.certifications(ctx, last)
	if err != nil {
		return out, err
	}
	out.Progress.Total = int64(len(last.Verdicts))
	// The status carries the route report itself, including the per-class
	// recall and the collapsed-class flag. It is additive diagnostics: the
	// same certifications below still decide the gate.
	for i := range plan.Profile.Questions {
		name := plan.Profile.Questions[i].Name
		if c, ok := certified[name]; ok {
			out.Certifications = append(out.Certifications, learning.QuestionCertification{Question: name, Certification: c})
		}
	}
	for _, v := range last.Verdicts {
		req := plan.Requirements.For(v.Question)
		need := req.LabelNeed(plan.Config.CertifyFraction, certified[v.Question])
		b, blocked := s.routeBlocker(v, certified[v.Question], need)
		if !blocked {
			out.Progress.Done++
			continue
		}
		out.Blockers = append(out.Blockers, b)
	}
	// The gate is per question: every certified answer is usable even
	// when another question cannot certify yet. The uncertified ones stay
	// as warnings (and abstain at answer time); the auto-label loop keeps
	// working on them. Only a pass with no certified question keeps the
	// gate shut.
	out.Note = fmt.Sprintf("%d of %d questions certified at the target", out.Progress.Done, out.Progress.Total)
	if out.Progress.Done > 0 {
		out.State = learning.Done
		return out, nil
	}
	out.State = learning.Blocked
	return out, nil
}

// configBlocker is the blocker of a plan that does not load, with the
// remedy of the file it comes from.
func configBlocker(err error) learning.Blocker {
	b := learning.Blocker{Code: BlockConfig, Detail: "Layer 1 cannot read its configuration: " + err.Error()}
	switch brokenFile(err) {
	case SettingTeachersFile:
		b.Subject = SettingTeachersFile
		b.Actions = []learning.Action{learning.Do(learning.ActionChooseTeacher)}
	case SettingRequirementsFile:
		b.Subject = SettingRequirementsFile
		b.Actions = []learning.Action{learning.Do(learning.ActionReviewRequirements)}
	default:
		b.Actions = []learning.Action{learning.Do(learning.ActionReviewFailures, "log", "layer1")}
	}
	return b
}

// codeText is each question blocker's plain-language explanation.
var codeText = map[string]string{
	BlockNoTeacher:    "No teacher gives this question a training target yet.",
	BlockSingleClass:  "Every label and target of this question gives the same answer: there is nothing to tell apart yet.",
	BlockTooFewLabels: "Too few training examples for this question.",
	BlockTooFewEval:   "Too few evaluation labels to measure this question.",
	BlockRunFailed:    "Training this question failed.",
}

// blocker is a verdict's blocker in the layer-1 stage (false: none here).
// The questions stage judges the route instead (routeBlocker).
func (s *stage) blocker(v Verdict) (learning.Blocker, bool) {
	q := v.Question
	b := learning.Blocker{Code: v.Code, Subject: q}
	labels := &learning.Measure{Have: float64(v.Eval), Need: float64(v.Requested), Unit: learning.UnitEvalLabels}
	// A label or judge purchase is only offered when the labels-stage plan
	// still has labels to buy. An unfixable question (Remaining 0: no label
	// count can certify it) keeps its measure and gets the re-run/review
	// remedies instead of a "label 0" action or a paid judge call.
	var ask []learning.Action
	if v.Remaining > 0 {
		ask = []learning.Action{s.r.judgeAction(q, v.Remaining), learning.Do(learning.ActionLabelMessages, "question", q, "count", strconv.Itoa(v.Remaining))}
	}
	recheck := []learning.Action{learning.Do(learning.ActionRunLayer1), learning.Do(learning.ActionReviewRequirements, "question", q)}
	switch v.Code {
	case "", BlockRequirement:
		return b, false
	}
	b.Detail = codeText[v.Code]
	switch v.Code {
	case BlockNoTeacher:
		b.Actions = append([]learning.Action{learning.Do(learning.ActionChooseTeacher, "question", q)}, ask...)
	case BlockTooFewLabels:
		b.Measure = &learning.Measure{Have: v.Effective, Need: v.RequiredTrain, Unit: "effective training examples"}
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
	case BlockTooFewEval:
		b.Measure, b.Actions = labels, ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
	case BlockSingleClass:
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		} else {
			b.Actions = b.Actions[1:]
		}
	default:
		b.Actions = []learning.Action{learning.Do(learning.ActionReviewFailures, "log", "layer1"), learning.Do(learning.ActionRunLayer1)}
	}
	return b, true
}

// routeBlocker is one question's route blocker against its requirement,
// with the first of the five plain-language cases that applies (the same
// order as the design): no evaluation label at all, a certification part
// too small to certify, too few certified answers, a precision lower
// bound below the target, or a certified coverage below the minimum. No
// detail carries a code.
func (s *stage) routeBlocker(v Verdict, cert selective.Certification, labelNeed LabelNeed) (learning.Blocker, bool) {
	q := v.Question
	b := learning.Blocker{Code: BlockRequirement, Subject: q}
	// The label count and its actions come from the route's LabelNeed plan,
	// never the layer-1 verdict's diagnostic Requested/Remaining. A route
	// whose precision is below target is unfixable and offers no purchase.
	remaining := max(0, labelNeed.Need-v.Eval)
	var ask []learning.Action
	if labelNeed.Fixable && remaining > 0 {
		ask = []learning.Action{
			learning.Do(learning.ActionLabelMessages, "question", q, "count", strconv.Itoa(remaining)),
			learning.Do(learning.ActionReviewRequirements, "question", q),
		}
	}
	recheck := []learning.Action{learning.Do(learning.ActionRunLayer1), learning.Do(learning.ActionReviewRequirements, "question", q)}
	c := cert
	precision := 1.0
	if c.Answered > 0 {
		precision = c.AnsweredPrecision()
	}
	// How many answers the observed precision needs, not how many a perfect
	// rule would need: a 97 % model needs more labels than a 99 % one, and a
	// model below the target cannot be bought into it at all.
	needed := neededAt(precision, v.Target, v.Z, maxLabelSearch)
	switch {
	case v.Eval == 0:
		b.Detail = "No evaluation label of this question exists yet, so none of its answers can be certified. Label messages first."
		b.Measure = &learning.Measure{Have: 0, Need: float64(labelNeed.Need), Unit: learning.UnitEvalLabels}
		if labelNeed.Fixable && remaining > 0 {
			b.Actions = append([]learning.Action{ask[0], ask[1]}, s.r.judgeAction(q, remaining))
		} else {
			b.Actions = recheck
		}
		return b, true
	case c.Answered > 0 && precision < v.Target:
		b.Detail = fmt.Sprintf("Its answers agree with the certification labels on %.1f%% of the %d it answered; %.2f is required. More labels cannot raise that precision: re-run layer 1 or review the requirement.",
			100*precision, c.Answered, v.Target)
		b.Measure = &learning.Measure{Have: precision, Need: v.Target, Unit: "observed precision"}
		b.Actions = []learning.Action{
			learning.Do(learning.ActionRunLayer1),
			learning.Do(learning.ActionReviewRequirements, "question", q),
		}
		return b, true
	case c.Eligible < needed:
		b.Detail = fmt.Sprintf("The certification labels number %d; a target of %.2f at z %.2f needs %d answered labels, so this question cannot certify yet.",
			c.Eligible, v.Target, v.Z, needed)
		b.Measure = &learning.Measure{Have: float64(c.Eligible), Need: float64(needed), Unit: "certification labels"}
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
		return b, true
	case c.Answered < needed:
		b.Detail = fmt.Sprintf("Its route answered %d of %d certification labels; %d answered labels are needed at %.2f.",
			c.Answered, c.Eligible, needed, v.Target)
		b.Measure = &learning.Measure{Have: float64(c.Answered), Need: float64(needed), Unit: "certified answers"}
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
		return b, true
	case !c.Certified:
		b.Detail = fmt.Sprintf("Its answers agree with the certification labels at a lower bound of %.3f (%d answers, %d wrong); %.2f is required.",
			c.Lower, c.Answered, c.Errors, v.Target)
		b.Measure = &learning.Measure{Have: c.Lower, Need: v.Target, Unit: "precision lower bound"}
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
		return b, true
	case !selective.Answering(c, v.Required):
		have := c.EffectiveCoverage()
		b.Detail = fmt.Sprintf("Its route is certified on an effective coverage of %.0f%% of the certification labels at precision %.2f; %.0f%% is required.",
			100*have, v.Target, 100*v.Required)
		b.Measure = &learning.Measure{Have: have, Need: v.Required, Unit: "certified coverage"}
		b.Actions = ask
		if len(b.Actions) == 0 {
			b.Actions = recheck
		}
		return b, true
	}
	return b, false
}

// judgeAction is the paid run_judge action for count labels, with its
// provider and cost estimate (every paid action carries one).
func (r *Runner) judgeAction(question string, count int) learning.Action {
	cost, note := "unknown", "the configured judge provider could not be priced (see judge.provider)"
	if r.judgeCost != nil {
		if usd, ok := r.judgeCost(count); ok {
			cost, note = usdText(usd), ""
		}
	}
	a := learning.Do(learning.ActionRunJudge, "question", question, "count", strconv.Itoa(count),
		"provider", r.cfg.String(judge.SettingProvider), "estimated_cost_usd", cost)
	if note != "" {
		a.Params["cost_note"] = note
	}
	return a
}

// usdText is a paid action's estimate: two decimals, but never "0.00" for
// a nonzero cost (a one-message judge call is a fraction of a cent).
func usdText(usd float64) string {
	if usd > 0 && usd < 0.005 {
		return strconv.FormatFloat(usd, 'f', 4, 64)
	}
	return strconv.FormatFloat(usd, 'f', 2, 64)
}

// freshness is how the last run stands against the current inputs.
type freshness int

const (
	stale      freshness = iota // a training run is needed
	current                     // nothing changed that matters
	relabelled                  // only evaluation labels changed: a re-score
)

// lastState is the last run and how it stands.
type lastState struct {
	report    *Report
	freshness freshness
	planErr   error // the configuration does not load (BlockConfig)
}

// lastRun is the last run's report and how it stands against the current
// inputs: stale when the fingerprint differs or at least
// layer1.retrain.min_new_labels label rows changed; relabelled when fewer
// changed but one of them is an evaluation label (its verdicts are void
// until re-scored); current otherwise. One status build reads it once.
func (r *Runner) lastRun(ctx context.Context) (lastState, error) {
	return learning.Memo(ctx, "layer1.lastRun", r.readLastRun)
}

// readLastRun is lastRun's uncached body. The returned report is shared
// read-only by every caller of the build.
func (r *Runner) readLastRun(ctx context.Context) (lastState, error) {
	out := lastState{}
	plan, err := r.plan(ctx)
	if err != nil {
		out.planErr = err
	}
	runs, err := r.statusStore().ListLayer1Runs(ctx, 1)
	if err != nil || len(runs) == 0 {
		return out, err
	}
	if out.report, err = decodeRun(runs[0].ID, runs[0].State, runs[0].Error, runs[0].Inputs, runs[0].Report); err != nil || plan == nil {
		return out, err
	}
	inputs, labels, err := r.fingerprint(ctx, plan)
	if err != nil {
		return out, err
	}
	rep := out.report
	switch {
	case inputs != rep.Inputs || changed(rep.Labels, labels) >= r.cfg.Int(SettingRetrainLabels):
		out.freshness = stale
	case changed(evalOnly(plan, rep.Labels), evalOnly(plan, labels)) > 0:
		out.freshness = relabelled
	default:
		out.freshness = current
	}
	return out, nil
}

// lastSucceeded is the newest succeeded run's report (nil: none), read
// once per status build. The report is shared read-only.
func (r *Runner) lastSucceeded(ctx context.Context) (*Report, error) {
	return learning.Memo(ctx, "layer1.lastSucceeded", func(ctx context.Context) (*Report, error) {
		runs, err := r.statusStore().ListLayer1Runs(ctx, 50)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.State == runstate.Succeeded {
				return decodeRun(run.ID, run.State, run.Error, run.Inputs, run.Report)
			}
		}
		return nil, nil
	})
}

// certifications is one report's route certifications, read once per
// build and keyed by the report's run: after a failed pass the newest run
// and the newest succeeded run differ, and their certifications must not
// be shared. A report stored before the run id was written has RunID 0;
// it falls back to its identity so two such reports never share. The
// returned map is read-only for every caller.
func (r *Runner) certifications(ctx context.Context, last *Report) (map[string]selective.Certification, error) {
	name := fmt.Sprintf("layer1.routeCertifications.%d", last.RunID)
	if last.RunID == 0 {
		name = fmt.Sprintf("layer1.routeCertifications.%p", last)
	}
	return learning.Memo(ctx, name, func(context.Context) (map[string]selective.Certification, error) {
		return routeCertifications(last), nil
	})
}

func decodeRun(id int64, state runstate.State, errMsg, inputs, report string) (*Report, error) {
	rep := &Report{State: state, Error: errMsg, Inputs: inputs}
	if state != runstate.Running && report != "" {
		if err := json.Unmarshal([]byte(report), rep); err != nil {
			return nil, fmt.Errorf("layer1: run %d report: %w", id, err)
		}
	}
	return rep, nil
}

// evalOnly keeps the label digests of the evaluation sources' labels.
func evalOnly(plan *Plan, digests []string) []string {
	eval := map[string]bool{}
	for _, s := range plan.Teachers.Sources {
		if s.Eval {
			eval[s.LabelSource] = true
		}
	}
	for _, q := range plan.Profile.Questions {
		for _, s := range plan.Teachers.For(q.Name) {
			if s.Eval {
				eval[s.LabelSource] = true
			}
		}
	}
	var out []string
	for _, d := range digests {
		if src, _, ok := strings.Cut(d, ":"); ok && eval[src] {
			out = append(out, d)
		}
	}
	return out
}

// changed counts the label rows in one list and not the other.
func changed(a, b []string) int {
	in := map[string]int{}
	for _, x := range a {
		in[x]++
	}
	n := 0
	for _, x := range b {
		if in[x] > 0 {
			in[x]--
		} else {
			n++
		}
	}
	for _, c := range in {
		n += c
	}
	return n
}
