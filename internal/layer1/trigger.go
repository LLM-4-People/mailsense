package layer1

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// AfterRuns starts a layer-1 run (a job) when its inputs changed, after
// every successful evidence run, every index run that committed work
// (stored or confirmed a vector), every label change and every change of a
// setting it reads, while layer1.after_runs is on. An index run that
// committed nothing (failed before its first write, or a 0/0 no-op)
// changed no vector, so it starts none. A trigger during a run is not
// lost: one more follows it (jobs.Single). A trigger while a judge run
// owns the store is dropped (triggers are paused): the judge requests the
// run its labels need when it ends. stop ends the subscriptions.
func (r *Runner) AfterRuns(bus *events.Bus, m *jobs.Manager) (stop func(), err error) {
	buffer := r.cfg.EventBuffer()
	start := func(why string) { r.startAfter(m, why) }
	stopAll, err := subscribeAll(
		func() (func(), error) {
			return events.On(bus, evidence.TopicFinished, buffer, func(rep evidence.Report) {
				if rep.State == runstate.Succeeded {
					start("evidence")
				}
			})
		},
		func() (func(), error) {
			return events.On(bus, index.TopicFinished, buffer, func(rep index.Report) {
				if rep.Committed() {
					start("index")
				}
			})
		},
		func() (func(), error) {
			return events.On(bus, triage.TopicFinished, buffer, func(rep triage.Report) {
				// Wait for the backlog to drain: a batch every few minutes
				// changing the teacher labels would otherwise restart a
				// full training pass before the previous one settled.
				if rep.State == runstate.Succeeded && rep.Decided+rep.Partial > 0 && rep.Drained() {
					start("triage")
				}
			})
		},
		func() (func(), error) {
			return events.On(bus, feedback.TopicLabel, buffer, func(feedback.LabelChange) { start("labels") })
		},
		func() (func(), error) {
			return events.On(bus, config.TopicChanged, buffer, func(c config.Change) {
				if c.Has(triggerKeys...) {
					start("settings")
				}
			})
		},
	)
	if err != nil {
		return nil, err
	}
	return func() {
		r.stopRetrain()
		stopAll()
	}, nil
}

// triggerKeys are the settings whose change can change what a run learns.
// The route key's settings are the same list (one owner): an input that
// moved the key but did not start a run would blank the route without ever
// rebuilding it.
var triggerKeys = append(append([]string(nil), routeKeySettingKeys...),
	taxonomy.ProfileFileSetting, triage.RulesFileSetting, engine.ProvidersFileSetting)

// pauseTriggers refuses trigger-started runs until a matching
// resumeTriggers: a judge run owns the store for its whole duration and
// writes one label batch per message, so a layer-1 run started by those
// label events would hold a read transaction over the store while the next
// batch waits for the write lock (a lock convoy, not a label problem).
// Pausing waits for a trigger already deciding to start, so no run starts
// after pauseTriggers returns. The pause is lifecycle state, never a
// setting: the owner must not auto-train in the middle of a bulk judge.
func (r *Runner) pauseTriggers() {
	r.triggerGate.Lock()
	r.paused.Add(1)
	r.triggerGate.Unlock()
}

// resumeTriggers releases one pause. The labels a judge wrote are learned
// by the next trigger, or by the startAfter the judge requests once it has
// resumed (StartJudge); a synchronous RunJudge has no manager, so only the
// next trigger or an explicit run trains.
func (r *Runner) resumeTriggers() {
	r.triggerGate.Lock()
	defer r.triggerGate.Unlock()
	if r.paused.Load() <= 0 {
		r.log.Warn("layer1 triggers resumed without a pause")
		return
	}
	r.paused.Add(-1)
}

// triggersPaused reports whether a judge run owns the store (the cheap
// first check; startTrigger checks again under the gate).
func (r *Runner) triggersPaused() bool { return r.paused.Load() > 0 }

// retryWhilePaused is how soon a request made during a judge run is tried
// again. A judge owns the store for minutes; the request is retried until the
// pause lifts, then starts (or waits out the retrain interval).
var retryWhilePaused = 20 * time.Second

// startAfter starts a run now (or right after the one in progress) when
// layer1.after_runs is on and the inputs changed since the last run: a
// re-score when only evaluation labels changed, a training run otherwise
// (a broken configuration is recorded once per distinct problem).
func (r *Runner) startAfter(m *jobs.Manager, why string) {
	if !r.cfg.Bool(SettingAfterRuns) {
		return
	}
	if r.triggersPaused() {
		// A judge run owns the store right now. The request must survive it:
		// dropping it here lost the only retrain a fired timer carried, and
		// a judge loop that keeps the pause busy starved training forever.
		r.scheduleRetrain(m, why, retryWhilePaused)
		return
	}
	r.single.Want(func() {
		started, err := r.startTrigger(m, why)
		if !started {
			// The caller of Want owns the slot; release it outside the
			// trigger gate (End runs a queued start synchronously).
			r.single.End()
		}
		if err != nil {
			r.log.Warn("layer1 run not started", "after", why, "err", logx.RedactedError(err))
		}
	})
}

// startTrigger is the gated decision of startAfter: it starts the run the
// changed inputs need, or reports false when none must run. The caller
// owns the slot and releases it when this returns false. A trigger that
// was queued before a judge paused it re-checks the pause here, under the
// same gate pauseTriggers takes, so it cannot start during the judge's run.
func (r *Runner) startTrigger(m *jobs.Manager, why string) (started bool, err error) {
	r.triggerGate.Lock()
	defer r.triggerGate.Unlock()
	if r.triggersPaused() {
		r.scheduleRetrain(m, why, retryWhilePaused)
		return false, nil
	}
	lr, err := r.lastRun(context.Background())
	last := lr.report
	rescore := false
	switch {
	case err != nil:
		r.log.Warn("layer1 state not read: training", "after", why, "err", logx.RedactedError(err))
	case lr.planErr != nil && last != nil && last.Code == BlockConfig && last.Error == lr.planErr.Error():
		return false, nil // the same problem is already recorded
	case lr.planErr != nil:
	case last != nil && last.State == runstate.Succeeded && lr.freshness == current:
		return false, nil // nothing changed: no run
	case last != nil && last.State == runstate.Succeeded && lr.freshness == relabelled:
		rescore = true
	}
	// A training pass is minutes of CPU. When teacher batches arrive every
	// few minutes, starting one after each batch means the model never
	// settles and the app looks stuck in a loop. A rescore is cheap and
	// still goes through; training waits out the interval and starts once,
	// on the final state.
	if !rescore {
		if wait := r.retrainWait(); wait > 0 {
			r.scheduleRetrain(m, why, wait)
			return false, nil
		}
	}
	if _, err := r.startClaimed(m, rescore); err != nil {
		return false, err
	}
	return true, nil
}

// retrainWait is how long the retrain interval still has to run (0: train
// now). It reads the in-process finish time; a restart trains immediately.
func (r *Runner) retrainWait() time.Duration {
	every := r.cfg.Duration(SettingRetrainEvery)
	if every <= 0 {
		return 0
	}
	last := r.lastFinished.Load()
	if last == 0 {
		return 0
	}
	if wait := every - r.clock.Now().Sub(time.Unix(0, last)); wait > 0 {
		return wait
	}
	return 0
}

// scheduleRetrain arms the one deferred run. Requests made meanwhile
// coalesce on the same timer; the run re-checks the inputs when it fires,
// so a trigger that became moot starts nothing.
func (r *Runner) scheduleRetrain(m *jobs.Manager, why string, wait time.Duration) {
	r.retrainMu.Lock()
	defer r.retrainMu.Unlock()
	if r.retrainTimer != nil {
		return
	}
	r.retrainTimer = time.AfterFunc(wait, func() {
		r.retrainMu.Lock()
		r.retrainTimer = nil
		r.retrainMu.Unlock()
		r.startAfter(m, why)
	})
}

// stopRetrain cancels a deferred run at shutdown.
func (r *Runner) stopRetrain() {
	r.retrainMu.Lock()
	if r.retrainTimer != nil {
		r.retrainTimer.Stop()
		r.retrainTimer = nil
	}
	r.retrainMu.Unlock()
}
