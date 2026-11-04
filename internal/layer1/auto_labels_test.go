package layer1

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// The automatic loop buys one bounded batch of the uncertainty queue,
// counts it against labels.auto.max, and stops at the cap.
func TestAutoLabelsBuysUncertainBatchesAndStopsAtMax(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 24, map[string]any{
		"labels.auto": true, "labels.auto.batch": 2, "labels.auto.max": 4,
		"labels.auto.budget_usd": 1.0, "layer1.after_runs": false,
	})
	fj, fs := newFakeJudge(), &fakeStates{fail: map[int64]bool{}}
	w.r.judge = func(string) (judge.Judge, error) { return fj, nil }
	w.r.state = fs

	bus := events.New()
	t.Cleanup(bus.Close)
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })

	run := func() {
		w.r.startAutoLabels(m, "test")
		for deadline := time.Now().Add(5 * time.Second); w.r.autoSingle.Busy(); {
			if time.Now().After(deadline) {
				t.Fatal("the automatic check did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitJudge := func(want int) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			done := 0
			for _, info := range m.List() {
				if info.Kind != JudgeJobKind {
					continue
				}
				if info.Status == runstate.Succeeded || info.Status == runstate.Failed || info.Status == runstate.Cancelled {
					done++
				}
			}
			if done >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d judge jobs finished, want %d", done, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	run()
	waitJudge(1)
	if bought, err := w.st.AutoLabelsBought(t.Context()); err != nil || bought != 2 {
		t.Fatalf("bought %d, want 2 (%v)", bought, err)
	}
	run()
	waitJudge(2)
	if bought, err := w.st.AutoLabelsBought(t.Context()); err != nil || bought != 4 {
		t.Fatalf("bought %d, want 4 (%v)", bought, err)
	}
	run()
	time.Sleep(100 * time.Millisecond)
	if got := len(m.List()); got != 2 {
		t.Fatalf("%d jobs after the cap, want 2", got)
	}
}

// A disabled loop starts nothing, and a judge that is not wired is a
// no-op rather than a paid surprise.
func TestAutoLabelsStaysOffWhenDisabledOrUnwired(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, map[string]any{"labels.auto": false, "labels.auto.batch": 2, "labels.auto.max": 4})
	w.r.judge = func(string) (judge.Judge, error) { return newFakeJudge(), nil }
	w.r.state = &fakeStates{fail: map[int64]bool{}}
	bus := events.New()
	t.Cleanup(bus.Close)
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	w.r.startAutoLabels(m, "test")
	time.Sleep(100 * time.Millisecond)
	if got := len(m.List()); got != 0 {
		t.Fatalf("%d jobs with labels.auto off", got)
	}
}
