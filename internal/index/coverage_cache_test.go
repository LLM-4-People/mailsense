package index

import (
	"context"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// cachedCoverage reads the coverage and proves the next read of the same
// inputs came from the cache: a canceled context cannot reach the database,
// so a miss would fail with context.Canceled.
func cachedCoverage(t *testing.T, w *world) Coverage {
	t.Helper()
	cov, err := w.ix.Coverage(t.Context())
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cached, err := w.ix.Coverage(ctx)
	if err != nil {
		t.Fatalf("repeated coverage was not served from the cache: %v", err)
	}
	if !reflect.DeepEqual(cached, cov) {
		t.Fatalf("cached coverage differs:\n%+v\n%+v", cached, cov)
	}
	return cov
}

// A coverage is reused while nothing it counts changed; each input the key
// names must make the next read recompute. The assertions are on the count
// the change produces, so a stale cache hit fails instead of passing.
func TestCoverageCacheIsInvalidatedByEveryInput(t *testing.T) {
	t.Run("mail write", func(t *testing.T) {
		w := newWorld(t, nil)
		if cov := cachedCoverage(t, w); cov.Eligible != 0 {
			t.Fatalf("empty mailbox: %+v", cov)
		}
		w.mail("a", mail.RoleInbox, 1, "alpha")
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Eligible != 1 {
			t.Fatalf("a mail write did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("detail write", func(t *testing.T) {
		w := newWorld(t, nil)
		partial := mailtest.Message("src", "partial")
		partial.Partial, partial.Body = true, "preview only"
		w.write(partial)
		if cov := cachedCoverage(t, w); cov.Eligible != 1 || cov.Partial != 1 {
			t.Fatalf("partial message: %+v", cov)
		}
		n, err := w.st.ApplyDetails(t.Context(), 0, "src", []store.DetailUpdate{{
			Key:     source.ItemRef{StoreID: "store-1", ID: "partial"},
			Details: mail.Details{Subject: partial.Subject, Body: "the complete body"},
		}})
		if err != nil || n != 1 {
			t.Fatalf("apply details: %d %v", n, err)
		}
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Partial != 0 || cov.Pending != 1 {
			t.Fatalf("a detail write did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("vector save", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		cov := cachedCoverage(t, w)
		if cov.Pending != 1 {
			t.Fatalf("no vector yet: %+v", cov)
		}
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		if err := w.st.SaveEmbeddings(t.Context(), []store.EmbeddingRow{{
			MessageID: states[0].RowID, Space: cov.Space, TextHash: "h", TextVersion: w.ix.textVersion(),
			Revision: states[0].Revision, Vector: vector.Quantize([]float32{1}),
		}}); err != nil {
			t.Fatal(err)
		}
		cov, err = w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Indexed != 1 || cov.Pending != 0 {
			t.Fatalf("a vector save did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("vector confirmation", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if rep := w.run(); rep.After.Indexed != 1 {
			t.Fatalf("first run: %+v", rep)
		}
		// A read-flag write advances the message revision without changing
		// its embedded text: the vector is stale until confirmed.
		m, err := w.st.GetMessage(t.Context(), "src", "store-1", "a")
		if err != nil {
			t.Fatal(err)
		}
		m.IsRead = false
		w.write(m)
		cov := cachedCoverage(t, w)
		if cov.Stale != 1 {
			t.Fatalf("changed message before confirmation: %+v", cov)
		}
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		if err := w.st.ConfirmEmbeddings(t.Context(), map[int64]int64{states[0].RowID: states[0].Revision}, w.ix.textVersion()); err != nil {
			t.Fatal(err)
		}
		cov, err = w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Indexed != 1 || cov.Stale != 0 {
			t.Fatalf("a vector confirmation did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("failure record", func(t *testing.T) {
		w := newWorld(t, map[string]any{SettingQuarantineAfter: 1})
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if cov := cachedCoverage(t, w); cov.Quarantined != 0 {
			t.Fatalf("no failure yet: %+v", cov)
		}
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{{
			MessageID: states[0].RowID, Revision: states[0].Revision, Error: "boom",
		}}); err != nil {
			t.Fatal(err)
		}
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Quarantined != 1 {
			t.Fatalf("a failure record did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("text version", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		w.run()
		if cov := cachedCoverage(t, w); cov.Indexed != 1 {
			t.Fatalf("indexed: %+v", cov)
		}
		w.ix.textVersion = func() string { return "another-version" }
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Stale != 1 || cov.Indexed != 0 {
			t.Fatalf("a text-version change did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("covered roles", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("in", mail.RoleInbox, 1, "inbox")
		w.mail("sent", mail.RoleSent, 2, "sent")
		if cov := cachedCoverage(t, w); cov.Eligible != 2 {
			t.Fatalf("both roles: %+v", cov)
		}
		if err := w.cfg.Set("index.folder_roles", []string{"inbox"}); err != nil {
			t.Fatal(err)
		}
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Eligible != 1 {
			t.Fatalf("a roles change did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("quarantine threshold", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{{
			MessageID: states[0].RowID, Revision: states[0].Revision, Error: "boom",
		}}); err != nil {
			t.Fatal(err)
		}
		if cov := cachedCoverage(t, w); cov.Quarantined != 0 {
			t.Fatalf("below the default threshold: %+v", cov)
		}
		if err := w.cfg.Set(SettingQuarantineAfter, 1); err != nil {
			t.Fatal(err)
		}
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Quarantined != 1 {
			t.Fatalf("a quarantine change did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("selected stores", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if cov := cachedCoverage(t, w); cov.Eligible != 1 {
			t.Fatalf("picked store: %+v", cov)
		}
		if err := w.cfg.Set(source.SettingSelected, []string{source.StoreKey("src", "store-2")}); err != nil {
			t.Fatal(err)
		}
		cov, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cov.Eligible != 0 {
			t.Fatalf("a selection change did not invalidate coverage: %+v", cov)
		}
	})

	t.Run("vector space", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		cov := cachedCoverage(t, w)
		if cov.Pending != 1 {
			t.Fatalf("no vector yet: %+v", cov)
		}
		if err := w.cfg.Set("embed.dimensions", 256); err != nil {
			t.Fatal(err)
		}
		next, err := w.ix.Coverage(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if next.Space == cov.Space || next.Pending != 1 {
			t.Fatalf("a space change did not invalidate coverage: %+v -> %+v", cov, next)
		}
	})
}

// Stage.Status shares the coverage cache: a status refresh with unchanged
// inputs must not plan the mailbox again.
func TestStageStatusUsesTheCoverageCache(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	if _, err := w.ix.Coverage(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.ix.planMu.Lock()
	if w.ix.planCache == nil {
		w.ix.planMu.Unlock()
		t.Fatal("coverage read did not populate the cache")
	}
	w.ix.planCache.cov.Counts.Eligible = 7
	w.ix.planCache.cov.Counts.Indexed = 5
	w.ix.planMu.Unlock()
	st := w.stageStatus()
	if st.Progress.Total != 7 || st.Progress.Done != 5 {
		t.Fatalf("status replanned instead of using the cached coverage: %+v", st)
	}
}
