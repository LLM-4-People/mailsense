package layer1

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// settled is one question's outcome of a run or a re-score: the model
// that answers it from now on (nil: none — untrainable, or retired) and
// its certification against the current labels.
type settled struct {
	q      *engine.Question
	model  *Model
	id     int64 // its stored row (0: none)
	meta   modelMeta
	cert   selective.Certification
	report QuestionReport // the question's measurement (its code, counts)
}

// outcome is what a run or a re-score commits.
type outcome struct {
	questions []*settled // in profile order
	promote   []int64
	retire    []string
	meta      map[int64]string       // re-scored thresholds of models already stored
	routes    map[string]route.State // the certified route of every question
}

// rescore certifies every settled model against the current evaluation
// labels (labels: question → message → label) and its question's
// requirement; a stored model whose threshold moved gets its meta
// rewritten in the commit.
func (o *outcome) rescore(plan *Plan, labels map[string]map[int64]evalLabel) {
	if o.meta == nil {
		o.meta = map[int64]string{}
	}
	for _, s := range o.questions {
		if s.model == nil {
			continue
		}
		s.cert = s.model.Holdout.certify(s.q.Type, labels[s.q.Name], plan.Profile.Cutoff(s.q.Name), plan.Requirements.For(s.q.Name))
		th := threshold(s.cert)
		moved := th != s.model.Threshold
		s.model.Threshold, s.meta.Threshold = th, th
		if moved && s.id > 0 && !slices.Contains(o.promote, s.id) {
			raw, _ := json.Marshal(s.meta)
			o.meta[s.id] = string(raw)
		}
	}
}

// commit answers every message with the settled models (answer gives a
// question's class distribution of a message, false: none) and writes
// the outcome, the answers, the route answers, the threshold-free route
// previews and the run's report in one transaction: a failure or a
// cancel promotes nothing and keeps the previous answers, routes and
// previews.
func (r *Runner) commit(ctx context.Context, plan *Plan, rep *Report, o *outcome, ids []int64, revision func(int64) int64,
	answer func(s *settled, id int64) ([]float64, bool), routes func(int64, map[string]engine.Answer) (string, error), step func(Step)) error {
	if step == nil {
		step = func(Step) {}
	}
	// The answer phase walks every message once: its decision is the one
	// inference output, and the route answer and threshold-free preview are
	// derived from that decision under the commit transaction (store
	// Layer1Answers.Derive), so the progress bar counts messages and the
	// disk holds one staged copy instead of three.
	total, done := len(ids), 0
	tick := func() {
		done++
		if done%500 == 0 || done == total {
			step(Step{Stage: StageAnswer, Done: done, Total: total})
		}
	}
	step(Step{Stage: StageAnswer, Done: 0, Total: total})
	var keys []string
	for _, s := range o.questions {
		if s.model != nil {
			keys = append(keys, s.q.Name+"="+strconv.FormatInt(s.id, 10)+"@"+strconv.FormatFloat(s.model.Threshold, 'g', -1, 64))
		}
	}
	state := digest.Hex([]byte(strings.Join(keys, ",")), 16)
	rep.Answered, rep.Routed, rep.Verdicts, rep.Retired = 0, 0, verdicts(plan, o.questions), o.retire
	// The preview copy is read only while the explicit test mode is on. A
	// commit with test mode off writes none (and CommitLayer1 clears the
	// table), so the disk never carries a second full answer copy for a
	// screen nobody can open.
	previewOn := r.cfg.Preview()
	rows := func(fn func(store.Layer1Answer) error) error {
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			tick()
			answers := map[string]engine.Answer{}
			for _, s := range o.questions {
				if s.model == nil {
					continue
				}
				if p, ok := answer(s, id); ok {
					answers[s.q.Name] = selective.Answer(s.q, p, plan.Profile.Cutoff(s.q.Name), s.model.Threshold)
				}
			}
			if len(answers) == 0 {
				continue
			}
			raw, err := json.Marshal(answers)
			if err != nil {
				return err
			}
			rep.Answered++
			if err := fn(store.Layer1Answer{MessageID: id, Revision: revision(id), RunID: rep.RunID, StateHash: state, Decision: string(raw)}); err != nil {
				return err
			}
		}
		return nil
	}
	// derive rebuilds a message's route answer and threshold-free preview
	// from its staged decision. The decision is the JSON of the answers the
	// inference pass produced, so the distributions (and the previews) are
	// the same bits; deriving them here keeps the two tables off the
	// staging disk and inside the commit's rollback.
	derive := func(row *store.Layer1Answer) error {
		var answers map[string]engine.Answer
		if err := json.Unmarshal([]byte(row.Decision), &answers); err != nil {
			return fmt.Errorf("layer1: message %d: read the staged decision: %w", row.MessageID, err)
		}
		if routes != nil {
			route, err := routes(row.MessageID, answers)
			if err != nil {
				return err
			}
			if route != "" {
				rep.Routed++
				row.Route = route
			}
		}
		if !previewOn {
			return nil
		}
		preview := make(map[string]engine.Answer, len(answers))
		for _, s := range o.questions {
			if s.model == nil {
				continue
			}
			a, ok := answers[s.q.Name]
			if !ok {
				continue
			}
			p, ok := selective.Probs(s.q, a)
			if !ok {
				return fmt.Errorf("layer1: message %d: question %s: the decision holds no distribution", row.MessageID, s.q.Name)
			}
			preview[s.q.Name] = selective.Given(s.q, p, plan.Profile.Cutoff(s.q.Name))
		}
		if len(preview) > 0 {
			raw, err := json.Marshal(preview)
			if err != nil {
				return err
			}
			row.Preview = string(raw)
		}
		return nil
	}
	report := func() (string, error) {
		body, err := json.Marshal(rep.stored())
		return string(body), err
	}
	return r.st.CommitLayer1(ctx, store.Layer1Commit{RunID: rep.RunID, Report: report, Promote: o.promote, Retire: o.retire, Meta: o.meta,
		Key:      store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: Decider},
		RouteKey: plan.RouteKey(),
		Answers:  &store.Layer1Answers{Each: rows, Derive: derive, Routes: routes != nil, Preview: previewOn}})
}

// settleTrained decides each question's model after training: the
// candidate is stored and promoted unless the active model is kept
// (keepActive); a question without a candidate (no teacher, a single
// class) retires its active model — it never answers with a model of
// targets it no longer has.
func (r *Runner) settleTrained(ctx context.Context, plan *Plan, tr *Trained, active map[string]*activeModel, rep *Report, step func(Step)) (*outcome, error) {
	if step == nil {
		step = func(Step) {}
	}
	maxReg := r.cfg.Float(SettingMaxNLLRegression)
	reports := map[string]QuestionReport{}
	for _, qr := range tr.Reports {
		reports[qr.Question] = qr
	}
	o := &outcome{}
	total := len(plan.Profile.Questions)
	for i := range plan.Profile.Questions {
		step(Step{Stage: StageSettle, Done: i, Total: total, StepDone: i, StepTotal: total})
		q := &plan.Profile.Questions[i]
		qr := reports[q.Name]
		s := &settled{q: q, report: qr}
		o.questions = append(o.questions, s)
		cand, cur := tr.Models[q.Name], active[q.Name]
		fits := cur != nil && cur.fits(tr, SpecOf(q), classesOf(q))
		if cand == nil {
			if cur != nil {
				o.retire = append(o.retire, q.Name)
			}
			continue
		}
		if why := keepActive(cur, fits, qr.OOF, maxReg); why != "" {
			rep.Kept[q.Name] = why
			s.model, s.id, s.meta = cur.model, cur.id, cur.meta
			s.report.Effective, s.report.Train = cur.meta.Effective, cur.meta.Train // the measurement of the model that answers
			continue
		}
		meta := metaOf(cand, qr)
		metaJSON, _ := json.Marshal(meta)
		metrics, _ := json.Marshal(qr.OOF)
		// The version key names everything that makes the head the same head:
		// the pass's inputs and labels, the question, and the evidence layout
		// its cells come from. Without the layout, a layout change that leaves
		// the inputs' fingerprint unchanged (a bins setting) would collide
		// with the active row of another layout and be dropped, leaving a head
		// that reads rows it was not trained on.
		key := digest.Hex([]byte(rep.Inputs+"\x00"+strings.Join(rep.Labels, ",")+"\x00"+qr.Question+"\x00"+tr.Layout.ID), 16)
		id, err := r.st.SaveLayer1Candidate(ctx, store.Layer1Model{Question: qr.Question, VersionKey: key, Kind: string(cand.Kind),
			Classes: cand.Classes, Params: cand.Encode(), Meta: string(metaJSON), Metrics: string(metrics), RunID: rep.RunID})
		if err != nil {
			return nil, err
		}
		s.model, s.id, s.meta = cand, id, meta
		if cur == nil || cur.id != id { // the same version already active: nothing to promote
			o.promote = append(o.promote, id)
			rep.Promoted = append(rep.Promoted, q.Name)
		}
	}
	step(Step{Stage: StageSettle, Done: total, Total: total, StepDone: total, StepTotal: total})
	labels := map[string]map[int64]evalLabel{}
	for name, qs := range tr.sets {
		labels[name] = qs.labels()
	}
	o.rescore(plan, labels)
	// The route is built from the models that answer (never the kept
	// candidates), after every threshold settled.
	states, err := routeStates(ctx, plan, tr, o.questions)
	if err != nil {
		return nil, err
	}
	o.routes, rep.Routes = states, routeReports(plan, states)
	return o, nil
}
