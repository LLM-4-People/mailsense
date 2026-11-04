package triage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// startAutomatic runs the one automatic entry point and waits for its job.
func startAutomatic(t *testing.T, w *world, jm *jobs.Manager, o Options) *jobs.Job {
	t.Helper()
	j, err := w.tr.StartIfPending(t.Context(), jm, o)
	if err != nil {
		t.Fatalf("StartIfPending: %v", err)
	}
	if j != nil {
		<-j.Done()
	}
	return j
}

// currentFailure is the stored failure of one message under the triager's
// current key.
func currentFailure(t *testing.T, w *world, id int64) (store.Failure, bool) {
	t.Helper()
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failures, err := w.st.LoadFailures(t.Context(), s.key)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := failures[id]
	return f, ok
}

// rejectPermanently makes the decider reject one message for itself, the
// provider's 400/404/422 class.
func rejectPermanently(w *world) {
	w.dec.fail = func(string) error { return fmt.Errorf("%w: HTTP 422 rejected", engine.ErrRejected) }
}

// quarantineFirstMessage writes one message and reaches the permanent
// failure bound with automatic runs.
func quarantineFirstMessage(t *testing.T) (*world, int64, *jobs.Manager) {
	t.Helper()
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	id := w.mail("poison@client.com", "P", "Poison", 0, 0)
	rejectPermanently(w)
	jm := newJobManager(t, w)
	for attempt := 1; attempt <= store.MaxPermanentFailures; attempt++ {
		if j := startAutomatic(t, w, jm, Options{Limit: 10}); j == nil {
			t.Fatalf("attempt %d was not planned", attempt)
		}
	}
	return w, id, jm
}

// A per-message rejection is retried to the safety bound, then the automatic
// plan selects nothing: the same poison message is never billed again by the
// catch-up loop until an input changes.
func TestPermanentRejectionIsQuarantined(t *testing.T) {
	w, id, jm := quarantineFirstMessage(t)
	f, ok := currentFailure(t, w, id)
	if !ok {
		t.Fatal("the rejected message has no stored failure")
	}
	if !f.Permanent || f.Attempts != store.MaxPermanentFailures || f.Revision != w.revision(t, id) {
		t.Fatalf("failure = %+v, want a permanent failure at attempt %d of revision %d", f, store.MaxPermanentFailures, w.revision(t, id))
	}
	if pending, err := w.tr.hasPending(t.Context(), Options{}); err != nil || pending {
		t.Fatalf("a quarantined message is pending: pending=%v err=%v", pending, err)
	}
	plan, err := w.tr.Plan(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Quarantined != 1 || plan.Pending != 0 || plan.Fresh != 0 {
		t.Fatalf("full plan = quarantined %d pending %d fresh %d, want 1/0/0", plan.Quarantined, plan.Pending, plan.Fresh)
	}

	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sc := w.tr.decisionScope(s, Options{})
	if ids, err := w.st.DecisionCandidates(t.Context(), sc); err != nil || len(ids) != 0 {
		t.Fatalf("candidates = %v err=%v, want none", ids, err)
	}
	if n, err := w.st.DecisionQuarantinedCount(t.Context(), sc); err != nil || n != 1 {
		t.Fatalf("quarantined = %d err=%v, want 1", n, err)
	}

	// The automatic no-work path takes one existence probe, loads no decision
	// row and makes no provider call.
	calls := w.dec.count()
	probes := 0
	pending := w.tr.pending
	w.tr.pending = func(ctx context.Context, sc store.DecisionScope) (bool, error) {
		probes++
		return pending(ctx, sc)
	}
	w.tr.candidates = func(context.Context, store.DecisionScope) ([]int64, error) {
		t.Fatal("a quarantined-only mailbox built the candidate plan")
		return nil, nil
	}
	w.tr.loadDecisions = func(context.Context, store.TriageKey, []int64) (map[int64]store.Decision, error) {
		t.Fatal("a quarantined-only mailbox loaded decisions")
		return nil, nil
	}
	if j := startAutomatic(t, w, jm, Options{Limit: 10}); j != nil {
		t.Fatal("a quarantined message started another automatic run")
	}
	if probes != 1 {
		t.Fatalf("the no-work path ran %d probes, want exactly 1", probes)
	}
	if got := w.dec.count(); got != calls {
		t.Fatalf("the quarantine made %d provider call(s), want 0", got-calls)
	}
}

// Every input change that can make the rejected request different clears the
// quarantine: a new message revision, decider key, route (or preview), and
// the explicit Redecide/Force actions.
func TestQuarantineClearsWhenTheInputsChange(t *testing.T) {
	t.Run("message revision", func(t *testing.T) {
		w, id, jm := quarantineFirstMessage(t)
		m := w.message("in-1", mail.RoleInbox, "Inbox", "poison@client.com", "P", "Poison changed", base.Add(time.Hour), me)
		m.Body = "A different body, a new revision."
		w.write(m)
		if !w.hasPending(t) {
			t.Fatal("a new message revision left the message quarantined")
		}
		startAutomatic(t, w, jm, Options{Limit: 10})
		f, ok := currentFailure(t, w, id)
		if !ok || f.Attempts != 1 || f.Revision != w.revision(t, id) {
			t.Fatalf("failure after the revision change = %+v, want one attempt at the new revision", f)
		}
	})

	t.Run("decider key", func(t *testing.T) {
		w, id, jm := quarantineFirstMessage(t)
		w.dec.name = "other"
		if !w.hasPending(t) {
			t.Fatal("a new decider key left the message quarantined")
		}
		startAutomatic(t, w, jm, Options{Limit: 10})
		f, ok := currentFailure(t, w, id)
		if !ok || f.Attempts != 1 {
			t.Fatalf("failure under the new key = %+v, want one attempt", f)
		}
	})

	t.Run("route", func(t *testing.T) {
		w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
		id := w.mail("poison@client.com", "P", "Poison", 0, 0)
		w.useRoute(t, w.st)
		rejectPermanently(w)
		jm := newJobManager(t, w)
		for attempt := 1; attempt <= store.MaxPermanentFailures; attempt++ {
			if j := startAutomatic(t, w, jm, Options{Limit: 10}); j == nil {
				t.Fatalf("attempt %d was not planned", attempt)
			}
		}
		if pending, err := w.tr.hasPending(t.Context(), Options{}); err != nil || pending {
			t.Fatalf("the routed message is pending: %v %v", pending, err)
		}
		w.tr.UseRoute(w.st, RouteKeyFunc(func() (string, error) { return "rk2-ffffffffffffffffffffffffffffffff", nil }))
		if !w.hasPending(t) {
			t.Fatal("a route change left the message quarantined")
		}
		startAutomatic(t, w, jm, Options{Limit: 10})
		if f, ok := currentFailure(t, w, id); !ok || f.Attempts != 1 {
			t.Fatalf("failure after the route change = %+v, want one attempt", f)
		}
	})

	t.Run("Redecide", func(t *testing.T) {
		w, id, jm := quarantineFirstMessage(t)
		w.dec.fail = nil
		if j := startAutomatic(t, w, jm, Options{Limit: 10, Redecide: true}); j == nil {
			t.Fatal("Redecide did not re-decide a quarantined message")
		}
		if _, ok := currentFailure(t, w, id); ok {
			t.Fatal("the successful Redecide left the failure in place")
		}
		if pending, err := w.tr.hasPending(t.Context(), Options{}); err != nil || pending {
			t.Fatalf("the decided message is still pending: %v %v", pending, err)
		}
	})

	t.Run("Force", func(t *testing.T) {
		w, id, jm := quarantineFirstMessage(t)
		w.dec.fail = nil
		if j := startAutomatic(t, w, jm, Options{Limit: 10, Force: true}); j == nil {
			t.Fatal("Force did not re-decide a quarantined message")
		}
		if _, ok := currentFailure(t, w, id); ok {
			t.Fatal("the successful Force left the failure in place")
		}
	})
}

// hasPending is the default automatic plan's existence question.
func (w *world) hasPending(t *testing.T) bool {
	t.Helper()
	pending, err := w.tr.hasPending(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

// A provider-wide transient failure keeps retrying: it is never quarantined,
// however many times it repeats.
func TestTransientFailureIsRetriedAndNeverQuarantined(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	id := w.mail("flaky@client.com", "F", "Flaky", 0, 0)
	w.dec.fail = func(string) error { return fmt.Errorf("%w: HTTP 503", engine.ErrTransient) }
	jm := newJobManager(t, w)
	attempts := store.MaxPermanentFailures + 3
	for attempt := 1; attempt <= attempts; attempt++ {
		j := startAutomatic(t, w, jm, Options{Limit: 10})
		if j == nil {
			t.Fatalf("transient attempt %d was quarantined", attempt)
		}
		if info := j.Info(); info.Status != runstate.Failed {
			t.Fatalf("transient attempt %d state = %s, want failed", attempt, info.Status)
		}
	}
	f, ok := currentFailure(t, w, id)
	if !ok || f.Permanent || f.Attempts != attempts {
		t.Fatalf("transient failure = %+v, want %d non-permanent attempts", f, attempts)
	}
	if !w.hasPending(t) {
		t.Fatal("a repeated transient failure was quarantined")
	}

	w.dec.fail = nil
	if j := startAutomatic(t, w, jm, Options{Limit: 10}); j == nil {
		t.Fatal("the transient message was not retried")
	}
	if _, ok := currentFailure(t, w, id); ok {
		t.Fatal("the successful retry left the transient failure in place")
	}
}

// Quarantine changes no coverage count: the held message keeps its failure
// and stays unjudged, exactly as before it was set aside.
func TestQuarantineKeepsFailedAndUnjudgedCoverage(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	seedMessages(t, w, 12)
	seedCurrentDecisions(t, w)
	w.mail("poison@client.com", "P", "Poison", time.Hour, 0)
	rejectPermanently(w)
	jm := newJobManager(t, w)

	if j := startAutomatic(t, w, jm, Options{Limit: 10}); j == nil {
		t.Fatal("the rejected message was not attempted")
	}
	before, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt < store.MaxPermanentFailures; attempt++ {
		if j := startAutomatic(t, w, jm, Options{Limit: 10}); j == nil {
			t.Fatalf("attempt %d was not planned", attempt+1)
		}
	}
	after, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Coverage != before.Coverage {
		t.Fatalf("coverage changed across the quarantine: before %+v after %+v", before.Coverage, after.Coverage)
	}
	if after.Coverage.Eligible != 13 || after.Coverage.Failed != 1 || after.Coverage.Unjudged != 1 {
		t.Fatalf("coverage = %+v, want eligible 13, failed 1, unjudged 1", after.Coverage)
	}

	// The routed summary reads the store's one aggregate: a quarantined row
	// with no route answer stays unjudged and its failure stays counted.
	w.useRoute(t, w.st)
	routed, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if routed.Coverage.Failed != 1 || routed.Coverage.Unjudged != 13 || routed.Coverage.Eligible != 13 {
		t.Fatalf("routed coverage = %+v, want eligible 13, failed 1, unjudged 13", routed.Coverage)
	}
}
