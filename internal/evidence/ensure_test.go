package evidence

import (
	"context"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// A restart owes a run when the inputs changed while the app was down: no
// sync, index or settings event fires on boot, so without the reconcile the
// stage would report "running" until the next one.
func TestEnsureStartsTheRunARestartOwes(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	job, err := w.ev.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("no mail was ever evidenced, yet no run started")
	}
	<-job.Done()
	if info := job.Info(); info.Status != runstate.Succeeded {
		t.Fatalf("the catch-up run %s: %+v", info.Status, info)
	}
	if st, _ := w.ev.Stage().Status(t.Context()); st.State != learning.Done {
		t.Fatalf("the stage after the catch-up: %+v", st)
	}
	// A second reconcile over the same inputs starts nothing.
	if job, err := w.ev.Ensure(t.Context(), m); err != nil || job != nil {
		t.Fatalf("a no-op reconcile started %v (%v)", job, err)
	}
}

// A failed run already reads the current inputs: the owner must review it,
// so the reconcile does not retry it forever.
func TestEnsureLeavesAFailedRunAlone(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	inputs, _, err := w.ev.stage.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginEvidenceRun(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.FinishEvidenceRun(t.Context(), run, runstate.Failed, "boom", "boom"); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if job, err := w.ev.Ensure(t.Context(), m); err != nil || job != nil {
		t.Fatalf("a failed run over the current inputs was retried: %v (%v)", job, err)
	}
}

// A run interrupted by a restart is started again: the work it was doing is
// still owed, unlike a failure the owner must review.
func TestEnsureStartsAgainAfterAnInterruptedRun(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	inputs, _, err := w.ev.stage.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.st.BeginEvidenceRun(t.Context(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.FinishEvidenceRun(t.Context(), run, runstate.Cancelled, "", "interrupted by a restart"); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	job, err := w.ev.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil {
		t.Fatal("an interrupted run left the current inputs unevidenced, yet no run started")
	}
	<-job.Done()
	if st, _ := w.ev.Stage().Status(t.Context()); st.State != learning.Done {
		t.Fatalf("the stage after the catch-up: %+v", st)
	}
}
