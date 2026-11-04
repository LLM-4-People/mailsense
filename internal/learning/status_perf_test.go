package learning_test

// Baseline and regression measurements for the learning status fan-out. A
// benchmark (not a timing assertion) so the numbers can be compared across
// rounds without making the suite host-dependent.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

var perfKey = source.StoreKey("src", "store-1")

type perfEmbedder struct {
	block   chan struct{} // when non-nil, Embed waits for it (a live run)
	entered chan struct{} // signalled once the first Embed is waiting
	once    sync.Once
}

func (e *perfEmbedder) Name() string               { return "perf" }
func (e *perfEmbedder) Model() string              { return "perf" }
func (e *perfEmbedder) Space() string              { return "perf/space/8" }
func (e *perfEmbedder) Dimensions() int            { return 8 }
func (e *perfEmbedder) Input(t engine.Text) string { return t.Title + "\x00" + t.Body }
func (e *perfEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	if e.block != nil {
		e.once.Do(func() { close(e.entered) })
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

type perfSource struct{ e engine.Embedder }

func (s perfSource) Embedder() (engine.Embedder, error) { return s.e, nil }

type perfWorld struct {
	cfg *config.Config
	st  *store.Store
	ix  *index.Indexer
	mb  learning.Stage
	sy  learning.Stage
}

// newPerfWorld builds a learned-looking mailbox of n messages: one picked,
// discovered store, every folder read, every message judged, no vectors
// yet (the embedding stage plans the full coverage).
func newPerfWorld(tb testing.TB, n int, emb *perfEmbedder) *perfWorld {
	tb.Helper()
	cfg := configtest.New(tb, map[string]any{
		source.SettingSelected: []any{perfKey},
		"time.zone":            "UTC",
	})
	st, err := store.Open(tb.Context(), cfg, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	caps := source.Capabilities{Bodies: true, Recipients: true, Headers: true, FolderRoles: true, OwnerIdentity: true, Incremental: true}
	if err := st.UpsertSource(tb.Context(), "src", "test", "Test", caps); err != nil {
		tb.Fatal(err)
	}
	if err := st.SaveMailboxes(tb.Context(), "src", []source.Mailbox{{
		Key: perfKey, SourceID: "src", StoreID: "store-1", Name: "Test", Kind: mail.StorePrimary,
		Folders: 1, Items: int64(n), IdentityComplete: true,
	}}); err != nil {
		tb.Fatal(err)
	}
	msgs := make([]mail.Message, n)
	for i := range msgs {
		m := mailtest.Message("src", fmt.Sprintf("m%05d", i))
		m.ReceivedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute).Format(mail.TimeLayout)
		msgs[i] = m
	}
	if err := st.UpsertMessages(tb.Context(), 0, msgs); err != nil {
		tb.Fatal(err)
	}
	if err := st.SaveReported(tb.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		tb.Fatal(err)
	}
	owners, err := st.Owners(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	var verdicts []store.VerdictRow
	if err := st.EachMessage(tb.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch, Revision: r.Revision})
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	if err := st.WriteVerdicts(tb.Context(), owners[perfKey].Identity.Digest(), verdicts); err != nil {
		tb.Fatal(err)
	}
	folders, err := st.ListFolders(tb.Context(), "src")
	if err != nil {
		tb.Fatal(err)
	}
	cursors := map[int64]string{}
	for _, f := range folders {
		cursors[f.ID] = "cursor"
	}
	if err := st.SetCursors(tb.Context(), cursors); err != nil {
		tb.Fatal(err)
	}
	ix := index.New(cfg, st, nil, perfSource{e: emb}, nil)
	return &perfWorld{cfg: cfg, st: st, ix: ix, mb: syncer.MailboxesStage(cfg, st), sy: syncer.SyncStage(cfg, st)}
}

// buildStatus is one synchronous full build: a new Machine has no cached
// status, so every stage is asked exactly once.
func buildStatus(tb testing.TB, w *perfWorld) learning.Status {
	tb.Helper()
	return buildStatusWith(tb, w, w.mb, w.sy, w.ix.Stage())
}

func buildStatusWith(tb testing.TB, w *perfWorld, mb, sy, embeddings learning.Stage) learning.Status {
	tb.Helper()
	m, err := learning.New(func() string { return perfKey }, w.st, nil, nil,
		mb,
		learning.MachineStage(w.cfg, nil),
		sy,
		learning.NotBuilt(learning.StageEvidence, "perf"),
		embeddings,
		learning.NotBuilt(learning.StageLabels, "perf"),
		learning.NotBuilt(learning.StageLayer1, "perf"),
		learning.NotBuilt(learning.StageCalibration, "perf"),
		learning.NotBuilt(learning.StageQuestions, "perf"),
	)
	if err != nil {
		tb.Fatal(err)
	}
	st, err := m.Status(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	return st
}

// BenchmarkStatusBuildIdle measures one idle status build over an unchanged
// mailbox: the coalesced progress refresh runs exactly this rebuild once per
// second while nothing moves. Each iteration builds a new Machine (whose
// first Status is synchronous) because a warm Machine returns its fresh
// status without asking a stage: that path is O(1) and would measure
// nothing. "fresh" gives every stage instance an empty cache (the behavior
// before this round); "reused" keeps the stage owners' caches, which is what
// a progress refresh does.
func BenchmarkStatusBuildIdle(b *testing.B) {
	for _, n := range []int{500, 5000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			w := newPerfWorld(b, n, &perfEmbedder{})
			b.Run("fresh", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					mb, sy := syncer.MailboxesStage(w.cfg, w.st), syncer.SyncStage(w.cfg, w.st)
					buildStatusWith(b, w, mb, sy, w.ix.Stage())
				}
			})
			buildStatus(b, w) // warm the owner caches
			b.Run("reused", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					buildStatus(b, w)
				}
			})
		})
	}
}

// BenchmarkOwnedStageStatus measures the three stages the status cache
// round owns, one at a time.
func BenchmarkOwnedStageStatus(b *testing.B) {
	for _, n := range []int{500, 5000} {
		w := newPerfWorld(b, n, &perfEmbedder{})
		for _, tc := range []struct {
			name  string
			stage learning.Stage
		}{{"mailboxes", w.mb}, {"sync", w.sy}, {"embeddings", w.ix.Stage()}} {
			b.Run(fmt.Sprintf("%s/messages=%d", tc.name, n), func(b *testing.B) {
				stage := tc.stage
				if _, err := stage.Status(b.Context()); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := stage.Status(b.Context()); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkEmbeddingsStageLive measures the embeddings stage while an index
// job is live: the status must come from the run counters, not the store.
func BenchmarkEmbeddingsStageLive(b *testing.B) {
	emb := &perfEmbedder{block: make(chan struct{}), entered: make(chan struct{})}
	w := newPerfWorld(b, 2000, emb)
	done := make(chan error, 1)
	go func() {
		_, err := w.ix.Run(context.Background(), index.Options{}, nil)
		done <- err
	}()
	<-emb.entered
	stage := w.ix.Stage()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		st, err := stage.Status(context.Background())
		if err != nil || st.State != learning.Running {
			b.Fatalf("live status: %+v %v", st, err)
		}
	}
	b.StopTimer()
	close(emb.block)
	if err := <-done; err != nil {
		b.Fatal(err)
	}
}
