package triage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Review 2a #2 (repro R5, failed first: two jobs ran, 12 calls for 3
// messages): one run at a time, in-process and across processes.
func TestOneRunAtATime(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 3 {
		w.mail(fmt.Sprintf("c%d@x.org", i), fmt.Sprint("C", i), "Hi", time.Duration(i)*time.Hour, 0)
	}
	w.dec.block = make(chan struct{})
	m := jobs.NewManager(w.cfg, w.bus, nil)
	j1, err := w.tr.Start(m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.tr.Start(m, Options{}); !errors.Is(err, ErrTriageBusy) {
		t.Fatalf("a second Start must be refused: %v", err)
	}
	if _, err := w.tr.Run(t.Context(), Options{}, nil); !errors.Is(err, ErrTriageBusy) {
		t.Fatalf("Run during a job must be refused: %v", err)
	}
	close(w.dec.block)
	<-j1.Done()
	if w.dec.count() != 6 {
		t.Fatalf("3 messages × 2 calls, once: %d", w.dec.count())
	}
	// Another process holds the lease.
	if ok, _ := w.st.AcquireLease(t.Context(), JobKind, "other-process", time.Now().Add(time.Hour)); !ok {
		t.Fatal("lease")
	}
	if _, err := w.tr.Run(t.Context(), Options{}, nil); !errors.Is(err, ErrTriageBusy) {
		t.Fatalf("a run held by another process must be refused: %v", err)
	}
	_ = w.st.ReleaseLease(t.Context(), JobKind, "other-process")
	if _, err := w.tr.Run(t.Context(), Options{}, nil); err != nil {
		t.Fatalf("free again: %v", err)
	}
	_ = m.Shutdown(context.Background())
}

// Review 2a #3 (repro D1, failed first: chunk 1 billed every run, nothing
// stored): partial answers are kept and only the missing questions are
// asked next time.
func TestPartialAnswersAreKeptAndCompleted(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "A", "Hi", 0, 0)
	w.dec.partial = func(string) bool { return true }
	rep := w.run(Options{})
	if rep.Partial != 1 || rep.Decided != 0 || rep.Failed != 0 || rep.State != runstate.Succeeded {
		t.Fatalf("first run keeps 16 paid answers: %+v", rep)
	}
	v, _ := w.tr.Read(t.Context())
	if r := v.Results[id]; len(r.Values) != 16 || len(r.Missing) != 1 || r.Failure == "" || v.Coverage.Partial != 1 {
		t.Fatalf("partial result %+v %+v", r, v.Coverage)
	}
	plan, _ := w.tr.Plan(t.Context(), Options{})
	if plan.Partial != 1 || plan.Estimate.Calls != 1 {
		t.Fatalf("the plan asks one question in one call: %+v", plan)
	}
	w.dec.partial = nil
	rep = w.run(Options{})
	if rep.Decided != 1 || w.dec.asked[len(w.dec.asked)-1] != 1 {
		t.Fatalf("second run completes with the missing question only: %+v asked %v", rep, w.dec.asked)
	}
	if v, _ = w.tr.Read(t.Context()); len(v.Results[id].Values) != 17 || v.Results[id].Failure != "" || v.Coverage.Fresh != 1 {
		t.Fatalf("complete now: %+v", v.Results[id])
	}
}

// Review 2a #8 (failed first: a null price silently disabled the USD
// guard): an unpriced plan is refused unless the owner allows it.
func TestUnpricedProviderIsRefusedUnlessAllowed(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@x.org", "A", "Hi", 0, 0)
	w.src.d = &fakeDecider{}
	w.src.d.name = "unpriced"
	unpriced := &unpricedDecider{fakeDecider: w.src.d}
	w.tr.src = &staticSource{d: unpriced}
	if _, err := w.tr.Run(t.Context(), Options{}, nil); !errors.Is(err, ErrBudget) || !strings.Contains(err.Error(), "no price") {
		t.Fatalf("unpriced refused: %v", err)
	}
	w.withSettings(map[string]any{"triage.budget.allow_unpriced": true})
	if rep, err := w.tr.Run(t.Context(), Options{}, nil); err != nil || rep.Decided != 1 {
		t.Fatalf("allowed: %v %+v", err, rep)
	}
}

type unpricedDecider struct{ *fakeDecider }

func (u *unpricedDecider) Plan(state string, qs []engine.Question) (engine.Estimate, error) {
	e, err := u.fakeDecider.Plan(state, qs)
	e.PriceKnown, e.EstimatedUSD = false, 0
	return e, err
}

type staticSource struct{ d engine.Decider }

func (s *staticSource) Decider(*config.Config) (engine.Decider, *engine.Catalog, error) {
	return s.d, nil, nil
}

// The in-run guard stops when spending (actual, plus the estimates of
// calls in flight) would exceed the budget; with actual costs above the
// estimates the overshoot is bounded by the calls already in flight (one
// worker: one message).
func TestBudgetStopsMidRun(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 1, "triage.budget.max_usd": 0.5})
	for i := range 5 {
		w.mail(fmt.Sprintf("b%d@x.org", i), fmt.Sprint("B", i), "Hi", time.Duration(i)*time.Hour, 0)
	}
	w.dec.extra = 0.2 // each message costs $0.2 more than planned (its ceiling covers it)
	rep := w.run(Options{})
	if rep.StoppedBy != StoppedBudget || rep.Decided != 2 || rep.NotAttempted != 3 || rep.Usage.EstimatedUSD > 0.5 || rep.Overruns != 0 {
		t.Fatalf("stops before a call whose ceiling would pass $0.5: %+v", rep)
	}
}

// Leftover of review 2a: calls in flight on parallel workers were covered
// only at their estimate, so a run could overshoot by the calls in flight.
// Each call now reserves its ceiling before it is sent.
func TestBudgetNeverOvershootsWithParallelWorkers(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 4, "triage.budget.max_usd": 0.5})
	for i := range 6 {
		w.mail(fmt.Sprintf("p%d@x.org", i), fmt.Sprint("P", i), "Hi", time.Duration(i)*time.Hour, 0)
	}
	w.dec.extra = 0.2
	w.dec.block, w.dec.entered = make(chan struct{}), make(chan struct{}, 8)
	done := make(chan Report)
	go func() { rep, _ := w.tr.Run(t.Context(), Options{}, nil); done <- rep }()
	for range 4 {
		<-w.dec.entered // four workers hold a message each before any call
	}
	close(w.dec.block)
	rep := <-done
	if rep.Usage.EstimatedUSD > 0.5 || rep.Decided != 2 || rep.StoppedBy != StoppedBudget || rep.Failed != 0 {
		t.Fatalf("never past $0.5 with 4 workers in flight: %+v", rep)
	}
}

func TestCallsBilledPastTheirCeilingAreCounted(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 1, "triage.budget.max_usd": 0.5})
	for i := range 5 {
		w.mail(fmt.Sprintf("o%d@x.org", i), fmt.Sprint("O", i), "Hi", time.Duration(i)*time.Hour, 0)
	}
	w.dec.extra, w.dec.under = 0.2, true // the provider bills more than reserve_tokens_per_byte allows for
	rep := w.run(Options{})
	if rep.Overruns != rep.Decided || rep.Decided != 3 || rep.StoppedBy != StoppedBudget {
		t.Fatalf("overruns are disclosed: %+v", rep)
	}
}

func TestUnavailableProviderStopsWithManyWorkers(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 4})
	for i := range 20 {
		w.mail(fmt.Sprintf("u%d@client.com", i), fmt.Sprint(i), "S", time.Duration(i)*time.Minute, 0)
	}
	w.dec.fail = func(string) error {
		return &decision.APIError{Provider: "fake", Status: 402, Message: "Add credits", Class: decision.ErrInsufficientCredits}
	}
	rep := w.run(Options{})
	if rep.State != runstate.Failed || rep.StoppedBy != StoppedUnavailable || rep.Failed > 4 || rep.Failed+rep.NotAttempted != 20 {
		t.Fatalf("at most one call per worker before stopping: %+v", rep)
	}
}

func TestOversizedMessageIsDisclosedAndFailsWithoutACall(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@x.org", "A", "Huge", 0, 0)
	w.mail("b@x.org", "B", "Small", time.Hour, 0)
	w.dec.tooBig = func(state string) bool { return strings.Contains(state, "Subject: Huge") }
	plan, _ := w.tr.Plan(t.Context(), Options{})
	if plan.Unsendable != 1 || plan.Estimate.Calls != 2 {
		t.Fatalf("the plan discloses the unsendable message: %+v", plan)
	}
	rep := w.run(Options{})
	if rep.Decided != 1 || rep.Failed != 1 || w.dec.count() != 2 || !strings.Contains(rep.Failures[0].Error, "context budget") {
		t.Fatalf("it fails without a call: %+v", rep)
	}
}

func TestCorruptCachedAnswersAreDecidedAgain(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "A", "Hi", 0, 0)
	w.run(Options{})
	v, _ := w.tr.Read(t.Context())
	state := w.states()[id]
	if err := w.st.SaveDecision(t.Context(), v.Key, store.Decision{MessageID: id, StateHash: StateHash(state), Answers: "{not json", RunID: 1}); err != nil {
		t.Fatal(err)
	}
	if v, _ = w.tr.Read(t.Context()); v.Coverage.Unjudged != 1 {
		t.Fatalf("unreadable answers are not shown: %+v", v.Coverage)
	}
	if plan, _ := w.tr.Plan(t.Context(), Options{}); plan.Invalidated != 1 || plan.Selected != 1 {
		t.Fatalf("and decided again: %+v", plan)
	}
}

// TestBothPlansRefuseTheSameUnpickedStore pins the shared plan prologue: the
// full plan and the pending plan refuse a store the user did not pick with
// the same error, before any decision read.
func TestBothPlansRefuseTheSameUnpickedStore(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@x.org", "A", "Hi", 0, 0)
	o := Options{Stores: []string{sourceKeyOf("store-2")}}
	_, fullErr := w.tr.Plan(t.Context(), o)
	_, _, pendingErr := w.tr.planPending(t.Context(), o)
	if fullErr == nil || pendingErr == nil {
		t.Fatalf("an unpicked store was accepted: full=%v pending=%v", fullErr, pendingErr)
	}
	if fullErr.Error() != pendingErr.Error() {
		t.Fatalf("the plans refuse differently: full=%q pending=%q", fullErr, pendingErr)
	}
}

func TestStoresOptionAndPerMessageResult(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "A", "Hi", 0, 0)
	if _, err := w.tr.Plan(t.Context(), Options{Stores: []string{"store-0123456789ab"}}); err == nil {
		t.Fatal("a store that is not picked is refused")
	}
	if plan, _ := w.tr.Plan(t.Context(), Options{Stores: []string{sourceKeyOf("store-1")}}); plan.Eligible != 1 {
		t.Fatalf("one store only: %+v", plan)
	}
	if r, err := w.tr.Result(t.Context(), id); err != nil || r.Values != nil || r.MessageID != id {
		t.Fatalf("eligible, not decided yet: %+v %v", r, err)
	}
	w.run(Options{})
	if r, err := w.tr.Result(t.Context(), id); err != nil || len(r.Values) != 17 {
		t.Fatalf("one message's result: %+v %v", r, err)
	}
	if _, err := w.tr.Result(t.Context(), 424242); err == nil {
		t.Fatal("a message that is not triaged is an error")
	}
}

// Rules review §1.1: the decider is built per run, so a changed
// triage.decider applies without a restart.
func TestDeciderIsBuiltPerRunFromSettings(t *testing.T) {
	cfg := configtest.New(t, nil)
	writeDeciders(t, cfg, "a", "b")
	e := NewEngines(engine.Deps{Config: cfg, Secrets: noSecrets{}})
	if _, _, err := e.Decider(cfg); err == nil {
		t.Fatal("an empty selector is an explicit not-configured error")
	}
	if err := cfg.Set(DeciderSetting, "a"); err != nil {
		t.Fatal(err)
	}
	d, _, err := e.Decider(cfg)
	if err != nil || d.Name() != "a" {
		t.Fatalf("configured: %v", err)
	}
	if err := cfg.Set(DeciderSetting, "b"); err != nil {
		t.Fatal(err)
	}
	if d, _, err = e.Decider(cfg); err != nil || d.Name() != "b" {
		t.Fatalf("changed setting applies at once: %v", err)
	}
	st, err := e.Status(cfg)
	if err != nil || st.KeyPresent || st.Provider != "b" {
		t.Fatalf("status %+v %v", st, err)
	}
	if _, err := e.TestProvider(t.Context(), cfg); !errors.Is(err, decision.ErrNoKey) {
		t.Fatalf("the probe without a key fails before any call: %v", err)
	}
	w := newWorld(t, nil)
	w.mail("a@x.org", "A", "Hi", 0, 0)
	w.run(Options{})
	w.run(Options{})
	if w.src.builds < 2 {
		t.Fatalf("one decider per run: %d builds", w.src.builds)
	}
}

// writeDeciders writes two complete decider records and leaves the role
// unconfigured.
func writeDeciders(t *testing.T, cfg *config.Config, ids ...string) {
	t.Helper()
	doc := "{"
	for i, id := range ids {
		if i > 0 {
			doc += ","
		}
		doc += `"` + id + `": {"protocol": "systemone", "base_url": "http://127.0.0.1:9/v1", "path": "/systemone",
			"model": "` + id + `-model", "api_key_secret": "PROVIDER_KEY", "max_questions": 16,
			"context_tokens": 1000, "chars_per_token": 3, "reserve_tokens_per_byte": 1, "probability_tolerance": 0.02,
			"price_input_per_mtok": 0.01, "price_output_per_mtok": 0}`
	}
	doc += "}"
	if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "providers.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The paid-decision cache key follows the record fingerprint: a model or
// endpoint change invalidates earlier decisions without a settings change.
func TestDeciderKeyFollowsTheRecordFingerprint(t *testing.T) {
	w := newWorld(t, nil)
	write := func(model, baseURL string) {
		t.Helper()
		doc := `{"a": {"protocol": "systemone", "base_url": "` + baseURL + `", "path": "/systemone",
			"model": "` + model + `", "api_key_secret": "PROVIDER_DECIDER_API_KEY", "max_questions": 16,
			"context_tokens": 1000, "chars_per_token": 3, "reserve_tokens_per_byte": 1,
			"probability_tolerance": 0.02, "price_input_per_mtok": 0.01, "price_output_per_mtok": 0}}`
		if err := os.WriteFile(filepath.Join(w.cfg.Paths().DataDir, "providers.json"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("m-1", "http://127.0.0.1:9/v1")
	if err := w.cfg.Set(DeciderSetting, "a"); err != nil {
		t.Fatal(err)
	}
	engines := NewEngines(engine.Deps{Config: w.cfg, Secrets: noSecrets{}})
	tr, err := New(w.cfg, w.st, w.bus, engines, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := func() string {
		s, err := tr.prepare(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return s.key.Decider
	}
	first := key()
	if first == "" {
		t.Fatal("empty decider key")
	}
	write("m-2", "http://127.0.0.1:9/v1")
	if got := key(); got == first {
		t.Fatal("a model change must invalidate the triage decision cache")
	}
	second := key()
	write("m-2", "http://127.0.0.1:10/v1")
	if got := key(); got == second {
		t.Fatal("an endpoint change must invalidate the triage decision cache")
	}
}

type noSecrets struct{ secrets.Store }

func (noSecrets) Get(string) (string, error) { return "", secrets.ErrNotFound }

func TestAnalyticsAssemblesEvaluationAndInsights(t *testing.T) {
	w := newWorld(t, nil)
	var ids []int64
	for i := range 6 {
		ids = append(ids, w.mail("a@client.com", fmt.Sprint("A", i), "Budget", time.Duration(i)*time.Hour, time.Duration(i%2)*10*time.Minute))
	}
	w.run(Options{})
	for _, id := range ids[:3] {
		_ = w.st.PutLabel(t.Context(), id, "reply_needed", "1", store.LabelManual)
	}
	a, err := w.tr.Analytics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Evaluation.Manual) != 1 || a.Evaluation.WeakReply == nil || a.Evaluation.WeakReply.Count != 6 || len(a.Insights.TopSenders) != 1 ||
		len(a.Thresholds) != 1 || a.Behaviour == nil || a.Coverage.Eligible != 9 {
		t.Fatalf("analytics %+v", a)
	}
}

// feedback.weak_reply.within: a reply later than the window is not a weak
// "replied" label (0 keeps the old rule: any later reply).
func TestWeakReplyWindow(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 6 {
		late := time.Hour
		if i%2 == 1 {
			late = 10 * 24 * time.Hour
		}
		w.mail("a@client.com", fmt.Sprint("W", i), "Budget", time.Duration(i)*time.Hour, late)
	}
	w.run(Options{})
	for within, want := range map[string]float64{"0s": 1, "168h": 0.5} {
		w.withSettings(map[string]any{"feedback.weak_reply.within": within})
		a, err := w.tr.Analytics(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		// the fake model says reply needed (0.8) for all six
		if got := a.Evaluation.WeakReply.Metrics["precision"]; a.Evaluation.WeakReply.Count != 6 || got != want {
			t.Fatalf("within %s: precision %v, want %v (%+v)", within, got, want, a.Evaluation.WeakReply)
		}
	}
}

// Rules review 2b §5 (failed first: the $0.20 call was admitted): once an
// unpriced call had been reserved, Estimate.Sub never made the reserved
// sum priced again, so max_usd was skipped for the rest of the run.
func TestAnUnpricedCallDoesNotDisableTheUSDBudget(t *testing.T) {
	rs := newRunState(&Report{}, Budget{MaxCalls: 100, MaxUSD: 0.10, AllowUnpriced: true})
	unpriced := engine.Estimate{Calls: 1}
	if !rs.Reserve(unpriced) {
		t.Fatal("an unpriced call is allowed (allow_unpriced)")
	}
	rs.Settle(unpriced, engine.Usage{Calls: 1})
	if rs.Reserve(engine.Estimate{Calls: 1, EstimatedUSD: 0.20, PriceKnown: true}) {
		t.Fatal("a priced call over max_usd is refused after an unpriced one settled")
	}
}

// User decision 2026-10-15: no triage answer before the picked mailboxes
// are fully learned; a triager without the gate cannot be built.
func TestTriageAnswersAreGated(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "A", "Hi", 0, 0)
	w.tr.gate = closedGate{}
	if _, err := w.tr.Read(t.Context()); !errors.Is(err, learning.ErrLocked) {
		t.Fatalf("Read: %v", err)
	}
	if _, err := w.tr.Result(t.Context(), id); !errors.Is(err, learning.ErrLocked) {
		t.Fatalf("Result: %v", err)
	}
	if _, err := w.tr.Analytics(t.Context()); !errors.Is(err, learning.ErrLocked) {
		t.Fatalf("Analytics: %v", err)
	}
	if _, err := w.tr.Plan(t.Context(), Options{}); err != nil {
		t.Fatalf("planning a teacher run is learning, not an answer: %v", err)
	}
	if _, err := New(w.cfg, w.st, nil, w.src, nil, nil); err == nil {
		t.Fatal("a triager needs the gate")
	}
}

type closedGate struct{}

func (closedGate) Allow(context.Context) error { return &learning.LockedError{} }

// #10: a label click no longer rebuilds the whole mailbox analysis: the
// threads, behaviour and history are reused, only the labels reload;
// new mail rebuilds everything.
func TestALabelReusesTheMailAnalysis(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "A", "Hi", 0, 0)
	first, err := w.tr.core(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), id, "reply_needed", "1", store.LabelManual); err != nil {
		t.Fatal(err)
	}
	second, _ := w.tr.core(t.Context())
	if second == first || second.analysis != first.analysis || second.history != first.history || len(second.labels) != 1 {
		t.Fatal("a label reloads the labels only")
	}
	w.mail("b@x.org", "B", "Hi", time.Hour, 0)
	if third, _ := w.tr.core(t.Context()); third.analysis == second.analysis {
		t.Fatal("new mail rebuilds the analysis")
	}
}

// A routed read takes its answers from the route table. It must not build a
// decider adapter or HTTP client per message: the identity is resolved once
// and cached under the route key.
func TestRoutedReadsDoNotBuildADecider(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@x.org", "c1", "Hi", 0, 0)
	w.useRoute(t, w.st)
	// Warm the identity: the first routed view resolves the profile and the
	// decider identity once.
	if _, err := w.tr.Page(t.Context(), PageQuery{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	builds := w.src.builds
	if _, err := w.tr.Results(t.Context(), []int64{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.tr.Result(t.Context(), id); err == nil {
		// An empty route fixture may answer or abstain; both paths are reads.
	}
	if w.src.builds != builds {
		t.Fatalf("routed reads built a decider: %d -> %d", builds, w.src.builds)
	}
}
