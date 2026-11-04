package search

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/safe"
)

// stubbornEmbedder blocks every embed until released, ignoring cancellation.
// It models a runtime call that has already been written and cannot be
// interrupted, which is the case a shutdown join exists for.
type stubbornEmbedder struct {
	inner engine.Embedder
	gate  chan struct{}
	once  sync.Once
	began chan struct{}
	panic bool
}

func (e *stubbornEmbedder) Name() string               { return e.inner.Name() }
func (e *stubbornEmbedder) Model() string              { return e.inner.Model() }
func (e *stubbornEmbedder) Space() string              { return e.inner.Space() }
func (e *stubbornEmbedder) Dimensions() int            { return e.inner.Dimensions() }
func (e *stubbornEmbedder) Input(t engine.Text) string { return e.inner.Input(t) }

func (e *stubbornEmbedder) Embed(_ context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.once.Do(func() { close(e.began) })
	if e.panic {
		panic("synthetic embedder crash")
	}
	<-e.gate
	return e.inner.Embed(context.Background(), texts)
}

// TestSemanticWorkerIsJoinable pins the shutdown contract of the detached
// semantic worker: it outlives the query that started it, the API's handler
// join cannot see it, so Close is the only thing that can wait for it before
// the store closes. A worker that does not stop inside the bound must report a
// miss (the caller then keeps the store open), and the next Close must join it
// once it finishes.
func TestSemanticWorkerIsJoinable(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	stubborn := &stubbornEmbedder{inner: inner, gate: make(chan struct{}), began: make(chan struct{})}
	w.s.src = fixedEmbedderSource{stubborn}

	// A non-waiting search answers from full text at its budget and leaves the
	// preparation worker running.
	if _, err := w.s.Search(t.Context(), Query{Text: "budget invoice", Budget: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stubborn.began:
	case <-time.After(5 * time.Second):
		t.Fatal("the detached worker never reached the embedder")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if w.s.Close(ctx) {
		t.Fatal("Close reported a join while the worker was still running")
	}

	// The worker finishes once the runtime call returns; a fresh Close joins it.
	close(stubborn.gate)
	joined := make(chan bool, 1)
	go func() { joined <- w.s.Close(t.Context()) }()
	select {
	case ok := <-joined:
		if !ok {
			t.Fatal("Close did not join a finished worker")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a finished worker")
	}
}

// TestSearchAfterCloseStartsNoWorker: once Close refused new workers, a search
// must answer from full text instead of starting a store reader behind the
// shutdown join.
func TestSearchAfterCloseStartsNoWorker(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingEmbedder{inner: inner}
	w.s.src = fixedEmbedderSource{counted}

	if !w.s.Close(t.Context()) {
		t.Fatal("Close reported a miss with nothing running")
	}
	res, err := w.s.Search(t.Context(), Query{Text: "budget invoice", Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Semantic.Used {
		t.Fatalf("a closed searcher reported a semantic ranking: %+v", res.Semantic)
	}
	if res.TextMatches == 0 {
		t.Fatalf("the full-text ranking was lost: %+v", res)
	}
	if got := counted.count(); got != 0 {
		t.Fatalf("a closed searcher embedded %d times", got)
	}
}

// panickingSource fails inside the semantic leg before the query cache, the
// part only the search worker's own recovery covers.
type panickingSource struct{}

func (panickingSource) Embedder() (engine.Embedder, error) { panic("synthetic source crash") }

// TestSemanticWorkerPanicIsRecovered: a panic inside the semantic leg becomes
// an error on a waiting search and a text-only answer on an interactive one.
// Without the recovery owner the goroutine would take the process down.
func TestSemanticWorkerPanicIsRecovered(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.s.src = panickingSource{}

	_, err := w.s.Search(t.Context(), Query{Text: "budget invoice", Wait: true})
	var panicErr *safe.PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("a waiting search ignored the worker's panic: err=%v", err)
	}

	res, err := w.s.Search(t.Context(), Query{Text: "budget invoice", Budget: time.Second})
	if err != nil {
		t.Fatalf("an interactive search failed on a semantic panic: %v", err)
	}
	if res.Semantic.Used || res.TextMatches == 0 {
		t.Fatalf("an interactive search lost its full-text answer: %+v", res)
	}
}

// TestQueryEmbedPanicKeepsTheNoticeHonest: a panic inside the embedder is
// recovered by the query cache and surfaces as the one unavailable sentence,
// never as the panic's internal text (which would be shown to the owner).
func TestQueryEmbedPanicKeepsTheNoticeHonest(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	w.s.src = fixedEmbedderSource{&stubbornEmbedder{inner: inner, began: make(chan struct{}), gate: closedGate(), panic: true}}

	res, err := w.s.Search(t.Context(), Query{Text: "budget invoice", Wait: true})
	if err != nil {
		t.Fatalf("a panicking embedder failed the search: %v", err)
	}
	if res.Semantic.Used || res.TextMatches == 0 {
		t.Fatalf("the full-text answer was lost: %+v", res)
	}
	if strings.Contains(strings.ToLower(res.Semantic.Reason), "panic") || strings.Contains(res.Notice, "panic") {
		t.Fatalf("the panic text reached the owner: reason=%q notice=%q", res.Semantic.Reason, res.Notice)
	}
	if res.Semantic.Reason != "the semantic index is unavailable" {
		t.Fatalf("reason %q, want the one unavailable sentence", res.Semantic.Reason)
	}
}

func closedGate() chan struct{} {
	gate := make(chan struct{})
	close(gate)
	return gate
}
