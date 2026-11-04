package index

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// scans is the number of state reads this indexer performed.
func scans(w *world) uint64 { return w.ix.planScans.Load() }

// planned runs the planner (not Ensure) for the current space: a test can
// change inputs without a background run racing the assertions.
func planned(t *testing.T, w *world) ([]store.EmbeddingState, Coverage) {
	t.Helper()
	e, err := w.ix.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	work, cov, err := w.ix.plan(t.Context(), e.Space(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return work, cov
}

// An unchanged, fully covered mailbox is reconciled without reading the
// states again: N Ensure calls perform one plan (the first) and none after.
func TestEnsureReusesThePlanWhileNothingChanged(t *testing.T) {
	w := newWorld(t, nil)
	w.mails(20)
	w.indexAll()
	start := scans(w)
	for i := range 5 {
		cov, err := w.ix.Ensure(t.Context(), nil)
		if err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
		if cov.Eligible != 20 || cov.Indexed != 20 || cov.Pending != 0 || cov.Stale != 0 {
			t.Fatalf("ensure %d coverage: %+v", i, cov)
		}
	}
	if got := scans(w) - start; got != 1 {
		t.Fatalf("5 reconciles over an unchanged mailbox read the states %d times, want 1", got)
	}
}

// Five concurrent Ensure calls coalesce on the planning slot: one state
// read, not one per caller, and every caller gets the same complete plan.
func TestConcurrentEnsurePlansOnce(t *testing.T) {
	w := newWorld(t, nil)
	w.mails(200)
	w.indexAll()
	start := scans(w)
	const callers = 5
	begin := make(chan struct{})
	results := make(chan Coverage, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			cov, err := w.ix.Ensure(t.Context(), nil)
			results <- cov
			errs <- err
		}()
	}
	close(begin)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for cov := range results {
		if cov.Eligible != 200 || cov.Indexed != 200 {
			t.Fatalf("a caller saw a partial plan: %+v", cov)
		}
	}
	if got := scans(w) - start; got != 1 {
		t.Fatalf("5 concurrent Ensure calls read the states %d times, want 1", got)
	}
}

// Every input the plan key names must force a replan; the assertions are on
// the counts the change produces, so a stale cache hit fails instead of
// passing.
func TestPlanCacheReplansOnEveryInput(t *testing.T) {
	t.Run("mail write", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		planned(t, w)
		before := scans(w)
		w.mail("b", mail.RoleInbox, 2, "beta")
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a mail write caused %d state reads, want 1", got)
		}
		if cov.Eligible != 2 || cov.Pending != 2 {
			t.Fatalf("a mail write did not replan: %+v", cov)
		}
	})

	t.Run("mail write through Ensure", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		w.indexAll()
		if _, err := w.ix.Ensure(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		m := jobs.NewManager(w.cfg, w.bus, nil)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = m.Shutdown(ctx)
		})
		w.mail("b", mail.RoleInbox, 2, "beta")
		before := scans(w)
		cov, err := w.ix.Ensure(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if got := scans(w) - before; got != 1 {
			t.Fatalf("Ensure after a mail write read the states %d times, want 1", got)
		}
		if cov.Eligible != 2 || cov.Pending != 1 {
			t.Fatalf("Ensure after a mail write: %+v", cov)
		}
		list := m.List()
		if len(list) != 1 {
			t.Fatalf("Ensure started %d jobs, want 1", len(list))
		}
		if info := waitJob(t, m, list[0]); info.Status != runstate.Succeeded || info.Progress.Done != 1 {
			t.Fatalf("catch-up job: %+v", info)
		}
	})

	t.Run("vector save", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if _, cov := planned(t, w); cov.Pending != 1 {
			t.Fatalf("no vector yet: %+v", cov)
		}
		before := scans(w)
		w.indexAll()
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a vector save caused %d state reads, want 1", got)
		}
		if cov.Indexed != 1 || cov.Pending != 0 {
			t.Fatalf("a vector save did not replan: %+v", cov)
		}
	})

	t.Run("vector confirmation", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		w.indexAll()
		// A read-flag write moves the revision without changing the text.
		m, err := w.st.GetMessage(t.Context(), "src", "store-1", "a")
		if err != nil {
			t.Fatal(err)
		}
		m.IsRead = false
		w.write(m)
		if _, cov := planned(t, w); cov.Stale != 1 {
			t.Fatalf("changed message before confirmation: %+v", cov)
		}
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		before := scans(w)
		if err := w.st.ConfirmEmbeddings(t.Context(), map[int64]int64{states[0].RowID: states[0].Revision}, w.ix.textVersion()); err != nil {
			t.Fatal(err)
		}
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a vector confirmation caused %d state reads, want 1", got)
		}
		if cov.Indexed != 1 || cov.Stale != 0 {
			t.Fatalf("a vector confirmation did not replan: %+v", cov)
		}
	})

	t.Run("failure record", func(t *testing.T) {
		w := newWorld(t, map[string]any{SettingQuarantineAfter: 1})
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if _, cov := planned(t, w); cov.Quarantined != 0 {
			t.Fatalf("no failure yet: %+v", cov)
		}
		states, err := w.st.EmbeddingStates(t.Context(), nil)
		if err != nil || len(states) != 1 {
			t.Fatalf("states: %+v %v", states, err)
		}
		before := scans(w)
		if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{{
			MessageID: states[0].RowID, Revision: states[0].Revision, Error: "boom",
		}}); err != nil {
			t.Fatal(err)
		}
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a failure record caused %d state reads, want 1", got)
		}
		if cov.Quarantined != 1 {
			t.Fatalf("a failure record did not replan: %+v", cov)
		}
	})

	t.Run("text version", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		w.indexAll()
		if _, cov := planned(t, w); cov.Indexed != 1 {
			t.Fatalf("indexed: %+v", cov)
		}
		before := scans(w)
		w.ix.textVersion = func() string { return "another-version" }
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a text-version change caused %d state reads, want 1", got)
		}
		if cov.Stale != 1 || cov.Indexed != 0 {
			t.Fatalf("a text-version change did not replan: %+v", cov)
		}
	})

	t.Run("covered roles", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("in", mail.RoleInbox, 1, "inbox")
		w.mail("sent", mail.RoleSent, 2, "sent")
		if _, cov := planned(t, w); cov.Eligible != 2 {
			t.Fatalf("both roles: %+v", cov)
		}
		before := scans(w)
		if err := w.cfg.Set("index.folder_roles", []string{"inbox"}); err != nil {
			t.Fatal(err)
		}
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a roles change caused %d state reads, want 1", got)
		}
		if cov.Eligible != 1 {
			t.Fatalf("a roles change did not replan: %+v", cov)
		}
	})

	t.Run("selected stores", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		if _, cov := planned(t, w); cov.Eligible != 1 {
			t.Fatalf("picked store: %+v", cov)
		}
		before := scans(w)
		if err := w.cfg.Set(source.SettingSelected, []string{source.StoreKey("src", "store-2")}); err != nil {
			t.Fatal(err)
		}
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a selection change caused %d state reads, want 1", got)
		}
		if cov.Eligible != 0 {
			t.Fatalf("a selection change did not replan: %+v", cov)
		}
	})

	t.Run("vector space", func(t *testing.T) {
		w := newWorld(t, nil)
		w.mail("a", mail.RoleInbox, 1, "alpha")
		_, cov := planned(t, w)
		before := scans(w)
		if err := w.cfg.Set("embed.dimensions", 256); err != nil {
			t.Fatal(err)
		}
		_, next := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a space change caused %d state reads, want 1", got)
		}
		if next.Space == cov.Space || next.Pending != 1 {
			t.Fatalf("a space change did not replan: %+v -> %+v", cov, next)
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
		if _, cov := planned(t, w); cov.Quarantined != 0 {
			t.Fatalf("below the default threshold: %+v", cov)
		}
		before := scans(w)
		if err := w.cfg.Set(SettingQuarantineAfter, 1); err != nil {
			t.Fatal(err)
		}
		_, cov := planned(t, w)
		if got := scans(w) - before; got != 1 {
			t.Fatalf("a quarantine change caused %d state reads, want 1", got)
		}
		if cov.Quarantined != 1 {
			t.Fatalf("a quarantine change did not replan: %+v", cov)
		}
	})
}

// A coverage-only entry (Stage/Coverage read) cannot answer which messages
// need work: the next plan must read the states and publish the full entry,
// and only then does a repeated plan reuse it.
func TestCoverageOnlyEntryDoesNotHideTheWorkList(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.mail("b", mail.RoleInbox, 2, "beta")
	cov, err := w.ix.Coverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cov.Pending != 2 {
		t.Fatalf("coverage: %+v", cov)
	}
	start := scans(w)
	work, planCov := planned(t, w)
	if got := scans(w) - start; got != 1 {
		t.Fatalf("a plan over a coverage-only entry read the states %d times, want 1", got)
	}
	if len(work) != 2 || planCov.Counts != cov.Counts {
		t.Fatalf("the plan lost the work list: %d work, %+v", len(work), planCov)
	}
	start = scans(w)
	if again, _ := planned(t, w); len(again) != 2 {
		t.Fatalf("the full entry did not serve the work list: %d", len(again))
	}
	if got := scans(w) - start; got != 0 {
		t.Fatalf("a repeated plan read the states %d times, want 0", got)
	}
}

// A cached plan always holds the unrestricted work list, so a run limited
// to some stores filters it exactly as the scan would have, and the
// unpicked-store validation still runs before the cache.
func TestPlanCacheServesRestrictedWorkFromTheFullPlan(t *testing.T) {
	other := source.StoreKey("src", "store-2")
	w := newWorld(t, map[string]any{source.SettingSelected: []any{pickedKey, other}})
	w.mail("a", mail.RoleInbox, 1, "alpha")
	second := mailtest.Message("src", "b")
	second.StoreID, second.FolderRole, second.FolderPath, second.Body = "store-2", mail.RoleInbox, string(mail.RoleInbox), "beta"
	w.write(second)
	e, err := w.ix.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	work, cov, err := w.ix.plan(t.Context(), e.Space(), Options{})
	if err != nil || len(work) != 2 || cov.Eligible != 2 {
		t.Fatalf("unrestricted plan: %d %+v %v", len(work), cov, err)
	}
	start := scans(w)
	work, planCov, err := w.ix.plan(t.Context(), e.Space(), Options{Stores: []string{other}})
	if err != nil {
		t.Fatal(err)
	}
	if got := scans(w) - start; got != 0 {
		t.Fatalf("a restricted plan read the states %d times, want 0 (cache hit)", got)
	}
	if len(work) != 1 || source.StoreKey(work[0].SourceID, work[0].StoreID) != other || planCov.Eligible != 2 {
		t.Fatalf("restricted plan: %d %+v", len(work), planCov)
	}
	if _, _, err := w.ix.plan(t.Context(), e.Space(), Options{Stores: []string{"src/not-picked"}}); err == nil {
		t.Fatal("a plan for an unpicked store was accepted from the cache")
	}
	if got := scans(w) - start; got != 0 {
		t.Fatalf("the unpicked-store rejection read the states %d times, want 0", got)
	}
}

// A run in progress owns the work: repeated reconciles must not plan again
// (the run is writing) or start a second run, and the stage must report the
// live progress of that run, not the cached starting coverage.
func TestEnsureDuringARunDoesNotPlanOrStartAgain(t *testing.T) {
	emb := &switchEmbedder{e: testEmbedder{name: "perf/space/8"}}
	w := newRunWorld(t, emb)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.mail("b", mail.RoleInbox, 2, "beta")
	w.run()
	w.mail("c", mail.RoleInbox, 3, "gamma")
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})

	block, began := make(chan struct{}), make(chan struct{})
	var once sync.Once
	emb.set(testEmbedder{name: "perf/space/8", block: block, once: &once, began: began})
	cov, err := w.ix.Ensure(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Eligible != 3 || cov.Indexed != 2 || cov.Pending != 1 {
		t.Fatalf("coverage before the catch-up: %+v", cov)
	}
	<-began // the run planned, published its live state and is embedding
	before := scans(w)
	for i := range 3 {
		got, err := w.ix.Ensure(t.Context(), m)
		if err != nil {
			t.Fatalf("ensure %d during the run: %v", i, err)
		}
		if got.Eligible != 3 || got.Indexed != 2 || got.Pending != 1 {
			t.Fatalf("ensure %d during the run returned %+v, want the run's starting coverage", i, got)
		}
	}
	if got := scans(w) - before; got != 0 {
		t.Fatalf("Ensure planned %d times while a run was writing, want 0", got)
	}
	if list := m.List(); len(list) != 1 {
		t.Fatalf("Ensure started %d runs while one was live, want 1", len(list))
	}
	st := w.ix.Stage().Status
	live, err := st(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if live.State != learning.Running || live.Progress.Total != 1 || live.Progress.Done != 0 {
		t.Fatalf("the stage masked the running job with the cached plan: %+v", live)
	}

	close(block)
	if info := waitJob(t, m, m.List()[0]); info.Status != runstate.Succeeded {
		t.Fatalf("catch-up job: %+v", info)
	}
	final, err := st(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if final.State != learning.Done || final.Progress.Done != 3 || final.Progress.Total != 3 {
		t.Fatalf("the stage after the run: %+v", final)
	}
}

// A failed plan is never published: the entry stays on the previous key, so
// the same unchanged inputs are still served from the cache and the failed
// key is recomputed when it is asked for again.
func TestAFailedPlanIsNotCached(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.indexAll()
	_, cov := planned(t, w)
	e, err := w.ix.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	oldKey := w.ix.keyFor(w.ix.coverageInput(cov.Space))
	version := w.ix.textVersion
	w.ix.textVersion = func() string { return "closed-store" }
	if err := w.st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.ix.plan(t.Context(), e.Space(), Options{}); err == nil {
		t.Fatal("a plan over a closed store succeeded")
	}
	w.ix.planMu.Lock()
	entry := w.ix.planCache
	w.ix.planMu.Unlock()
	if entry == nil || entry.key != oldKey {
		t.Fatalf("the failed plan replaced the cache: %+v (want key %+v)", entry, oldKey)
	}
	// Restoring the inputs makes the old key current again: it is served
	// without touching the closed store.
	w.ix.textVersion = version
	before := scans(w)
	_, again := planned(t, w)
	if got := scans(w) - before; got != 0 {
		t.Fatalf("the unchanged plan was not reused after a failed plan: %d reads", got)
	}
	if again.Counts != cov.Counts {
		t.Fatalf("reused coverage differs: %+v vs %+v", again, cov)
	}
}

// A packed mailbox (indexed, stale, partial and quarantined mail in several
// roles, plus mail of an unpicked store) must produce the same coverage and
// work list from the cache as from a cold planner, and the stage and the
// run must report those same counts.
func TestPlanAndCoverageParityOnAPackedMailbox(t *testing.T) {
	w := newWorld(t, map[string]any{SettingQuarantineAfter: 2, "embed.batch_size": 50})
	w.mails(100)
	for i := range 10 {
		w.mail(fmt.Sprintf("sent%02d", i), mail.RoleSent, 200+i, fmt.Sprintf("sent %d", i))
	}
	for i := range 5 {
		w.mail(fmt.Sprintf("arch%02d", i), mail.RoleArchive, 300+i, fmt.Sprintf("archive %d", i))
	}
	unpicked := mailtest.Message("src", "elsewhere")
	unpicked.StoreID, unpicked.Body = "store-2", "mail of a store that is not picked"
	w.write(unpicked)
	w.indexAll()
	// Six messages change without a text change: five stale, three of them
	// quarantined at this revision (they overlap by two).
	for i := range 6 {
		w.mail(fmt.Sprintf("m%05d", i), mail.RoleInbox, i, fmt.Sprintf("text %d", i))
	}
	states, err := w.st.EmbeddingStates(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, st := range states {
		if failed == 3 {
			break
		}
		if st.Space == "" || st.Checked == st.Revision {
			continue // only a stale message can be quarantined
		}
		if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{
			{MessageID: st.RowID, Revision: st.Revision, Error: "boom"},
			{MessageID: st.RowID, Revision: st.Revision, Error: "boom again"},
		}); err != nil {
			t.Fatal(err)
		}
		failed++
	}
	// Four messages wait for details: they count as pending but can never
	// receive a vector now.
	for i := range 4 {
		p := mailtest.Message("src", fmt.Sprintf("partial%02d", i))
		p.Partial, p.Body = true, "preview only"
		p.FolderRole, p.FolderPath = mail.RoleInbox, string(mail.RoleInbox)
		p.ReceivedAt = fmt.Sprintf("2026-02-01T0%d:00:00Z", i)
		w.write(p)
	}

	e, err := w.ix.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	warmWork, warmCov, err := w.ix.plan(t.Context(), e.Space(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cold := New(w.cfg, w.st, w.bus, w.ix.src, nil)
	coldWork, coldCov, err := cold.plan(t.Context(), e.Space(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(warmWork, coldWork) {
		t.Fatalf("cached work list differs from a cold plan: %d vs %d", len(warmWork), len(coldWork))
	}
	if !reflect.DeepEqual(warmCov, coldCov) {
		t.Fatalf("cached coverage differs from a cold plan:\n%+v\n%+v", warmCov, coldCov)
	}
	var sum Counts
	for _, g := range warmCov.Groups {
		sum.Eligible += g.Eligible
		sum.Indexed += g.Indexed
		sum.Stale += g.Stale
		sum.Pending += g.Pending
		sum.Partial += g.Partial
		sum.Quarantined += g.Quarantined
	}
	if sum != warmCov.Counts {
		t.Fatalf("group counts do not add up: %+v vs %+v", sum, warmCov.Counts)
	}
	if warmCov.Eligible != 119 || warmCov.Indexed != 109 || warmCov.Stale != 6 || warmCov.Pending != 4 ||
		warmCov.Partial != 4 || warmCov.Quarantined != 3 {
		t.Fatalf("packed coverage: %+v (work %d)", warmCov, len(warmWork))
	}
	if len(warmWork) != 3 {
		t.Fatalf("packed work list: %d, want the 3 stale messages below the quarantine threshold", len(warmWork))
	}
	st := w.stageStatus()
	if st.Progress.Done != int64(warmCov.Indexed) || st.Progress.Total != int64(warmCov.Eligible) {
		t.Fatalf("stage progress does not match coverage: %+v", st)
	}
	rep := w.run()
	if !reflect.DeepEqual(rep.Before, warmCov) {
		t.Fatalf("the run did not start from the cached coverage:\n%+v\n%+v", rep.Before, warmCov)
	}
	after, err := New(w.cfg, w.st, w.bus, w.ix.src, nil).Coverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.After, after) {
		t.Fatalf("the run's after coverage differs from a cold read:\n%+v\n%+v", rep.After, after)
	}
	// The three below-threshold stale messages are confirmed; the three
	// quarantined ones stay stale until their revision changes.
	if rep.After.Stale != 3 || rep.After.Indexed != 112 {
		t.Fatalf("after the run: %+v", rep.After)
	}
}
