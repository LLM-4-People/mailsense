package layer1

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Rescore re-certifies the active models against the current evaluation
// labels without training (rules review #2: a corrected label changes
// the verdicts at once): each model's holdout predictions are scored on
// the labels as they stand now, its threshold moves accordingly, and its
// stored answers are re-decided from their own distributions — all in one
// commit. When the inputs differ from the last successful run's (or no
// run succeeded) it trains instead.
func (r *Runner) Rescore(ctx context.Context) (Report, error) {
	if !r.single.Begin() {
		return Report{State: runstate.Failed, Error: ErrBusy.Error()}, ErrBusy
	}
	defer r.single.End()
	return r.rescore(ctx, nil)
}

func (r *Runner) rescore(ctx context.Context, report func(stage string, done, total int64)) (Report, error) {
	if report == nil {
		report = func(string, int64, int64) {}
	}
	plan, err := NewPlan(r.cfg)
	if err != nil {
		return r.run(ctx, nil) // records the broken configuration
	}
	inputs, labels, err := r.fingerprint(ctx, plan)
	if err != nil {
		return Report{State: runstate.Failed, Error: err.Error()}, err
	}
	report("reading the last run", 0, 0)
	last, err := r.lastSucceeded(ctx)
	if err != nil {
		return Report{State: runstate.Failed, Error: err.Error()}, err
	}
	if last == nil || last.Inputs != inputs {
		return r.run(ctx, nil)
	}
	start := r.clock.Now()
	rep := Report{Kept: map[string]string{}, Rescored: true, Inputs: inputs, RouteKey: plan.RouteKey(), Labels: labels, Space: last.Space, Layout: last.Layout,
		Questions: last.Questions, Pending: last.Pending}
	if rep.RunID, err = r.st.BeginLayer1Run(ctx, inputs); err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	active, err := r.active(ctx)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	rows, err := r.in.Labels(ctx)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	report("measuring the labels", 0, int64(len(rows)))
	now := evalLabelsNow(plan, rows)
	o := &outcome{}
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		s := &settled{q: q}
		for _, qr := range last.Questions {
			if qr.Question == q.Name {
				s.report = qr
			}
		}
		s.report.Eval = len(now[q.Name])
		if a := active[q.Name]; a != nil {
			s.model, s.id, s.meta = a.model, a.id, a.meta
		}
		o.questions = append(o.questions, s)
	}
	o.rescore(plan, now)
	key := store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: Decider}
	stored, err := r.st.LoadDecisions(ctx, key)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	ids := make([]int64, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var cur int64 = -1
	var decoded map[string]engine.Answer // the message being read (decoded one at a time)
	answersAt := func(id int64) map[string]engine.Answer {
		if id == cur {
			return decoded
		}
		cur, decoded = id, nil
		if d, ok := stored[id]; ok {
			if err := json.Unmarshal([]byte(d.Answers), &decoded); err != nil {
				r.log.Warn("layer1 stored answer unreadable: left out", "message", id, "err", logx.RedactedError(err))
				decoded = nil
			}
		}
		return decoded
	}
	answer := func(s *settled, id int64) ([]float64, bool) {
		return selective.Probs(s.q, answersAt(id)[s.q.Name])
	}
	// The route is rebuilt from the stored answers, the rules re-measured
	// on the current selection labels and the current evaluation labels:
	// a label edit re-certifies and re-answers it in the same commit.
	p, groups, err := r.rescoreRoute(ctx, plan, rows, ids)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	p.layer1 = func(s *settled, id int64) (engine.Answer, bool) {
		a, ok := answersAt(id)[s.q.Name]
		return a, ok && !a.Refused && a.Type == s.q.Type
	}
	report("re-certifying the route", int64(len(rows)), int64(len(rows)))
	states, err := routeStatesFrom(ctx, plan, o.questions, func(s *settled) []route.Item {
		return routeItemsNow(plan, p, s, now[s.q.Name], groups)
	})
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	o.routes, rep.Routes = states, routeReports(plan, states)
	rep.State, rep.Wall = runstate.Succeeded, r.clock.Now().Sub(start)
	err = r.commit(ctx, plan, &rep, o, ids, func(id int64) int64 { return stored[id].Revision }, answer,
		routeAnswerer(o, func(s *settled, id int64, stored engine.Answer, has bool) []route.Offer {
			return p.offersWith(plan, s, id, stored, has, layer1Final, nil)
		}, &rep), nil)
	return r.finish(ctx, &rep, start, err, err == nil)
}

// rescoreRoute rebuilds the route's inputs from the store: the members'
// conversations (the split layer 1 used), the rules re-measured on the
// current selection labels and the configured decider's stored answers,
// read streamed for the stored population only. The layer-1 offers are
// the stored answers (the re-score itself).
func (r *Runner) rescoreRoute(ctx context.Context, plan *Plan, rows []store.LabelRow, ids []int64) (routeParticipants, map[int64]string, error) {
	members, err := r.in.Members(ctx)
	if err != nil {
		return routeParticipants{}, nil, err
	}
	groups := make(map[int64]string, len(members))
	for _, m := range members {
		groups[m.ID] = m.Group
	}
	eval := evalLabels(plan.Teachers.Sources, &gathered{labels: labelIndex(rows)})
	for id := range eval {
		if selective.InCertifyPart(groups[id], plan.Config.CertifyFraction) {
			delete(eval, id)
		}
	}
	rules, fires, err := r.in.Rules(ctx, eval)
	if err != nil {
		return routeParticipants{}, nil, err
	}
	key := store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: plan.DeciderKey}
	ondelle, err := readDecisions(ctx, r.in, key, plan.Profile, membershipOf(ids), plan.DeciderTolerance)
	if err != nil {
		return routeParticipants{}, nil, err
	}
	return routeParticipants{rules: rules, fires: fires, ondelle: ondelle, rows: membershipOf(ids)}, groups, nil
}

// evalLabelsNow are the current evaluation labels per question (the
// teachers' evaluation sources in precedence order).
func evalLabelsNow(plan *Plan, rows []store.LabelRow) map[string]map[int64]evalLabel {
	labels := labelIndex(rows)
	ids := map[int64]bool{}
	for _, l := range rows {
		ids[l.MessageID] = true
	}
	out := map[string]map[int64]evalLabel{}
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		sources := plan.Teachers.For(q.Name)
		out[q.Name] = map[int64]evalLabel{}
		for id := range ids {
			if l, ok := evalOf(q, sources, labels, id, plan.Profile.Cutoff(q.Name)); ok {
				out[q.Name][id] = l
			}
		}
	}
	return out
}

// labelIndex is label rows as label source → message → question → value.
func labelIndex(rows []store.LabelRow) map[string]map[int64]map[string]string {
	out := map[string]map[int64]map[string]string{}
	for _, l := range rows {
		if out[l.Source] == nil {
			out[l.Source] = map[int64]map[string]string{}
		}
		if out[l.Source][l.MessageID] == nil {
			out[l.Source][l.MessageID] = map[string]string{}
		}
		out[l.Source][l.MessageID][l.Question] = l.Value
	}
	return out
}
