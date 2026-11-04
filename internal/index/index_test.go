package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

type world struct {
	t    testing.TB
	cfg  *config.Config
	st   *store.Store
	bus  *events.Bus
	fake *embedtest.Server
	ix   *Indexer
}

// pickedKey is the key of the test store ("src", "store-1").
var pickedKey = source.StoreKey("src", "store-1")

func newWorld(t testing.TB, overrides map[string]any) *world {
	t.Helper()
	w := &world{t: t, bus: events.New()}
	t.Cleanup(w.bus.Close)
	base := map[string]any{"embed.batch_size": 2, "http.retry.max_attempts": 1, source.SettingSelected: []any{pickedKey}}
	for k, v := range overrides {
		base[k] = v
	}
	w.cfg = configtest.New(t, base)
	st, err := store.Open(t.Context(), w.cfg, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w.st = st
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	w.fake = embedtest.New(t, &embedtest.Fake{})
	embedtest.Install(t, w.cfg, w.fake.URL)
	sec, _ := secrets.Open(w.cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: w.cfg, Bus: w.bus, Secrets: sec, Locator: embedtest.Locator{URL: w.fake.URL}})
	w.ix = New(w.cfg, st, w.bus, src, nil)
	return w
}

// newManagedWorld is newWorld with the managed-runtime record. A
// llama-server 500 is its oversize-input answer, so these tests keep the
// message-by-message split; a remote provider's 500 is provider-wide.
func newManagedWorld(t *testing.T, overrides map[string]any) *world {
	t.Helper()
	w := newWorld(t, overrides)
	embedtest.InstallRuntime(t, w.cfg, "llama-server", "embeddinggemma-2")
	return w
}

// mail writes a message; hours orders them (larger = newer).
func (w *world) mail(id string, role mail.FolderRole, hours int, body string) {
	w.t.Helper()
	m := mailtest.Message("src", id)
	m.FolderRole, m.FolderPath, m.Body, m.SentOn = role, string(role), body, ""
	m.ReceivedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(hours) * time.Hour).Format(mail.TimeLayout)
	w.write(m)
}

func (w *world) write(m mail.Message) {
	w.t.Helper()
	if err := w.st.UpsertMessages(w.t.Context(), 0, []mail.Message{m}); err != nil {
		w.t.Fatal(err)
	}
}

// mails writes n inbox messages in one batch, oldest first (hours order
// them, larger = newer): the fixture of a realistic mailbox without one
// transaction per message.
func (w *world) mails(n int) {
	w.t.Helper()
	msgs := make([]mail.Message, n)
	for i := range msgs {
		m := mailtest.Message("src", fmt.Sprintf("m%05d", i))
		m.FolderRole, m.FolderPath, m.Body, m.SentOn = mail.RoleInbox, string(mail.RoleInbox), fmt.Sprintf("text %d", i), ""
		m.ReceivedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(mail.TimeLayout)
		msgs[i] = m
	}
	if err := w.st.UpsertMessages(w.t.Context(), 0, msgs); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) run() Report {
	w.t.Helper()
	rep, err := w.ix.Run(w.t.Context(), Options{}, nil)
	if err != nil {
		w.t.Fatalf("run: %v (%+v)", err, rep)
	}
	return rep
}

// indexAll stores a vector of the current space for every visible message
// without the embedder: a fully covered mailbox for tests and benchmarks
// that exercise the planner rather than embedding.
func (w *world) indexAll() {
	w.t.Helper()
	e, err := w.ix.src.Embedder()
	if err != nil {
		w.t.Fatal(err)
	}
	states, err := w.st.EmbeddingStates(w.t.Context(), nil)
	if err != nil {
		w.t.Fatal(err)
	}
	rows := make([]store.EmbeddingRow, len(states))
	for i, s := range states {
		rows[i] = store.EmbeddingRow{MessageID: s.RowID, Space: e.Space(), TextHash: "cached",
			TextVersion: w.ix.textVersion(), Revision: s.Revision, Vector: vector.Quantize([]float32{1})}
	}
	if err := w.st.SaveEmbeddings(w.t.Context(), rows); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) stageStatus() learning.StageStatus {
	w.t.Helper()
	st, err := w.ix.Stage().Status(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// A partial message stays in the UI's Coverage but never enters the
// semantic coverage target: it cannot receive a vector until its details
// arrive.
func TestEligibleExcludesPartialMail(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("complete", mail.RoleInbox, 1, "complete")
	partial := mailtest.Message("src", "partial")
	partial.Partial, partial.Body = true, "preview only"
	w.write(partial)

	ids, err := w.ix.Eligible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("eligible ids %v, want only the complete message", ids)
	}
	cov, err := w.ix.Coverage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if cov.Eligible != 2 || cov.Partial != 1 || cov.Pending != 2 {
		t.Fatalf("coverage lost the partial message: %+v", cov)
	}
}

func TestFirstRunEmbedsEligibleMailNewestFirst(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("old", mail.RoleInbox, 1, "the oldest message")
	w.mail("new", mail.RoleSent, 30, "the newest message")
	w.mail("mid", mail.RoleArchive, 10, "a middle message")
	w.mail("bin", mail.RoleDeleted, 40, "deleted items are not indexed by default")
	partial := mailtest.Message("src", "partial")
	partial.Partial, partial.Body = true, "preview only"
	w.write(partial)
	fin, _ := events.Subscribe(w.bus, TopicFinished, 4)
	rep := w.run()
	if rep.Selected != 3 || rep.Embedded != 3 || rep.State != runstate.Succeeded || rep.Usage.InputTokens == 0 || rep.MailsPerSecond <= 0 {
		t.Fatalf("three eligible complete messages: %+v", rep)
	}
	if rep.After.Eligible != 4 || rep.After.Indexed != 3 || rep.After.Pending != 1 || rep.After.Partial != 1 {
		t.Fatalf("the partial message waits for its details: %+v", rep.After)
	}
	vecs, _ := w.st.LoadVectors(t.Context(), rep.Space, 0)
	order := []string{}
	for _, v := range vecs {
		m, _ := w.st.MessageByRow(t.Context(), v.MessageID)
		order = append(order, m.ID)
	}
	if fmt.Sprint(order) != "[new mid old]" {
		t.Fatalf("newest first: %v", order)
	}
	if got := <-fin.C; got.RunID != rep.RunID || got.Embedded != 3 {
		t.Fatalf("finished event: %+v", got)
	}
	runs, _ := w.st.ListIndexRuns(t.Context(), 5)
	if len(runs) != 1 || runs[0].State != runstate.Succeeded || runs[0].Space != rep.Space {
		t.Fatalf("run persisted: %+v", runs)
	}
	if again := w.run(); again.Selected != 0 || w.fake.Fake.Calls() != 2 {
		t.Fatalf("nothing changed, nothing selected and no call: %+v (calls %d)", again, w.fake.Fake.Calls())
	}
}

func TestOnlyChangedTextIsEmbeddedAgain(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "first text")
	w.mail("b", mail.RoleInbox, 2, "second text")
	first := w.run()
	calls := w.fake.Fake.Calls()
	flip := mailtest.Message("src", "a")
	flip.FolderRole, flip.FolderPath, flip.Body, flip.SentOn = mail.RoleInbox, "inbox", "first text", ""
	flip.ReceivedAt, flip.IsRead = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).Format(mail.TimeLayout), false
	w.write(flip)
	if cov, _ := w.ix.Coverage(t.Context()); cov.Stale != 1 || cov.Indexed != 1 {
		t.Fatalf("a changed message is stale until checked: %+v", cov)
	}
	rep := w.run()
	if rep.Selected != 1 || rep.Unchanged != 1 || rep.Embedded != 0 || w.fake.Fake.Calls() != calls {
		t.Fatalf("a read flag changes no text: confirmed without a call: %+v", rep)
	}
	w.mail("b", mail.RoleInbox, 2, "second text, edited with a quote\n> old quoted line")
	before, _ := w.st.LoadVectors(t.Context(), first.Space, 0)
	if rep = w.run(); rep.Embedded != 1 || rep.After.Indexed != 2 {
		t.Fatalf("a changed body is embedded again: %+v", rep)
	}
	after, _ := w.st.LoadVectors(t.Context(), first.Space, before[len(before)-1].Seq)
	if len(after) != 1 {
		t.Fatalf("one newer vector: %+v", after)
	}
	w.mail("b", mail.RoleInbox, 2, "second text, edited with a quote\n> another quoted line")
	if rep = w.run(); rep.Unchanged != 1 || rep.Embedded != 0 {
		t.Fatalf("only the quote changed: the cleaned text is the same: %+v", rep)
	}
}

func TestANewSpaceKeepsOldVectorsUntilReplaced(t *testing.T) {
	w := newManagedWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	w.mail("b", mail.RoleInbox, 2, "beta")
	old := w.run()
	if err := w.cfg.Set("embed.dimensions", 256); err != nil {
		t.Fatal(err)
	}
	cov, _ := w.ix.Coverage(t.Context())
	if cov.Space == old.Space || cov.Pending != 2 || cov.Indexed != 0 {
		t.Fatalf("a new length is a new space, all pending: %+v", cov)
	}
	w.fake.Fake.SetStatus(http.StatusInternalServerError)
	rep, err := w.ix.Run(t.Context(), Options{}, nil)
	if err == nil || rep.State != runstate.Failed || rep.Failed != 2 || len(rep.Failures) != 2 { // the batch, then each message
		t.Fatalf("every batch failed: %+v %v", rep, err)
	}
	if n, _ := w.st.CountVectors(t.Context(), old.Space); n != 2 {
		t.Fatalf("a failed run never removes vectors: %d", n)
	}
	w.fake.Fake.SetStatus(0)
	if rep = w.run(); rep.Embedded != 2 || rep.After.Indexed != 2 {
		t.Fatalf("the new space is filled: %+v", rep)
	}
	if n, _ := w.st.CountVectors(t.Context(), old.Space); n != 0 {
		t.Fatalf("one vector per message: the old ones are replaced: %d", n)
	}
}

func TestUnavailableEmbedderStopsTheRun(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 6 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	rep, err := w.ix.Run(t.Context(), Options{}, nil)
	if err == nil || rep.StoppedBy != StoppedUnavailable || rep.Failed != 2 || rep.NotAttempted != 4 || w.fake.Fake.Calls() != 1 {
		t.Fatalf("one call, then stop: %+v %v (calls %d)", rep, err, w.fake.Fake.Calls())
	}
}

func TestCancelledRunKeepsCompletedBatches(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 6 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	ctx, cancel := context.WithCancel(t.Context())
	rep, err := w.ix.Run(ctx, Options{}, func(p Progress) {
		if p.Done >= 2 {
			cancel()
		}
	})
	if rep.State != runstate.Cancelled || !errors.Is(err, context.Canceled) || rep.Embedded != 2 || rep.StoppedBy != StoppedCancelled {
		t.Fatalf("cancelled after the first batch: %+v %v", rep, err)
	}
	if n, _ := w.st.CountVectors(t.Context(), rep.Space); n != 2 {
		t.Fatalf("its vectors are kept: %d", n)
	}
}

func TestOneRunAtATime(t *testing.T) {
	w := newWorld(t, nil)
	if !w.ix.single.Begin() {
		t.Fatal("the slot is free")
	}
	if _, err := w.ix.Run(t.Context(), Options{}, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("Run while busy: %v", err)
	}
	m := jobs.NewManager(w.cfg, w.bus, nil)
	if _, err := w.ix.Start(m, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("Start while busy: %v", err)
	}
	w.ix.single.End()
	w.mail("a", mail.RoleInbox, 1, "alpha")
	j, err := w.ix.Start(m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	<-j.Done()
	if info := j.Info(); info.Status != runstate.Succeeded || info.Progress.Done != 1 {
		t.Fatalf("job: %+v", info)
	}
}

func TestStoresAndRolesFollowTheSettings(t *testing.T) {
	w := newWorld(t, map[string]any{"index.folder_roles": []string{"deleted"}})
	w.mail("in", mail.RoleInbox, 1, "inbox")
	w.mail("bin", mail.RoleDeleted, 2, "bin")
	if rep := w.run(); rep.Embedded != 1 || rep.After.Eligible != 1 {
		t.Fatalf("only deleted items: %+v", rep)
	}
	if err := w.cfg.Set(source.SettingSelected, []string{"store-000000000000"}); err != nil {
		t.Fatal(err)
	}
	if cov, _ := w.ix.Coverage(t.Context()); cov.Eligible != 0 {
		t.Fatalf("no picked store holds mail: %+v", cov)
	}
}

// Found by the local real-model run: one oversize input failed its whole
// batch, so its neighbours were never indexed (every run again). A failed
// batch is now embedded message by message.
func TestOneBadInputNeverKeepsItsBatchOut(t *testing.T) {
	w := newManagedWorld(t, nil) // batches of 2
	w.mail("good", mail.RoleInbox, 1, "a normal message")
	w.mail("bad", mail.RoleInbox, 2, "this one the server refuses")
	w.fake.Fake.MaxInputBytes = 60 // only the longer input is over it
	rep, _ := w.ix.Run(t.Context(), Options{}, nil)
	if rep.Embedded != 1 || rep.Failed != 1 || len(rep.Failures) != 1 || len(rep.Failures[0].Messages) != 1 || rep.State != runstate.Succeeded {
		t.Fatalf("the good message is indexed, the bad one reported: %+v", rep)
	}
}

// Review 2b #2 (failed first: the message was tried in every run, and a
// crashing input blocked newest-first indexing): a message that fails
// index.quarantine_after times at one revision is left out until it
// changes, and the coverage shows it.
func TestAMessageThatKeepsFailingIsQuarantinedUntilItChanges(t *testing.T) {
	w := newManagedWorld(t, nil)
	w.mail("good", mail.RoleInbox, 1, "a normal message")
	w.mail("bad", mail.RoleInbox, 2, "this one the server refuses")
	w.fake.Fake.MaxInputBytes = 60
	w.run()
	rep, _ := w.ix.Run(t.Context(), Options{}, nil) // only the bad one is left: the run fails
	if rep.Quarantined != 1 || rep.Failed != 1 || rep.After.Quarantined != 1 {
		t.Fatalf("second failure quarantines: %+v", rep)
	}
	calls := w.fake.Fake.Calls()
	if rep = w.run(); rep.Selected != 0 || w.fake.Fake.Calls() != calls {
		t.Fatalf("a quarantined message is not tried again: %+v", rep)
	}
	w.mail("bad", mail.RoleInbox, 2, "short now")
	if rep = w.run(); rep.Embedded != 1 || rep.After.Quarantined != 0 || rep.After.Indexed != 2 {
		t.Fatalf("a changed message is tried again: %+v", rep)
	}
}

func TestCoverageGroupsAndStoreOption(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("in", mail.RoleInbox, 1, "inbox")
	w.mail("out", mail.RoleSent, 2, "sent")
	rep := w.run()
	if g := rep.After.Groups; g[pickedKey+"/inbox"].Indexed != 1 || g[pickedKey+"/sent"].Indexed != 1 || len(g) != 2 {
		t.Fatalf("per store and role: %+v", g)
	}
	if _, err := w.ix.Run(t.Context(), Options{Stores: []string{"store-000000000000"}}, nil); err == nil {
		t.Fatal("a store that is not picked is refused")
	}
}

func TestAnIndexRunFollowsEverySuccessfulSync(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	m := jobs.NewManager(w.cfg, w.bus, nil)
	fin, _ := events.Subscribe(w.bus, TopicFinished, 4)
	stop, err := w.ix.AfterSync(w.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{Status: runstate.Failed})
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{SourceID: "src", Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}})
	if rep := <-fin.C; rep.Embedded != 1 {
		t.Fatalf("the sync's mail is indexed: %+v", rep)
	}
	if err := w.cfg.Set("index.after_sync", false); err != nil {
		t.Fatal(err)
	}
	w.mail("b", mail.RoleInbox, 2, "beta")
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{SourceID: "src", Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}})
	stop()
	if len(fin.C) != 0 {
		t.Fatal("index.after_sync off: no run")
	}
}

// #2: a missing embedding engine is not fatal, but learning cannot finish
// without it: the stage is blocked with the reason and remedy until an
// index run succeeds; the quarantine blocks too.
func TestEmbeddingsStage(t *testing.T) {
	w := newManagedWorld(t, nil)
	stage := w.ix.Stage()
	status := func() learning.StageStatus {
		t.Helper()
		st, err := stage.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := status(); st.State != learning.Pending {
		t.Fatalf("no mail yet: %+v", st)
	}
	w.mail("a", mail.RoleInbox, 1, "a normal message")
	w.mail("b", mail.RoleInbox, 2, "this one the server refuses")
	if st := status(); st.State != learning.Running || st.Progress.Total != 2 || st.Progress.Done != 0 {
		t.Fatalf("pending mail: %+v", st)
	}
	w.fake.Fake.SetStatus(http.StatusUnauthorized)
	_, _ = w.ix.Run(t.Context(), Options{}, nil)
	if st := status(); st.State != learning.Blocked || st.Blockers[0].Code != BlockNoEmbedder || st.Blockers[0].Actions[0].ID != learning.ActionChooseEmbedder {
		t.Fatalf("unavailable embedder: %+v", st)
	}
	w.fake.Fake.SetStatus(0)
	w.fake.Fake.MaxInputBytes = 1 // every input refused
	for range 2 {
		_, _ = w.ix.Run(t.Context(), Options{}, nil)
	}
	if st := status(); st.State != learning.Blocked || st.Blockers[0].Code != BlockQuarantined {
		t.Fatalf("quarantine: %+v", st)
	}
	w.fake.Fake.MaxInputBytes = 0
	w.mail("a", mail.RoleInbox, 1, "changed")
	w.mail("b", mail.RoleInbox, 2, "changed too")
	w.run()
	if st := status(); st.State != learning.Done || st.Progress.Done != 2 {
		t.Fatalf("every eligible message has a vector: %+v", st)
	}
}

// A status refresh during a run reads the live counters and the run's
// selected work, without waiting for finish or planning the full coverage
// again. The final status goes back to the full coverage.
func TestStageReportsLiveProgressDuringARun(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.batch_size": 1})
	for i := range 4 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	w.fake.Fake.Delay = 60 * time.Millisecond
	reports := make(chan Progress, 8)
	done := make(chan Report, 1)
	go func() {
		rep, err := w.ix.Run(t.Context(), Options{}, func(p Progress) { reports <- p })
		if err != nil {
			t.Errorf("run: %v (%+v)", err, rep)
		}
		done <- rep
	}()
	first := <-reports
	if first.Done >= first.Total {
		t.Fatalf("the run already finished: %+v", first)
	}
	live := w.stageStatus()
	if live.State != learning.Running || live.Progress.Total != int64(first.Total) ||
		live.Progress.Done < int64(first.Done) || live.Progress.Done >= int64(first.Total) {
		t.Fatalf("live status after one batch: %+v (event %+v)", live, first)
	}
	second := <-reports
	if second.Done <= first.Done {
		t.Fatalf("progress does not advance: %+v then %+v", first, second)
	}
	live = w.stageStatus()
	if live.State != learning.Running || live.Progress.Done < int64(second.Done) || live.Progress.Done >= int64(second.Total) {
		t.Fatalf("live status after two batches: %+v (event %+v)", live, second)
	}
	if live.Progress.Embedded != live.Progress.Done || live.Progress.Failed != 0 {
		t.Fatalf("live breakdown: %+v", live.Progress)
	}
	rep := <-done
	final := w.stageStatus()
	if final.State != learning.Done || final.Progress.Done != int64(rep.After.Indexed) || final.Progress.Total != int64(rep.After.Eligible) {
		t.Fatalf("final status uses the full coverage: %+v (report after %+v)", final, rep.After)
	}
}

// The learning machine sees the live run through the throttled progress
// invalidation, and its last status comes from the full coverage after
// index.finished.
func TestLearningStatusSeesLiveIndexProgressAndFinalCoverage(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.batch_size": 1})
	for i := range 4 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	w.fake.Fake.Delay = 60 * time.Millisecond
	m, err := learning.New(func() string { return pickedKey }, w.st, w.bus, nil, w.ix.Stage())
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := events.Subscribe(w.bus, learning.TopicStatus, 16)
	stopProgress, err := learning.RefreshOnProgress(m, w.bus, TopicProgress, 16, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stopProgress()
	stopFinished, err := learning.RefreshOn(m, w.bus, TopicFinished, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer stopFinished()
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan Report, 1)
	go func() {
		rep, err := w.ix.Run(t.Context(), Options{}, nil)
		if err != nil {
			t.Errorf("run: %v (%+v)", err, rep)
		}
		done <- rep
	}()
	waitStatus := func(what string, pred func(learning.StageStatus) bool) learning.StageStatus {
		t.Helper()
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case st := <-sub.C:
				stage := st.Stages[0]
				if pred(stage) {
					return stage
				}
			case <-deadline.C:
				t.Fatalf("no %s status during the run", what)
			}
		}
	}
	live := waitStatus("live", func(st learning.StageStatus) bool {
		return st.State == learning.Running && st.Progress.Done > 0 && st.Progress.Total == 4
	})
	if live.Progress.Done >= 4 {
		t.Fatalf("the live status already shows the finished run: %+v", live)
	}
	rep := <-done
	final := waitStatus("final", func(st learning.StageStatus) bool {
		return st.State == learning.Done && st.Progress.Done == int64(rep.After.Indexed)
	})
	if final.Progress.Total != int64(rep.After.Eligible) {
		t.Fatalf("final status does not use the full coverage: %+v (after %+v)", final, rep.After)
	}
}

// Regression (review L0, bugs #7, rules #5): a sync that finishes while an
// index run is in progress is not lost: one more run follows it and
// indexes the sync's mail.
func TestASyncDuringARunIsNotLost(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "alpha")
	m := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
	fin, _ := events.Subscribe(w.bus, TopicFinished, 4)
	stop, err := w.ix.AfterSync(w.bus, m)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !w.ix.single.Begin() { // a run in progress
		t.Fatal("the slot is free")
	}
	events.Publish(w.bus, syncer.TopicFinished, syncer.Report{SourceID: "src", Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}})
	deadline := time.Now().Add(10 * time.Second)
	for !w.ix.single.Pending() {
		if time.Now().After(deadline) {
			t.Fatal("the sync was dropped while a run was in progress")
		}
		time.Sleep(time.Millisecond)
	}
	w.ix.single.End()
	select {
	case rep := <-fin.C:
		if rep.Embedded != 1 {
			t.Fatalf("the follow-up run indexes the sync's mail: %+v", rep)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no index run followed the sync")
	}
}

// A rate limit, an unavailable gateway or a remote 500 is about the
// provider, not about these messages. The run stops once, nothing is
// quarantined, and the next healthy run indexes every message.
func TestATemporaryProviderFailureDoesNotQuarantine(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			w := newWorld(t, map[string]any{"embed.batch_size": 2})
			for i := range 4 {
				w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
			}
			w.fake.Fake.SetStatus(status)
			rep, err := w.ix.Run(t.Context(), Options{}, nil)
			if err == nil || rep.State != runstate.Failed || rep.StoppedBy != StoppedTransient ||
				rep.Failed != 0 || rep.Quarantined != 0 || rep.Embedded != 0 || rep.NotAttempted != 4 {
				t.Fatalf("transient stop: %+v %v", rep, err)
			}
			if calls := w.fake.Fake.Calls(); calls != 1 {
				t.Fatalf("the batch was split into %d calls; a provider-wide stop must not fan out", calls)
			}
			// A second transient run must not quarantine either.
			if rep, _ = w.ix.Run(t.Context(), Options{}, nil); rep.Quarantined != 0 || rep.Failed != 0 {
				t.Fatalf("second transient run: %+v", rep)
			}
			w.fake.Fake.SetStatus(0)
			if rep = w.run(); rep.Embedded != 4 || rep.After.Indexed != 4 || rep.After.Quarantined != 0 {
				t.Fatalf("recovery run: %+v", rep)
			}
		})
	}
}

// splitEmbedder is a direct Embedder (no HTTP adapter) that records the
// size of every request and exposes a provider request limit.
type splitEmbedder struct {
	maxBatch int
	calls    []int
}

func (e *splitEmbedder) Name() string               { return "split" }
func (e *splitEmbedder) Model() string              { return "split-1" }
func (e *splitEmbedder) Space() string              { return "split/1/8" }
func (e *splitEmbedder) Dimensions() int            { return 8 }
func (e *splitEmbedder) Input(t engine.Text) string { return t.Title + "\n" + t.Body }
func (e *splitEmbedder) MaxBatch() int              { return e.maxBatch }
func (e *splitEmbedder) Embed(_ context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.calls = append(e.calls, len(texts))
	out := engine.Embeddings{Vectors: make([][]float32, len(texts))}
	for i := range texts {
		out.Vectors[i] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
	}
	return out, nil
}

type splitSource struct{ e engine.Embedder }

func (s splitSource) Embedder() (engine.Embedder, error) { return s.e, nil }

func TestIndexBatchHonoursTheEmbedderRequestLimit(t *testing.T) {
	w := newWorld(t, map[string]any{"embed.batch_size": 5})
	for i := range 5 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	e := &splitEmbedder{maxBatch: 2}
	w.ix.src = splitSource{e: e}
	rep := w.run()
	if rep.Embedded != 5 || len(e.calls) != 3 {
		t.Fatalf("embedded=%d calls=%v", rep.Embedded, e.calls)
	}
	if fmt.Sprint(e.calls) != "[2 2 1]" {
		t.Fatalf("request sizes %v, want [2 2 1]", e.calls)
	}
}

// exclusiveLock takes the write lock the way another process would: a
// second connection outside the store's pool. WAL lets readers continue;
// every writer waits store.busy_timeout and then gets SQLITE_BUSY.
func exclusiveLock(t *testing.T, cfg *config.Config) func() {
	t.Helper()
	path := store.Path(cfg)
	uri := "file://" + (&url.URL{Path: path}).EscapedPath() + "?" + url.Values{"_busy_timeout": {"10"}}.Encode()
	db, err := sql.Open("sqlite", uri)
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
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_ = db.Close()
	}
}

// startLockedRun begins a run and waits until its embeddings request is in
// flight, then takes the store's write lock. The fake server's per-input
// delay keeps that request open long enough for the lock to be held before
// SaveEmbeddings runs.
func startLockedRun(t *testing.T, w *world) (<-chan Report, <-chan error, func()) {
	t.Helper()
	w.fake.Fake.Delay = 20 * time.Millisecond
	done := make(chan Report, 1)
	failed := make(chan error, 1)
	go func() {
		rep, err := w.ix.Run(context.Background(), Options{}, nil)
		done <- rep
		failed <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for w.fake.Fake.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the embeddings request never started")
		}
		time.Sleep(time.Millisecond)
	}
	return done, failed, exclusiveLock(t, w.cfg)
}

// A writer that holds the database longer than store.busy_timeout must not
// cost the run its vectors: the store attempt is retried and the run
// continues.
func TestAStoreBusyWriteIsRetried(t *testing.T) {
	w := newWorld(t, map[string]any{
		"embed.batch_size": 4, "store.busy_timeout": "10ms",
		"index.store_busy_attempts": 6, "index.store_busy_delay": "20ms",
	})
	for i := range 4 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	done, failed, release := startLockedRun(t, w)
	go func() {
		time.Sleep(150 * time.Millisecond)
		release()
	}()
	rep := <-done
	err := <-failed
	if err != nil || rep.Embedded != 4 || rep.StoppedBy != "" || rep.Failed != 0 {
		t.Fatalf("busy retry: %+v %v", rep, err)
	}
	if rep.After.Indexed != 4 {
		t.Fatalf("coverage after the retry: %+v", rep.After)
	}
}

// When the lock outlives every attempt the run stops cleanly: nothing is
// counted as a message failure or quarantined, and the next run indexes the
// same mail.
func TestStoreBusyExhaustionStopsWithoutQuarantining(t *testing.T) {
	w := newWorld(t, map[string]any{
		"embed.batch_size": 4, "store.busy_timeout": "10ms",
		"index.store_busy_attempts": 2, "index.store_busy_delay": "10ms",
	})
	for i := range 4 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	done, failed, release := startLockedRun(t, w)
	rep := <-done
	err := <-failed
	release()
	if !errors.Is(err, ErrStoreBusy) || rep.StoppedBy != StoppedBusy || rep.State != runstate.Failed ||
		rep.Failed != 0 || rep.Quarantined != 0 || rep.Embedded != 0 || rep.NotAttempted != 4 {
		t.Fatalf("busy exhaustion: %+v %v", rep, err)
	}
	if n, _ := w.st.CountVectors(t.Context(), rep.Space); n != 0 {
		t.Fatalf("a stopped batch stored %d vectors", n)
	}
	rep = w.run()
	if rep.Embedded != 4 || rep.After.Indexed != 4 || rep.Quarantined != 0 {
		t.Fatalf("recovery run: %+v", rep)
	}
}

// A cancellation during a busy wait ends the run as cancelled at once; the
// retry loop must not sleep past its context.
func TestStoreBusyRetryStopsOnCancellation(t *testing.T) {
	w := newWorld(t, map[string]any{
		"embed.batch_size": 4, "store.busy_timeout": "10ms",
		"index.store_busy_attempts": 10, "index.store_busy_delay": "2s",
	})
	for i := range 4 {
		w.mail(fmt.Sprint(i), mail.RoleInbox, i, fmt.Sprint("text ", i))
	}
	w.fake.Fake.Delay = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Report, 1)
	failed := make(chan error, 1)
	go func() {
		rep, err := w.ix.Run(ctx, Options{}, nil)
		done <- rep
		failed <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for w.fake.Fake.Calls() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the embeddings request never started")
		}
		time.Sleep(time.Millisecond)
	}
	release := exclusiveLock(t, w.cfg)
	// The embeddings request takes 80 ms and the first store attempt waits
	// store.busy_timeout (10 ms); cancel during the long retry sleep.
	time.Sleep(150 * time.Millisecond)
	cancel()
	release()
	rep := <-done
	err := <-failed
	if !errors.Is(err, context.Canceled) || rep.State != runstate.Cancelled ||
		rep.StoppedBy != StoppedCancelled || rep.Failed != 0 || rep.Quarantined != 0 {
		t.Fatalf("cancelled retry: %+v %v", rep, err)
	}
}

// Round 26: index_runs had no retention. A stuck embedder selects the same
// mail every reconcile (indexReconcileInterval, 2 minutes) and writes a failed
// run each time, so the table grows without bound. The newest
// index.runs.keep rows are kept after every finished run; the run that just
// finished is always one of them.
func TestIndexRunHistoryIsBounded(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a", mail.RoleInbox, 1, "one message, indexed once")
	if err := w.cfg.Set("index.runs.keep", 2); err != nil {
		// The row assertion below is the requirement: without the setting no
		// prune happens either, so the test still fails for the right reason.
		t.Logf("index.runs.keep is not a setting yet: %v", err)
	}
	var last Report
	for range 5 {
		last = w.run()
	}
	runs, err := w.st.ListIndexRuns(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("5 runs with index.runs.keep=2 left %d rows, want 2", len(runs))
	}
	if runs[0].ID != last.RunID {
		t.Fatalf("the latest run %d is not the newest row %d", last.RunID, runs[0].ID)
	}
	var kept Report
	if err := json.Unmarshal([]byte(runs[0].Report), &kept); err != nil || kept.RunID != last.RunID || kept.State != runstate.Succeeded {
		t.Fatalf("the newest report is intact: %+v (%v)", kept, err)
	}
	t.Logf("5 runs with index.runs.keep=2 left %d rows; newest run %d (%s) is intact", len(runs), kept.RunID, kept.State)
}

// A message whose details the source could not read can never get a vector.
// The stage reports it as a warning and is done: waiting for it forever kept
// the whole app in "learning" at N-1/N messages.
func TestEmbeddingsStageIsDoneWithAPartialMessage(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("ok", mail.RoleInbox, 2, "a complete message")
	partial := mailtest.Message("src", "partial")
	partial.FolderRole, partial.FolderPath = mail.RoleInbox, string(mail.RoleInbox)
	partial.Partial, partial.Body = true, "preview only"
	partial.ReceivedAt = time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC).Format(mail.TimeLayout)
	w.write(partial)
	w.run()
	st := w.stageStatus()
	if st.State != learning.Done {
		t.Fatalf("the stage is not done: %+v", st)
	}
	if !strings.Contains(st.Note, "waiting for details") {
		t.Fatalf("the partial message is not reported: %+v", st)
	}
}
