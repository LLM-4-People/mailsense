package evidence

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// counted wraps a signal's extractor with a call counter.
func counted(w *world, name string, version int) *atomic.Int64 {
	var n atomic.Int64
	for i, s := range w.ev.signals {
		if s.Name == name {
			inner := s.Extract
			w.ev.signals[i].Version = version
			w.ev.signals[i].Extract = func(in *Input) Value { n.Add(1); return inner(in) }
		}
	}
	return &n
}

// A second run over unchanged mail reuses every message-only value and
// writes nothing; a changed message is computed again; a signal whose
// version changes is computed again alone.
func TestIncrementalAndVersioned(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	links, kinds := counted(w, "link_count", 1), counted(w, "message_kind", 1)
	if rep := w.run(); rep.Messages != 5 || rep.Done != 5 || rep.Written != 5 || rep.Computed != 5 || links.Load() != 5 {
		t.Fatalf("first run %+v, links %d", rep, links.Load())
	}
	if rep := w.run(); rep.Written != 0 || rep.Computed != 0 || links.Load() != 5 {
		t.Fatalf("an unchanged mailbox: %+v, links %d", rep, links.Load())
	}
	changed := conversation()[2]
	changed.Body = "Thursday, see https://example.org"
	w.write(changed)
	if rep := w.run(); rep.Computed != 1 || rep.Written != 1 || links.Load() != 6 {
		t.Fatalf("one changed message: %+v, links %d", rep, links.Load())
	}
	want(t, w.record("q2"), "link_count", Int(1))
	counted(w, "link_count", 2)
	if rep := w.run(); rep.Computed != 5 || links.Load() != 11 || kinds.Load() != 6 {
		t.Fatalf("a new version recomputes that signal only: %+v, links %d, kinds %d", rep, links.Load(), kinds.Load())
	}
}

func TestStageFollowsTheMail(t *testing.T) {
	w := newWorld(t, everything, nil)
	stage := w.ev.Stage()
	status := func() learning.StageStatus {
		s, err := stage.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); s.State != learning.Pending || stage.ID() != learning.StageEvidence {
		t.Fatalf("no mail: %+v", s)
	}
	w.write(conversation()...)
	if s := status(); s.State != learning.Running || s.Progress.Done != 0 || s.Progress.Total != 5 {
		t.Fatalf("before a run: %+v", s)
	}
	w.run()
	if s := status(); s.State != learning.Done || s.Progress.Done != 5 {
		t.Fatalf("after a run: %+v", s)
	}
	w.write(msg("new", "ann@example.org", 300, "Again"))
	if s := status(); s.State != learning.Running || s.Progress.Done != 5 || s.Progress.Total != 6 {
		t.Fatalf("new mail: %+v", s)
	}
	w.run()
	if s := status(); s.State != learning.Done {
		t.Fatalf("learned again: %+v", s)
	}
	// A setting a signal reads moves its effective version: not done until recomputed.
	if err := w.cfg.Set("evidence.lookalike.max_edits", 2); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.State != learning.Running {
		t.Fatalf("a changed setting: %+v", s)
	}
}

func TestStageWaitsForTheZoneAndBlocksOnAFailure(t *testing.T) {
	w := newWorld(t, everything, map[string]any{"time.zone": ""})
	w.write(conversation()...)
	if s, _ := w.ev.Stage().Status(t.Context()); s.State != learning.Pending {
		t.Fatalf("an unconfirmed zone: %+v", s)
	}
	if _, err := w.ev.Run(t.Context(), nil); err == nil {
		t.Fatal("a run must refuse an unconfirmed zone (dates are resolved in the owner's day)")
	}
	w2 := newWorld(t, everything, nil)
	w2.write(conversation()...)
	for i, s := range w2.ev.signals {
		if s.Name == "link_count" {
			w2.ev.signals[i].Extract = func(*Input) Value { return Value{State: "bogus"} }
		}
	}
	rep, err := w2.ev.Run(t.Context(), nil)
	if err == nil || rep.State != runstate.Failed {
		t.Fatalf("a broken value must fail the run: %+v %v", rep, err)
	}
	s, _ := w2.ev.Stage().Status(t.Context())
	if s.State != learning.Blocked || len(s.Blockers) != 1 || s.Blockers[0].Code != BlockRunFailed {
		t.Fatalf("blocked with its action: %+v", s)
	}
}

func TestCancelledRunKeepsItsBatchesAndOneRunAtATime(t *testing.T) {
	w := newWorld(t, everything, map[string]any{"evidence.batch_size": 2, "evidence.workers": 1})
	w.write(conversation()...)
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	release := make(chan struct{})
	for i, s := range w.ev.signals {
		if s.Name == "link_count" {
			inner := s.Extract
			var n atomic.Int64
			w.ev.signals[i].Extract = func(in *Input) Value {
				if n.Add(1) == 3 { // the second batch
					close(entered)
					<-release
				}
				return inner(in)
			}
		}
	}
	done := make(chan Report)
	go func() { rep, _ := w.ev.Run(ctx, nil); done <- rep }()
	<-entered
	if _, err := w.ev.Run(t.Context(), nil); !errors.Is(err, ErrBusy) {
		t.Errorf("a second run: %v", err)
	}
	cancel()
	close(release)
	rep := <-done
	if rep.State != runstate.Cancelled || !rep.Cancelled || rep.Written != 2 || rep.Error == "" {
		t.Fatalf("cancelled: %+v", rep)
	}
	if s, _ := w.ev.Stage().Status(t.Context()); s.State != learning.Running || s.Progress.Done != 2 {
		t.Fatalf("a cancelled run is no learned state: %+v", s)
	}
}
