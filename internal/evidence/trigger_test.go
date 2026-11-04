package evidence

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// A successful sync and an index run start a run; a failed sync does not;
// the setting turns it off (checked on the gate itself: event handlers run
// on their own goroutines).
func TestRunsFollowSyncsAndIndexRuns(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	finished, err := events.Subscribe(w.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := w.ev.AfterRuns(w.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	wait := func(what string) Report {
		select {
		case rep := <-finished.C:
			return rep
		case <-time.After(10 * time.Second):
			t.Fatalf("no evidence run after %s", what)
		}
		return Report{}
	}
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{Status: runstate.Failed})
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}})
	if rep := wait("a sync"); rep.State != runstate.Succeeded || rep.Done != 5 {
		t.Fatalf("%+v", rep)
	}
	events.Publish(w.bus, index.TopicFinished, index.Report{State: runstate.Succeeded, Embedded: 1})
	if rep := wait("an index run"); rep.Written != 0 {
		t.Fatalf("nothing changed: %+v", rep)
	}
	if err := w.cfg.Set("evidence.after_runs", false); err != nil {
		t.Fatal(err)
	}
	before := len(m.List())
	w.ev.startAfter(m, "test")
	if len(m.List()) != before {
		t.Fatal("a run started while evidence.after_runs is off")
	}
}

// Regression (review L0, bugs #7, rules #5): an index run that finishes
// while an evidence run is in progress is not lost: one more evidence run
// follows the current one.
func TestATriggerDuringARunIsNotLost(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	finished, err := events.Subscribe(w.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := w.ev.AfterRuns(w.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	hold, release := make(chan struct{}), make(chan struct{})
	go func() {
		first := true
		_, _ = w.ev.Run(t.Context(), func(Progress) {
			if first {
				first = false
				close(hold)
				<-release // the run is in progress while the index run finishes
			}
		})
	}()
	<-hold
	events.Publish(w.bus, index.TopicFinished, index.Report{State: runstate.Succeeded, Embedded: 1})
	deadline := time.Now().Add(10 * time.Second)
	for !w.ev.single.Pending() {
		if time.Now().After(deadline) {
			t.Fatal("the trigger was dropped while the run was in progress")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for range 2 { // the held run, then the one that followed it
		select {
		case <-finished.C:
		case <-time.After(10 * time.Second):
			t.Fatal("no evidence run followed the trigger")
		}
	}
}

// A settings change on a key a signal reads starts a run; another key does not.
func TestASettingsChangeOnAReadKeyStartsARun(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	finished, err := events.Subscribe(w.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := w.ev.AfterRuns(w.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !w.ev.readsAny([]string{settingMaxDates}) || w.ev.readsAny([]string{"log.level"}) {
		t.Fatal("readsAny follows the signals' declared settings")
	}
	if err := w.cfg.Set(settingMaxDates, 3); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished.C:
	case <-time.After(10 * time.Second):
		t.Fatal("no evidence run after a change of a setting a signal reads")
	}
}

// A failed sync starts no evidence run; a successful one starts exactly
// one. Each phase drains its handlers before counting, so the negative
// assertion is not a sleep: stop waits for every subscriber's queue.
func TestSyncRunsStartEvidenceOnlyWhenSucceeded(t *testing.T) {
	failed := newWorld(t, everything, nil)
	failed.write(conversation()...)
	m := jobs.NewManager(failed.cfg, failed.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	stop, err := failed.ev.AfterRuns(failed.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	events.Publish(failed.bus, syncer.TopicFinished, syncer.Report{Status: runstate.Failed})
	stop()
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a failed sync started %d evidence job(s): %+v", len(got), got)
	}
	if got := failed.evidenceRows(); got != 0 {
		t.Fatalf("a failed sync wrote %d evidence row(s)", got)
	}

	ok := newWorld(t, everything, nil)
	ok.write(conversation()...)
	m2 := jobs.NewManager(ok.cfg, ok.bus, nil)
	t.Cleanup(func() { _ = m2.Shutdown(t.Context()) })
	finished, err := events.Subscribe(ok.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err = ok.ev.AfterRuns(ok.bus, m2)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	events.Publish(ok.bus, syncer.TopicFinished, syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}})
	select {
	case rep := <-finished.C:
		if rep.State != runstate.Succeeded {
			t.Fatalf("%+v", rep)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no evidence run after a successful sync")
	}
	if got := m2.List(); len(got) != 1 || got[0].Kind != JobKind {
		t.Fatalf("a successful sync must start exactly one evidence job: %+v", got)
	}
}

// uncommittedIndexReports are the index outcomes that changed no evidence
// input: a run stopped before its first successful vector write, every
// attempted batch failed, and a succeeded run that selected nothing.
func uncommittedIndexReports() []struct {
	name string
	rep  index.Report
} {
	return []struct {
		name string
		rep  index.Report
	}{
		{"failed unavailable", index.Report{State: runstate.Failed, StoppedBy: index.StoppedUnavailable, Failed: 8, NotAttempted: 33834}},
		{"failed transient", index.Report{State: runstate.Failed, StoppedBy: index.StoppedTransient, Failed: 4, NotAttempted: 100}},
		{"failed store busy", index.Report{State: runstate.Failed, StoppedBy: index.StoppedBusy, Failed: 0, NotAttempted: 100}},
		{"cancelled", index.Report{State: runstate.Cancelled, StoppedBy: index.StoppedCancelled, NotAttempted: 100}},
		{"every attempted batch failed", index.Report{State: runstate.Failed, Failed: 7, NotAttempted: 0}},
		{"succeeded with nothing selected", index.Report{State: runstate.Succeeded}},
	}
}

// A sync that advanced the stored mail starts evidence even when some folders
// failed; an output failure and a no-op run do not. This is the partial-sync
// counterpart of TestSyncRunsStartEvidenceOnlyWhenSucceeded.
func TestEvidenceFollowsAdvancedSyncs(t *testing.T) {
	cases := []struct {
		name string
		rep  syncer.Report
		want bool
	}{
		{"partial with a write", syncer.Report{Status: runstate.Failed, Partial: true, Totals: store.RunTotals{Written: 1}}, true},
		{"output failure with a write", syncer.Report{Status: runstate.Failed, Totals: store.RunTotals{Written: 1}}, false},
		{"successful no-op", syncer.Report{Status: runstate.Succeeded}, false},
		{"successful write", syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, everything, nil)
			w.write(conversation()...)
			m := jobs.NewManager(w.cfg, w.bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
			finished, err := events.Subscribe(w.bus, TopicFinished, 8)
			if err != nil {
				t.Fatal(err)
			}
			stop, err := w.ev.AfterRuns(w.bus, m)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			events.Publish(w.bus, syncer.TopicFinished, c.rep)
			if c.want {
				select {
				case rep := <-finished.C:
					if rep.State != runstate.Succeeded || rep.Done != 5 {
						t.Fatalf("%+v", rep)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("no evidence run after a sync that advanced the mail")
				}
				if got := m.List(); len(got) != 1 || got[0].Kind != JobKind {
					t.Fatalf("exactly one evidence job must follow: %+v", got)
				}
				return
			}
			stop() // every handler drained: the event was handled
			if got := m.List(); len(got) != 0 {
				t.Fatalf("a sync that did not advance started %d evidence job(s): %+v", len(got), got)
			}
			if got := w.evidenceRows(); got != 0 {
				t.Fatalf("a sync that did not advance wrote %d evidence row(s)", got)
			}
		})
	}
}

// An index run that stored or confirmed no vector starts no evidence run,
// writes no row and changes no database or WAL byte.
func TestIndexRunWithoutCommittedWorkStartsNoEvidence(t *testing.T) {
	for _, c := range uncommittedIndexReports() {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, everything, nil)
			w.write(conversation()...)
			m := jobs.NewManager(w.cfg, w.bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
			stop, err := w.ev.AfterRuns(w.bus, m)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			dbBefore, walBefore := w.storeBytes()
			runsBefore := w.evidenceRuns()
			events.Publish(w.bus, index.TopicFinished, c.rep)
			stop() // every handler drained: the event was handled
			if got := m.List(); len(got) != 0 {
				t.Fatalf("an index run with no committed work started %d job(s): %+v", len(got), got)
			}
			if got := w.evidenceRows(); got != 0 {
				t.Fatalf("an index run with no committed work wrote %d evidence row(s)", got)
			}
			if got := w.evidenceRuns(); got != runsBefore {
				t.Fatalf("an index run with no committed work persisted %d evidence run(s), want %d", got, runsBefore)
			}
			dbAfter, walAfter := w.storeBytes()
			if dbAfter != dbBefore || walAfter != walBefore {
				t.Fatalf("an index run with no committed work changed the store: db %d -> %d, wal %d -> %d",
					dbBefore, dbAfter, walBefore, walAfter)
			}
		})
	}
}

// An index run that stored or confirmed a vector starts exactly one
// evidence run, even when the run failed partway: the snapshot represents
// the vectors still missing.
func TestIndexRunWithCommittedWorkStartsEvidence(t *testing.T) {
	cases := []struct {
		name string
		rep  index.Report
	}{
		{"failed with embedded vectors", index.Report{State: runstate.Failed, StoppedBy: index.StoppedUnavailable, Embedded: 3}},
		{"failed with confirmed vectors", index.Report{State: runstate.Failed, StoppedBy: index.StoppedTransient, Unchanged: 2}},
		{"succeeded with embedded vectors", index.Report{State: runstate.Succeeded, Embedded: 2}},
		{"succeeded with confirmed vectors", index.Report{State: runstate.Succeeded, Unchanged: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, everything, nil)
			w.write(conversation()...)
			m := jobs.NewManager(w.cfg, w.bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
			finished, err := events.Subscribe(w.bus, TopicFinished, 8)
			if err != nil {
				t.Fatal(err)
			}
			stop, err := w.ev.AfterRuns(w.bus, m)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			events.Publish(w.bus, index.TopicFinished, c.rep)
			select {
			case rep := <-finished.C:
				if rep.State != runstate.Succeeded || rep.Done != 5 {
					t.Fatalf("%+v", rep)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no evidence run after an index run that committed work")
			}
			list := m.List()
			if len(list) != 1 || list[0].Kind != JobKind {
				t.Fatalf("exactly one evidence job must follow a committed index run: %+v", list)
			}
			if got := w.evidenceRuns(); got != 1 {
				t.Fatalf("persisted %d evidence run(s), want 1", got)
			}
		})
	}
}
