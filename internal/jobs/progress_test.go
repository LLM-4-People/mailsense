package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// TestStageDetailAndETA: a staged job keeps its stage, detail and step
// position, and the manager fills the ETA from the job's own rate. A stage
// that changes the denominator starts a new rate, so the classifier's
// questions never measure its messages.
func TestStageDetailAndETA(t *testing.T) {
	bus := events.New()
	sub, err := events.Subscribe(bus, TopicJob, 32)
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.New(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC))
	m := NewManager(configtest.New(t, nil), bus, clk)
	j, err := m.Start("layer1", "local classifier", func(_ context.Context, report func(Progress)) error {
		report(Progress{Done: 0, Total: 4, Unit: "questions", Stage: "design"})
		clk.Advance(30 * time.Second)
		report(Progress{Done: 1, Total: 4, Unit: "questions", Stage: "fit", Detail: "topic", StepDone: 2, StepTotal: 6})
		clk.Advance(30 * time.Second)
		report(Progress{Done: 2, Total: 4, Unit: "questions", Stage: "fold", Detail: "topic", StepDone: 1, StepTotal: 5})
		clk.Advance(time.Minute)
		report(Progress{Done: 0, Total: 300, Unit: "items", Stage: "answer"})
		clk.Advance(time.Minute)
		report(Progress{Done: 100, Total: 300, Unit: "items", Stage: "answer"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, j)
	var got []Progress
	for len(got) < 5 {
		info := <-sub.C
		if info.Status == runstate.Running && info.Progress.Stage != "" {
			got = append(got, info.Progress)
		}
	}
	if first := got[0]; first.Stage != "design" || first.ETA != nil {
		t.Fatalf("first report %+v", first)
	}
	if fit := got[1]; fit.Stage != "fit" || fit.Detail != "topic" || fit.StepDone != 2 || fit.StepTotal != 6 {
		t.Fatalf("fit report %+v", fit)
	}
	if got[1].ETA == nil || *got[1].ETA != 90*time.Second {
		t.Fatalf("fit eta %v, want 1m30s", got[1].ETA)
	}
	if fold := got[2]; fold.Stage != "fold" || fold.StepDone != 1 || fold.StepTotal != 5 {
		t.Fatalf("fold report %+v", fold)
	}
	if got[2].ETA == nil || *got[2].ETA != 60*time.Second {
		t.Fatalf("fold eta %v, want 1m", got[2].ETA)
	}
	if answer := got[3]; answer.Stage != "answer" || answer.Total != 300 || answer.ETA != nil {
		t.Fatalf("answer start %+v (a new denominator starts a new rate)", answer)
	}
	if got[4].ETA == nil || *got[4].ETA != 2*time.Minute {
		t.Fatalf("answer eta %v, want 2m", got[4].ETA)
	}
}

func TestETAForGuards(t *testing.T) {
	base := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	now := base.Add(time.Minute)
	for _, tc := range []struct {
		name      string
		gained    int64
		remaining int64
		want      time.Duration // 0: none
	}{
		{"no progress yet", 0, 10, 0},
		{"nothing left", 10, 0, 0},
		{"half done", 5, 5, time.Minute},
		{"ten percent done", 10, 90, 9 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := etaFor(base, now, tc.gained, tc.remaining)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("eta %v, want none", *got)
				}
				return
			}
			if got == nil || *got != tc.want {
				t.Fatalf("eta %v, want %v", got, tc.want)
			}
		})
	}
}
