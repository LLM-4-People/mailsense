package triage

import (
	"context"
	"database/sql"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// lockStoreWrites holds the database's write lock on its own connection
// until the returned function runs.
func lockStoreWrites(t *testing.T, cfg *config.Config) func() {
	t.Helper()
	db, err := sql.Open("sqlite", "file://"+(&url.URL{Path: store.Path(cfg)}).EscapedPath())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
			_ = db.Close()
		})
	}
}

type runResult struct {
	rep Report
	err error
}

// blockedRun starts a run whose provider call waits, and returns the
// channels needed to release the call and collect the run.
func blockedRun(t *testing.T, w *world) (<-chan struct{}, chan struct{}, <-chan runResult) {
	t.Helper()
	block, entered := make(chan struct{}), make(chan struct{})
	w.dec.block, w.dec.entered = block, entered
	done := make(chan runResult, 1)
	go func() {
		rep, err := w.tr.Run(t.Context(), Options{}, nil)
		done <- runResult{rep: rep, err: err}
	}()
	return entered, block, done
}

// A paid decision is not lost when another writer holds the database past
// store.busy_timeout: the store write retries until the lock is released.
// When the lock outlives the decision write's budget, the message is
// recorded as failed and stays pending instead of being marked decided.
func TestTriageDoesNotLoseADecisionOnBusy(t *testing.T) {
	t.Run("released inside the budget", func(t *testing.T) {
		clk := clocktest.New(time.Now())
		w := newWorldClock(t, map[string]any{
			"store.busy_timeout":        "20ms",
			"index.store_busy_attempts": 10,
			"index.store_busy_delay":    "20ms",
		}, clk)
		id := w.mail("client@example.org", "A", "Budget", 0, 0)
		entered, block, done := blockedRun(t, w)
		<-entered
		release := lockStoreWrites(t, w.cfg)
		defer release()
		// The lock is released after the decision write's first busy
		// attempt: the retry then succeeds inside the attempt budget.
		clk.OnSleep = func(context.Context, time.Duration) error {
			release()
			return nil
		}
		close(block)

		res := <-done
		if res.err != nil || res.rep.Decided != 1 || res.rep.Failed != 0 {
			t.Fatalf("run: %+v %v", res.rep, res.err)
		}
		decisions, err := w.st.LoadDecisions(t.Context(), res.rep.Plan.Key)
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 1 || decisions[id].Answers == "" {
			t.Fatalf("decisions %v", decisions)
		}
		failures, err := w.st.LoadFailures(t.Context(), res.rep.Plan.Key)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 0 {
			t.Fatalf("failures %v", failures)
		}
		if calls := w.dec.count(); calls != res.rep.Plan.Estimate.Calls {
			t.Fatalf("the paid answer was requested %d times, planned %d", calls, res.rep.Plan.Estimate.Calls)
		}
	})

	t.Run("recorded as pending after the budget", func(t *testing.T) {
		clk := clocktest.New(time.Now())
		w := newWorldClock(t, map[string]any{
			"store.busy_timeout":        "20ms",
			"index.store_busy_attempts": 2,
			"index.store_busy_delay":    "100ms",
		}, clk)
		id := w.mail("client@example.org", "B", "Budget", 0, 0)
		entered, block, done := blockedRun(t, w)
		<-entered
		release := lockStoreWrites(t, w.cfg)
		defer release()
		// Sleep 1 follows the decision's first busy attempt, sleep 2 the
		// failure's first. Releasing there makes the decision exhaust its
		// last attempt while the failure write's last attempt succeeds.
		sleeps := 0
		clk.OnSleep = func(context.Context, time.Duration) error {
			sleeps++
			if sleeps == 2 {
				release()
			}
			return nil
		}
		close(block)

		res := <-done
		decisions, err := w.st.LoadDecisions(t.Context(), res.rep.Plan.Key)
		if err != nil {
			t.Fatal(err)
		}
		if len(decisions) != 0 {
			t.Fatalf("a busy decision was marked decided: %v", decisions)
		}
		failures, err := w.st.LoadFailures(t.Context(), res.rep.Plan.Key)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := failures[id]; !ok || len(failures) != 1 {
			t.Fatalf("failures %v", failures)
		}
		if res.rep.Decided != 0 || res.rep.Failed != 1 {
			t.Fatalf("run: %+v", res.rep)
		}

		// The failure leaves the message pending: the next run decides it.
		clk.OnSleep = nil
		w.dec.block, w.dec.entered = nil, nil
		rep := w.run(Options{})
		if rep.Decided != 1 {
			t.Fatalf("the pending message was not decided on the next run: %+v", rep)
		}
		decisions, err = w.st.LoadDecisions(t.Context(), rep.Plan.Key)
		if err != nil || len(decisions) != 1 {
			t.Fatalf("decisions %v: %v", decisions, err)
		}
		failures, err = w.st.LoadFailures(t.Context(), rep.Plan.Key)
		if err != nil || len(failures) != 0 {
			t.Fatalf("failures %v: %v", failures, err)
		}
	})
}
