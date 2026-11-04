package evidence

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// AfterRuns starts an evidence run (a job) after every sync run that advanced
// the stored mail (syncer.Report.Advanced), every index run that committed
// work (stored or confirmed a vector: new vectors feed template_matches, and
// a consistent snapshot represents the ones still missing) and every change
// of a setting a signal reads, while evidence.after_runs is on. An index run
// that committed nothing (failed before its first write: unavailable,
// transient, store busy, cancelled, every batch failed; or a 0/0 no-op)
// starts none: it changed no evidence input, so a run would only repeat the
// previous snapshot. A trigger that arrives during a run is not lost: one more
// run follows it (jobs.Single). stop ends the subscriptions.
func (e *Engine) AfterRuns(bus *events.Bus, m *jobs.Manager) (stop func(), err error) {
	buffer := e.cfg.EventBuffer()
	start := func(why string) { e.startAfter(m, why) }
	var stops []func()
	stopAll := func() {
		for _, s := range stops {
			s()
		}
	}
	for _, sub := range []func() (func(), error){
		func() (func(), error) {
			return events.On(bus, syncer.TopicFinished, buffer, func(rep syncer.Report) {
				if rep.Advanced() {
					start("sync")
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
			return events.On(bus, config.TopicChanged, buffer, func(c config.Change) {
				if e.readsAny(c.Keys) {
					start("settings")
				}
			})
		},
	} {
		s, err := sub()
		if err != nil {
			stopAll()
			return nil, err
		}
		stops = append(stops, s)
	}
	return stopAll, nil
}

// readsAny reports whether a signal reads one of the settings.
func (e *Engine) readsAny(keys []string) bool {
	for _, s := range e.signals {
		for _, k := range keys {
			if slices.Contains(s.Settings, k) {
				return true
			}
		}
	}
	return false
}

// startAfter wants a run after a sync, an index run or a settings change
// while evidence.after_runs is on: it starts now, or right after the run
// in progress.
func (e *Engine) startAfter(m *jobs.Manager, why string) {
	if !e.cfg.Bool(settingAfterRuns) {
		return
	}
	e.single.Want(func() {
		if _, err := e.startClaimed(m); err != nil {
			e.log.Warn("evidence run not started", "after", why, "err", err)
		}
	})
}
