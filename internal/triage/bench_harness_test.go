package triage

// Decider benchmark harness (scripts/bench-deciders.sh, docs/DEVELOPMENT.md
// "Decider benchmark"): two or more decision providers decide the SAME
// stratified sample of primary-inbox messages, through the triage pipeline
// itself — the same profile, composer and causal states (identical states
// are checked), the same budget guard. It is test code, like the live
// check, so it reuses the package's internals and never ships in the exe.
// The offline tests (bench_test.go) drive it against fake providers; the
// live entry is bench_live_test.go (build tag deciderbench).

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

var benchLog = logx.For("triage.bench")

// benchOptions are a benchmark's parameters; their defaults live in
// scripts/bench-deciders.sh (the one place), never here.
type benchOptions struct {
	Providers         []string      `json:"providers"`
	Sample            int           `json:"sample"`
	Seed              uint64        `json:"seed"`
	Repeat            int           `json:"repeat"`
	Concurrency       int           `json:"concurrency"`
	RepeatConcurrency int           `json:"repeat_concurrency"`
	Round             int           `json:"round"` // messages per provider per main-phase round
	MaxUSD            float64       `json:"max_usd"`
	MaxCalls          int           `json:"max_calls"`
	ReplyWithin       time.Duration `json:"reply_within"`
	Gold              int           `json:"gold"`
	ExportDir         string        `json:"-"` // "" = no export
}

// validate checks the options against the catalog before any call.
func (o *benchOptions) validate(cat *engine.Catalog) error {
	var errs []error
	if len(o.Providers) == 0 {
		errs = append(errs, errors.New("name at least one provider"))
	}
	seen := map[string]bool{}
	for _, name := range o.Providers {
		p, ok := cat.Get(name)
		switch {
		case seen[name]:
			errs = append(errs, fmt.Errorf("provider %q named twice", name))
		case !ok:
			errs = append(errs, fmt.Errorf("unknown provider %q (known deciders: %v)", name, cat.Names(engine.KindDecider)))
		case p.Kind() != engine.KindDecider:
			errs = append(errs, fmt.Errorf("provider %q is a %s, not a decider", name, p.Kind()))
		}
		seen[name] = true
	}
	if o.Sample < 1 || o.Round < 1 || o.Concurrency < 1 || o.RepeatConcurrency < 1 || o.MaxCalls < 1 {
		errs = append(errs, errors.New("sample, round, concurrency, repeat concurrency and max calls must be ≥ 1"))
	}
	if o.Repeat < 0 || o.Repeat > o.Sample || o.Gold < 0 || o.Gold > o.Sample {
		errs = append(errs, fmt.Errorf("repeat (%d) and gold (%d) must be within 0..sample (%d)", o.Repeat, o.Gold, o.Sample))
	}
	if !(o.MaxUSD > 0) || o.ReplyWithin < 0 {
		errs = append(errs, errors.New("max USD must be > 0 and the reply window ≥ 0"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("bench: %w", err)
	}
	return nil
}

// benchEnv is what a benchmark runs on: a store holding the mail, its
// config, and the production engines (real adapters) on a bus.
type benchEnv struct {
	cfg     *config.Config
	st      *store.Store
	bus     *events.Bus
	engines *Engines
}

// decideRecord is one Decide call (one message's questions, all chunks).
type decideRecord struct {
	provider   string
	start, end time.Time
	err        error
}

// benchSource wraps the production DeciderSource and times every Decide.
type benchSource struct {
	inner DeciderSource
	mu    sync.Mutex
	recs  []decideRecord
}

func (s *benchSource) Decider(cfg *config.Config) (engine.Decider, *engine.Catalog, error) {
	d, cat, err := s.inner.Decider(cfg)
	if err != nil {
		return nil, nil, err
	}
	return &timedDecider{Decider: d, src: s}, cat, nil
}

// take returns and clears the records so far.
func (s *benchSource) take() []decideRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.recs
	s.recs = nil
	return out
}

type timedDecider struct {
	engine.Decider
	src *benchSource
}

func (d *timedDecider) Decide(ctx context.Context, state string, qs []engine.Question) (engine.Result, error) {
	start := time.Now()
	res, err := d.Decider.Decide(ctx, state, qs)
	d.src.mu.Lock()
	d.src.recs = append(d.src.recs, decideRecord{provider: d.Name(), start: start, end: time.Now(), err: err})
	d.src.mu.Unlock()
	return res, err
}

// bench is one benchmark in progress.
type bench struct {
	env     benchEnv
	o       benchOptions
	src     *benchSource
	tr      *Triager
	usage   *events.Subscription[engine.UsageRecord]
	retries *events.Subscription[httpx.Retry]
	spent   engine.Usage
	profile *taxonomy.Profile
	keys    map[string]store.TriageKey
	hashes  map[string]map[int64]string // provider → message → state hash it was decided for (main phase)
	down    map[string]string           // provider → why its remaining work was skipped
}

// benchEventBuffer holds every call and retry event of one run (each run
// is drained after it finishes).
const benchEventBuffer = 1 << 16

// runBench samples, decides with every provider, measures and exports.
func runBench(ctx context.Context, env benchEnv, o benchOptions) (*benchReport, error) {
	cat, err := engine.LoadCatalog(env.cfg)
	if err != nil {
		return nil, err
	}
	if err := o.validate(cat); err != nil {
		return nil, err
	}
	for k, v := range map[string]any{"triage.folder_roles": []any{string(mail.RoleInbox)},
		"feedback.weak_reply.within": o.ReplyWithin.String(), DeciderSetting: o.Providers[0]} {
		if err := env.cfg.Set(k, v); err != nil {
			return nil, fmt.Errorf("bench: %s: %w", k, err)
		}
	}
	b := &bench{env: env, o: o, src: &benchSource{inner: env.engines}, keys: map[string]store.TriageKey{},
		hashes: map[string]map[int64]string{}, down: map[string]string{}}
	if b.tr, err = New(env.cfg, env.st, env.bus, b.src, learningtest.Open{}, nil); err != nil {
		return nil, err
	}
	if b.usage, err = events.Subscribe(env.bus, engine.TopicUsage, benchEventBuffer); err != nil {
		return nil, err
	}
	defer b.usage.Unsubscribe()
	if b.retries, err = events.Subscribe(env.bus, httpx.TopicRetry, benchEventBuffer); err != nil {
		return nil, err
	}
	defer b.retries.Unsubscribe()
	s, err := b.tr.prepare(ctx)
	if err != nil {
		return nil, err
	}
	b.profile = s.profile
	smp, err := drawSample(ctx, s, env.st, o)
	if err != nil {
		return nil, err
	}
	benchLog.Info("bench sample drawn", "eligible", smp.Eligible, "sampled", len(smp.ids), "repeat", len(smp.repeat), "gold", len(smp.gold), "providers", o.Providers)
	rep := newBenchReport(o, s, cat, smp)
	first := b.mainPhase(ctx, rep, smp)
	second := b.repeatPhase(ctx, rep, smp, first)
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	rep.measure(s, first, second, b.hashes, b.down, b.spent)
	if o.ExportDir != "" {
		if err := exportBench(ctx, env.cfg, o.ExportDir, s, env.st, smp, first); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// mainPhase decides the sample in rounds, provider after provider, so a
// budget stop leaves every provider with the same messages decided; the
// plan of each round must fit what is left of the cap (the triage budget
// guard), and its calls are reserved against it.
func (b *bench) mainPhase(ctx context.Context, rep *benchReport, smp *sample) map[string]answerSet {
	for start := 0; start < len(smp.ids); start += b.o.Round {
		ids := smp.ids[start:min(start+b.o.Round, len(smp.ids))]
		for _, name := range b.o.Providers {
			if b.down[name] != "" {
				continue
			}
			pr := b.run(ctx, name, ids, b.o.Concurrency, false)
			rep.Providers[name].Main.add(pr)
			if pr.StoppedBy == StoppedBudget {
				rep.StoppedBy = StoppedBudget
				benchLog.Warn("bench main phase stopped by the budget", "provider", name, "spent_usd", b.spent.EstimatedUSD)
				return b.answers(ctx, smp.ids)
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	return b.answers(ctx, smp.ids)
}

// repeatPhase decides the repeat subset again (forced) for every provider
// that answered it, at the repeat concurrency.
func (b *bench) repeatPhase(ctx context.Context, rep *benchReport, smp *sample, first map[string]answerSet) map[string]answerSet {
	second := map[string]answerSet{}
	for _, name := range b.o.Providers {
		var ids []int64
		for _, id := range smp.repeat {
			if _, ok := first[name][id]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 || b.down[name] != "" || ctx.Err() != nil {
			continue
		}
		pr := b.run(ctx, name, ids, b.o.RepeatConcurrency, true)
		rep.Providers[name].Repeat.add(pr)
		if pr.StoppedBy == StoppedBudget && rep.StoppedBy == "" {
			rep.StoppedBy = StoppedBudget
		}
		second[name], _, _ = b.load(ctx, name, ids, pr.report.RunID) // only what this run decided
	}
	return second
}

// run is one triage run of one provider over ids, within what is left of
// the benchmark's budget.
func (b *bench) run(ctx context.Context, name string, ids []int64, workers int, force bool) passRun {
	pr := passRun{Concurrency: workers, Messages: len(ids)}
	calls := b.o.MaxCalls - b.spent.Calls
	usd := b.o.MaxUSD - b.spent.EstimatedUSD
	if calls < 1 || usd <= 0 {
		pr.StoppedBy, pr.Error = StoppedBudget, "the benchmark budget is spent"
		return pr
	}
	for k, v := range map[string]any{DeciderSetting: name, "triage.workers": workers, "triage.budget.max_calls": calls, "triage.budget.max_usd": usd} {
		if err := b.env.cfg.Set(k, v); err != nil {
			pr.Error = err.Error()
			b.down[name] = pr.Error
			return pr
		}
	}
	rep, err := b.tr.Run(ctx, Options{MessageIDs: ids, Force: force}, nil)
	pr.collect(rep, b.src.take(), drain(b.usage), drain(b.retries), name)
	b.spent.Add(rep.Usage)
	if rep.Plan != nil {
		b.keys[name] = rep.Plan.Key
	}
	if err != nil {
		pr.Error = err.Error()
	}
	switch {
	case errors.Is(err, ErrBudget) || rep.StoppedBy == StoppedBudget:
		pr.StoppedBy = StoppedBudget
	case err != nil && (rep.StoppedBy == StoppedUnavailable || rep.Decided+rep.Partial == 0):
		b.down[name] = pr.Error // every message failed or the provider is unavailable: skip its remaining work
	}
	benchLog.Info("bench run", "provider", name, "messages", len(ids), "workers", workers, "force", force, "decided", rep.Decided,
		"failed", rep.Failed, "stopped_by", pr.StoppedBy, "calls", rep.Usage.Calls, "usd", rep.Usage.EstimatedUSD, "spent_usd", b.spent.EstimatedUSD, "err", err)
	return pr
}

// answerSet is one provider's raw answers per message row.
type answerSet map[int64]map[string]engine.Answer

// answers loads every provider's answers of ids (after the main phase).
func (b *bench) answers(ctx context.Context, ids []int64) map[string]answerSet {
	out := map[string]answerSet{}
	for _, name := range b.o.Providers {
		if got, hashes, ok := b.load(ctx, name, ids, 0); ok {
			out[name], b.hashes[name] = got, hashes
		}
	}
	return out
}

// load reads a provider's stored answers of ids (decided by run, or any
// run for 0) and the state hash each was decided for.
func (b *bench) load(ctx context.Context, name string, ids []int64, run int64) (answerSet, map[int64]string, bool) {
	key, ok := b.keys[name]
	if !ok {
		return nil, nil, false
	}
	cached, err := b.env.st.LoadDecisions(context.WithoutCancel(ctx), key)
	if err != nil {
		benchLog.Error("bench answers not loaded", "provider", name, "err", err)
		return nil, nil, false
	}
	out, hashes := answerSet{}, map[int64]string{}
	for _, id := range ids {
		if d, ok := cached[id]; ok && (run == 0 || d.RunID == run) {
			if have, _ := decodeAnswers(b.profile, d.Answers); len(have) > 0 {
				out[id], hashes[id] = have, d.StateHash
			}
		}
	}
	return out, hashes, true
}

func drain[T any](sub *events.Subscription[T]) []T {
	var out []T
	for {
		select {
		case v, ok := <-sub.C:
			if !ok {
				return out
			}
			out = append(out, v)
		default:
			if n := sub.Dropped(); n > 0 {
				benchLog.Warn("bench events dropped: call statistics are incomplete", "dropped", n)
			}
			return out
		}
	}
}

// sample is the drawn message set: rows (sorted), the repeat and gold
// subsets, the strata and the states' hashes (to prove identical states).
type sample struct {
	Eligible int       `json:"eligible"`
	Strata   []stratum `json:"strata"`
	ids      []int64
	repeat   []int64
	gold     []int64
	hashes   map[int64]string
}

// stratum is one (replied within the window, year) group of the eligible mail.
type stratum struct {
	Replied    bool   `json:"replied"`
	Year       string `json:"year"`
	Population int    `json:"population"`
	Sampled    int    `json:"sampled"`
	rows       []int64
}

// drawSample draws a stratified random sample of the eligible messages
// (primary inbox, owner scope) with a fixed seed: strata are whether the
// owner replied within the window (the weak label) × the year received,
// allocated in proportion (largest remainder), each drawn without
// replacement. The repeat and gold subsets are drawn from the sample with
// the same generator, so one seed fixes all three.
func drawSample(ctx context.Context, s *session, st *store.Store, o benchOptions) (*sample, error) {
	by := map[[2]string]*stratum{}
	smp := &sample{hashes: map[int64]string{}}
	err := s.each(ctx, st, nil, nil, func(r *store.MessageRow, state string) error {
		smp.Eligible++
		smp.hashes[r.RowID] = StateHash(state)
		reply, ok := s.analysis.MyReplies[r.RowID]
		replied := ok && (o.ReplyWithin == 0 || reply.Seconds <= o.ReplyWithin.Seconds())
		year := mail.Head(r.Message.ReceivedAt, yearChars)
		k := [2]string{fmt.Sprint(replied), year}
		if by[k] == nil {
			by[k] = &stratum{Replied: replied, Year: year}
		}
		by[k].rows = append(by[k].rows, r.RowID)
		by[k].Population++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if smp.Eligible < o.Sample {
		return nil, fmt.Errorf("bench: %d eligible primary-inbox messages, the sample needs %d", smp.Eligible, o.Sample)
	}
	for _, k := range slices.SortedFunc(maps.Keys(by), func(a, b [2]string) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) }) {
		smp.Strata = append(smp.Strata, *by[k])
	}
	sizes := make([]int, len(smp.Strata))
	for i, s := range smp.Strata {
		sizes[i] = s.Population
	}
	for i, n := range mathx.Allocate(sizes, o.Sample) {
		smp.Strata[i].Sampled = n
	}
	rng := rand.New(rand.NewPCG(o.Seed, o.Seed))
	for i := range smp.Strata {
		rows := slices.Sorted(slices.Values(smp.Strata[i].rows))
		rng.Shuffle(len(rows), func(a, b int) { rows[a], rows[b] = rows[b], rows[a] })
		smp.ids = append(smp.ids, rows[:smp.Strata[i].Sampled]...)
	}
	slices.Sort(smp.ids)
	pick := func(n int) []int64 {
		ids := slices.Clone(smp.ids)
		rng.Shuffle(len(ids), func(a, b int) { ids[a], ids[b] = ids[b], ids[a] })
		return slices.Sorted(slices.Values(ids[:n]))
	}
	smp.repeat, smp.gold = pick(o.Repeat), pick(o.Gold)
	return smp, nil
}
