package index

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// retryStart is the fake clock's start: every backoff arithmetic below is
// deterministic and the tests never wait the configured duration.
var retryStart = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// newRetryWorld is the usual index world with a fake clock and a job
// manager (both cleaned up before the store closes).
func newRetryWorld(t *testing.T, overrides map[string]any) (*world, *clocktest.Fake, *jobs.Manager) {
	t.Helper()
	w := newWorld(t, overrides)
	clk := clocktest.New(retryStart)
	w.ix.clock = clk
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return w, clk, m
}

// newestRun reads the persisted latest run and its report.
func newestRun(t *testing.T, w *world) (store.IndexRun, Report) {
	t.Helper()
	runs, err := w.st.ListIndexRuns(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("no index run was persisted")
	}
	var rep Report
	if err := json.Unmarshal([]byte(runs[0].Report), &rep); err != nil {
		t.Fatalf("run %d report: %v", runs[0].ID, err)
	}
	return runs[0], rep
}

// ensureStartsOne expects Ensure to start exactly one job, waits for it and
// returns its persisted report.
func ensureStartsOne(t *testing.T, w *world, m *jobs.Manager) (store.IndexRun, Report) {
	t.Helper()
	before := len(m.List())
	if _, err := w.ix.Ensure(t.Context(), m); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	list := m.List()
	if len(list) != before+1 {
		t.Fatalf("Ensure started %d job(s), want exactly 1", len(list)-before)
	}
	waitJob(t, m, list[len(list)-1])
	return newestRun(t, w)
}

// ensureDoesNotStart expects Ensure to return the cached coverage without
// planning or starting a run. A run that starts anyway is drained before
// the failure so the test never races the store close.
func ensureDoesNotStart(t *testing.T, w *world, m *jobs.Manager, want Coverage) Coverage {
	t.Helper()
	before := len(m.List())
	got, err := w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatalf("Ensure during the wait: %v", err)
	}
	if list := m.List(); len(list) != before {
		for _, info := range list[before:] {
			waitJob(t, m, info)
		}
		t.Fatalf("Ensure started %d run(s) during the wait", len(list)-before)
	}
	if got.Space != want.Space || got.Counts != want.Counts {
		t.Fatalf("Ensure did not return the stopped run's starting coverage: got %+v, want %+v", got, want)
	}
	return got
}

// waitRetryClear waits for an asynchronous reset (events.On handles in its
// own goroutine).
func waitRetryClear(t *testing.T, ix *Indexer, key retryKey) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := ix.retryActive(key, ix.clock.Now()); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the unavailable backoff was not cleared")
		}
		time.Sleep(time.Millisecond)
	}
}

// Round 27: an unavailable embedder used to select the same mail and start
// a full run (plan + begin + finish + persisted row) on every 2-minute
// reconcile. After the first unavailable stop the automatic reconcile must
// stay silent until the configured wait ends.
func TestUnavailableRunBacksOffTheAutomaticReconcile(t *testing.T) {
	w, clk, m := newRetryWorld(t, map[string]any{"index.quarantine_after": 1, "embed.batch_size": 1})
	w.mail("a", mail.RoleInbox, 2, "alpha")
	w.mail("b", mail.RoleInbox, 1, "beta")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)

	_, rep := ensureStartsOne(t, w, m)
	if rep.StoppedBy != StoppedUnavailable || rep.Committed() || rep.Before.Pending != 2 {
		t.Fatalf("first unavailable run: %+v", rep)
	}
	if calls := w.fake.Fake.Calls(); calls != 1 {
		t.Fatalf("the unavailable run made %d embedding calls, want 1", calls)
	}

	// Probe a fresh plan without changing the retry key (index_failures is
	// not a mail write): a plan would now report the newest message
	// quarantined (quarantine_after=1) while the cached starting coverage
	// still reports two pending messages.
	states, err := w.st.EmbeddingStates(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{
		{MessageID: states[0].RowID, Revision: states[0].Revision, Error: "probe"}}); err != nil {
		t.Fatal(err)
	}

	for tick := range 3 {
		clk.Advance(2 * time.Minute)
		cov := ensureDoesNotStart(t, w, m, rep.Before)
		if cov.Quarantined != 0 {
			t.Fatalf("tick %d planned again: quarantine count %d, want the cached 0", tick, cov.Quarantined)
		}
		if runs, _ := w.st.ListIndexRuns(t.Context(), 10); len(runs) != 1 {
			t.Fatalf("tick %d left %d index_runs rows, want 1", tick, len(runs))
		}
	}

	clk.Advance(15 * time.Minute)
	_, again := ensureStartsOne(t, w, m)
	if again.StoppedBy != StoppedUnavailable {
		t.Fatalf("the probe after the wait: %+v", again)
	}
	if runs, _ := w.st.ListIndexRuns(t.Context(), 10); len(runs) != 2 {
		t.Fatalf("expiry left %d rows, want 2", len(runs))
	}
}

func TestUnavailableRetryUsesTheConfiguredWait(t *testing.T) {
	w, clk, m := newRetryWorld(t, map[string]any{"index.retry_unavailable": "30m"})
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	_, rep := ensureStartsOne(t, w, m)

	clk.Advance(15 * time.Minute)
	ensureDoesNotStart(t, w, m, rep.Before)

	clk.Advance(15*time.Minute + time.Second)
	if _, again := ensureStartsOne(t, w, m); again.StoppedBy != StoppedUnavailable {
		t.Fatalf("probe after 30m: %+v", again)
	}
}

// A run that stored vectors before the provider went away keeps its report:
// the evidence/layer-1 triggers already ran, and the next reconcile must
// continue the remaining mail at once.
func TestACommittedUnavailableRunDoesNotBackOff(t *testing.T) {
	w, clk, m := newRetryWorld(t, map[string]any{"embed.batch_size": 1})
	for i := range 3 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	emb := &scriptedEmbedder{space: "scripted/1/8", dims: 8, embed: func(call int, texts []engine.Text) (engine.Embeddings, error) {
		if call == 1 {
			return unitVectors(len(texts), 8), nil
		}
		return engine.Embeddings{}, engine.ErrUnavailable
	}}
	w.ix = New(w.cfg, w.st, w.bus, splitSource{e: emb}, clk)

	_, rep := ensureStartsOne(t, w, m)
	if rep.StoppedBy != StoppedUnavailable || !rep.Committed() || rep.Embedded == 0 {
		t.Fatalf("committed unavailable run: %+v", rep)
	}
	if _, err := w.ix.Ensure(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if list := m.List(); len(list) != 2 {
		t.Fatalf("a committed unavailable run backed off: %d jobs, want 2", len(list))
	} else {
		waitJob(t, m, list[1])
	}
}

// A provider-wide rate limit is temporary: it must not arm the long wait.
func TestATransientStopDoesNotBackOff(t *testing.T) {
	w, _, m := newRetryWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusTooManyRequests)
	_, rep := ensureStartsOne(t, w, m)
	if rep.StoppedBy != StoppedTransient {
		t.Fatalf("transient stop: %+v", rep)
	}
	if _, again := ensureStartsOne(t, w, m); again.StoppedBy != StoppedTransient {
		t.Fatalf("the next reconcile: %+v", again)
	}
}

func TestASettingsChangeEndsTheUnavailableBackoff(t *testing.T) {
	w, clk, m := newRetryWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	stop, err := w.ix.AfterChanges(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, rep := ensureStartsOne(t, w, m)

	clk.Advance(2 * time.Minute)
	ensureDoesNotStart(t, w, m, rep.Before)

	events.Publish(w.bus, config.TopicChanged, config.Change{Keys: []string{"log.level"}})
	waitRetryClear(t, w.ix, w.ix.retryKeyFor(rep.Space, rep.TextVersion))
	ensureStartsOne(t, w, m)
}

func TestASuccessfulEmbedderCallEndsTheUnavailableBackoff(t *testing.T) {
	w, clk, m := newRetryWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	stop, err := w.ix.AfterChanges(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, rep := ensureStartsOne(t, w, m)
	key := w.ix.retryKeyFor(rep.Space, rep.TextVersion)

	clk.Advance(2 * time.Minute)
	events.Publish(w.bus, engine.TopicProviderHealth, engine.ProviderStatus{Provider: "someone-else", Healthy: true})
	time.Sleep(50 * time.Millisecond)
	ensureDoesNotStart(t, w, m, rep.Before)

	events.Publish(w.bus, engine.TopicProviderHealth, engine.ProviderStatus{Provider: w.cfg.String("embed.provider"), Healthy: true})
	waitRetryClear(t, w.ix, key)
	ensureStartsOne(t, w, m)
}

func TestResetUnavailableLetsTheNextReconcileProbe(t *testing.T) {
	w, clk, m := newRetryWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	_, rep := ensureStartsOne(t, w, m)

	clk.Advance(2 * time.Minute)
	ensureDoesNotStart(t, w, m, rep.Before)

	w.ix.ResetUnavailable()
	ensureStartsOne(t, w, m)
}

// A change of the work a backoff was armed for makes the cached coverage
// stale: every one of these must probe at the next reconcile.
func TestMailboxAndSpaceChangesEndTheUnavailableBackoff(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, w *world)
		change func(t *testing.T, w *world)
	}{
		{"new mail", nil, func(t *testing.T, w *world) {
			w.mail("new", mail.RoleInbox, 5, "new mail")
		}},
		{"text version", nil, func(t *testing.T, w *world) {
			w.ix.textVersion = func() string { return "another-text-version" }
		}},
		{"space", nil, func(t *testing.T, w *world) {
			if err := w.cfg.Set("embed.dimensions", 256); err != nil {
				t.Fatal(err)
			}
		}},
		{"folder roles", func(t *testing.T, w *world) {
			w.mail("bin", mail.RoleDeleted, 1, "deleted mail waits for its role")
		}, func(t *testing.T, w *world) {
			if err := w.cfg.Set("index.folder_roles", []string{"inbox", "sent", "archive", "other", "deleted"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"quarantine threshold", nil, func(t *testing.T, w *world) {
			if err := w.cfg.Set("index.quarantine_after", 3); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, clk, m := newRetryWorld(t, nil)
			w.mail("a", mail.RoleInbox, 1, "alpha")
			if tc.setup != nil {
				tc.setup(t, w)
			}
			w.fake.Fake.SetStatus(http.StatusUnauthorized)
			_, rep := ensureStartsOne(t, w, m)

			clk.Advance(2 * time.Minute)
			ensureDoesNotStart(t, w, m, rep.Before)

			tc.change(t, w)
			ensureStartsOne(t, w, m)
		})
	}
}

func TestTheBackoffExpiresAndRecovers(t *testing.T) {
	w, clk, m := newRetryWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	_, rep := ensureStartsOne(t, w, m)

	clk.Advance(15*time.Minute - time.Second)
	ensureDoesNotStart(t, w, m, rep.Before)

	w.fake.Fake.SetStatus(0)
	clk.Advance(2 * time.Second)
	_, recovered := ensureStartsOne(t, w, m)
	if recovered.State != runstate.Succeeded || recovered.Embedded != 1 {
		t.Fatalf("recovery run: %+v", recovered)
	}
	if st := w.stageStatus(); st.State != learning.Done {
		t.Fatalf("stage after recovery: %+v", st)
	}
	if list := m.List(); len(list) != 2 {
		t.Fatalf("recovery left %d jobs, want 2", len(list))
	}
	// A complete index starts nothing and keeps no wait.
	if _, err := w.ix.Ensure(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if list := m.List(); len(list) != 2 {
		t.Fatalf("a complete index started %d extra job(s)", len(list)-2)
	}
}

// Round 26's retention plus Round 27's wait must not hide the reason: the
// latest unavailable run stays the newest row and the stage keeps reporting
// its blocker while the automatic reconcile is silent.
func TestTheUnavailableBlockerStaysVisibleWhileBackedOff(t *testing.T) {
	w, clk, m := newRetryWorld(t, map[string]any{"index.runs.keep": 1})
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	entry, rep := ensureStartsOne(t, w, m)
	if rep.StoppedBy != StoppedUnavailable || rep.Error == "" {
		t.Fatalf("unavailable run: %+v", rep)
	}

	clk.Advance(2 * time.Minute)
	ensureDoesNotStart(t, w, m, rep.Before)

	runs, err := w.st.ListIndexRuns(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != entry.ID {
		t.Fatalf("the blocker row changed while backed off: %+v (want run %d)", runs, entry.ID)
	}
	st := w.stageStatus()
	if st.State != learning.Blocked || len(st.Blockers) != 1 || st.Blockers[0].Code != BlockNoEmbedder {
		t.Fatalf("stage while backed off: %+v", st)
	}
	if st.Blockers[0].Detail != rep.Error {
		t.Fatalf("stage detail %q, want the latest unavailable error %q", st.Blockers[0].Detail, rep.Error)
	}
}

// Option A: a run that committed no vector and stopped unavailable reuses the
// coverage it started from instead of planning again. The plan would now
// fold this run's recorded failures into the quarantine count, reporting a
// write-shaped change that the run never made.
func TestAnUnavailableRunReusesItsStartingCoverage(t *testing.T) {
	w, clk, m := newRetryWorld(t, map[string]any{"index.quarantine_after": 1, "embed.batch_size": 1})
	w.mail("a", mail.RoleInbox, 2, "alpha")
	w.mail("b", mail.RoleInbox, 1, "beta")
	emb := &scriptedEmbedder{space: "scripted/1/8", dims: 8, embed: func(call int, texts []engine.Text) (engine.Embeddings, error) {
		if call == 1 {
			return engine.Embeddings{}, fmt.Errorf("this input is refused")
		}
		return engine.Embeddings{}, engine.ErrUnavailable
	}}
	w.ix = New(w.cfg, w.st, w.bus, splitSource{e: emb}, clk)

	_, rep := ensureStartsOne(t, w, m)
	if rep.StoppedBy != StoppedUnavailable || rep.Committed() || rep.Quarantined != 1 {
		t.Fatalf("unavailable run after a recorded failure: %+v", rep)
	}
	if !reflect.DeepEqual(rep.After, rep.Before) {
		t.Fatalf("the finishing run planned again: after %+v, before %+v", rep.After, rep.Before)
	}
	if rep.After.Quarantined != 0 {
		t.Fatalf("starting coverage reported quarantine %d, want 0", rep.After.Quarantined)
	}
	// The run's own failure record is the only one; the silent tick must not
	// try the mail again and add another attempt.
	clk.Advance(2 * time.Minute)
	ensureDoesNotStart(t, w, m, rep.Before)
	if got := emb.Calls(); got != 2 {
		t.Fatalf("the backed-off tick made %d extra embedding calls", got-2)
	}
}

// scriptedEmbedder is a direct Embedder whose calls are scripted by number.
type scriptedEmbedder struct {
	mu    sync.Mutex
	calls int
	space string
	dims  int
	embed func(call int, texts []engine.Text) (engine.Embeddings, error)
}

func (e *scriptedEmbedder) Name() string               { return "scripted" }
func (e *scriptedEmbedder) Model() string              { return "scripted-1" }
func (e *scriptedEmbedder) Space() string              { return e.space }
func (e *scriptedEmbedder) Dimensions() int            { return e.dims }
func (e *scriptedEmbedder) Input(t engine.Text) string { return t.Title + "\n" + t.Body }

func (e *scriptedEmbedder) Embed(_ context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	return e.embed(call, texts)
}

func (e *scriptedEmbedder) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func unitVectors(n, dims int) engine.Embeddings {
	out := engine.Embeddings{Vectors: make([][]float32, n)}
	for i := range out.Vectors {
		v := make([]float32, dims)
		v[0] = 1
		out.Vectors[i] = v
	}
	return out
}
