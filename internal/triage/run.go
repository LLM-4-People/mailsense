package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// JobKind is the jobs.Manager kind of triage runs (and the lease name).
const JobKind = "triage"

// leaseWaitTimeout bounds the store-lease handshake of a Start. The run itself
// is bounded by its job; this only keeps a blocked lease (another process
// holds it) from ignoring shutdown before the job even exists.
const leaseWaitTimeout = 30 * time.Second

// Start runs triage as a background job. Only one run at a time: a second
// Start (or Run) while one is in progress fails with ErrTriageBusy
// before any job exists.
func (t *Triager) Start(m *jobs.Manager, o Options) (*jobs.Job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), leaseWaitTimeout)
	defer cancel()
	release, err := t.acquire(ctx)
	if err != nil {
		return nil, err
	}
	j, err := m.Start(JobKind, "triage", func(ctx context.Context, report func(jobs.Progress)) error {
		defer release()
		_, err := t.run(ctx, o, func(p Progress) {
			report(jobs.Progress{Done: int64(p.Done), Total: int64(p.Total), Unit: learning.UnitMessages,
				Message: fmt.Sprintf("%d decided, %d failed, $%.4f", p.Decided, p.Failed, p.Usage.EstimatedUSD)})
		})
		return err
	})
	if err != nil {
		release()
	}
	return j, err
}

// Run plans, checks the budget and decides the selected messages with
// triage.workers in parallel (one run at a time: ErrTriageBusy).
// Per-message failures never abort the run; they are stored (an earlier
// decision is kept) and reported. The run stops early when the provider
// is unavailable for everyone (no credits, rejected key, quota), is
// temporarily failing (rate limit, timeout, gateway) or the budget would
// be exceeded.
func (t *Triager) Run(ctx context.Context, o Options, report func(Progress)) (Report, error) {
	release, err := t.acquire(ctx)
	if err != nil {
		return Report{State: runstate.Failed, Error: err.Error()}, err
	}
	defer release()
	return t.run(ctx, o, report)
}

func (t *Triager) run(ctx context.Context, o Options, report func(Progress)) (Report, error) {
	plan, s, err := t.planPending(ctx, o)
	if err != nil {
		return Report{State: runstate.Of(ctx, err), Error: err.Error()}, err
	}
	rep := Report{Plan: plan}
	if rep.RunID, err = t.st.BeginTriageRun(ctx, plan.Key); err != nil {
		return rep, err
	}
	t.log.Info("triage run started", "run", rep.RunID, "decider", plan.Key.Decider, "fingerprint", plan.Key.Fingerprint,
		"eligible", plan.Eligible, "fresh", plan.Fresh, "invalidated", plan.Invalidated, "quarantined", plan.Quarantined,
		"selected", plan.Selected, "calls", plan.Estimate.Calls, "usd", plan.Estimate.EstimatedUSD)
	if plan.Refusal != "" {
		err = fmt.Errorf("%w: %s", ErrBudget, plan.Refusal)
		rep.NotAttempted, rep.StoppedBy = plan.Selected, StoppedBudget
	} else {
		err = t.execute(ctx, s, plan, &rep, report)
	}
	return t.finish(ctx, &rep, err)
}

func (t *Triager) finish(ctx context.Context, rep *Report, err error) (Report, error) {
	rep.State = runstate.Of(ctx, err)
	if err == nil && rep.Decided == 0 && rep.Partial == 0 && rep.Failed > 0 {
		err = fmt.Errorf("triage: every attempted message failed (%d); earlier results are kept", rep.Failed)
		rep.State = runstate.Failed
	}
	if err != nil {
		rep.Error = err.Error()
	}
	body, _ := json.Marshal(rep)
	if ferr := t.st.FinishTriageRun(context.WithoutCancel(ctx), rep.RunID, rep.State, string(body), rep.Error); ferr != nil && err == nil {
		err = ferr
	}
	t.log.Info("triage run finished", "run", rep.RunID, "state", rep.State, "decided", rep.Decided, "partial", rep.Partial,
		"failed", rep.Failed, "not_attempted", rep.NotAttempted, "stopped_by", rep.StoppedBy, "overruns", rep.Overruns, "calls", rep.Usage.Calls,
		"usd", rep.Usage.EstimatedUSD, "err", rep.Error)
	if t.bus != nil {
		events.Publish(t.bus, TopicFinished, *rep)
	}
	return *rep, err
}

// execute dispatches the plan's items to the workers.
func (t *Triager) execute(ctx context.Context, s *session, plan *Plan, rep *Report, report func(Progress)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rs := newRunState(rep, plan.Budget)
	work := make(chan item)
	var wg sync.WaitGroup
	for range t.cfg.Int("triage.workers") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				t.decideOne(ctx, cancel, s, rs, it, report)
			}
		}()
	}
dispatch:
	for _, it := range plan.items {
		if rs.stopped() {
			break
		}
		select {
		case work <- it:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(work)
	wg.Wait()
	rep.NotAttempted = plan.Selected - rep.Decided - rep.Partial - rep.Failed
	switch {
	case rs.output != nil:
		return rs.output
	case rs.stop == StoppedUnavailable:
		rep.StoppedBy = rs.stop
		return fmt.Errorf("triage: stopped, the provider is unavailable: %s", rep.Failures[len(rep.Failures)-1].Error)
	case rs.stop == StoppedTransient:
		rep.StoppedBy = rs.stop
		return fmt.Errorf("triage: stopped, the provider is temporarily unavailable: %s", rep.Failures[len(rep.Failures)-1].Error)
	case rs.stop == StoppedBudget:
		rep.StoppedBy = rs.stop
	case ctx.Err() != nil && rep.NotAttempted > 0:
		rep.StoppedBy = StoppedCancelled
	}
	return context.Cause(ctx)
}

// errDecisionPending marks a paid decision that could not be stored because
// the store stayed busy past its retry budget. The failure owner has already
// recorded it; the message stays pending and is never counted as decided.
var errDecisionPending = errors.New("triage: the paid decision could not be stored")

// decideOne decides one message's missing questions and records the
// outcome: answers paid for are stored even when a later chunk failed or
// the run is being stopped.
func (t *Triager) decideOne(ctx context.Context, cancel context.CancelFunc, s *session, rs *runState, it item, report func(Progress)) {
	state, err := t.stateOf(ctx, s, &it)
	var res engine.Result
	if err == nil {
		res, err = s.decider.Decide(engine.WithReserver(ctx, rs), state, s.missing(it.have))
	}
	merged := maps.Clone(it.have)
	if merged == nil {
		merged = map[string]engine.Answer{}
	}
	maps.Copy(merged, res.Answers)
	out := t.record(ctx, s, rs, it, merged, len(res.Answers) > 0, err)
	rs.mu.Lock()
	complete := len(merged) == len(s.profile.Questions)
	// removed since the plan, or not sent for the budget: not a failure
	gone := errors.Is(err, store.ErrNotFound) || errors.Is(err, engine.ErrBudget)
	switch {
	case errors.Is(out, errDecisionPending):
		t.recordFailure(rs, it.rowID, out, true, cancel)
	case out != nil:
		rs.output = fmt.Errorf("triage: cannot record message %d: %w", it.rowID, out)
		cancel()
	case err == nil && complete:
		rs.rep.Decided++
	case len(res.Answers) > 0:
		rs.rep.Partial++
	}
	if err != nil && out == nil && ctx.Err() == nil && !gone {
		t.recordFailure(rs, it.rowID, err, len(res.Answers) == 0, cancel)
	}
	p := Progress{RunID: rs.rep.RunID, Done: rs.rep.Decided + rs.rep.Partial + rs.rep.Failed, Total: rs.rep.Plan.Selected,
		Decided: rs.rep.Decided, Failed: rs.rep.Failed, Usage: rs.rep.Usage, Estimate: rs.rep.Plan.Estimate.EstimatedUSD}
	rs.mu.Unlock()
	t.renew(ctx)
	t.progress(p, report)
}

// record stores the answers (when any were paid for) and the failure. A
// failed attempt's partial answers keep its failure row (and attempt count),
// so a repeating rejection is never mistaken for a fresh start. A decision
// whose write stays busy is recorded as a failure instead: the message must
// stay pending, never be marked decided, and the run goes on.
func (t *Triager) record(ctx context.Context, s *session, rs *runState, it item, merged map[string]engine.Answer, paid bool, err error) error {
	keep := context.WithoutCancel(ctx)
	if paid {
		answers, out := encodeAnswers(merged)
		if out == nil {
			out = t.st.SaveDecision(keep, s.key, store.Decision{MessageID: it.rowID, Revision: it.revision, StateHash: it.hash,
				Answers: answers, RunID: rs.rep.RunID, KeepFailure: err != nil})
		}
		if out != nil {
			if errors.Is(out, store.ErrStoreBusy) {
				ferr := t.st.SaveFailure(keep, s.key, store.Failure{MessageID: it.rowID, Revision: it.revision,
					Error: out.Error(), RunID: rs.rep.RunID, Context: s.answerContext})
				if ferr != nil {
					return ferr
				}
				return fmt.Errorf("%w: %w", errDecisionPending, out)
			}
			return out
		}
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, engine.ErrBudget) {
		// Only a provider per-message rejection is permanent: the same input
		// gets the same 400/404/422. ErrTransient and ErrUnavailable are
		// provider-wide (the run stops and retries later); contract, context
		// and store failures may clear on a retry and never quarantine.
		return t.st.SaveFailure(keep, s.key, store.Failure{MessageID: it.rowID, Revision: it.revision, Error: err.Error(),
			RunID: rs.rep.RunID, Permanent: errors.Is(err, engine.ErrRejected), Context: s.answerContext})
	}
	return nil
}

// stateOf composes the planned message's state again; when it changed
// since the plan, the new state (and hash) is decided in full. The
// revision read with the message is the one the decision is saved at.
func (t *Triager) stateOf(ctx context.Context, s *session, it *item) (string, error) {
	var row *store.MessageRow
	err := t.st.EachMessage(ctx, store.MessageFilter{RowIDs: []int64{it.rowID}}, func(r *store.MessageRow) error { row = r; return nil })
	if err == nil && row == nil {
		err = store.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("triage: message %d: %w", it.rowID, err)
	}
	state := s.composer.State(it.rowID, &row.Message)
	if h := StateHash(state); h != it.hash || row.Revision != it.revision {
		t.log.Info("message changed since the plan", "message", it.rowID)
		it.hash, it.revision, it.have = h, row.Revision, nil
	}
	return state, nil
}

// recordFailure counts a failure (rs.mu held); a provider-wide stop
// (unavailable, or temporarily overloaded) stops the run.
func (t *Triager) recordFailure(rs *runState, rowID int64, err error, count bool, cancel context.CancelFunc) {
	if count {
		rs.rep.Failed++
	}
	if rs.logged < t.cfg.Int("triage.max_logged_failures") {
		rs.logged++
		rs.rep.Failures = append(rs.rep.Failures, Failure{MessageID: rowID, Error: err.Error()})
		t.log.Warn("message not decided", "message", rowID, "err", err)
	}
	if rs.stop == "" {
		stop := ""
		switch {
		case errors.Is(err, engine.ErrUnavailable):
			stop = StoppedUnavailable
		case errors.Is(err, engine.ErrTransient):
			stop = StoppedTransient
		}
		if stop != "" {
			rs.stop = stop
			if len(rs.rep.Failures) == 0 || rs.rep.Failures[len(rs.rep.Failures)-1].MessageID != rowID {
				rs.rep.Failures = append(rs.rep.Failures, Failure{MessageID: rowID, Error: err.Error()})
			}
			cancel()
		}
	}
}

func (t *Triager) progress(p Progress, report func(Progress)) {
	events.PublishProgress(t.bus, TopicProgress, p, report)
}
