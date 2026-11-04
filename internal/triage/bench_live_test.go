//go:build deciderbench

package triage

// Live decider benchmark (scripts/bench-deciders.sh, which loads the keys
// without printing them and sets every BENCH_* parameter): the legacy
// export is imported into a temporary database and the named providers
// decide the same stratified sample (bench_harness_test.go). Output and the
// report file are aggregates only.

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// benchEnvVars reads the BENCH_* parameters; every one must be set.
type benchEnvVars struct {
	t       *testing.T
	missing []string
}

func (e *benchEnvVars) str(key string) string {
	v := os.Getenv(key)
	if v == "" {
		e.missing = append(e.missing, key)
	}
	return v
}

func (e *benchEnvVars) int(key string) int {
	n, err := strconv.Atoi(e.str(key))
	if err != nil {
		e.t.Fatalf("%s: %v", key, err)
	}
	return n
}

func TestDeciderBench(t *testing.T) {
	if os.Getenv("BENCH_JSONL") == "" {
		t.Skip("run through scripts/bench-deciders.sh")
	}
	e := &benchEnvVars{t: t}
	usd, err := strconv.ParseFloat(e.str("BENCH_MAX_USD"), 64)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := strconv.ParseUint(e.str("BENCH_SEED"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	within, err := time.ParseDuration(e.str("BENCH_REPLY_WITHIN"))
	if err != nil {
		t.Fatal(err)
	}
	o := benchOptions{Providers: strings.Fields(e.str("BENCH_PROVIDERS")), Sample: e.int("BENCH_SAMPLE"), Seed: seed,
		Repeat: e.int("BENCH_REPEAT"), Concurrency: e.int("BENCH_CONCURRENCY"), RepeatConcurrency: e.int("BENCH_REPEAT_CONCURRENCY"),
		Round: e.int("BENCH_ROUND"), MaxUSD: usd, MaxCalls: e.int("BENCH_MAX_CALLS"), ReplyWithin: within, Gold: e.int("BENCH_GOLD"),
		ExportDir: e.str("BENCH_DIR")}
	out := e.str("BENCH_REPORT")
	if len(e.missing) > 0 {
		t.Fatalf("missing parameters: %v", e.missing)
	}
	start := time.Now()
	providersFile := os.Getenv("BENCH_PROVIDERS_FILE")
	if providersFile == "" {
		t.Fatal("BENCH_PROVIDERS_FILE is not set (scripts/bench-deciders.sh resolves it)")
	}
	cfg, st := fixturetest.Import(t, os.Getenv("BENCH_JSONL"), map[string]any{
		engine.ProvidersFileSetting: providersFile,
		DeciderSetting:              o.Providers[0],
	})
	t.Logf("BENCH import wall=%s", time.Since(start).Round(time.Second))
	var environ []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MAILSENSE_SECRET_") {
			environ = append(environ, kv)
		}
	}
	bus := events.New()
	t.Cleanup(bus.Close)
	sec, err := secrets.Open(cfg, environ, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runBench(t.Context(), benchEnv{cfg: cfg, st: st, bus: bus, engines: NewEngines(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: st})}, o)
	if rep != nil {
		raw, _ := json.MarshalIndent(rep, "", "  ")
		if werr := fsx.WriteFileAtomic(out, append(raw, '\n'), fsx.FilePerm, cfg.Snapshot().Replace(nil)); werr != nil {
			t.Error(werr)
		}
		printBench(t, rep)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func printBench(t *testing.T, r *benchReport) {
	t.Logf("BENCH sample eligible=%d sampled=%d repeat=%d gold=%d strata=%d fingerprint=%s questions=%d stopped_by=%q",
		r.Sample.Eligible, r.Sampled, r.Repeat, r.Gold, len(r.Sample.Strata), r.Fingerprint, r.Questions, r.StoppedBy)
	t.Logf("BENCH spend calls=%d usd=%.6f cap=%.4f language=%s", r.Spend.Calls, r.Spend.EstimatedUSD, r.Options.MaxUSD, r.Language)
	for _, name := range r.Options.Providers {
		p := r.Providers[name]
		for _, ph := range []struct {
			name string
			s    passStats
		}{{"main", p.Main}, {"repeat", p.Repeat}} {
			s := ph.s
			t.Logf("BENCH %s %s c=%d decided=%d/%d failed=%d calls=%d in_tok=%d out_tok=%d usd=%.6f usd_list=%.6f billed=%d wall_s=%.1f msg_s=%.2f call_ms=%s msg_ms=%s errors=%v failed_calls=%v retries=%d rejections=%d",
				name, ph.name, s.Concurrency, s.Decided, s.Messages, s.Failed, s.Calls, s.InputTokens, s.OutputTokens, s.USD, s.USDAtListPrice,
				s.BilledCalls, s.WallSeconds, s.MsgsPerSecond, lat(s.CallMS), lat(s.MessageMS), s.ErrorsByClass, s.FailedCalls, s.Retries, s.DecodingRejections)
		}
		t.Logf("BENCH %s decided=%d state_mismatch=%d down=%q repeat=%s", name, p.Decided, p.StateMismatch, p.Down, agreeLine(p.RepeatAgree))
		if p.WeakReply != nil {
			m := p.WeakReply.Metrics
			t.Logf("BENCH %s weak_reply n=%d acc=%.3f f1=%.3f precision=%.3f recall=%.3f auc=%.3f brier=%.3f",
				name, p.WeakReply.Count, m["accuracy"], m["f1"], m["precision"], m["recall"], m["auc"], m["brier"])
		}
	}
	for _, pair := range slices.Sorted(maps.Keys(r.Cross)) {
		t.Logf("BENCH cross %s %s", pair, agreeLine(r.Cross[pair]))
	}
}

func lat(l *latency) string {
	if l == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f/%.0f/%.0f", l.P50, l.P95, l.P99)
}

// agreeLine prints question=agree[,kappa|Δ] in profile order of names.
func agreeLine(a map[string]*agreement) string {
	var parts []string
	for _, q := range slices.Sorted(maps.Keys(a)) {
		ag := a[q]
		s := fmt.Sprintf("%s=%.2f", q, ag.Agree)
		if ag.Kappa != nil {
			s += fmt.Sprintf(",k%.2f", *ag.Kappa)
		}
		if ag.MeanAbs != nil {
			s += fmt.Sprintf(",d%.3f", *ag.MeanAbs)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestDeciderBenchScore(t *testing.T) {
	dir, out := os.Getenv("BENCH_DIR"), os.Getenv("BENCH_GOLD_REPORT")
	if dir == "" || out == "" {
		t.Skip("run through scripts/bench-deciders.sh score")
	}
	rep, err := scoreGold(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	if err := fsx.WriteFileAtomic(out, append(raw, '\n'), fsx.FilePerm, fsx.Replace{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("BENCH gold labelled=%d labels=%d", rep.Labelled, rep.Labels)
	for _, name := range slices.Sorted(maps.Keys(rep.Providers)) {
		p := rep.Providers[name]
		var parts []string
		for _, q := range p.Questions {
			m := q.Metrics
			switch {
			case q.Kind == "score":
				parts = append(parts, fmt.Sprintf("%s=%.2f,mae%.2f", q.Question, m["within_half_level"], m["mean_absolute_error"]))
			default:
				parts = append(parts, fmt.Sprintf("%s=%.2f", q.Question, m["accuracy"]))
			}
		}
		t.Logf("BENCH gold %s answered=%d unanswered=%d %s", name, p.Answered, p.Unanswered, strings.Join(parts, " "))
	}
}

// TestDeciderBenchExport is the export-only mode: a stratified sample of
// the whole chosen mailbox for the gold judge, no provider call ($0).
// The ids of earlier gold labels (BENCH_KEEP_LABELS) are checked to still
// name messages of the mailbox.
func TestDeciderBenchExport(t *testing.T) {
	if os.Getenv("BENCH_JSONL") == "" || os.Getenv("BENCH_EXPORT_DIR") == "" {
		t.Skip("run through scripts/bench-deciders.sh export")
	}
	e := &benchEnvVars{t: t}
	seed, err := strconv.ParseUint(e.str("BENCH_SEED"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	o := goldexport.Options{Size: e.int("BENCH_EXPORT_SIZE"), Seed: seed, RareFloor: e.int("BENCH_RARE_FLOOR"), Legacy: os.Getenv("BENCH_LEGACY"),
		Dir: e.str("BENCH_EXPORT_DIR")}
	if len(e.missing) > 0 {
		t.Fatalf("missing parameters: %v", e.missing)
	}
	cfg, st := fixturetest.Import(t, os.Getenv("BENCH_JSONL"), nil)
	tr, err := New(cfg, st, nil, noDecider{}, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := tr.ExportGold(t.Context(), st, o)
	if err != nil {
		t.Fatal(err)
	}
	kinds, scripts := map[string]int{}, map[string]int{}
	for _, s := range rep.Strata {
		kinds[s.SenderKind] += s.Sampled
		scripts[s.Script] += s.Sampled
	}
	t.Logf("BENCH export eligible=%d sampled=%d strata=%d seed=%d floor=%d kinds=%v scripts=%v categories=%v legacy_bad_lines=%d",
		rep.Eligible, rep.Sampled, len(rep.Strata), rep.Seed, rep.Floor, kinds, scripts, rep.Categories, rep.LegacyBadLines)
	if keep := os.Getenv("BENCH_KEEP_LABELS"); keep != "" {
		known, total, err := knownGoldIDs(t, st, keep)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("BENCH kept_gold_labels ids_found=%d/%d", known, total)
		if known != total {
			t.Errorf("BENCH earlier gold labels no longer name messages: %d of %d", total-known, total)
		}
	}
}

// noDecider is the export's decider source: it refuses (no call is made).
type noDecider struct{}

func (noDecider) Decider(*config.Config) (engine.Decider, *engine.Catalog, error) {
	return nil, nil, fmt.Errorf("bench: the export mode makes no provider call")
}

// knownGoldIDs counts the ids of a gold-labels file that name a stored message.
func knownGoldIDs(t *testing.T, st *store.Store, path string) (int, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	ids := map[string]bool{}
	_ = st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		ids[goldexport.ID(&r.Message)] = true
		return nil
	})
	known, total := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var g goldLine
		if json.Unmarshal([]byte(line), &g) != nil {
			continue
		}
		total++
		if ids[g.ID] {
			known++
		}
	}
	return known, total, nil
}
