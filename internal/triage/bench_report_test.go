package triage

// The benchmark's aggregate report: counts, tokens, dollars, latencies
// and agreement only — never a message text, address or id.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

type benchReport struct {
	GeneratedAt string                     `json:"generated_at"`
	Options     benchOptions               `json:"options"`
	Fingerprint string                     `json:"fingerprint"`
	Questions   int                        `json:"questions"`
	Sample      *sample                    `json:"sample"`
	Sampled     int                        `json:"sampled"`
	Repeat      int                        `json:"repeat_messages"`
	Gold        int                        `json:"gold_messages"`
	Language    string                     `json:"language_split"`
	Spend       engine.Usage               `json:"spend"`
	StoppedBy   string                     `json:"stopped_by,omitempty"`
	Providers   map[string]*providerReport `json:"providers"`
	// Cross maps "a|b" to per-question agreement on the messages both decided.
	Cross map[string]map[string]*agreement `json:"cross_agreement"`
}

// benchNoLanguage records why there is no French/English split.
const benchNoLanguage = "skipped: the repository has no structural or statistical language detector (no keyword lists are added for this)"

type providerReport struct {
	Model   string    `json:"model"`
	Main    passStats `json:"main"`
	Repeat  passStats `json:"repeat"`
	Decided int       `json:"decided"`
	// StateMismatch counts decided messages whose stored state differs
	// from the sampled one (0 = every provider saw identical states).
	StateMismatch int                    `json:"state_mismatch"`
	RepeatAgree   map[string]*agreement  `json:"repeat_agreement,omitempty"`
	WeakReply     *feedback.QuestionEval `json:"weak_reply,omitempty"`
	Down          string                 `json:"down,omitempty"`
	provider      engine.Provider
}

func newBenchReport(o benchOptions, s *session, cat *engine.Catalog, smp *sample) *benchReport {
	r := &benchReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Options: o, Fingerprint: s.profile.Fingerprint,
		Questions: len(s.profile.Questions), Sample: smp, Sampled: len(smp.ids), Repeat: len(smp.repeat), Gold: len(smp.gold),
		Language: benchNoLanguage, Providers: map[string]*providerReport{}, Cross: map[string]map[string]*agreement{}}
	for _, name := range o.Providers {
		p, _ := cat.Get(name)
		r.Providers[name] = &providerReport{Model: p.Model, provider: p}
	}
	return r
}

// passRun is one triage run of one provider.
type passRun struct {
	Concurrency  int
	Messages     int
	StoppedBy    string
	Error        string
	report       Report
	decides      []decideRecord
	calls        []engine.UsageRecord
	retries      []httpx.Retry
	decideSpan   time.Duration
	hasDecisions bool
}

func (pr *passRun) collect(rep Report, decides []decideRecord, calls []engine.UsageRecord, retries []httpx.Retry, name string) {
	pr.report = rep
	var first, last time.Time
	for _, d := range decides {
		if d.provider != name {
			continue
		}
		pr.decides = append(pr.decides, d)
		if first.IsZero() || d.start.Before(first) {
			first = d.start
		}
		if d.end.After(last) {
			last = d.end
		}
	}
	pr.decideSpan, pr.hasDecisions = last.Sub(first), len(pr.decides) > 0
	for _, c := range calls {
		if c.Provider == name {
			pr.calls = append(pr.calls, c)
		}
	}
	for _, r := range retries {
		if r.Client == "decision."+name {
			pr.retries = append(pr.retries, r)
		}
	}
}

// passStats sums the runs of one phase (main or repeat) of one provider.
type passStats struct {
	Runs         int            `json:"runs"`
	Concurrency  int            `json:"concurrency"`
	Messages     int            `json:"messages"`
	Decided      int            `json:"decided"`
	Partial      int            `json:"partial"`
	Failed       int            `json:"failed"`
	NotAttempted int            `json:"not_attempted"`
	StoppedBy    string         `json:"stopped_by,omitempty"`
	Errors       []string       `json:"run_errors,omitempty"`
	Calls        int            `json:"calls"`
	FailedCalls  map[string]int `json:"failed_calls_by_status,omitempty"`
	InputTokens  int64          `json:"input_tokens"`
	OutputTokens int64          `json:"output_tokens"`
	// USD is what the run accounted: the provider's reported charge when it
	// reports one (BilledCalls), else tokens × the catalog price.
	USD            float64 `json:"usd"`
	USDAtListPrice float64 `json:"usd_tokens_x_price"`
	BilledCalls    int     `json:"billed_calls"`
	EstimatedCalls int     `json:"estimated_calls"`
	// WallSeconds sums each run's span from its first Decide to its last
	// (planning excluded); MsgsPerSecond = decided / WallSeconds.
	WallSeconds   float64        `json:"wall_seconds"`
	MsgsPerSecond float64        `json:"msgs_per_second"`
	CallMS        *latency       `json:"call_latency_ms,omitempty"`
	MessageMS     *latency       `json:"message_latency_ms,omitempty"`
	ErrorsByClass map[string]int `json:"errors_by_class,omitempty"`
	// DecodingRejections are answers the strict decoder refused (contract).
	DecodingRejections int            `json:"decoding_rejections"`
	Retries            int            `json:"retries"`
	RetriesByReason    map[string]int `json:"retries_by_reason,omitempty"`
	callMS, messageMS  []float64
}

type latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
	N   int     `json:"n"`
}

// benchErrorClasses name the adapter's error classes (first match wins).
var benchErrorClasses = []struct {
	name string
	err  error
}{{"insufficient_credits", decision.ErrInsufficientCredits}, {"auth", decision.ErrAuth}, {"quota", decision.ErrQuota},
	{"no_key", decision.ErrNoKey}, {"rejected", decision.ErrRejected}, {"contract", decision.ErrContract},
	{"context_budget", decision.ErrContextBudget}, {"input", decision.ErrInput}, {"transient", decision.ErrTransient},
	{"http", decision.ErrHTTP},
	{"budget", engine.ErrBudget}, {"cancelled", context.Canceled}}

func errorClass(err error) string {
	for _, c := range benchErrorClasses {
		if errors.Is(err, c.err) {
			return c.name
		}
	}
	return "other"
}

func (ps *passStats) add(pr passRun) {
	rep := pr.report
	ps.Runs++
	ps.Concurrency = pr.Concurrency
	ps.Messages += pr.Messages
	ps.Decided += rep.Decided
	ps.Partial += rep.Partial
	ps.Failed += rep.Failed
	ps.NotAttempted += rep.NotAttempted
	if rep.Plan == nil && pr.StoppedBy != "" {
		ps.NotAttempted += pr.Messages // refused before planning (cap spent)
	}
	ps.StoppedBy = cmp.Or(pr.StoppedBy, ps.StoppedBy)
	if pr.Error != "" {
		ps.Errors = append(ps.Errors, pr.Error)
	}
	ps.WallSeconds += pr.decideSpan.Seconds()
	for _, d := range pr.decides {
		ps.messageMS = append(ps.messageMS, ms(d.end.Sub(d.start)))
		if d.err != nil {
			c := errorClass(d.err)
			ps.ErrorsByClass = inc(ps.ErrorsByClass, c)
			if c == "contract" {
				ps.DecodingRejections++
			}
		}
	}
	for _, c := range pr.calls {
		ps.Calls++
		ps.InputTokens += c.InputTokens
		ps.OutputTokens += c.OutputTokens
		ps.USD += c.EstimatedUSD
		ps.callMS = append(ps.callMS, ms(c.Duration))
		if c.Billed {
			ps.BilledCalls++
		}
		if c.Estimated {
			ps.EstimatedCalls++
		}
		if c.Failed {
			key := "transport"
			if c.Status != 0 {
				key = fmt.Sprintf("http_%d", c.Status)
			}
			ps.FailedCalls = inc(ps.FailedCalls, key)
		}
	}
	for _, r := range pr.retries {
		ps.Retries++
		ps.RetriesByReason = inc(ps.RetriesByReason, r.Reason)
	}
}

// finish derives rates, list-price dollars and latency percentiles.
func (ps *passStats) finish(p engine.Provider) {
	ps.USDAtListPrice = round6(p.Cost(ps.InputTokens, ps.OutputTokens))
	ps.USD = round6(ps.USD)
	if ps.WallSeconds > 0 {
		ps.MsgsPerSecond = mathx.Round(float64(ps.Decided)/ps.WallSeconds, 3)
	}
	ps.WallSeconds = mathx.Round(ps.WallSeconds, 3)
	ps.CallMS, ps.MessageMS = latencyOf(ps.callMS), latencyOf(ps.messageMS)
}

func latencyOf(v []float64) *latency {
	if len(v) == 0 {
		return nil
	}
	q := func(x float64) float64 { p, _ := mathx.Percentile(v, x); return mathx.Round(p, 1) }
	return &latency{P50: q(0.5), P95: q(0.95), P99: q(0.99), Max: q(1), N: len(v)}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func round6(v float64) float64 { return mathx.Round(v, 6) }

func inc(m map[string]int, k string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	m[k]++
	return m
}

// agreement compares two answer sets on one question.
type agreement struct {
	Type engine.QuestionType `json:"type"`
	N    int                 `json:"n"`
	// Agree: choice = same option; noul = same side of the profile cutoff;
	// score = same rounded level.
	Agree   float64  `json:"agree"`
	Kappa   *float64 `json:"kappa,omitempty"`         // choice: Cohen's kappa
	MeanAbs *float64 `json:"mean_abs_diff,omitempty"` // noul: |Δp|; score: |Δ level|
	MaxAbs  *float64 `json:"max_abs_diff,omitempty"`
}

// compareAnswers measures per question how a and b agree on the messages
// both answered.
func compareAnswers(p *taxonomy.Profile, a, b answerSet) map[string]*agreement {
	out := map[string]*agreement{}
	ids := slices.Sorted(maps.Keys(a))
	for _, q := range p.Questions {
		ag := &agreement{Type: q.Type}
		var same int
		var ca, cb []string
		var diffs []float64
		for _, id := range ids {
			x, okx := a[id][q.Name]
			y, oky := b[id][q.Name]
			if !okx || !oky || x.Refused || y.Refused {
				continue
			}
			ag.N++
			switch q.Type {
			case engine.Choice:
				ca, cb = append(ca, x.Choice), append(cb, y.Choice)
				if x.Choice == y.Choice {
					same++
				}
			case engine.Noul:
				cut := p.Cutoff(q.Name)
				diffs = append(diffs, math.Abs(x.Value-y.Value))
				if (x.Value >= cut) == (y.Value >= cut) {
					same++
				}
			case engine.Score:
				diffs = append(diffs, math.Abs(x.Value-y.Value))
				if mathx.RoundInt(x.Value) == mathx.RoundInt(y.Value) {
					same++
				}
			}
		}
		if ag.N == 0 {
			continue
		}
		ag.Agree = mathx.Round(mathx.Ratio(same, ag.N), 3)
		if k, ok := mathx.Kappa(ca, cb); ok {
			ag.Kappa = ptr(mathx.Round(k, 3))
		}
		if mean, ok := mathx.Mean(diffs); ok {
			ag.MeanAbs, ag.MaxAbs = ptr(mathx.Round(mean, 4)), ptr(mathx.Round(slices.Max(diffs), 4))
		}
		out[q.Name] = ag
	}
	return out
}

func ptr(v float64) *float64 { return &v }

// measure fills the per-provider results: decided counts, identical-state
// check, repeat agreement, weak-label evaluation (feedback.Evaluate on the
// weak reply labels of triage), and cross-provider agreement.
func (r *benchReport) measure(s *session, first, second map[string]answerSet, hashes map[string]map[int64]string, down map[string]string, spent engine.Usage) {
	r.Spend = spent
	r.Spend.EstimatedUSD = round6(spent.EstimatedUSD)
	within := r.Options.ReplyWithin
	for _, name := range r.Options.Providers {
		pr := r.Providers[name]
		pr.Main.finish(pr.provider)
		pr.Repeat.finish(pr.provider)
		pr.Down = down[name]
		got := first[name]
		pr.Decided = len(got)
		for id, h := range hashes[name] {
			if h != r.Sample.hashes[id] {
				pr.StateMismatch++
			}
		}
		if len(second[name]) > 0 {
			pr.RepeatAgree = compareAnswers(s.profile, pick(got, second[name]), second[name])
		}
		if len(got) == 0 {
			continue
		}
		judged := feedback.Judged{}
		v := &View{Analysis: s.analysis, Results: map[int64]Result{}}
		for id, answers := range got {
			values := map[string]taxonomy.Value{}
			for q, a := range answers {
				values[q] = taxonomy.FromAnswer(a)
			}
			judged[id], v.Results[id] = values, Result{MessageID: id, Values: values}
		}
		pr.WeakReply = feedback.Evaluate(judged, nil, weakReplies(v, within), s.profile).WeakReply
	}
	for i, a := range r.Options.Providers {
		for _, b := range r.Options.Providers[i+1:] {
			if len(first[a]) > 0 && len(first[b]) > 0 {
				r.Cross[a+"|"+b] = compareAnswers(s.profile, first[a], first[b])
			}
		}
	}
}

// pick keeps the answers of a for the messages of only.
func pick(a, only answerSet) answerSet {
	out := answerSet{}
	for id := range only {
		if x, ok := a[id]; ok {
			out[id] = x
		}
	}
	return out
}
