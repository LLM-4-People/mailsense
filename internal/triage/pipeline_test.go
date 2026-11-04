package triage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func TestRunDecidesOnceAndOnlyPaysForChanges(t *testing.T) {
	w := newWorld(t, nil)
	a := w.mail("a@client.com", "A", "Budget", 0, time.Hour)
	w.mail("b@vendor.com", "B", "Promo", time.Hour, 0)
	fin, _ := events.Subscribe(w.bus, TopicFinished, 4)
	rep := w.run(Options{})
	// 2 inbox + 1 sent reply = 3 eligible, 17 questions = 2 calls each
	if rep.State != runstate.Succeeded || rep.Decided != 3 || rep.Usage.Calls != 6 || w.dec.count() != 6 || rep.Plan.Unseen != 3 {
		t.Fatalf("first run: %+v", rep)
	}
	if ev := <-fin.C; ev.RunID != rep.RunID || ev.Decided != 3 {
		t.Fatalf("finished event %+v", ev)
	}
	again := w.run(Options{})
	if again.Decided != 0 || again.Plan.Fresh != 3 || w.dec.count() != 6 {
		t.Fatalf("an unchanged mailbox costs nothing: %+v", again)
	}
	// Edit one message: only it is re-decided.
	m, _ := w.st.MessageByRow(t.Context(), a)
	m.Body = "Changed text?"
	w.write(m)
	third := w.run(Options{})
	if third.Decided != 1 || third.Plan.Invalidated != 1 || w.dec.count() != 8 {
		t.Fatalf("only the changed message is paid for: %+v", third)
	}
	forced := w.run(Options{Force: true, MessageIDs: []int64{a}})
	if forced.Decided != 1 || forced.Plan.Eligible != 1 {
		t.Fatalf("force re-decides a fresh message: %+v", forced)
	}
	runs, _ := w.st.ListTriageRuns(t.Context(), 10)
	if len(runs) != 4 || !strings.Contains(runs[3].Report, `"decided":3`) {
		t.Fatalf("runs are recorded with their report: %+v", runs)
	}
}

func TestBudgetRefusesBeforeAnyCall(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.budget.max_calls": 3})
	w.mail("a@client.com", "A", "Budget", 0, 0)
	w.mail("b@client.com", "B", "Budget", 0, 0)
	rep, err := w.tr.Run(t.Context(), Options{}, nil)
	if !errors.Is(err, ErrBudget) || w.dec.count() != 0 || rep.State != runstate.Failed || rep.NotAttempted != 2 || rep.StoppedBy != StoppedBudget {
		t.Fatalf("a plan of 4 calls with max_calls 3 is refused: %v %+v", err, rep)
	}
	if rep := w.run(Options{Limit: 1}); rep.Decided != 1 || w.dec.count() != 2 {
		t.Fatalf("a limited run within budget proceeds: %+v", rep)
	}
	w2 := newWorld(t, map[string]any{"triage.budget.max_usd": 0.0})
	w2.mail("a@client.com", "A", "Budget", 0, 0)
	if _, err := w2.tr.Run(t.Context(), Options{}, nil); !errors.Is(err, ErrBudget) || w2.dec.count() != 0 {
		t.Fatalf("max_usd 0 refuses priced calls: %v", err)
	}
	plan, err := w2.tr.Plan(t.Context(), Options{})
	if err != nil || plan.Refusal == "" || plan.Estimate.Calls != 2 || !plan.Estimate.PriceKnown {
		t.Fatalf("the plan is available to the UI before running: %+v %v", plan, err)
	}
}

func TestFailuresAreRecordedAndGoodResultsKept(t *testing.T) {
	w := newWorld(t, nil)
	a := w.mail("a@client.com", "A", "Alpha", 0, 0)
	b := w.mail("b@client.com", "B", "Beta", 0, 0)
	w.run(Options{})
	// Both change; then a's decision fails.
	for _, id := range []int64{a, b} {
		m, _ := w.st.MessageByRow(t.Context(), id)
		m.Body += " edited"
		w.write(m)
	}
	w.dec.fail = func(state string) error {
		if strings.Contains(state, "Subject: Alpha") {
			return fmt.Errorf("%w: q: missing answer", decision.ErrContract)
		}
		return nil
	}
	rep := w.run(Options{})
	if rep.State != runstate.Succeeded || rep.Decided != 1 || rep.Failed != 1 || len(rep.Failures) != 1 || rep.Failures[0].MessageID != a {
		t.Fatalf("one failure does not abort the run: %+v", rep)
	}
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r := v.Results[a]; !r.Stale || r.Failure == "" || v.Coverage.Fresh != 1 || v.Coverage.Stale != 1 || v.Coverage.Failed != 1 {
		t.Fatalf("the failed message keeps its earlier (stale) decision and discloses the failure: %+v %+v", r, v.Coverage)
	}
	// Everything fails: the run fails and nothing good is lost.
	w.dec.fail = func(string) error { return fmt.Errorf("%w: 500", decision.ErrHTTP) }
	rep = w.run(Options{})
	if rep.State != runstate.Failed || rep.Decided != 0 || rep.Failed != 1 {
		t.Fatalf("a fully failed run fails: %+v", rep)
	}
	if v2, _ := w.tr.Read(t.Context()); len(v2.Results) != 2 {
		t.Fatal("a failed run never removes results")
	}
}

func TestUnavailableProviderStopsTheRun(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 1})
	for i := range 5 {
		w.mail(fmt.Sprintf("u%d@client.com", i), fmt.Sprint(i), "S", time.Duration(i)*time.Minute, 0)
	}
	w.dec.fail = func(string) error {
		return &decision.APIError{Provider: "fake", Status: 402, Message: "Add credits", Class: decision.ErrInsufficientCredits}
	}
	rep := w.run(Options{})
	if rep.State != runstate.Failed || rep.Failed != 1 || rep.NotAttempted != 4 || rep.StoppedBy != StoppedUnavailable ||
		!strings.Contains(rep.Error, "Add credits") || !errors.Is(&decision.APIError{Class: decision.ErrInsufficientCredits}, engine.ErrUnavailable) {
		t.Fatalf("402 stops at the first message: %+v", rep)
	}
}

// A rate limit, an unavailable gateway or a remote 500 is about the
// provider, not about these messages: the run stops after the first wave,
// keeps the one failure row, stores nothing for the unattempted mail and
// retries all of it later.
func TestTransientProviderStopsTheRun(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w := newWorld(t, map[string]any{"triage.workers": 1})
			for i := range 5 {
				w.mail(fmt.Sprintf("t%d@client.com", i), fmt.Sprint(i), "S", time.Duration(i)*time.Minute, 0)
			}
			w.dec.fail = func(string) error {
				return engine.Classify("fake", status, []byte(`{"error":{"message":"slow down"}}`), nil, 200)
			}
			rep := w.run(Options{})
			if rep.State != runstate.Failed || rep.StoppedBy != StoppedTransient || rep.Failed != 1 || rep.NotAttempted != 4 ||
				len(rep.Failures) != 1 || w.dec.count() != 2 || len(w.dec.states) != 1 {
				t.Fatalf("%d stops after the first message: %+v calls=%d decides=%d", status, rep, w.dec.count(), len(w.dec.states))
			}
			if !strings.Contains(rep.Error, "temporarily unavailable") {
				t.Fatalf("%d: the report says why: %q", status, rep.Error)
			}
			fails, err := w.st.LoadFailures(t.Context(), rep.Plan.Key)
			if err != nil || len(fails) != 1 {
				t.Fatalf("%d: only the attempted message has a failure row: %v %+v", status, err, fails)
			}
			w.dec.fail = nil
			if again := w.run(Options{}); again.Decided != 5 || again.Failed != 0 || again.StoppedBy != "" {
				t.Fatalf("%d: the next healthy run decides every message: %+v", status, again)
			}
		})
	}
}

// A transport failure that survives the HTTP retry policy carries
// engine.ErrTransient (pinned by the decision adapter test) and stops the
// run exactly like a rate limit.
func TestTransientTransportFailureStopsTheRun(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 1})
	for i := range 4 {
		w.mail(fmt.Sprintf("x%d@client.com", i), fmt.Sprint(i), "S", time.Duration(i)*time.Minute, 0)
	}
	w.dec.fail = func(string) error {
		return fmt.Errorf("%w: %w", engine.ErrTransient, errors.New("dial tcp: connection refused"))
	}
	rep := w.run(Options{})
	if rep.State != runstate.Failed || rep.StoppedBy != StoppedTransient || rep.Failed != 1 || rep.NotAttempted != 3 ||
		w.dec.count() != 2 || len(w.dec.states) != 1 {
		t.Fatalf("a transport failure stops after the first message: %+v calls=%d decides=%d", rep, w.dec.count(), len(w.dec.states))
	}
}

func TestStoreSwitchAndFolderRoles(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@client.com", "A", "Mine", 0, 0)
	if _, err := w.st.UpsertFolder(t.Context(), mail.Folder{SourceID: "src", StoreID: "deleg", StoreName: "Boss", StoreKind: mail.StoreDelegate,
		Path: "Inbox", Role: mail.RoleInbox, ItemCount: -1, RemoteID: "r"}); err != nil {
		t.Fatal(err)
	}
	other := w.message("boss-1", mail.RoleInbox, "Inbox", "x@client.com", "D", "Boss mail", base, "boss@x.com")
	other.StoreID, other.StoreName = "deleg", "Boss"
	draft := w.message("draft-1", mail.RoleDrafts, "Drafts", me, "E", "Draft", base, "a@client.com")
	w.write(other, draft)
	if plan, _ := w.tr.Plan(t.Context(), Options{}); plan.Eligible != 1 {
		t.Fatalf("default: the picked store only, no drafts (eligible %d)", plan.Eligible)
	}
	w2 := w.withSettings(map[string]any{source.SettingSelected: []any{sourceKeyOf("store-1"), sourceKeyOf("deleg")}, "triage.folder_roles": []any{"inbox", "drafts"}})
	w2.ownAll() // the newly picked store's mail is judged (scope runs after the pick)
	if plan, _ := w2.tr.Plan(t.Context(), Options{}); plan.Eligible != 3 {
		t.Fatalf("picked delegate and drafts: eligible %d", plan.Eligible)
	}
}

func TestCancellationKeepsWhatWasPaidFor(t *testing.T) {
	w := newWorld(t, map[string]any{"triage.workers": 1})
	w.mail("a@client.com", "A", "S1", 0, 0)
	w.mail("b@client.com", "B", "S2", time.Minute, 0)
	w.dec.block = make(chan struct{})
	mgr := jobs.NewManager(w.cfg, w.bus, nil)
	prog, _ := events.Subscribe(w.bus, TopicProgress, 8)
	job, err := w.tr.Start(mgr, Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.dec.block <- struct{}{} // first message decided
	<-prog.C
	job.Cancel()
	<-job.Done()
	if info := job.Info(); info.Status != runstate.Cancelled {
		t.Fatalf("job %+v", info)
	}
	runs, _ := w.st.ListTriageRuns(t.Context(), 1)
	if runs[0].State != runstate.Cancelled || !strings.Contains(runs[0].Report, `"decided":1`) || !strings.Contains(runs[0].Report, `"stopped_by":"cancelled"`) {
		t.Fatalf("cancelled run: %+v", runs[0])
	}
	_ = mgr.Shutdown(context.Background())
}

func TestReadAnnotatesPriorsAndFlagsLowConfidence(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 4 {
		w.mail("a@client.com", fmt.Sprint(i), "Budget", time.Duration(i)*time.Hour, 10*time.Minute)
	}
	target := w.mail("a@client.com", "T", "Budget", 9*time.Hour, 0)
	w.dec.value = 0.2 // noul 0.2, confidence 0.2 (< low_confidence 0.5)
	w.run(Options{})
	v, err := w.tr.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r := v.Results[target]
	reply := r.Values["reply_needed"]
	// Causal: before the target a@ was answered 4 of 4, overall 4 of 4 →
	// prior (4 + 3·1)/(4 + 3) = 1. The prior is a diagnostic; the answer
	// stays the model's 0.2, which is "no" at the profile cutoff 0.5. The
	// old 0.76 blend would have flipped the displayed class to yes.
	if reply.Model != 0.2 || reply.Prior == nil || abs(*reply.Prior-1) > 1e-9 ||
		abs(reply.Number-0.2) > 1e-9 || reply.Decided != engine.NoulNo {
		t.Fatalf("reply value: %+v", reply)
	}
	if s := r.Values["suspicious"]; s.Prior != nil || s.Number != 0.2 {
		t.Fatalf("suspicious weight 0: model kept: %+v", s)
	}
	// Role-aware: category confidence 0.2 < 0.40 and urgency 0.2 < 0.25;
	// reply 0.2 is 0.3 away from 0.5 (decisive); topic is no escalation role.
	if !r.Escalate || len(r.Uncertain) != 2 || r.Uncertain[0] != "category" || r.Uncertain[1] != "urgency" || v.Coverage.Eligible != 9 || v.Coverage.Fresh != 9 {
		t.Fatalf("low confidence is flagged for escalation: %+v %+v", r, v.Coverage)
	}
	raw, _ := w.st.LoadDecisions(t.Context(), v.Key)
	if strings.Contains(raw[target].Answers, "0.76") {
		t.Fatal("the cache stores raw answers only")
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// Regression (review of this phase): a message removed between the plan
// and its dispatch made SaveFailure hit the foreign key, which aborted the
// whole run as an output failure.
func TestMessageRemovedAfterThePlanIsSkipped(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@client.com", "A", "S", 0, 0)
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rep := &Report{Plan: &Plan{Selected: 1}}
	rs := newRunState(rep, Budget{MaxCalls: 10, MaxUSD: 1})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.tr.decideOne(ctx, cancel, s, rs, item{rowID: 987654}, nil)
	if rs.output != nil || rep.Failed != 0 || rep.Decided != 0 || ctx.Err() != nil || w.dec.count() != 0 {
		t.Fatalf("a vanished message is neither decided, failed nor fatal: %v %+v", rs.output, rep)
	}
}
