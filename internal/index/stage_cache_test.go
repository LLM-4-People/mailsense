package index

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// switchEmbedder is the test's one source: a status build and a run read
// the same instance, and a test switches it between an unavailable and a
// serving provider.
type switchEmbedder struct {
	mu  sync.Mutex
	e   engine.Embedder
	err error
}

func (s *switchEmbedder) Embedder() (engine.Embedder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.e, nil
}

func (s *switchEmbedder) set(e engine.Embedder) {
	s.mu.Lock()
	s.e, s.err = e, nil
	s.mu.Unlock()
}

type testEmbedder struct {
	name  string
	block chan struct{}
	once  *sync.Once
	began chan struct{}
}

func (e testEmbedder) Name() string    { return e.name }
func (e testEmbedder) Model() string   { return e.name }
func (e testEmbedder) Space() string   { return e.name }
func (e testEmbedder) Dimensions() int { return 8 }
func (e testEmbedder) Input(t engine.Text) string {
	return t.Title + "\x00" + t.Body
}
func (e testEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	if e.name == "unavailable" {
		return engine.Embeddings{}, engine.ErrUnavailable
	}
	if e.block != nil {
		e.once.Do(func() { close(e.began) })
		select {
		case <-e.block:
		case <-ctx.Done():
			return engine.Embeddings{}, ctx.Err()
		}
	}
	out := engine.Embeddings{Vectors: make([][]float32, len(texts))}
	for i := range out.Vectors {
		v := make([]float32, 8)
		v[i%8] = 1
		out.Vectors[i] = v
	}
	return out, nil
}

// newRunWorld is the coverage-cache world with a switchable embedder.
func newRunWorld(t *testing.T, emb *switchEmbedder) *world {
	t.Helper()
	w := newWorld(t, nil)
	w.ix.src = emb
	return w
}

// A failed run's unavailable report is read once and reused while no run
// finishes; the next finished run (success or failure) makes the stage
// read the newest report again.
func TestLastRunUnavailableReusesUntilARunFinishes(t *testing.T) {
	emb := &switchEmbedder{e: testEmbedder{name: "unavailable"}}
	w := newRunWorld(t, emb)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	stage := w.ix.Stage()

	// The run stops unavailable and records its report.
	if _, err := w.ix.Run(t.Context(), Options{}, nil); err == nil {
		t.Fatal("an unavailable run reported success")
	}
	first, err := stage.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.State != learning.Blocked || len(first.Blockers) == 0 || first.Blockers[0].Code != BlockNoEmbedder {
		t.Fatalf("unavailable run: %+v", first)
	}
	// A canceled context cannot reach the database: the second read is the
	// cached unavailable detail (and the cached coverage).
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	again, err := stage.Status(ctx)
	if err != nil {
		t.Fatalf("the unavailable report was not reused: %v", err)
	}
	if again.State != first.State || len(again.Blockers) != 1 || again.Blockers[0].Detail != first.Blockers[0].Detail {
		t.Fatalf("cached status differs:\n%+v\n%+v", first, again)
	}

	// A serving run finishes: its sequence invalidates the check. The
	// stage now reaches the store again (a canceled context proves it), and
	// a normal read no longer blocks on the old report.
	emb.set(testEmbedder{name: "perf/space/8"})
	if rep, err := w.ix.Run(t.Context(), Options{}, nil); err != nil {
		t.Fatalf("serving run: %+v %v", rep, err)
	}
	if _, err := stage.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a finished run left the old report cached: %v", err)
	}
	after, err := stage.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.State == learning.Blocked {
		t.Fatalf("the succeeded run still reports the unavailable blocker: %+v", after)
	}
}

// A provider change is read on every status call, before any cached
// coverage or report: a cached done status must never hide a missing
// embedder.
func TestAProviderChangeIsNeverMaskedByTheCache(t *testing.T) {
	emb := &switchEmbedder{e: testEmbedder{name: "perf/space/8"}}
	w := newRunWorld(t, emb)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	stage := w.ix.Stage()
	if st, err := stage.Status(t.Context()); err != nil || st.State == learning.Blocked {
		t.Fatalf("initial status: %+v %v", st, err)
	}
	emb.mu.Lock()
	emb.err = engine.ErrUnavailable
	emb.mu.Unlock()
	st, err := stage.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != learning.Blocked || len(st.Blockers) == 0 || st.Blockers[0].Code != BlockNoEmbedder {
		t.Fatalf("a provider change was hidden by the cache: %+v", st)
	}
}

// A live run answers from its progress before any coverage or run read:
// the stage must stay live even when a cached, canceled context could not
// reach the store.
func TestStageStatusStaysLiveDuringARun(t *testing.T) {
	emb := &switchEmbedder{}
	block, began := make(chan struct{}), make(chan struct{})
	var once sync.Once
	emb.e = testEmbedder{name: "perf/space/8", block: block, once: &once, began: began}
	w := newRunWorld(t, emb)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	done := make(chan error, 1)
	go func() {
		_, err := w.ix.Run(context.Background(), Options{}, nil)
		done <- err
	}()
	<-began

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	st, err := w.ix.Stage().Status(ctx)
	if err != nil {
		t.Fatalf("live status read the store: %v", err)
	}
	if st.State != learning.Running || st.Progress.Total == 0 || st.Progress.Unit != learning.UnitMessages {
		t.Fatalf("live status: %+v", st)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
