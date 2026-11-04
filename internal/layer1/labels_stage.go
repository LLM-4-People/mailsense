package layer1

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// LabelsStage is the learning stage "labels": every profile question needs
// evaluation labels from the sources its requirement certifies against
// (manual and judge by default). It waits for the stages it reads, counts
// one label per message and question (the first evaluation source with a
// value valid under the current profile), and blocks each short question
// with the manual and judge remedies. It is done when every question that
// can gain labels has its requirement's labels; a question whose answers
// cannot reach the target is not asked for more labels (the questions
// stage owns that quality blocker).
func (r *Runner) LabelsStage(after ...learning.Stage) learning.Stage {
	return &labelsStage{r: r, after: after}
}

type labelsStage struct {
	r     *Runner
	after []learning.Stage
}

// labelRows is every stored label, read once per status build. The
// returned rows are shared read-only by the build's stages.
func (r *Runner) labelRows(ctx context.Context) ([]store.LabelRow, error) {
	return learning.Memo(ctx, "layer1.labels", func(ctx context.Context) ([]store.LabelRow, error) {
		return r.in.Labels(ctx)
	})
}

func (s *labelsStage) ID() string { return learning.StageLabels }

func (s *labelsStage) Status(ctx context.Context) (learning.StageStatus, error) {
	out := learning.StageStatus{State: learning.Running, Progress: learning.Progress{Unit: learning.UnitEvalLabels}}
	if wait, ready, err := learning.WaitFor(ctx, s.after...); err != nil {
		return out, err
	} else if !ready {
		return wait, nil
	}
	plan, err := s.r.plan(ctx)
	if err != nil {
		out.State, out.Blockers = learning.Blocked, []learning.Blocker{configBlocker(err)}
		return out, nil
	}
	memberIDs, err := s.r.in.MemberIDs(ctx)
	if err != nil {
		return out, err
	}
	rows, err := s.r.labelRows(ctx)
	if err != nil {
		return out, err
	}
	labels := labelIndex(rows)
	fraction := plan.Config.CertifyFraction
	// The per-question request follows the route certification, the same
	// one the questions stage judges: the route is what answers. The
	// layer-1 verdict certification is the training-stage diagnostic. An
	// old stored report with no route entry falls back to its verdict and
	// says so in the note.
	var certified map[string]selective.Certification
	var fallbackCertified map[string]selective.Certification
	fallback := 0
	if s.r.st != nil {
		if last, _ := s.r.lastSucceeded(ctx); last != nil {
			certified, _ = s.r.certifications(ctx, last)
			for _, v := range last.Verdicts {
				if _, ok := certified[v.Question]; !ok {
					// The memoized map is shared read-only: keep the
					// verdict fallbacks in an overlay of their own.
					if fallbackCertified == nil {
						fallbackCertified = map[string]selective.Certification{}
					}
					fallbackCertified[v.Question] = v.Certification
					fallback++
				}
			}
		}
	}
	satisfied, unfixable := 0, 0
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		req := plan.Requirements.For(q.Name)
		cert, ok := certified[q.Name]
		if !ok {
			cert = fallbackCertified[q.Name]
		}
		need := req.LabelNeed(fraction, cert)
		if !need.Fixable {
			// More labels cannot certify this route: the questions stage
			// reports the precision blocker with the re-run/review remedies.
			// The labels stage neither counts it in its progress nor asks
			// for labels the judge budget cannot turn into a verdict.
			unfixable++
			continue
		}
		sources := plan.Teachers.For(q.Name)
		have := 0
		for _, id := range memberIDs {
			l, ok := evalOf(q, sources, labels, id, plan.Profile.Cutoff(q.Name))
			if !ok || !slices.Contains(req.CertifySources, l.source) {
				continue
			}
			have++
		}
		out.Progress.Total += int64(need.Need)
		out.Progress.Done += int64(min(have, need.Need))
		if have >= need.Need {
			satisfied++
			continue
		}
		remaining := need.Need - have
		out.Blockers = append(out.Blockers, learning.Blocker{
			Code:    BlockTooFewEval,
			Subject: q.Name,
			Detail: fmt.Sprintf("This question has %d of the %d evaluation labels it needs; %d more are needed.",
				have, need.Need, remaining),
			Measure: &learning.Measure{Have: float64(have), Need: float64(need.Need), Unit: learning.UnitEvalLabels},
			Actions: []learning.Action{
				learning.Do(learning.ActionLabelMessages, "question", q.Name, "count", strconv.Itoa(remaining)),
				s.r.judgeAction(q.Name, remaining),
				learning.Do(learning.ActionReviewRequirements, "question", q.Name),
			},
		})
	}
	// The gate is per question: one question with its labels is enough
	// for the stage to be done; the others stay as warnings so the gate
	// opens for the questions that can be certified instead of waiting
	// for every question of the profile. The auto-label loop reads the
	// warnings and keeps labelling.
	out.Note = fmt.Sprintf("%d of %d questions have their evaluation labels", satisfied, len(plan.Profile.Questions))
	if unfixable > 0 {
		out.Note += fmt.Sprintf("; %d cannot be certified by more labels (the questions stage reports why)", unfixable)
	}
	if fallback > 0 {
		out.Note += fmt.Sprintf("; %d questions have no route certification in the last report (using the layer-1 diagnostic)", fallback)
	}
	// No remaining question can gain labels: the labels stage is done even
	// when none is satisfied, and the note above names the other stage that
	// owns the quality blocker. Never block with an empty blocker list.
	if satisfied > 0 || len(out.Blockers) == 0 {
		out.State = learning.Done
		return out, nil
	}
	out.State = learning.Blocked
	return out, nil
}
