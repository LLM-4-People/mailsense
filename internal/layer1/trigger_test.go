package layer1

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/triage"
)

// An evidence run starts a layer-1 run when the inputs changed; with
// nothing changed no run starts; layer1.after_runs off starts none.
func TestRunsFollowTheirInputs(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	finished, err := events.Subscribe(bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := events.Subscribe(bus, jobs.TopicJob, 64)
	if err != nil {
		t.Fatal(err)
	}
	// The job reports every stage, so the 64-event buffer can wrap while
	// the run is still going: drain it as it happens, and keep the
	// transition that matters (the run released its slot).
	jobEnded := make(chan struct{})
	go func() {
		defer close(jobEnded)
		for u := range updates.C {
			if u.Kind == JobKind && u.Status != runstate.Running {
				return
			}
		}
	}()
	stop, err := r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	events.Publish(bus, evidence.TopicFinished, evidence.Report{State: runstate.Failed}) // no run
	events.Publish(bus, evidence.TopicFinished, evidence.Report{State: runstate.Succeeded})
	select {
	case rep := <-finished.C:
		if rep.State != runstate.Succeeded {
			t.Fatalf("%+v", rep)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no layer-1 run after an evidence run")
	}
	// the job's last update comes after its run released the slot
	select {
	case <-jobEnded:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never ended")
	}
	before := len(m.List())
	r.startAfter(m, "test")
	if len(m.List()) != before || r.single.Busy() {
		t.Fatal("a run started over unchanged inputs")
	}
	if err := w.cfg.Set(SettingAfterRuns, false); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	r.startAfter(m, "test")
	if len(m.List()) != before {
		t.Fatal("a run started while layer1.after_runs is off")
	}
}

// A judge run owns the store: while triggers are paused, the events that
// normally start a layer-1 run start none. Resuming and requesting the run
// (what StartJudge does when it ends) trains the labels the judge bought.
func TestTriggersArePausedForAJudgeRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stop, err := r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	r.pauseTriggers()
	if !r.triggersPaused() {
		t.Fatal("pauseTriggers did not pause")
	}
	events.Publish(bus, feedback.TopicLabel, feedback.LabelChange{MessageID: 1, Question: "category", Action: "set", Count: 1})
	events.Publish(bus, evidence.TopicFinished, evidence.Report{State: runstate.Succeeded})
	events.Publish(bus, index.TopicFinished, index.Report{State: runstate.Succeeded, Embedded: 1})
	stop() // waits for every subscriber to drain: each handler saw the pause
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a trigger started a job during the pause: %+v", got)
	}
	if r.single.Busy() || r.single.Pending() {
		t.Fatal("a trigger claimed the run slot during the pause")
	}
	r.resumeTriggers()
	if r.triggersPaused() {
		t.Fatal("resumeTriggers did not resume")
	}
	r.startAfter(m, "test")
	if list := m.List(); len(list) != 1 || list[0].Kind != JobKind {
		t.Fatalf("no layer-1 run after the resume: %+v", list)
	}
}

// An index run that stored or confirmed no vector changed no input of
// layer 1, so it starts no run.
func TestIndexRunWithoutCommittedWorkStartsNoLayer1(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rep  index.Report
	}{
		{"failed unavailable", index.Report{State: runstate.Failed, StoppedBy: index.StoppedUnavailable, Failed: 8, NotAttempted: 33834}},
		{"failed store busy", index.Report{State: runstate.Failed, StoppedBy: index.StoppedBusy, NotAttempted: 100}},
		{"cancelled", index.Report{State: runstate.Cancelled, StoppedBy: index.StoppedCancelled, NotAttempted: 100}},
		{"succeeded with nothing selected", index.Report{State: runstate.Succeeded}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, 30, nil)
			bus := events.New()
			t.Cleanup(bus.Close)
			r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
			if err != nil {
				t.Fatal(err)
			}
			m := jobs.NewManager(w.cfg, bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			stop, err := r.AfterRuns(bus, m)
			if err != nil {
				t.Fatal(err)
			}
			events.Publish(bus, index.TopicFinished, c.rep)
			stop() // every handler drained: the event was handled
			if got := m.List(); len(got) != 0 {
				t.Fatalf("an index run with no committed work started %d layer-1 job(s): %+v", len(got), got)
			}
			if r.single.Busy() || r.single.Pending() {
				t.Fatal("an index run with no committed work claimed the run slot")
			}
		})
	}
}

// An index run that stored or confirmed a vector starts exactly one
// layer-1 run, even when the run failed partway.
func TestIndexRunWithCommittedWorkStartsLayer1(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		rep  index.Report
	}{
		{"failed with embedded vectors", index.Report{State: runstate.Failed, StoppedBy: index.StoppedUnavailable, Embedded: 2}},
		{"succeeded with confirmed vectors", index.Report{State: runstate.Succeeded, Unchanged: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, 30, nil)
			bus := events.New()
			t.Cleanup(bus.Close)
			r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
			if err != nil {
				t.Fatal(err)
			}
			m := jobs.NewManager(w.cfg, bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			finished, err := events.Subscribe(bus, TopicFinished, 8)
			if err != nil {
				t.Fatal(err)
			}
			stop, err := r.AfterRuns(bus, m)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			events.Publish(bus, index.TopicFinished, c.rep)
			select {
			case rep := <-finished.C:
				if rep.State != runstate.Succeeded {
					t.Fatalf("%+v", rep)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("no layer-1 run after an index run that committed work")
			}
			if got := m.List(); len(got) != 1 || got[0].Kind != JobKind {
				t.Fatalf("exactly one layer-1 job must follow a committed index run: %+v", got)
			}
		})
	}
}

// A trigger queued behind the run in progress before a pause re-checks the
// pause when the slot is released and starts nothing: the queued closure
// runs synchronously inside Single.End, so the check needs no sleep.
func TestATriggerQueuedBeforeThePauseDoesNotStartARun(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if !r.single.Begin() {
		t.Fatal("the run slot is busy")
	}
	r.startAfter(m, "test") // no run yet: it queues behind the claimed slot
	if !r.single.Pending() {
		t.Fatal("the trigger was not queued")
	}
	r.pauseTriggers()
	r.single.End() // runs the queued closure with the pause on
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a queued trigger started a job during the pause: %+v", got)
	}
	if r.single.Busy() || r.single.Pending() {
		t.Fatal("the queued trigger kept the run slot")
	}
	// Keep the pause for teardown. A queued closure can run late; with the
	// pause still on it re-checks the gate and returns instead of starting a
	// job after the test's manager has shut down. Resuming here made TempDir
	// cleanup race a late start and fail with "directory not empty".
}

// The judge's post-run request and a label event arriving at the same
// instant coalesce: the event queues one more start behind the run, and
// that start finds the run's inputs unchanged and opens no job. It is the
// jobs.Single contract that makes the extra request harmless.
func TestAQueuedTriggerAfterARunOpensNoSecondJob(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	r.startAfter(m, "judge") // nothing trained yet: it starts a run
	list := m.List()
	if len(list) != 1 || list[0].Kind != JobKind {
		t.Fatalf("the first run did not start: %+v", list)
	}
	r.startAfter(m, "labels") // a label event at the same instant: queued
	if !r.single.Pending() {
		t.Fatal("the later trigger was not queued behind the run")
	}
	first, ok := m.Get(list[0].ID)
	if !ok {
		t.Fatal("the first job vanished")
	}
	select {
	case <-first.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the first run did not finish")
	}
	if got := m.List(); len(got) != 1 {
		t.Fatalf("a second run started over unchanged inputs: %+v", got)
	}
}

// A training pass is minutes of CPU: a teacher batch arriving during the
// retrain interval must not start another one. The request is kept and one
// run starts when the interval ends.
func TestTrainingWaitsForTheRetrainInterval(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, map[string]any{SettingRetrainEvery: "1s"})
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stop, err := r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r.lastFinished.Store(time.Now().UnixNano())
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	r.startAfter(m, "test")
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a run started inside the interval: %+v", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := m.List(); len(got) == 1 && got[0].Kind == JobKind {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deferred run never started: %+v", m.List())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A triage backlog changes teacher labels every batch. Layer 1 must wait
// for it to drain, then train once, instead of retraining after every
// hundred messages.
func TestTriageBacklogDoesNotTrainUntilDrained(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, map[string]any{SettingRetrainEvery: "0s"})
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stop, err := r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	events.Publish(bus, triage.TopicFinished, triage.Report{State: runstate.Succeeded,
		Plan: &triage.Plan{Pending: 500, Selected: 100}, Decided: 100})
	time.Sleep(100 * time.Millisecond)
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a run started over a pending triage backlog: %+v", got)
	}
	events.Publish(bus, triage.TopicFinished, triage.Report{State: runstate.Succeeded,
		Plan: &triage.Plan{Pending: 100, Selected: 100}, Decided: 100})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got := m.List(); len(got) == 1 && got[0].Kind == JobKind {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no run after the backlog drained: %+v", m.List())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A trigger that arrives while a judge owns the store must not be lost: the
// fired timer used to be dropped by the pause, and a judge loop that kept the
// pause busy starved training indefinitely (live: one run at 21:03, none
// after).
func TestAPausedTriggerIsRetriedAfterTheJudge(t *testing.T) {
	t.Parallel()
	old := retryWhilePaused
	retryWhilePaused = 30 * time.Millisecond
	t.Cleanup(func() { retryWhilePaused = old })
	w := newWorld(t, 30, map[string]any{SettingRetrainEvery: "0s"})
	bus := events.New()
	t.Cleanup(bus.Close)
	r, err := New(Deps{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stop, err := r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r.pauseTriggers()
	if err := w.cfg.Set(SettingGrid, []any{-3}); err != nil {
		t.Fatal(err)
	}
	r.startAfter(m, "test")
	time.Sleep(80 * time.Millisecond)
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a run started during the pause: %+v", got)
	}
	r.resumeTriggers()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := m.List(); len(got) == 1 && got[0].Kind == JobKind {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the paused request never ran: %+v", m.List())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
