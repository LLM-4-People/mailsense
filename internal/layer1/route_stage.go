package layer1

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// RouteStage is the learning stage "calibration": the routing mechanism.
// It waits for the stages it reads (the layer-1 pass and, through it,
// evidence and embeddings); it is pending until a pass succeeds; it is
// blocked route_not_routed when the last succeeded pass wrote no route
// record for every question or no route answers for every message the
// pass answered; it is done otherwise. Certification is not a blocker
// here: the questions stage judges the route's requirement.
func (r *Runner) RouteStage(after ...learning.Stage) learning.Stage {
	return &routeStage{r: r, after: after}
}

type routeStage struct {
	r     *Runner
	after []learning.Stage
}

func (s *routeStage) ID() string { return learning.StageCalibration }

func (s *routeStage) Status(ctx context.Context) (learning.StageStatus, error) {
	out := learning.StageStatus{State: learning.Running, Progress: learning.Progress{Unit: learning.UnitQuestions}}
	if wait, ready, err := learning.WaitFor(ctx, s.after...); err != nil {
		return out, err
	} else if !ready {
		return wait, nil
	}
	last, err := s.r.lastSucceeded(ctx)
	if err != nil {
		return out, err
	}
	if last == nil {
		out.State, out.Note = learning.Pending, "not routed yet"
		return out, nil
	}
	plan, err := s.r.plan(ctx)
	if err != nil {
		return out, err
	}
	// A report of another configuration cannot describe these route rows:
	// its routes were written under another key (or under none, before the
	// route key existed). Block before reading or counting them.
	if last.RouteKey != plan.RouteKey() {
		out.State = learning.Blocked
		out.Note = "the route was written for another configuration"
		out.Progress.Total = int64(len(plan.Profile.Questions))
		out.Blockers = []learning.Blocker{s.blocker("",
			"The last pass wrote its route for another profile, teacher, requirement, decider, rules or algorithm configuration. Run layer 1 again.")}
		return out, nil
	}
	routes := make(map[string]RouteReport, len(last.Routes))
	answering := 0
	for _, r := range last.Routes {
		routes[r.Question] = r
		// Count the same predicate the answer gate uses. The stored field is
		// informational; a report written before it existed must not reset
		// the stage to zero.
		if selective.Answering(r.Certification, plan.Requirements.For(r.Question).MinCoverage) {
			answering++
		}
	}
	total := len(plan.Profile.Questions)
	missing := s.missing(plan, routes)
	out.Progress.Total = int64(total)
	out.Progress.Done = int64(answering)
	out.Note = fmt.Sprintf("%d of %d questions answerable at the target", answering, total)

	var blockers []learning.Blocker
	if last.Routed != last.Answered {
		blockers = append(blockers, s.blocker("", fmt.Sprintf(
			"The last pass answered %d messages but wrote route answers for %d: the route is out of date. Run layer 1 again.",
			last.Answered, last.Routed)))
	}
	for _, q := range missing {
		blockers = append(blockers, s.blocker(q, "The last pass has no route record for this question: run layer 1 again."))
	}
	if len(blockers) > 0 {
		out.State, out.Blockers = learning.Blocked, blockers
		return out, nil
	}
	out.State = learning.Done
	return out, nil
}

// missing is the profile's questions without a route record in the pass.
func (s *routeStage) missing(plan *Plan, routes map[string]RouteReport) []string {
	var out []string
	for i := range plan.Profile.Questions {
		if _, ok := routes[plan.Profile.Questions[i].Name]; !ok {
			out = append(out, plan.Profile.Questions[i].Name)
		}
	}
	return out
}

// blocker is the route_not_routed blocker of one question ("" for the
// mechanism as a whole): the remedy is another pass, and the failures
// log says why it wrote none.
func (s *routeStage) blocker(question, detail string) learning.Blocker {
	return learning.Blocker{Code: BlockRouteNotRouted, Subject: question, Detail: detail,
		Actions: []learning.Action{learning.Do(learning.ActionRunLayer1), learning.Do(learning.ActionReviewFailures, "log", "layer1")}}
}
