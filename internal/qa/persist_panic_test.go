package qa

import (
	"context"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// panickingSaveStore fails the history write the way a broken store driver
// would: not with an error, but with a panic.
type panickingSaveStore struct{ *fakeStore }

func (s *panickingSaveStore) SaveQA(context.Context, store.QAHistory) (store.QAHistory, error) {
	panic("history write panicked")
}

// TestPanickingHistoryWriteIsContained pins the panic path of the detached
// save. The write runs after the paid answer exists, so a panic in the store
// must reach the caller as an ordinary error the API can report, not abort the
// response mid-write with the answer unsaved and unexplained.
func TestPanickingHistoryWriteIsContained(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.svc.store = &panickingSaveStore{fakeStore: w.store}

	_, err := w.svc.Ask(t.Context(), "What happened with the budget?", "en")
	if err == nil {
		t.Fatal("a panicking history write returned no error")
	}
	var panicErr *safe.PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("a panicking history write returned %v, want a *safe.PanicError", err)
	}
	if len(w.store.saved) != 0 {
		t.Fatalf("a panicking write left %d rows behind", len(w.store.saved))
	}
}
