package qa

import (
	"context"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// cancellingGenerator produces the normal answer and cancels the request
// context before returning, the way a browser refresh or the caller's
// deadline arrives just as a paid answer completes.
type cancellingGenerator struct {
	*fakeGenerator
	cancel context.CancelFunc
}

func (g *cancellingGenerator) Generate(ctx context.Context, system, user string, opts ...engine.GenerateOptions) (engine.Generation, error) {
	res, err := g.fakeGenerator.Generate(ctx, system, user, opts...)
	g.cancel()
	return res, err
}

// contextRecordingStore records whether the history write saw a cancelled
// request context.
type contextRecordingStore struct {
	*fakeStore
	saveCalls  int
	saveCtxErr error
}

func (s *contextRecordingStore) SaveQA(ctx context.Context, rec store.QAHistory) (store.QAHistory, error) {
	s.saveCalls++
	s.saveCtxErr = ctx.Err()
	return s.fakeStore.SaveQA(ctx, rec)
}

// A completed, paid answer is persisted even when the request is cancelled
// immediately after the generator returns: the request context must not
// discard it.
func TestCancelledRequestStillPersistsTheCompletedAnswer(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.source.gen = &cancellingGenerator{fakeGenerator: w.gen, cancel: cancel}
	recorder := &contextRecordingStore{fakeStore: w.store}
	w.svc.store = recorder

	view, err := w.svc.Ask(ctx, "What happened with the budget?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if recorder.saveCalls != 1 {
		t.Fatalf("history saves = %d, want 1", recorder.saveCalls)
	}
	if recorder.saveCtxErr != nil {
		t.Fatalf("the history write used the cancelled request context: %v", recorder.saveCtxErr)
	}
	if len(w.store.saved) != 1 {
		t.Fatalf("history rows = %d, want 1", len(w.store.saved))
	}
	saved := w.store.saved[0]
	if saved.Answer == "" || saved.Answer != view.Answer {
		t.Fatalf("saved answer %q, generated %q", saved.Answer, view.Answer)
	}
	if view.ID == 0 || view.ID != saved.ID {
		t.Fatalf("returned id %d, saved id %d", view.ID, saved.ID)
	}
}
