package index

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// waitJob waits for one background job and returns its final info.
func waitJob(t *testing.T, m *jobs.Manager, info jobs.Info) jobs.Info {
	t.Helper()
	j, ok := m.Get(info.ID)
	if !ok {
		t.Fatalf("job %s disappeared", info.ID)
	}
	select {
	case <-j.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("job %s did not finish", info.ID)
	}
	return j.Info()
}

func TestEnsureStartsOneJobForPendingMail(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	cov, err := w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Eligible != 1 || cov.Pending != 1 || cov.Indexed != 0 || cov.Space == "" {
		t.Fatalf("pending coverage: %+v", cov)
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("one job started, got %d: %+v", len(list), list)
	}
	if info := waitJob(t, m, list[0]); info.Status != runstate.Succeeded || info.Progress.Done != 1 {
		t.Fatalf("job: %+v", info)
	}

	cov, err = w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Eligible != 1 || cov.Indexed != 1 || cov.Pending != 0 || cov.Stale != 0 {
		t.Fatalf("complete coverage: %+v", cov)
	}
	if list = m.List(); len(list) != 1 {
		t.Fatalf("a complete index starts nothing, got %d jobs", len(list))
	}
}

func TestEnsureReportsStaleCoverageAndHealsIt(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.mail("b", mail.RoleInbox, 2, "beta")
	w.run()
	// A changed revision makes the vector stale until the next run confirms
	// that its embedded input did not change.
	w.mail("a", mail.RoleInbox, 3, "alpha")
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	cov, err := w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Eligible != 2 || cov.Indexed != 1 || cov.Stale != 1 {
		t.Fatalf("stale coverage: %+v", cov)
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("one heal job, got %d", len(list))
	}
	if info := waitJob(t, m, list[0]); info.Status != runstate.Succeeded {
		t.Fatalf("heal job: %+v", info)
	}
	cov, err = w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Indexed != 2 || cov.Stale != 0 {
		t.Fatalf("healed coverage: %+v", cov)
	}
}

// A run already in progress is not duplicated: repeated reconciles must leave
// the one running job alone until it ends.
func TestEnsureDoesNotStartASecondJobWhileOneRuns(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.fake.Fake.Hang = true // keeps the first job's embedding call in flight
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	for i := range 3 {
		cov, err := w.ix.Ensure(t.Context(), m)
		if err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		if cov.Pending != 1 {
			t.Fatalf("ensure %d coverage: %+v", i, cov)
		}
	}
	if list := m.List(); len(list) != 1 {
		t.Fatalf("repeated Ensure started %d jobs, want the one running job", len(list))
	}
}

func TestEnsureUnavailableEmbedderIsExplicitAndStartsNothing(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	if err := w.cfg.Set("embed.provider", ""); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	cov, err := w.ix.Ensure(t.Context(), m)
	if !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("unavailable embedder error: %v", err)
	}
	if cov.Space != "" || cov.Eligible != 0 {
		t.Fatalf("no coverage without a space: %+v", cov)
	}
	if list := m.List(); len(list) != 0 {
		t.Fatalf("no job without an embedder: %+v", list)
	}
	if calls := w.fake.Fake.Calls(); calls != 0 {
		t.Fatalf("Ensure made %d provider calls", calls)
	}
}

// Round 26: retention with index.runs.keep=1 must keep the latest run even
// when it stopped because the embedder was unavailable. Stage.Status reads
// that row to explain the blocker, so pruning it would hide why the index is
// stuck.
func TestEnsureKeepsTheLatestUnavailableRunAfterRetention(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "a message the embedder will refuse")
	if err := w.cfg.Set("index.runs.keep", 1); err != nil {
		// The row assertion below is the requirement: without the setting no
		// prune happens either, so the test still fails for the right reason.
		t.Logf("index.runs.keep is not a setting yet: %v", err)
	}
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	var last Report
	for i := range 2 {
		rep, err := w.ix.Run(t.Context(), Options{}, nil)
		if err == nil || rep.StoppedBy != StoppedUnavailable || rep.State != runstate.Failed {
			t.Fatalf("unavailable run %d: %+v %v", i, rep, err)
		}
		last = rep
	}
	runs, err := w.st.ListIndexRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("2 unavailable runs with index.runs.keep=1 left %d rows, want only the latest run (run %d)", len(runs), last.RunID)
	}
	if runs[0].ID != last.RunID {
		t.Fatalf("retention kept run %d, want the latest run (run %d)", runs[0].ID, last.RunID)
	}
	st := w.stageStatus()
	if st.State != learning.Blocked || len(st.Blockers) != 1 || st.Blockers[0].Code != BlockNoEmbedder {
		t.Fatalf("the latest unavailable run still blocks the stage: %+v", st)
	}
	if st.Blockers[0].Detail != last.Error {
		t.Fatalf("stage detail %q, want the latest run's error %q", st.Blockers[0].Detail, last.Error)
	}
	t.Logf("2 unavailable runs with index.runs.keep=1 left %d row (run %d); stage is %s/%s with the latest error",
		len(runs), runs[0].ID, st.State, st.Blockers[0].Code)
}
