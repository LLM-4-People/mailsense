package layer1

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// TestTrainReportsStages: a pass tells the job where it is, from the design
// to the certification, with the overall question position on every step.
func TestTrainReportsStages(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var steps []Step
	plan.Step = func(s Step) { steps = append(steps, s) }
	if _, err := Train(t.Context(), plan, w.r.in, nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range steps {
		seen[s.Stage] = true
		if s.Total != len(plan.Profile.Questions) {
			t.Fatalf("step %+v: total %d, want %d", s, s.Total, len(plan.Profile.Questions))
		}
	}
	for _, stage := range []string{StageDesign, StageGather, StageFit, StageFold, StageMeasure, StageCertify} {
		if !seen[stage] {
			t.Fatalf("no %q step in %v", stage, steps)
		}
	}
	last := steps[len(steps)-1]
	if last.Done != last.Total {
		t.Fatalf("last step is not the finished pass: %+v", last)
	}
}

// TestLiveProgressIsVisibleWhileRunning: the learning stage reads the
// runner's live position while a run holds the slot, and forgets it once
// the run ends.
func TestLiveProgressIsVisibleWhileRunning(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	if _, ok := r.liveProgress(); ok {
		t.Fatal("a fresh runner reports live progress")
	}
	r.reportProgress(jobs.Progress{Done: 1, Total: 5, Unit: "questions", Stage: StageFold, Detail: "topic"}, nil)
	got, ok := r.liveProgress()
	if !ok || got.Stage != StageFold || got.Detail != "topic" || got.Done != 1 || got.Total != 5 {
		t.Fatalf("live progress %+v (ok %v)", got, ok)
	}
	r.clearLive()
	if _, ok := r.liveProgress(); ok {
		t.Fatal("live progress survived the run")
	}
}

// TestRunReportsTheAnswerPhase: after the questions, the run reports the
// answer phase with its own denominator, so the job does not sit at 100%
// while it writes every message.
func TestRunReportsTheAnswerPhase(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	var reports []jobs.Progress
	if _, err := w.r.Run(t.Context(), func(p jobs.Progress) { reports = append(reports, p) }); err != nil {
		t.Fatal(err)
	}
	var answerTotal, lastAnswer int64
	answerUnit := ""
	seen := map[string]bool{}
	for _, p := range reports {
		seen[p.Stage] = true
		if p.Stage == StageAnswer {
			answerTotal, lastAnswer, answerUnit = p.Total, p.Done, p.Unit
		}
	}
	if !seen[StageAnswer] || answerTotal == 0 {
		t.Fatalf("no answer-phase progress in %d reports (stages %v)", len(reports), seen)
	}
	// One message is one unit of answer work: decisions, route answers and
	// previews come from the same pass, never three times the mailbox.
	if answerTotal != int64(len(w.ids)) {
		t.Fatalf("the answer phase reported %d items for %d messages", answerTotal, len(w.ids))
	}
	if lastAnswer != answerTotal {
		t.Fatalf("the answer phase ended at %d/%d", lastAnswer, answerTotal)
	}
	// The unit is the producer's, not a second hand-maintained fact: the
	// answer phase counts messages, the question phases count questions.
	if answerUnit != "messages" {
		t.Fatalf("the answer phase reported unit %q, want messages", answerUnit)
	}
	for _, p := range reports {
		if p.Stage != StageAnswer && p.Unit != "questions" {
			t.Fatalf("stage %q reported unit %q, want questions", p.Stage, p.Unit)
		}
	}
}

// TestStageCopiesTheLiveUnitAndStep: the Learning stage reports the same
// numbers as the job chip — the producer's unit and the step position —
// instead of hardcoding "questions" while the answer phase counts messages.
func TestStageCopiesTheLiveUnitAndStep(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	w.r.reportProgress(jobs.Progress{Done: 1500, Total: 104289, Unit: "messages",
		Stage: StageAnswer, Detail: "category", StepDone: 2, StepTotal: 3}, nil)
	st, err := w.r.Stage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Progress.Unit != "messages" || st.Progress.Done != 1500 || st.Progress.Total != 104289 {
		t.Fatalf("stage progress %+v", st.Progress)
	}
	if st.Progress.StepDone != 2 || st.Progress.StepTotal != 3 {
		t.Fatalf("stage step %+v", st.Progress)
	}
}
