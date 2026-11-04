package search

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// recordingSource records the purpose of every text sent through its
// embedder, so a test can prove Warm embeds a query and no document.
type recordingSource struct {
	src EmbedderSource
	mu  sync.Mutex
	got []engine.Purpose
}

func (r *recordingSource) Embedder() (engine.Embedder, error) {
	e, err := r.src.Embedder()
	if err != nil {
		return nil, err
	}
	return &recordingEmbedder{Embedder: e, src: r}, nil
}

func (r *recordingSource) record(texts []engine.Text) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range texts {
		r.got = append(r.got, t.Purpose)
	}
}

func (r *recordingSource) purposes() []engine.Purpose {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.Purpose(nil), r.got...)
}

type recordingEmbedder struct {
	engine.Embedder
	src *recordingSource
}

func (e *recordingEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.src.record(texts)
	return e.Embedder.Embed(ctx, texts)
}

func TestWarmRefreshesVectorsAndEmbedsOneQuery(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	rec := &recordingSource{src: w.s.src}
	w.s.src = rec
	before := w.fake.Fake.Calls()

	if err := w.s.Warm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := w.s.vectors.Len(); got != 3 {
		t.Fatalf("warm loaded %d vectors, want 3", got)
	}
	if calls := w.fake.Fake.Calls() - before; calls != 1 {
		t.Fatalf("warm made %d embedding calls, want 1", calls)
	}
	if got := rec.purposes(); len(got) != 1 || got[0] != engine.PurposeQuery {
		t.Fatalf("warm embedded %v, want one query", got)
	}
}

func TestWarmFailureIsReturned(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.fake.Fake.SetStatus(401)
	if err := w.s.Warm(t.Context()); err == nil {
		t.Fatal("a failing warm must return its error")
	}
}

// Warm must be safe beside live searches: both refresh the one vector copy
// and read coverage through the existing locks.
func TestWarmIsSafeBesideSearch(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := w.s.Warm(t.Context()); err != nil {
			errs <- err
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.s.Search(t.Context(), Query{Text: "budget invoice"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// Prepare's vector refresh takes the set's exclusive lock, but only the
// semantic leg may wait on it: the interactive search still answers from
// full text within its budget and discloses the timed-out semantic side.
func TestSearchWhilePrepareRuns(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()

	entered := make(chan struct{})
	release := make(chan struct{})
	base := w.s.refresh
	var once sync.Once
	w.s.refresh = func(ctx context.Context, src vector.Source, space string, dims int) error {
		once.Do(func() { close(entered) })
		<-release
		return base(ctx, src, space, dims)
	}

	prepared := make(chan error, 1)
	go func() { prepared <- w.s.Prepare(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Prepare never entered the vector refresh")
	}

	start := time.Now()
	res, err := w.s.Search(t.Context(), Query{Text: "invoice"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("search during Prepare: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("search during Prepare took %s, want the text answer within ~1 s", elapsed)
	}
	if len(res.Hits) == 0 {
		t.Fatal("search during Prepare returned no full-text hits")
	}
	if res.Semantic.Used || !res.Semantic.TimedOut {
		t.Fatalf("search during Prepare did not disclose the blocked semantic leg: %+v", res.Semantic)
	}
	if !strings.HasPrefix(res.Notice, "full-text results only (") {
		t.Fatalf("search during Prepare did not show the text-only notice: %q", res.Notice)
	}

	close(release)
	select {
	case err := <-prepared:
		if err != nil {
			t.Fatalf("Prepare failed after the search: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Prepare did not finish after the release")
	}
}
