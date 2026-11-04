//go:build embedlocal

package layer1

// The embedding job of the layer-1 harness (scripts/layer1-fixture.sh
// embed): real EmbeddingGemma vectors through the pinned Linux sidecar,
// kept in the private vector cache (LAYER1_CACHE_DIR, never out/) so
// every later harness run reads them instead of embedding again. Phase
// "sample": the experiment sample (gold + a stratified draw) in each E1
// purpose; phase "full": the index of the whole fixture (document
// vectors). Prints and writes aggregates only.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type fixedSource struct{ e engine.Embedder }

func (s fixedSource) Embedder() (engine.Embedder, error) { return s.e, nil }

func TestLayer1Embed(t *testing.T) {
	t.Parallel()
	cacheDir := harnessEnv(t, "LAYER1_CACHE_DIR")
	gold := goldOf(t)
	ctx := harnessContext(t, "embed")
	n, _ := strconv.Atoi(os.Getenv("LAYER1_SAMPLE"))
	phases := strings.Split(os.Getenv("LAYER1_EMBED_PHASES"), ",")
	cfg, st := fixturetest.Import(t, fixturetest.Path(t), providerOverrides(map[string]any{"sidecar.dir": harnessEnv(t, "EMBEDLOCAL_RUNTIME_DIR")}))
	sc, err := sidecar.New(sidecar.Options{Config: cfg, Jobs: jobs.NewManager(cfg, nil, nil), Probe: embed.Probe(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Stop()
	cache, err := fixturetest.OpenVectorCache(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	deps := engine.Deps{Config: cfg, Secrets: sec, Locator: sc}
	report := map[string]any{}
	for _, phase := range phases {
		switch {
		case phase == "sample" || strings.HasPrefix(phase, "sample:"):
			sample := experimentSample(t, st, gold, n)
			texts, err := st.MessageTexts(t.Context(), sample)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range purposes {
				if phase == "sample" || phase == "sample:"+p.name {
					report["sample/"+p.name] = embedSample(ctx, t, cache, p, p.embedder(t, deps), sample, texts)
				}
			}
		case phase == "full":
			src := cache.Source(embed.NewSource(deps))
			ix := index.New(cfg, st, nil, src, nil)
			start := time.Now()
			rep, err := ix.Run(ctx, index.Options{}, func(p index.Progress) {
				if p.Done%2048 < cfg.Int("embed.batch_size") {
					t.Logf("LAYER1 full %d/%d embedded=%d", p.Done, p.Total, p.Embedded)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			wall := time.Since(start)
			t.Logf("LAYER1 full eligible=%d indexed=%d failed=%d cached=%d embedded_now=%d wall=%s", rep.After.Eligible, rep.After.Indexed,
				rep.Failed, src.Hits, src.Misses, wall.Round(time.Second))
			report["full"] = map[string]any{"eligible": rep.After.Eligible, "indexed": rep.After.Indexed, "failed": rep.Failed,
				"cached": src.Hits, "embedded": src.Misses, "wall_s": wall.Seconds(), "truncated": rep.Truncated,
				"embedded_per_s": float64(src.Misses) / wall.Seconds(), "space": rep.Space}
		default:
			t.Fatalf("unknown phase %q (sample, sample:<purpose>, full)", phase)
		}
	}
	if out := os.Getenv("LAYER1_REPORT_DIR"); out != "" {
		if err := fsx.WriteJSON(filepath.Join(out, "layer1-embed.json"), report, fsx.FilePerm, cfg.Snapshot().Replace(nil)); err != nil {
			t.Fatal(err)
		}
	}
}

// embedSample embeds the sample in one purpose through the cache, in
// chunks, logging the rate.
func embedSample(ctx context.Context, t *testing.T, cache *fixturetest.VectorCache, p purpose, e engine.Embedder, sample []int64, texts map[int64]store.MessageText) map[string]any {
	src := cache.Source(fixedSource{e})
	ce, _ := src.Embedder()
	start, chunk := time.Now(), 64
	truncated := 0
	for i := 0; i < len(sample); i += chunk {
		var in []engine.Text
		for _, id := range sample[i:min(i+chunk, len(sample))] {
			in = append(in, p.text(texts[id]))
		}
		res, err := ce.Embed(ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		truncated += res.Truncated
		if (i/chunk)%16 == 15 {
			el := time.Since(start).Seconds()
			t.Logf("LAYER1 sample %s %d/%d embedded=%d (%.2f/s)", p.name, i+chunk, len(sample), src.Misses, float64(src.Misses)/el)
		}
	}
	wall := time.Since(start)
	t.Logf("LAYER1 sample %s messages=%d cached=%d embedded=%d wall=%s space=%s", p.name, len(sample), src.Hits, src.Misses, wall.Round(time.Second), e.Space())
	return map[string]any{"messages": len(sample), "cached": src.Hits, "embedded": src.Misses, "wall_s": wall.Seconds(),
		"embedded_per_s": float64(src.Misses) / wall.Seconds(), "truncated": truncated, "space": e.Space()}
}
