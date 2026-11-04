//go:build triagelive

package triage

// Small, cheap live check against the real decision provider (run through
// scripts/triage-live.sh, which loads the API key without printing it).
// 1. One probe call; a 402 (no credits) ends the check and is reported.
// 2. The legacy export is imported into a temporary database, the newest
//    TRIAGE_LIVE_MESSAGES owner messages are triaged.
// 3. TRIAGE_LIVE_REPEAT of them are decided again to measure agreement.
// Output: aggregates only (counts, tokens, dollars, latencies, agreement),
// never a subject, body or address.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixtureconf"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

func TestTriageLive(t *testing.T) {
	if os.Getenv("TRIAGE_LIVE_PROVIDER") == "" || os.Getenv("TRIAGE_LIVE_PROVIDERS_FILE") == "" || os.Getenv("TRIAGE_LIVE_JSONL") == "" {
		t.Skip("run through scripts/triage-live.sh")
	}
	ctx := t.Context()
	cfg, bus, engines, dec, st := liveSetup(t)
	usage, _ := events.Subscribe(bus, engine.TopicUsage, 4096)
	res, err := engines.TestProvider(ctx, cfg)
	if errors.Is(err, decision.ErrInsufficientCredits) {
		t.Logf("LIVE probe=402 insufficient credits: live work stopped (%v)", err)
		return
	}
	if err != nil {
		t.Fatalf("LIVE probe failed: %v", err)
	}
	t.Logf("LIVE probe=ok model=%s provider=%s latency_ms=%d detail=%q", res.Model, dec.Name(), res.LatencyMS, res.Detail)
	importFixture(t, cfg, st)
	tr, err := New(cfg, st, bus, engines, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(os.Getenv("TRIAGE_LIVE_MESSAGES"))
	rep, err := tr.Run(ctx, Options{Limit: n}, nil)
	t.Logf("LIVE run state=%s eligible=%d selected=%d decided=%d failed=%d calls=%d input_tokens=%d usd=%.5f planned_calls=%d planned_usd=%.5f err=%v",
		rep.State, rep.Plan.Eligible, rep.Plan.Selected, rep.Decided, rep.Failed, rep.Usage.Calls, rep.Usage.InputTokens,
		rep.Usage.EstimatedUSD, rep.Plan.Estimate.Calls, rep.Plan.Estimate.EstimatedUSD, err)
	if err != nil {
		return
	}
	reportSanity(t, tr, st)
	repeatAgreement(t, tr, st, rep)
	latencies(t, usage)
}

func liveSetup(t *testing.T) (*config.Config, *events.Bus, *Engines, engine.Decider, *store.Store) {
	dir, _ := filepath.Abs(t.TempDir())
	var environ []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MAILSENSE_SECRET_") {
			environ = append(environ, kv)
		}
	}
	environ = append(environ, "MAILSENSE_ENGINE_PROVIDERS_FILE="+os.Getenv("TRIAGE_LIVE_PROVIDERS_FILE"))
	bus := events.New()
	t.Cleanup(bus.Close)
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}, Environ: environ, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := strconv.Atoi(os.Getenv("TRIAGE_LIVE_MAX_CALLS"))
	usd, _ := strconv.ParseFloat(os.Getenv("TRIAGE_LIVE_MAX_USD"), 64)
	settings, err := fixtureconf.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings["triage.budget.max_calls"], settings["triage.budget.max_usd"] = calls, usd
	settings[DeciderSetting] = os.Getenv("TRIAGE_LIVE_PROVIDER")
	for k, v := range settings {
		if err := cfg.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	sec, err := secrets.Open(cfg, environ, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	engines := NewEngines(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: st})
	dec, _, err := engines.Decider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, bus, engines, dec, st
}

func importFixture(t *testing.T, cfg *config.Config, st *store.Store) {
	conn, err := archive.New(fixtureconf.SourceID, os.Getenv("TRIAGE_LIVE_JSONL"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rep, err := syncer.New(st, cfg, nil, nil).Run(t.Context(), conn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE import messages=%d wall=%s", rep.Totals.Written, time.Since(start).Round(time.Second))
}

// reportSanity prints value distributions (aggregates only).
func reportSanity(t *testing.T, tr *Triager, st *store.Store) {
	v, err := tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cats := map[string]int{}
	var replyYes, n, esc int
	var urg []float64
	for _, r := range v.Results {
		n++
		cats[r.Values["category"].Choice]++
		if r.Values["reply_needed"].Model >= 0.5 {
			replyYes++
		}
		urg = append(urg, r.Values["urgency"].Model)
		if r.Escalate {
			esc++
		}
	}
	mu, _ := mathx.Mean(urg)
	t.Logf("LIVE sanity judged=%d categories=%v reply_needed_yes=%d mean_urgency=%.2f escalate=%d coverage=%+v",
		n, cats, replyYes, mu, esc, v.Coverage)
}

// repeatAgreement decides some messages a second time and compares.
func repeatAgreement(t *testing.T, tr *Triager, st *store.Store, first Report) {
	k, _ := strconv.Atoi(os.Getenv("TRIAGE_LIVE_REPEAT"))
	before, _ := st.LoadDecisions(t.Context(), first.Plan.Key)
	ids := slices.Sorted(func(yield func(int64) bool) {
		for id, d := range before {
			if d.RunID == first.RunID && !yield(id) {
				return
			}
		}
	})
	ids = ids[:min(k, len(ids))]
	rep, err := tr.Run(t.Context(), Options{Force: true, MessageIDs: ids}, nil)
	t.Logf("LIVE repeat decided=%d calls=%d usd=%.5f err=%v", rep.Decided, rep.Usage.Calls, rep.Usage.EstimatedUSD, err)
	after, _ := st.LoadDecisions(t.Context(), first.Plan.Key)
	p, _ := tr.Read(t.Context())
	agree := map[string][2]int{}
	var maxDiff, sumDiff float64
	var diffs int
	for _, id := range ids {
		a, _ := decodeAnswers(p.Profile, before[id].Answers)
		b, _ := decodeAnswers(p.Profile, after[id].Answers)
		for name, x := range a {
			y := b[name]
			same := x.Choice == y.Choice
			if x.Type != engine.Choice {
				d := math.Abs(x.Value - y.Value)
				maxDiff, sumDiff, diffs = max(maxDiff, d), sumDiff+d, diffs+1
				same = (x.Type == engine.Noul && (x.Value >= 0.5) == (y.Value >= 0.5)) || (x.Type == engine.Score && mathx.RoundInt(x.Value) == mathx.RoundInt(y.Value))
			}
			c := agree[string(x.Type)]
			c[1]++
			if same {
				c[0]++
			}
			agree[string(x.Type)] = c
		}
	}
	out := ""
	for _, typ := range []string{"choice", "noul", "score"} {
		c := agree[typ]
		out += fmt.Sprintf(" %s=%d/%d", typ, c[0], c[1])
	}
	t.Logf("LIVE agreement messages=%d%s max_abs_diff=%.4f mean_abs_diff=%.4f", len(ids), out, maxDiff, sumDiff/float64(max(1, diffs)))
}

func latencies(t *testing.T, sub *events.Subscription[engine.UsageRecord]) {
	var d []float64
	for {
		select {
		case rec := <-sub.C:
			d = append(d, float64(rec.Duration.Milliseconds()))
			continue
		default:
		}
		break
	}
	p50, _ := mathx.Percentile(d, 0.5)
	p95, _ := mathx.Percentile(d, 0.95)
	t.Logf("LIVE latency calls=%d p50_ms=%.0f p95_ms=%.0f", len(d), p50, p95)
}
