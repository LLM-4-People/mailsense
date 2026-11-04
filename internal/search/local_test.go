//go:build embedlocal

package search

// Local benchmark with the real model (scripts/embed-local.sh): the
// pinned Linux llama-server through the sidecar (download, verify, tune,
// start: the code path of Windows), a sample of the fixture indexed for
// throughput, the int8 storage form against float32 (recall of the vector
// ranking, size), hybrid quality against full text alone, and search
// latency. Prints aggregates only.

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// capturing keeps the float32 vectors the index receives, by input hash.
type capturing struct {
	src *embed.Source
	mu  sync.Mutex
	vec map[string][]float32
}

func (c *capturing) Embedder() (engine.Embedder, error) {
	e, err := c.src.Embedder()
	return &capturingEmbedder{Embedder: e, c: c}, err
}

type capturingEmbedder struct {
	engine.Embedder
	c *capturing
}

func (e *capturingEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	res, err := e.Embedder.Embed(ctx, texts)
	if err == nil {
		e.c.mu.Lock()
		for i, t := range texts {
			e.c.vec[digest.Hex([]byte(e.Input(t)), 16)] = res.Vectors[i]
		}
		e.c.mu.Unlock()
	}
	return res, err
}

func TestLocalRealModel(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("EMBEDLOCAL_MESSAGES"))
	subset := sample(t, fixturetest.Path(t), max(n, 100))
	overrides := map[string]any{"sidecar.dir": os.Getenv("EMBEDLOCAL_RUNTIME_DIR"), "search.embed_timeout": "30s"}
	if batch := os.Getenv("EMBEDLOCAL_BATCH"); batch != "" {
		v, err := strconv.Atoi(batch)
		if err != nil || v < 1 {
			t.Fatalf("EMBEDLOCAL_BATCH=%q: want a positive integer", batch)
		}
		overrides["embed.batch_size"] = v
	}
	if file := os.Getenv("EMBEDLOCAL_PROVIDERS"); file != "" {
		overrides[engine.ProvidersFileSetting] = file
	}
	if id := os.Getenv("EMBEDLOCAL_PROVIDER"); id != "" {
		overrides["embed.provider"] = id
	}
	cfg, st := fixturetest.Import(t, subset, overrides)
	sc, err := sidecar.New(sidecar.Options{Config: cfg, Jobs: jobs.NewManager(cfg, nil, nil), Probe: embed.Probe(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Stop()
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: cfg, Secrets: sec, Locator: sc})
	cap := &capturing{src: src, vec: map[string][]float32{}}
	ix := index.New(cfg, st, nil, cap, nil)
	var batches int
	rep, err := ix.Run(t.Context(), index.Options{}, func(index.Progress) { batches++ })
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Failures {
		t.Logf("LOCAL failed batch of %d: %s", len(f.Messages), f.Error)
	}
	effective := 0
	if e, err := src.Embedder(); err == nil {
		if b, ok := e.(interface{ MaxBatch() int }); ok {
			effective = b.MaxBatch()
		}
	}
	stat := sc.Status()[0]
	t.Logf("LOCAL index batch=%d effective_batch=%d messages=%d failed=%d quarantined=%d embedded=%d batches=%d tokens=%d wall=%s mails_per_s=%.2f tokens_per_s=%.0f truncated=%d variant=%s threads=%d",
		cfg.Int("embed.batch_size"), effective, rep.After.Eligible, rep.Failed, rep.Quarantined, rep.Embedded, batches,
		rep.Usage.InputTokens, rep.Duration.Round(time.Second), rep.MailsPerSecond, rep.TokensPerSecond, rep.Truncated, stat.Variant, stat.Threads)
	s := newSearcher(t, cfg, Deps{Store: st, Embedder: src, Coverage: ix, Gate: learningtest.Open{}})
	queries := subjects(t, st, 100)
	recall(t, cfg.Int("embed.dimensions"), st, src, cap, rep.Space, queries)
	quality(t, s, st, queries)
}

// sample writes every k-th record of the export (an even spread over time).
func sample(t *testing.T, path string, n int) string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var lines [][]byte
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	out := filepath.Join(t.TempDir(), "sample.jsonl")
	f, _ := os.Create(out)
	defer f.Close()
	step := max(1, len(lines)/n)
	for i := 0; i < len(lines); i += step {
		_, _ = f.Write(append(lines[i], '\n'))
	}
	return out
}

type knownItem struct {
	row   int64
	query string
}

func subjects(t *testing.T, st *store.Store, n int) []knownItem {
	ids, _ := st.FilterIDs(t.Context(), store.SearchFilter{})
	slices.Sort(ids)
	var out []knownItem
	for i := 0; i < len(ids) && len(out) < n; i += max(1, len(ids)/n) {
		m, _ := st.MessageByRow(t.Context(), ids[i])
		if len(mail.Words(m.Subject)) >= 3 {
			out = append(out, knownItem{ids[i], m.Subject})
		}
	}
	return out
}

// recall compares the int8 ranking with the exact float32 one.
func recall(t *testing.T, dims int, st *store.Store, src *embed.Source, cap *capturing, space string, queries []knownItem) {
	states, _ := st.EmbeddingStates(t.Context(), nil)
	stored, _ := st.LoadVectors(t.Context(), space, 0)
	q8 := map[int64]vector.Quantized{}
	for _, v := range stored {
		q8[v.MessageID] = v.Vector
	}
	f32 := map[int64][]float32{}
	for _, s := range states {
		if v, ok := cap.vec[s.TextHash]; ok {
			f32[s.RowID] = v
		}
	}
	e, _ := src.Embedder()
	var r10, r50 float64
	for _, q := range queries {
		res, err := e.Embed(t.Context(), []engine.Text{{Purpose: engine.PurposeQuery, Body: q.query}})
		if err != nil {
			t.Fatal(err)
		}
		exact := rank(f32, func(id int64) float32 { return float32(vector.Dot(f32[id], res.Vectors[0])) })
		approx := rank(f32, func(id int64) float32 { return vector.DotInt8(q8[id].Values, q8[id].Scale, res.Vectors[0]) })
		r10 += overlap(exact, approx, 10)
		r50 += overlap(exact, approx, 50)
	}
	nq := float64(len(queries))
	t.Logf("LOCAL storage docs=%d dims=%d float32_bytes=%d int8_bytes=%d recall@10=%.4f recall@50=%.4f (int8 vs exact float32, %d queries)",
		len(f32), dims, len(f32)*dims*4, len(f32)*(dims+4), r10/nq, r50/nq, len(queries))
}

func rank(docs map[int64][]float32, score func(int64) float32) []int64 {
	ids := make([]int64, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sims := map[int64]float32{}
	for _, id := range ids {
		sims[id] = score(id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		if sims[a] != sims[b] {
			if sims[a] > sims[b] {
				return -1
			}
			return 1
		}
		return int(a - b)
	})
	return ids
}

func overlap(a, b []int64, k int) float64 {
	k = min(k, len(a), len(b))
	hit := 0
	for _, id := range b[:k] {
		if slices.Contains(a[:k], id) {
			hit++
		}
	}
	return float64(hit) / float64(k)
}

// quality: a message's own subject (a known item) found in the top 10,
// hybrid vs full text alone, with latency.
func quality(t *testing.T, s *Searcher, st *store.Store, queries []knownItem) {
	var lat []float64
	hybrid, text := 0, 0
	for _, q := range queries {
		start := time.Now()
		res, err := s.Search(t.Context(), Query{Text: q.query, Limit: 10})
		lat = append(lat, float64(time.Since(start).Microseconds())/1000)
		if err != nil || !res.Semantic.Used {
			t.Fatalf("search: %v %+v", err, res.Semantic)
		}
		if slices.ContainsFunc(res.Hits, func(h Hit) bool { return h.RowID == q.row }) {
			hybrid++
		}
		if fts, _ := st.SearchText(t.Context(), q.query, store.SearchFilter{}, 10); slices.ContainsFunc(fts.Hits, func(h store.Hit) bool { return h.RowID == q.row }) {
			text++
		}
	}
	p50, _ := mathx.Percentile(lat, 0.5)
	p95, _ := mathx.Percentile(lat, 0.95)
	t.Logf("LOCAL known_item queries=%d hybrid_hit@10=%d fts_hit@10=%d search_ms p50=%.1f p95=%.1f", len(queries), hybrid, text, p50, p95)
}
