package triage

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/goldexport"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

// fakeProvider is a systemone server: every noul answer is noul, choices
// take the first (or, flipped, the last) option by name, scores the lowest
// (flipped: highest) level; status ≠ 200 answers every call with it.
type fakeProvider struct {
	*httptest.Server
	hits   atomic.Int64
	noul   float64
	flip   bool
	status int
	billed bool
	// failAfter > 0: every call after that many answers 503.
	failAfter int64
}

func newFakeProvider(t *testing.T, noul float64, flip bool, status int) *fakeProvider {
	f := &fakeProvider{noul: noul, flip: flip, status: status}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	n := f.hits.Add(1)
	body, _ := io.ReadAll(r.Body)
	if status := f.status; status != 0 || (f.failAfter > 0 && n > f.failAfter) {
		w.WriteHeader(cmp.Or(status, http.StatusServiceUnavailable))
		_, _ = io.WriteString(w, `{"detail":{"error_type":"down","message":"fake outage"}}`)
		return
	}
	var req struct {
		Questions map[string]struct {
			Type     string          `json:"type"`
			Criteria json.RawMessage `json:"criteria"`
		} `json:"questions"`
	}
	_ = json.Unmarshal(body, &req)
	answers := map[string]any{}
	for name, q := range req.Questions {
		switch q.Type {
		case "noul":
			answers[name] = map[string]any{"type": "noul", "noul": f.noul}
		case "choice":
			var opts map[string]any
			_ = json.Unmarshal(q.Criteria, &opts)
			names := slices.Sorted(func(yield func(string) bool) {
				for k := range opts {
					if !yield(k) {
						return
					}
				}
			})
			pick := names[0]
			if f.flip {
				pick = names[len(names)-1]
			}
			probs := map[string]float64{}
			for _, n := range names {
				probs[n] = 0
			}
			probs[pick] = 1
			answers[name] = map[string]any{"type": "choice", "choice": pick, "probabilities": probs, "confidence": 1}
		case "score":
			var levels []string
			_ = json.Unmarshal(q.Criteria, &levels)
			level := 0
			if f.flip {
				level = len(levels) - 1
			}
			probs := map[string]float64{}
			for i := range levels {
				probs[fmt.Sprint(i)] = 0
			}
			probs[fmt.Sprint(level)] = 1
			answers[name] = map[string]any{"type": "score", "score": level, "probabilities": probs, "confidence": 1}
		}
	}
	resp := map[string]any{"model": "fake", "answers": answers, "usage": map[string]any{"input_tokens": len(body) / 4, "output_tokens": 0}}
	if f.billed {
		resp["cost"] = map[string]any{"request_cost_usd": 0.000001}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// benchWorld is a store with 12 owner inbox messages (half answered within
// an hour, a quarter after ten days) and the fake providers in the
// providers file, wired to the production engines.
func benchWorld(t *testing.T, fakes map[string]*fakeProvider) benchEnv {
	t.Helper()
	w := newWorld(t, map[string]any{"http.retry.base_delay": "1ms", "http.retry.max_delay": "1ms", "http.retry.unsent_max_delay": "1ms",
		"http.retry.max_attempts": 2, "http.retry.jitter": 0.0})
	for i := range 12 {
		reply := time.Duration(0)
		switch i % 4 {
		case 0, 1:
			reply = time.Hour
		case 2:
			reply = 10 * 24 * time.Hour
		}
		w.mail(fmt.Sprintf("p%d@client%d.com", i%6, i%3), fmt.Sprint("C", i), fmt.Sprint("Budget ", i), time.Duration(i)*24*time.Hour, reply)
	}
	providers := map[string]any{}
	var environ []string
	for name, f := range fakes {
		secret := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_KEY"
		providers[name] = map[string]any{"label": name, "protocol": "systemone", "base_url": f.URL + "/v1", "path": "/systemone",
			"model": "fake-" + name, "api_key_secret": secret, "max_questions": 16, "context_tokens": 100000, "chars_per_token": 3,
			"reserve_tokens_per_byte": 1, "probability_tolerance": 0.02, "price_input_per_mtok": 0.05, "price_output_per_mtok": 0}
		environ = append(environ, "MAILSENSE_SECRET_"+secret+"=sk-test-0123456789abcdef")
	}
	raw, _ := json.Marshal(providers)
	if err := os.WriteFile(w.cfg.Paths().Resolve(w.cfg.String(engine.ProvidersFileSetting)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Open(w.cfg, environ, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	return benchEnv{cfg: w.cfg, st: w.st, bus: w.bus, engines: NewEngines(engine.Deps{Config: w.cfg, Bus: w.bus, Secrets: sec, Sink: w.st})}
}

func benchOpts(dir string, providers ...string) benchOptions {
	return benchOptions{Providers: providers, Sample: 8, Seed: 7, Repeat: 3, Concurrency: 2, RepeatConcurrency: 1, Round: 3,
		MaxUSD: 1, MaxCalls: 1000, ReplyWithin: 168 * time.Hour, Gold: 4, ExportDir: dir}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestBenchComparesProvidersOnIdenticalStates(t *testing.T) {
	a, b := newFakeProvider(t, 0.8, false, 0), newFakeProvider(t, 0.3, true, 0)
	b.billed = true
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a, "bench-b": b})
	dir := t.TempDir()
	rep, err := runBench(t.Context(), env, benchOpts(dir, "bench-a", "bench-b"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Sampled != 8 || rep.Sample.Eligible != 12 || rep.Repeat != 3 || rep.Gold != 4 || rep.Language != benchNoLanguage {
		t.Fatalf("sample %+v", rep)
	}
	n := 0
	for _, s := range rep.Sample.Strata {
		n += s.Sampled
	}
	if n != 8 || len(rep.Sample.Strata) < 2 {
		t.Fatalf("strata %+v", rep.Sample.Strata)
	}
	for name, f := range map[string]*fakeProvider{"bench-a": a, "bench-b": b} {
		pr := rep.Providers[name]
		// 17 questions at 16 per call: 2 calls a message; 3 rounds of ≤3
		if pr.Decided != 8 || pr.StateMismatch != 0 || pr.Main.Calls != 16 || pr.Main.Runs != 3 || pr.Repeat.Calls != 6 ||
			pr.Repeat.Concurrency != 1 || int(f.hits.Load()) != pr.Main.Calls+pr.Repeat.Calls || pr.Main.MessageMS.N != 8 || pr.Main.CallMS.N != 16 {
			t.Fatalf("%s: %+v", name, pr)
		}
		for q, ag := range pr.RepeatAgree {
			if ag.Agree != 1 || ag.N != 3 {
				t.Fatalf("%s repeat agreement on %s: %+v", name, q, ag)
			}
		}
		if pr.WeakReply == nil || pr.WeakReply.Count != 8 {
			t.Fatalf("%s weak labels: %+v", name, pr.WeakReply)
		}
	}
	if pa, pb := rep.Providers["bench-a"].Main, rep.Providers["bench-b"].Main; pa.BilledCalls != 0 || pb.BilledCalls != 16 ||
		pb.USD != 16*0.000001 || pb.USDAtListPrice <= pb.USD || pa.USD != pa.USDAtListPrice {
		t.Fatalf("accounted vs list price: a %+v b %+v", pa, pb)
	}
	cross := rep.Cross["bench-a|bench-b"]
	if cross["reply_needed"].Agree != 0 || *cross["reply_needed"].MeanAbs != 0.5 || cross["category"].Agree != 0 || cross["category"].N != 8 {
		t.Fatalf("cross agreement %+v %+v", cross["reply_needed"], cross["category"])
	}
	if got := rep.Spend.EstimatedUSD; got <= 0 || got > 1 {
		t.Fatalf("spend %v", got)
	}
	gold := readLines(t, filepath.Join(dir, goldexport.InputFile))
	if len(gold) != 4 || len(readLines(t, filepath.Join(dir, benchAnswersFile("bench-a")))) != 8 {
		t.Fatalf("export: %d gold lines", len(gold))
	}
	for _, l := range gold {
		var g goldexport.Input
		if err := json.Unmarshal([]byte(l), &g); err != nil || !strings.Contains(g.Text, "Email text:") || strings.Contains(g.Text, `"noul"`) || len(g.ID) != len(goldexport.ID(&mail.Message{})) {
			t.Fatalf("gold input line %q: %v", l, err)
		}
	}
	// The report holds no message text or address.
	out, _ := json.Marshal(rep)
	if strings.Contains(string(out), "client") || strings.Contains(string(out), "Budget") {
		t.Fatalf("the aggregate report leaks message data: %s", out)
	}
}

func TestBenchSampleIsDeterministicPerSeed(t *testing.T) {
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": newFakeProvider(t, 0.8, false, 0)})
	draw := func(seed uint64) []int64 {
		tr, _ := New(env.cfg, env.st, env.bus, env.engines, learningtest.Open{}, nil)
		_ = env.cfg.Set(DeciderSetting, "bench-a")
		s, err := tr.prepare(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		o := benchOpts("", "bench-a")
		o.Seed = seed
		smp, err := drawSample(t.Context(), s, env.st, o)
		if err != nil {
			t.Fatal(err)
		}
		return smp.ids
	}
	if !slices.Equal(draw(7), draw(7)) {
		t.Fatal("same seed, different sample")
	}
	differs := false
	for seed := uint64(8); seed < 20 && !differs; seed++ {
		differs = !slices.Equal(draw(7), draw(seed))
	}
	if !differs {
		t.Fatal("the seed never changes the sample")
	}
}

func TestBenchRefusesMisuseBeforeAnyCall(t *testing.T) {
	a := newFakeProvider(t, 0.8, false, 0)
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a})
	for name, mutate := range map[string]func(*benchOptions){
		"unknown provider": func(o *benchOptions) { o.Providers = []string{"bench-a", "nope"} },
		"named twice":      func(o *benchOptions) { o.Providers = []string{"bench-a", "bench-a"} },
		"no provider":      func(o *benchOptions) { o.Providers = nil },
		"repeat > sample":  func(o *benchOptions) { o.Repeat = 9 },
		"gold > sample":    func(o *benchOptions) { o.Gold = 9 },
		"no budget":        func(o *benchOptions) { o.MaxUSD = 0 },
		"no concurrency":   func(o *benchOptions) { o.Concurrency = 0 },
		"not a decider":    func(o *benchOptions) { o.Providers = []string{"embeddinggemma"} },
		"sample > mail":    func(o *benchOptions) { o.Sample, o.Repeat, o.Gold = 13, 0, 0 },
	} {
		o := benchOpts("", "bench-a")
		mutate(&o)
		if _, err := runBench(t.Context(), env, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if a.hits.Load() != 0 {
		t.Fatalf("misuse made %d calls", a.hits.Load())
	}
}

// The cap holds: a round whose plan the rest of the cap cannot cover is
// refused by the triage budget guard and nothing is spent.
func TestBenchBudgetRefusalSpendsNothing(t *testing.T) {
	a, b := newFakeProvider(t, 0.8, false, 0), newFakeProvider(t, 0.3, true, 0)
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a, "bench-b": b})
	o := benchOpts("", "bench-a", "bench-b")
	o.MaxUSD = 1e-9
	rep, err := runBench(t.Context(), env, o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StoppedBy != StoppedBudget || a.hits.Load()+b.hits.Load() != 0 || rep.Spend.EstimatedUSD != 0 ||
		rep.Providers["bench-a"].Main.StoppedBy != StoppedBudget || rep.Providers["bench-a"].Decided != 0 {
		t.Fatalf("budget refusal: %+v %+v", rep, rep.Providers["bench-a"].Main)
	}
}

// A provider that is down (503 on every attempt, or 402) is reported with
// its errors, retries and failed calls; the others are measured anyway.
func TestBenchReportsAProviderThatIsDown(t *testing.T) {
	a, down, broke := newFakeProvider(t, 0.8, false, 0), newFakeProvider(t, 0, false, 503), newFakeProvider(t, 0, false, 402)
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a, "bench-down": down, "bench-broke": broke})
	rep, err := runBench(t.Context(), env, benchOpts(t.TempDir(), "bench-a", "bench-down", "bench-broke"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Providers["bench-a"].Decided != 8 {
		t.Fatalf("the healthy provider: %+v", rep.Providers["bench-a"])
	}
	d := rep.Providers["bench-down"]
	if d.Down == "" || d.Decided != 0 || d.Main.Runs != 1 || d.Main.Retries == 0 || d.Main.FailedCalls["http_503"] == 0 || d.Main.ErrorsByClass["transient"] == 0 {
		t.Fatalf("down provider: %+v", d.Main)
	}
	if br := rep.Providers["bench-broke"]; br.Down == "" || br.Main.ErrorsByClass["insufficient_credits"] == 0 || br.Main.Retries != 0 || broke.hits.Load() > 2 {
		t.Fatalf("402 provider: %+v hits %d", br.Main, broke.hits.Load())
	}
	if _, ok := rep.Cross["bench-a|bench-down"]; ok {
		t.Fatal("no cross agreement without answers")
	}
}

// A message the repeat run failed keeps its first answers in the cache;
// they never count as agreeing with themselves.
func TestBenchRepeatCountsOnlyTheRepeatRunsAnswers(t *testing.T) {
	a := newFakeProvider(t, 0.8, false, 0)
	a.failAfter = 16 // the main phase: 8 messages × 2 calls
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a})
	rep, err := runBench(t.Context(), env, benchOpts("", "bench-a"))
	if err != nil {
		t.Fatal(err)
	}
	pr := rep.Providers["bench-a"]
	if pr.Decided != 8 || pr.Repeat.Failed != 1 || len(pr.RepeatAgree) != 0 {
		t.Fatalf("repeat: failed %d, agreement %+v", pr.Repeat.Failed, pr.RepeatAgree)
	}
}

func TestBenchGoldScoring(t *testing.T) {
	a, b := newFakeProvider(t, 0.8, false, 0), newFakeProvider(t, 0.3, true, 0)
	env := benchWorld(t, map[string]*fakeProvider{"bench-a": a, "bench-b": b})
	dir := t.TempDir()
	if _, err := runBench(t.Context(), env, benchOpts(dir, "bench-a", "bench-b")); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range readLines(t, filepath.Join(dir, goldexport.InputFile)) {
		var g goldexport.Input
		_ = json.Unmarshal([]byte(l), &g)
		ids = append(ids, g.ID)
	}
	// Synthetic gold: what bench-a answers (category: first option by name,
	// noul yes, urgency level 0.4 → |Δ| ≤ 0.5 for a, not for b at the top).
	writeGold := func(lines ...string) {
		if err := os.WriteFile(filepath.Join(dir, benchGoldLabels), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var lines []string
	for _, id := range ids {
		lines = append(lines, fmt.Sprintf(`{"id":%q,"answers":{"category":"action","reply_needed":true,"urgency":0.4}}`, id))
	}
	writeGold(lines...)
	rep, err := scoreGold(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Labelled != 4 || rep.Labels != 12 || len(rep.Providers) != 2 {
		t.Fatalf("gold report %+v", rep)
	}
	metric := func(provider, question, name string) float64 {
		for _, q := range rep.Providers[provider].Questions {
			if q.Question == question {
				return q.Metrics[name]
			}
		}
		t.Fatalf("%s: no %s", provider, question)
		return 0
	}
	if metric("bench-a", "category", "accuracy") != 1 || metric("bench-b", "category", "accuracy") != 0 ||
		metric("bench-a", "reply_needed", "accuracy") != 1 || metric("bench-b", "reply_needed", "accuracy") != 0 ||
		metric("bench-a", "urgency", "within_half_level") != 1 || metric("bench-a", "urgency", "mean_absolute_error") != 0.4 ||
		metric("bench-b", "urgency", "within_half_level") != 0 {
		t.Fatalf("gold scores %+v", rep.Providers)
	}
	for name, bad := range map[string]string{
		"unknown id":       `{"id":"000000000000","answers":{"category":"action"}}`,
		"unknown question": fmt.Sprintf(`{"id":%q,"answers":{"nope":1}}`, ids[0]),
		"invalid value":    fmt.Sprintf(`{"id":%q,"answers":{"category":"not-an-option"}}`, ids[0]),
		"out of range":     fmt.Sprintf(`{"id":%q,"answers":{"urgency":99}}`, ids[0]),
		"labelled twice":   fmt.Sprintf(`{"id":%q,"answers":{}}`+"\n"+`{"id":%q,"answers":{}}`, ids[0], ids[0]),
		"not json":         `{"id":`,
		"unknown field":    fmt.Sprintf(`{"id":%q,"answers":{},"extra":1}`, ids[0]),
	} {
		writeGold(bad)
		if _, err := scoreGold(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Mismatched answers: an answers file whose answer does not fit the taxonomy.
	writeGold(lines...)
	path := filepath.Join(dir, benchAnswersFile("bench-a"))
	orig, _ := os.ReadFile(path)
	_ = os.WriteFile(path, []byte(strings.Replace(string(orig), `"reply_needed":{"type":"noul"`, `"reply_needed":{"type":"score"`, 1)), 0o600)
	if _, err := scoreGold(dir); err == nil || !strings.Contains(err.Error(), "does not match the taxonomy") {
		t.Fatalf("mismatched answers accepted: %v", err)
	}
	_ = os.WriteFile(path, orig, 0o600)
	tax := filepath.Join(dir, goldexport.TaxonomyFile)
	raw, _ := os.ReadFile(tax)
	var doc goldexport.Taxonomy
	_ = json.Unmarshal(raw, &doc)
	doc.Fingerprint = "000000000000"
	raw, _ = json.Marshal(doc)
	_ = os.WriteFile(tax, raw, 0o600)
	if _, err := scoreGold(dir); err == nil || !strings.Contains(err.Error(), "not the built-in profile") {
		t.Fatalf("another taxonomy accepted: %v", err)
	}
}
