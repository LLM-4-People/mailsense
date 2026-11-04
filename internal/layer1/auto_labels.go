package layer1

import (
	"context"
	"errors"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Automatic label settings (internal/config/schema.json).
const (
	SettingAutoLabels = "labels.auto"
	SettingAutoBatch  = "labels.auto.batch"
	SettingAutoMax    = "labels.auto.max"
	SettingAutoBudget = "labels.auto.budget_usd"
	SettingAutoEvery  = "labels.auto.interval"
)

// autoLabelKeys are the settings whose change re-checks the loop.
var autoLabelKeys = []string{SettingAutoLabels, SettingAutoBatch, SettingAutoMax, SettingAutoBudget, SettingAutoEvery}

// AutoLabels buys the evaluation labels a question still needs: after a
// sync's evidence run, after a layer-1 run, on a settings change and on a
// timer, it selects the most uncertain messages (the label queue's
// uncertainty strategy, not the oldest mail), starts a bounded judge run
// and lets the judge's own startAfter train layer 1 on the new labels.
// The judge is a teacher, never an answer: answers still come from layer
// 1. The loop stops when every question has its labels, when
// labels.auto.max messages were bought, or when the judge refuses the
// per-batch budget. stop ends the subscriptions and the timer.
func (r *Runner) AutoLabels(bus *events.Bus, m *jobs.Manager) (stop func(), err error) {
	if !r.cfg.Bool(SettingAutoLabels) {
		return func() {}, nil
	}
	buffer := r.cfg.EventBuffer()
	trigger := func(why string) { r.startAutoLabels(m, why) }
	stopAll, err := subscribeAll(
		func() (func(), error) {
			return events.On(bus, evidence.TopicFinished, buffer, func(rep evidence.Report) {
				if rep.State == runstate.Succeeded {
					trigger("evidence")
				}
			})
		},
		func() (func(), error) {
			return events.On(bus, TopicFinished, buffer, func(rep Report) {
				if rep.State == runstate.Succeeded {
					trigger("layer1")
				}
			})
		},
		func() (func(), error) {
			return events.On(bus, config.TopicChanged, buffer, func(c config.Change) {
				if c.Has(autoLabelKeys...) {
					trigger("settings")
				}
			})
		},
	)
	if err != nil {
		return nil, err
	}
	if every := r.cfg.Duration(SettingAutoEvery); every > 0 {
		done := make(chan struct{})
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					trigger("interval")
				}
			}
		}()
		stopSubs := stopAll
		stopAll = func() {
			close(done)
			stopSubs()
		}
	}
	return stopAll, nil
}

// startAutoLabels coalesces checks: one runs at a time, and a request
// during a check is served by the next trigger (the labels and the
// training run it starts publish their own events).
func (r *Runner) startAutoLabels(m *jobs.Manager, why string) {
	if !r.cfg.Bool(SettingAutoLabels) || r.judge == nil || r.state == nil {
		return
	}
	r.autoSingle.Want(func() {
		go func() {
			defer r.autoSingle.End()
			// A layer-1 run publishes TopicFinished just before it
			// releases its slot; wait a moment so the judge never starts
			// inside the run's read transaction.
			time.Sleep(2 * time.Second)
			if r.single.Busy() || r.triggersPaused() {
				return
			}
			if err := r.autoLabelOnce(m, why); err != nil {
				r.log.Warn("automatic labels not started", "after", why, "err", logx.RedactedError(err))
			}
		}()
	})
}

// autoLabelOnce buys one bounded batch. It reads the labels stage's
// warnings (not its state: the stage is done as soon as one question has
// its labels) and stops at the mailbox's bought-message cap.
func (r *Runner) autoLabelOnce(m *jobs.Manager, why string) error {
	ctx := context.Background()
	maxMessages := r.cfg.Int(SettingAutoMax)
	if maxMessages <= 0 {
		return nil
	}
	bought, err := r.st.AutoLabelsBought(ctx)
	if err != nil {
		return err
	}
	if bought >= maxMessages {
		return nil
	}
	st, err := r.LabelsStage().Status(ctx)
	if err != nil {
		return err
	}
	// The labels stage emits BlockTooFewEval only for a LabelNeed.Fixable
	// subject: an unfixable question is the questions stage's blocker, so
	// this loop never buys labels that cannot change the verdict.
	var short []string
	for _, b := range st.Blockers {
		if b.Code == BlockTooFewEval && b.Subject != "" {
			short = append(short, b.Subject)
		}
	}
	if len(short) == 0 {
		return nil
	}
	batch := min(r.cfg.Int(SettingAutoBatch), maxMessages-bought)
	if batch <= 0 {
		return nil
	}
	ids, err := r.uncertainMessages(ctx, short, batch)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	o := JudgeOptions{Limit: len(ids), MessageIDs: ids,
		Budget: engine.Budget{MaxCalls: len(ids), MaxUSD: r.cfg.Float(SettingAutoBudget), AllowUnpriced: false}}
	j, err := r.StartJudge(ctx, m, o)
	if err != nil {
		if errors.Is(err, ErrJudgeBusy) {
			return nil // the running judge's end requests the next check
		}
		return err
	}
	if err := r.st.AddAutoLabelsBought(ctx, len(ids)); err != nil {
		return err
	}
	r.log.Info("automatic judge batch started", "after", why, "messages", len(ids),
		"bought", bought+len(ids), "max", maxMessages, "job", j.Info().ID)
	return nil
}

// uncertainMessages is the union of the per-question uncertainty queues,
// in queue order and without duplicates. It is the one selection owner:
// the same queue the owner's Labels screen uses.
func (r *Runner) uncertainMessages(ctx context.Context, questions []string, limit int) ([]int64, error) {
	seen := map[int64]bool{}
	out := make([]int64, 0, limit)
	queueLimit := min(limit, LabelQueueMax)
	for _, name := range questions {
		queue, err := r.LabelQueue(ctx, LabelQuery{Question: name, Limit: queueLimit, Strategy: LabelStrategyUncertainty})
		if err != nil {
			return nil, err
		}
		for _, task := range queue.Tasks {
			if seen[task.MessageID] {
				continue
			}
			seen[task.MessageID] = true
			out = append(out, task.MessageID)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}
