package triage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// Automatic triage must create no job on an idle mailbox and a bounded job
// once eligible mail exists. This is the scheduling half of "decide without
// waiting for the message to be opened".
func TestStartIfPendingSkipsIdleAndStartsOnNewMail(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 1})
	jm := jobs.NewManager(w.cfg, w.bus, nil)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Fatal("an idle mailbox created a triage job")
	}

	w.mail("new@client.com", "New", "Please review", 0, 0)
	j, err = w.tr.StartIfPending(t.Context(), jm, Options{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("new eligible mail did not start automatic triage")
	}
	<-j.Done()
	info := j.Info()
	if info.Status != "succeeded" {
		t.Fatalf("automatic triage job %+v", info)
	}
}

// The triage trigger follows the mail that changed: a partial sync (some
// folders failed) still triages what arrived, while an output failure and a
// no-op run do not.
func TestTriageFollowsAdvancedSyncs(t *testing.T) {
	cases := []struct {
		name string
		rep  syncer.Report
		want bool
	}{
		{"partial with a write", syncer.Report{Status: runstate.Failed, Partial: true, Totals: store.RunTotals{Written: 1}}, true},
		{"output failure with a write", syncer.Report{Status: runstate.Failed, Totals: store.RunTotals{Written: 1}}, false},
		{"successful no-op", syncer.Report{Status: runstate.Succeeded}, false},
		{"successful write", syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
			w.mail("new@client.com", "New", "Please review", 0, 0)
			jm := jobs.NewManager(w.cfg, w.bus, nil)
			t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
			fin, err := events.Subscribe(w.bus, TopicFinished, 4)
			if err != nil {
				t.Fatal(err)
			}
			stop, err := w.tr.AfterSync(w.bus, jm)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			events.Publish(w.bus, syncer.TopicFinished, c.rep)
			if c.want {
				select {
				case rep := <-fin.C:
					if rep.State != runstate.Succeeded || rep.Decided != 1 {
						t.Fatalf("%+v", rep)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("no triage run after a sync that advanced the mail")
				}
				if got := jm.List(); len(got) != 1 {
					t.Fatalf("exactly one triage job must follow: %+v", got)
				}
				return
			}
			stop() // every handler drained: the event was handled
			if got := jm.List(); len(got) != 0 {
				t.Fatalf("a sync that did not advance started %d triage job(s): %+v", len(got), got)
			}
			if got := w.dec.count(); got != 0 {
				t.Fatalf("a sync that did not advance made %d decider call(s)", got)
			}
		})
	}
}

// advancedSync is one sync report that changed the stored mail.
func advancedSync() syncer.Report {
	return syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}
}

// startSyncTrigger starts the worker over the world's bus and cleans it up.
func startSyncTrigger(t *testing.T, w *world, jm *jobs.Manager) *syncTrigger {
	t.Helper()
	tr := newSyncTrigger(w.tr, jm)
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return tr
}

// countPlans wraps the planner's candidate read so a test proves how many
// plans were built.
func countPlans(w *world, plans *atomic.Int64) {
	w.tr.candidates = func(ctx context.Context, sc store.DecisionScope) ([]int64, error) {
		plans.Add(1)
		return w.st.DecisionCandidates(ctx, sc)
	}
}

// Three advanced reports for one mail generation are one cheap probe and one
// run: the pending message with no decision is still decided, and no second
// plan is built on the event path.
func TestTriggerProbesOncePerGenerationAndDecides(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	w.mail("new@client.com", "New", "Please review", 0, 0)
	jm := newJobManager(t, w)

	var scans, builds, plans atomic.Int64
	w.tr.build = func(ctx context.Context) (*core, error) {
		builds.Add(1)
		return w.tr.buildCore(ctx)
	}
	countPlans(w, &plans)
	tr := newSyncTrigger(w.tr, jm)
	tr.probe = func(ctx context.Context, o Options) (bool, error) {
		scans.Add(1)
		return w.tr.hasPending(ctx, o)
	}
	fin, err := events.Subscribe(w.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	for range 3 {
		events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	}
	fixturetest.WaitUntil(t, 10*time.Second, "the three reports to reach the worker", func() bool { return tr.requested.Load() == 3 })
	select {
	case rep := <-fin.C:
		if rep.State != runstate.Succeeded || rep.Decided != 1 {
			t.Fatalf("automatic triage report = %+v, want one decided message", rep)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no triage run after three advanced reports")
	}
	stop()

	if got := scans.Load(); got != 1 {
		t.Fatalf("three reports of one generation ran %d probes, want 1", got)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("three reports of one generation built the core %d times, want 1 (the admitted run)", got)
	}
	if got := plans.Load(); got != 1 {
		t.Fatalf("three reports of one generation built %d plans, want 1 (the admitted run)", got)
	}
	if got := jm.List(); len(got) != 1 {
		t.Fatalf("three reports of one generation started %d jobs, want 1: %+v", len(got), got)
	}
}

// An unchanged generation after a completed probe is skipped without a scan,
// and the no-work probe builds no core: the junk write makes the cached core
// stale without adding a candidate.
func TestNoWorkProbeBuildsNoCoreAndSkipsUnchangedGeneration(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	w.useRoute(t, w.st) // the app's probe path: the cached view identity, never the core
	seedMessages(t, w, 2)
	seedCurrentDecisions(t, w)
	junk := w.message("junk-1", mail.RoleJunk, "Junk", "junk@x.com", "j", "Junk", base, me)
	w.write(junk)
	jm := newJobManager(t, w)

	var scans, builds, plans atomic.Int64
	w.tr.build = func(ctx context.Context) (*core, error) {
		builds.Add(1)
		return w.tr.buildCore(ctx)
	}
	countPlans(w, &plans)
	tr := newSyncTrigger(w.tr, jm)
	probed := make(chan struct{}, 8)
	tr.probe = func(ctx context.Context, o Options) (bool, error) {
		scans.Add(1)
		pending, err := w.tr.hasPending(ctx, o)
		probed <- struct{}{}
		return pending, err
	}
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	select {
	case <-probed:
	case <-time.After(10 * time.Second):
		t.Fatal("the first advanced report did not reach the probe")
	}
	for range 2 {
		events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	}
	fixturetest.WaitUntil(t, 10*time.Second, "the coalesced reports", func() bool { return tr.requested.Load() == 3 })
	stop()

	if got := scans.Load(); got != 1 {
		t.Fatalf("an unchanged generation ran %d scans, want 1", got)
	}
	if got := builds.Load(); got != 0 {
		t.Fatalf("the no-work probe built the core %d time(s), want 0", got)
	}
	if got := plans.Load(); got != 0 {
		t.Fatalf("the no-work path built %d plan(s), want 0", got)
	}
	if got := jm.List(); len(got) != 0 {
		t.Fatalf("an unchanged generation started %d job(s): %+v", len(got), got)
	}
}

// A report that arrives while a run holds the lease does no work on the event
// path; one follow-up run serves the new mail after the run ends.
func TestReportDuringRunIsOneFollowUp(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	w.mail("first@client.com", "A", "First", 0, 0)
	jm := newJobManager(t, w)

	var scans, builds, plans atomic.Int64
	w.tr.build = func(ctx context.Context) (*core, error) {
		builds.Add(1)
		return w.tr.buildCore(ctx)
	}
	countPlans(w, &plans)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	w.dec.block, w.dec.entered = release, entered
	tr := newSyncTrigger(w.tr, jm)
	tr.probe = func(ctx context.Context, o Options) (bool, error) {
		scans.Add(1)
		return w.tr.hasPending(ctx, o)
	}
	fin, err := events.Subscribe(w.bus, TopicFinished, 8)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first run did not reach the decider")
	}

	// New mail arrives while the first run is still deciding it.
	w.mail("second@client.com", "B", "Second", time.Hour, 0)
	for range 3 {
		events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	}
	fixturetest.WaitUntil(t, 10*time.Second, "the reports during the run", func() bool { return tr.requested.Load() == 4 })

	if got := scans.Load(); got != 1 {
		t.Fatalf("a report during a run ran %d probes, want 1 (the first)", got)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("a report during a run built the core %d time(s), want 1 (the first)", got)
	}
	if got := plans.Load(); got != 1 {
		t.Fatalf("a report during a run built %d plans, want 1 (the first)", got)
	}
	close(release)

	for range 2 {
		select {
		case <-fin.C:
		case <-time.After(10 * time.Second):
			t.Fatal("the run and its one follow-up did not both finish")
		}
	}
	stop()

	if got := scans.Load(); got != 2 {
		t.Fatalf("the follow-up ran %d probes, want 2 total (first generation and new mail)", got)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("the follow-up built the core %d time(s), want 2 total", got)
	}
	if got := plans.Load(); got != 2 {
		t.Fatalf("the follow-up built %d plans, want 2 total", got)
	}
	if got := jm.List(); len(got) != 2 {
		t.Fatalf("the run and its follow-up started %d jobs, want 2", len(got))
	}
}

// stop cancels a probe in flight, unsubscribes and joins the worker.
func TestStopCancelsProbeAndJoins(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	jm := newJobManager(t, w)
	tr := newSyncTrigger(w.tr, jm)
	entered, canceled := make(chan struct{}), make(chan struct{})
	tr.probe = func(ctx context.Context, o Options) (bool, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return false, ctx.Err()
	}
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}

	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe did not start")
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not join the worker")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("stop did not cancel the probe")
	}
	select {
	case <-tr.done:
	default:
		t.Fatal("the worker outlived stop")
	}
	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	if got := tr.requested.Load(); got != 1 {
		t.Fatalf("a report after stop reached the worker (%d requests)", got)
	}
}

type triggerSampler struct {
	mu  sync.Mutex
	seq []resource.Usage
}

func (s *triggerSampler) Sample(context.Context) (resource.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seq) == 0 {
		return resource.Usage{}, nil
	}
	u := s.seq[0]
	s.seq = s.seq[1:]
	return u, nil
}

func triggerUsage(cpu float64) resource.Usage {
	known := func(p float64) resource.Reading { return resource.Reading{Percent: p, Known: true} }
	return resource.Usage{CPU: known(cpu), RAM: known(10), Disk: known(10)}
}

// A guard refusal leaves the request pending without spinning: no new attempt
// happens until the next report, which reuses the probe result and admits the
// run once the host is under the ceiling.
func TestGuardPausedStartWaitsForTheNextReport(t *testing.T) {
	clk := clocktest.New(base)
	w := newWorldClock(t, map[string]any{SettingAuto: true, SettingBatch: 10}, clk)
	w.mail("new@client.com", "New", "Please review", 0, 0)
	sampler := &triggerSampler{seq: []resource.Usage{triggerUsage(95), triggerUsage(10)}}
	g := resource.NewGuardWithSampler(w.cfg, t.TempDir(), clk, sampler)
	jm := jobs.NewManager(w.cfg, w.bus, clk, jobs.WithGuard(g))
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var scans, attempts atomic.Int64
	started := make(chan struct{}, 2)
	tr := newSyncTrigger(w.tr, jm)
	tr.probe = func(ctx context.Context, o Options) (bool, error) {
		scans.Add(1)
		return w.tr.hasPending(ctx, o)
	}
	realStart := w.tr.Start
	tr.start = func(m *jobs.Manager, o Options) (*jobs.Job, error) {
		attempts.Add(1)
		j, err := realStart(m, o)
		started <- struct{}{}
		return j, err
	}
	fin, err := events.Subscribe(w.bus, TopicFinished, 4)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := tr.watch(w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the paused admission was not attempted")
	}
	// The worker must wait for a new report, not spin on the refusal.
	time.Sleep(20 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("a paused admission was attempted %d times without a new report", got)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("a paused admission ran %d probes without a new report", got)
	}
	if got := jm.List(); len(got) != 0 {
		t.Fatalf("a paused admission created %d job(s)", len(got))
	}

	clk.Advance(3 * time.Second) // the guard re-reads the host
	events.Publish(w.bus, syncer.TopicFinished, advancedSync())
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the next report did not retry the admission")
	}
	select {
	case rep := <-fin.C:
		if rep.State != runstate.Succeeded || rep.Decided != 1 {
			t.Fatalf("the admitted run = %+v, want one decided message", rep)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the admitted run did not finish")
	}
	stop()

	if got := attempts.Load(); got != 2 {
		t.Fatalf("the paused admission made %d attempts, want 2", got)
	}
	if got := scans.Load(); got != 1 {
		t.Fatalf("the retry rescanned the same generation (%d probes), want its cached result", got)
	}
	if got := jm.List(); len(got) != 1 {
		t.Fatalf("the admitted retry created %d jobs, want 1", len(got))
	}
}
