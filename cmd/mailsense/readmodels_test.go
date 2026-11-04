package main

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/learning"
)

// fakeReadModel is a read model that reports a settable join outcome.
type fakeReadModel struct {
	joined bool
	calls  int
}

func (f *fakeReadModel) Close(context.Context) bool {
	f.calls++
	return f.joined
}

// The closer list is derived from the models that expose the join: each is
// asked exactly once, and a model without the method is simply absent.
func TestReadModelClosersDerivesFromTheModelsThatExposeTheJoin(t *testing.T) {
	hubSvc, commitmentsSvc, briefSvc := &fakeReadModel{joined: true}, &fakeReadModel{joined: true}, &fakeReadModel{joined: true}
	closers := readModelClosers(hubSvc, struct{}{}, commitmentsSvc, "not a closer", briefSvc)
	if len(closers) != 3 {
		t.Fatalf("%d closers, want the three models that expose the join", len(closers))
	}
	if !joinReadModels(t.Context(), closers) {
		t.Fatal("a fake closer reported a miss")
	}
	for name, model := range map[string]*fakeReadModel{"hub": hubSvc, "commitments": commitmentsSvc, "brief": briefSvc} {
		if model.calls != 1 {
			t.Fatalf("%s closer was called %d times, want 1", name, model.calls)
		}
	}
}

// A typed-nil learning machine (a partially wired app) is still safe to
// close: its Close guards its own receiver instead of the caller testing for
// nil.
func TestReadModelClosersToleratesATypedNilModel(t *testing.T) {
	var machine *learning.Machine
	closers := readModelClosers(machine, &fakeReadModel{joined: true})
	if len(closers) != 2 {
		t.Fatalf("%d closers, want the typed-nil machine and the fake", len(closers))
	}
	if !joinReadModels(t.Context(), closers) {
		t.Fatal("a typed-nil model made the join a miss")
	}
}

// The conjunction is the report: one model that missed its join makes the
// whole shutdown a miss, and every model is still asked.
func TestJoinReadModelsReportsTheConjunction(t *testing.T) {
	joined, missed, untouched := &fakeReadModel{joined: true}, &fakeReadModel{}, &fakeReadModel{joined: true}
	closers := []cacheCloser{joined.Close, missed.Close, untouched.Close}
	if joinReadModels(t.Context(), closers) {
		t.Fatal("a missed join was reported as joined")
	}
	if missed.calls != 1 || untouched.calls != 1 {
		t.Fatal("a later model was not asked after an earlier miss")
	}
	if !joinReadModels(t.Context(), []cacheCloser{joined.Close, untouched.Close}) {
		t.Fatal("a fully joined set was reported as a miss")
	}
}

// The join honors the caller's bound: a model that ignores its context does
// not turn the shutdown into a hang.
func TestJoinReadModelsHonorsTheCallerBound(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	stuck := cacheCloser(func(ctx context.Context) bool {
		select {
		case <-blocked:
			return true
		case <-ctx.Done():
			return false
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if joinReadModels(ctx, []cacheCloser{stuck}) {
		t.Fatal("a stuck join was reported as joined")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("the join waited %s, want the caller's bound", elapsed)
	}
}

// TestEveryCloserToleratesAnUnwiredApp: shutdown runs the join even for a
// partially built app (a failed start, a tool with no API), and a nil
// dereference there would abort the process instead of closing cleanly.
func TestEveryCloserToleratesAnUnwiredApp(t *testing.T) {
	ctx := context.Background()
	for name, closer := range map[string]cacheCloser{
		"hub": hubAPI{}.Close, "companies": companiesAPI{}.Close, "conversations": conversationsAPI{}.Close,
		"commitments": commitmentsAPI{}.Close, "triage": triageAPI{}.Close,
		"messages": messagesAPI{}.Close, "brief": briefAPI{}.Close, "search": searchAPI{}.Close,
	} {
		if !closer(ctx) {
			t.Errorf("%s closer reported a miss with nothing to join", name)
		}
	}
	// A wire with no agenda service contributes no closer at all, so the join
	// has nothing to call rather than a nil receiver.
	if !joinReadModels(ctx, readModelCloserList(&wire{app: &app{}}, nil)) {
		t.Fatal("an unwired app's closer list reported a miss")
	}
}
