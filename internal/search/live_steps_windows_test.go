//go:build windows && embedlive

package search

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/source/outlook"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

type startReport struct {
	Variant      string   `json:"variant"`
	Fallback     string   `json:"fallback,omitempty"`
	Canary       float64  `json:"canary_cosine,omitempty"`
	Threads      int      `json:"threads"`
	Probed       bool     `json:"probed"` // the probing state was published
	StartSeconds float64  `json:"start_seconds"`
	GPULoaded    []string `json:"gpu_while_loaded,omitempty"`
}

type throughput struct {
	Variant         string  `json:"variant"`
	Messages        int     `json:"messages"`
	Tokens          int64   `json:"tokens"`
	Seconds         float64 `json:"seconds"`
	TokensPerSecond float64 `json:"tokens_per_second"`
	MailsPerSecond  float64 `json:"mails_per_second"`
}

// sampleInbox syncs the default store's Inbox under the live limits
// (livetest.Open: -live.details, -live.rate, the same defaults as the
// other live scripts; unpicked stores not walked), keeps its newest
// complete messages and returns their index inputs (the text never leaves
// the machine).
func sampleInbox(t *testing.T, ctx context.Context, cfg *config.Config, bus *events.Bus, st *store.Store, rep *liveData) []engine.Text {
	conn, err := outlook.New("outlook", cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The test picks the profile's default store, as the user would.
	livetest.Open(t, ctx, cfg, conn, "default")
	key := source.Selected(cfg.Snapshot())
	start := time.Now()
	sr, err := syncer.New(st, cfg, bus, nil).RunFolders(ctx, conn, func(f mail.Folder) bool {
		return f.Role == mail.RoleInbox && key[source.StoreKey(conn.ID(), f.StoreID)]
	}, nil)
	if err != nil || sr.Status != runstate.Succeeded {
		t.Fatalf("sync: %v %s", err, sr.Error)
	}
	rep.SyncSeconds = time.Since(start).Seconds()
	states, _ := st.EmbeddingStates(ctx, nil)
	rep.SyncedInbox = len(states)
	var keep, drop []int64
	for _, s := range states {
		if !s.Partial && len(keep) < *liveMessages {
			keep = append(keep, s.RowID)
		} else {
			drop = append(drop, s.RowID)
		}
	}
	for i := 0; i < len(drop); i += 500 {
		if _, err := st.RemoveMessages(ctx, drop[i:min(i+500, len(drop))]); err != nil {
			t.Fatal(err)
		}
	}
	rep.Sample = len(keep)
	msgs, _ := st.MessageTexts(ctx, keep)
	var out []engine.Text
	for _, id := range keep {
		out = append(out, engine.Text{Purpose: engine.PurposeDocument, Title: msgs[id].Subject, Body: mail.CleanBody(msgs[id].Body)})
	}
	t.Logf("LIVE synced %d inbox messages in %.1fs, sample %d", rep.SyncedInbox, rep.SyncSeconds, rep.Sample)
	return out
}

func newManager(t *testing.T, cfg *config.Config, bus *events.Bus, jm *jobs.Manager) *sidecar.Manager {
	m, err := sidecar.New(sidecar.Options{Config: cfg, Bus: bus, Jobs: jm, Probe: embed.Probe(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// startAuto starts the runtime with sidecar.variant = auto (after a fresh
// re-probe when reprobe) and, when tp is given, times the sample on it.
func startAuto(t *testing.T, ctx context.Context, cfg *config.Config, bus *events.Bus, jm *jobs.Manager, texts []engine.Text, reprobe bool, tp *throughput) startReport {
	must(t, cfg.Set("sidecar.variant", sidecar.VariantAuto))
	sub, _ := events.Subscribe(bus, sidecar.TopicState, 1024)
	defer sub.Unsubscribe()
	m := newManager(t, cfg, bus, jm)
	defer m.Stop()
	if reprobe {
		must(t, m.Reprobe(liveRuntime))
	}
	start := time.Now()
	if _, err := m.Locate(ctx, liveRuntime, liveModel); err != nil {
		t.Fatalf("locate: %v", err)
	}
	s := m.Status()[0]
	r := startReport{Variant: s.Variant, Fallback: s.Fallback, Canary: s.Canary, Threads: s.Threads, StartSeconds: time.Since(start).Seconds(), GPULoaded: gpu()}
	for len(sub.C) > 0 {
		if (<-sub.C).State == sidecar.StateProbing {
			r.Probed = true
		}
	}
	if tp != nil {
		*tp = timeSample(t, ctx, cfg, m, texts, s.Variant)
	}
	t.Logf("LIVE start %+v", r)
	return r
}

func timeSample(t *testing.T, ctx context.Context, cfg *config.Config, m *sidecar.Manager, texts []engine.Text, variant string) throughput {
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	e, err := embed.NewSource(engine.Deps{Config: cfg, Secrets: sec, Locator: m}).Embedder()
	if err == nil {
		_, err = e.Embed(ctx, texts[:1]) // warm-up
	}
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	start := time.Now()
	res, err := e.Embed(ctx, texts)
	secs := time.Since(start).Seconds()
	if err != nil {
		t.Fatalf("embed sample: %v", err)
	}
	tp := throughput{Variant: variant, Messages: len(texts), Tokens: res.Usage.InputTokens, Seconds: secs,
		TokensPerSecond: float64(res.Usage.InputTokens) / secs, MailsPerSecond: float64(len(texts)) / secs}
	t.Logf("LIVE throughput %+v", tp)
	return tp
}

// indexAndSearch runs the real index job on the sample with the chosen
// build, then known-item searches (the messages' subjects).
func indexAndSearch(t *testing.T, ctx context.Context, cfg *config.Config, jm *jobs.Manager, st *store.Store, rep *liveData) {
	m := newManager(t, cfg, nil, jm)
	defer m.Stop()
	sec, _ := secrets.Open(cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: cfg, Secrets: sec, Sink: st, Locator: m})
	ix := index.New(cfg, st, nil, src, nil)
	ir, err := ix.Run(ctx, index.Options{}, nil)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	ir.Failures = nil
	rep.Index = &ir
	s := newSearcher(t, cfg, Deps{Store: st, Embedder: src, Coverage: ix, Gate: learningtest.Open{}})
	ids, _ := st.FilterIDs(ctx, store.SearchFilter{})
	var lat []float64
	hybrid, text, n, used := 0, 0, 0, 0
	for _, id := range ids {
		msg, _ := st.MessageByRow(ctx, id)
		if len(mail.Words(msg.Subject)) < 3 || n >= 100 {
			continue
		}
		n++
		start := time.Now()
		res, err := s.Search(ctx, Query{Text: msg.Subject, Limit: 10})
		lat = append(lat, float64(time.Since(start).Microseconds())/1000)
		if err == nil && res.Semantic.Used {
			used++
		}
		if err == nil && slices.ContainsFunc(res.Hits, func(h Hit) bool { return h.RowID == id }) {
			hybrid++
		}
		if fts, _ := st.SearchText(ctx, msg.Subject, store.SearchFilter{}, 10); slices.ContainsFunc(fts.Hits, func(h store.Hit) bool { return h.RowID == id }) {
			text++
		}
	}
	p50, _ := mathx.Median(lat)
	p95, _ := mathx.Percentile(lat, 0.95)
	rep.Search = map[string]any{"queries": n, "semantic_used": used, "hybrid_hit_at_10": hybrid, "fts_hit_at_10": text, "p50_ms": p50, "p95_ms": p95, "vectors": ir.After.Indexed}
	t.Logf("LIVE index %d mails/s=%.2f tokens/s=%.0f; search %v", ir.Embedded, ir.MailsPerSecond, ir.TokensPerSecond, rep.Search)
	if used != n {
		t.Errorf("LIVE semantic search was not used on %d of %d queries", n-used, n)
	}
}
