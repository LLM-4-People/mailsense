// Command mailsense is the desktop app. It wires configuration, logging,
// the store, secrets, the triage engine, the semantic side (local runtime
// manager, embedder, index, search) and the learning machine that gates
// every answer, then serves the loopback API; the window is phase 4's
// desktop layer.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/bootstrap"
	"github.com/LLM-4-People/mailsense/internal/buildinfo"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/desktop"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/installprofile"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/onboarding"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

const instanceLockFile = "mailsense.lock"

func main() {
	home, _ := os.UserHomeDir()
	os.Exit(run(os.Environ(), runtime.GOOS, home, bootstrap.Report))
}

// run starts the app; startup failures go to report (file + native dialog)
// because the Windows exe has no console.
func run(environ []string, goos, home string, report func(dataDir string, err error) string) int {
	return runWithShutdownUI(environ, goos, home, report, newShutdownRequest(), runUI)
}

// runWithShutdown is run with an explicit shutdown signal, so tests can
// start the real API and stop it without a listening probe hanging the
// test suite. It deliberately opens no window or browser: a test process
// must not depend on a desktop session.
func runWithShutdown(environ []string, goos, home string, report func(dataDir string, err error) string, shutdown *shutdownRequest) int {
	return runWithShutdownUI(environ, goos, home, report, shutdown, nil)
}

// uiRunner opens the desktop surface after the API is ready. A blocking
// runner returns after its window closed (and has requested shutdown); a
// non-blocking runner has launched the browser and returns immediately.
type uiRunner func(url string, shutdown *shutdownRequest) (blocking bool, err error)

// runWithShutdownUI is run with an explicit UI opener and shutdown signal.
func runWithShutdownUI(environ []string, goos, home string, report func(dataDir string, err error) string, shutdown *shutdownRequest, desktopUI uiRunner) (code int) {
	paths, err := config.ResolvePaths(environ, goos, home)
	if err != nil {
		report("", fmt.Errorf("cannot locate the data directory: %w", err))
		return 1
	}
	// unjoined reports that at least one producer or handler outlived its
	// bounded join. The store and the instance lock then stay open: closing
	// either would let another process use data a running goroutine still
	// owns. The process exits, so the OS reclaims the lock.
	var unjoined bool
	lock, err := fsx.LockFile(filepath.Join(paths.DataDir, instanceLockFile))
	if err != nil {
		if errors.Is(err, fsx.ErrLocked) {
			report(paths.DataDir, errors.New("another Mailsense instance is already running with this data directory"))
			return 1
		}
		report(paths.DataDir, fmt.Errorf("cannot lock the data directory: %w", err))
		return 1
	}
	defer func() {
		if unjoined || shutdown.Unjoined() {
			unjoined, code = true, 1
			return
		}
		if err := lock.Close(); err != nil {
			report(paths.DataDir, fmt.Errorf("cannot release the data directory lock: %w", err))
			code = 1
		}
	}()

	bus := events.New()
	defer bus.Close()
	cfg, err := config.Load(config.Options{Paths: paths, Environ: environ, Bus: bus})
	if err != nil {
		// Name the file the owner must fix and the safe recovery. A bad hand
		// edit or an unknown key otherwise leaves the app unable to start
		// with no next step, and the mail database is untouched by this
		// failure.
		report(paths.DataDir, fmt.Errorf("invalid configuration in %s: %w\n\nFix or rename that file; your mail data is untouched",
			paths.SettingsFile(), err))
		return 1
	}
	logs, err := logx.Setup(cfg, bus)
	if err != nil {
		report(paths.DataDir, fmt.Errorf("cannot start logging: %w", err))
		return 1
	}
	defer logs.Close()
	log := logx.For("main")
	log.Info("mailsense starting", "version", buildinfo.Version, "data_dir", paths.DataDir, "os", goos)
	st, err := store.Open(context.Background(), cfg, bus, nil)
	if err != nil {
		log.Error("cannot open the store", "err", logx.RedactedError(err))
		report(paths.DataDir, fmt.Errorf("cannot open the database: %w", err))
		return 1
	}
	defer func() {
		if unjoined || shutdown.Unjoined() {
			unjoined, code = true, 1
			log.Error("store left open: a handler or producer did not join before shutdown")
			return
		}
		if err := st.Close(); err != nil {
			log.Error("cannot close the store", "err", logx.RedactedError(err))
			code = 1
		}
	}()
	// First run: the pinned local embedder becomes an ordinary provider
	// record before the engines read the provider file, so the semantic
	// side and the learning status see the same configuration. A malformed
	// provider file is reported and startup continues.
	if err := onboarding.EnsureLocalEmbedder(context.Background(), cfg); err != nil {
		log.Warn("local embedder was not seeded; the owner can still configure one", "err", logx.RedactedError(err))
	}
	a, err := wireEngines(cfg, environ, bus, st, paths.DataDir, shutdown)
	if err != nil {
		log.Error("cannot set up the engines", "err", logx.RedactedError(err))
		report(paths.DataDir, fmt.Errorf("cannot set up the engines: %w", err))
		return 1
	}
	a.logs = logs
	defer func() {
		if !a.stop() {
			unjoined = true
			code = 1
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), api.OptionsFromConfig(cfg).ShutdownTimeout)
		defer cancel()
		if err := a.jobs.Shutdown(ctx); err != nil {
			log.Error("jobs did not stop cleanly", "err", logx.RedactedError(err))
			unjoined = true
			code = 1
		}
		a.sidecar.Stop()
	}()

	boot := newBootState()
	services := servicesFor(a, shutdown, boot)
	srv, err := api.New(services, api.OptionsFromConfig(cfg))
	if err != nil {
		log.Error("cannot build the API", "err", logx.RedactedError(err))
		report(paths.DataDir, fmt.Errorf("cannot build the API: %w", err))
		return 1
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), api.OptionsFromConfig(cfg).ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Error("API shutdown did not finish cleanly", "err", logx.RedactedError(err))
			code = 1
			if errors.Is(err, api.ErrHandlersUnjoined) {
				unjoined = true
			}
		}
	}()
	// The listener and its descriptor come first: the desktop window and the
	// API have a real, usable address while the derived views are still cold.
	if _, err := srv.Start(); err != nil {
		log.Error("cannot start the API", "err", logx.RedactedError(err))
		report(paths.DataDir, fmt.Errorf("cannot start the API: %w", err))
		return 1
	}

	bootstrap.Clear(paths.DataDir)
	// The warm is one bounded background task. The window opens immediately;
	// ready is announced when the warm and the semantic prepare finish.
	// Shutdown cancels it and joins it for up to 15 s before the API and store
	// stop: a cooperative view honors the context, while a non-cooperative one
	// is logged after that bound. Production views honor the context.
	warmCtx, warmCancel := shutdown.requestContext()
	warmDone := startWarm(warmCtx, a, services, boot, srv.URL())
	defer func() {
		warmCancel()
		if !joinWarm(warmDone) {
			unjoined = true
			code = 1
		}
	}()

	// First open only: list the stores once the warm has finished, so the
	// picking screen is populated without competing with the startup reads.
	// Nothing syncs until the owner picks a mailbox. Its stop is registered
	// after st.Close, so LIFO joins the discovery before the store closes.
	// The stop waits without a timeout: production connectors honor the
	// context, and a non-cooperative connector is a residual rather than a
	// reason to close the store underneath the pass.
	stopFirstOpen := startFirstOpenDiscovery(a, shutdown, warmDone)
	defer stopFirstOpen()
	// Fetch new mail automatically as it arrives: the picked sources sync
	// once at startup and then on sync.auto.interval. It starts after the
	// same warm as discovery, so it never competes with the startup reads;
	// its stop joins before the jobs manager and the store close.
	stopAutoSync := startAutoSync(a, shutdown, warmDone)
	defer stopAutoSync()
	// Keep the semantic side self-healing: one catch-up plus one query warm-up
	// after the warm, then a low-frequency reconcile. Starting it during the
	// warm would run the index scan beside the views it competes with.
	// This defer runs before the warm join above (LIFO), because the worker
	// may still be reading the runtime the warm's prepare uses.
	stopMaintenance := startSemanticMaintenance(a, shutdown, warmDone)
	defer stopMaintenance()
	stopTriageMaintenance := startTriageMaintenance(a, shutdown, warmDone)
	defer stopTriageMaintenance()
	// A restart fires no sync/index/settings event: start the evidence run a
	// change made while the app was down owes, instead of waiting forever.
	stopEvidence := startEvidenceReconcile(a, shutdown, warmDone)
	defer stopEvidence()
	// An upgrade can change the feature version while the app is down; no
	// event fires on a restart, so the run the new inputs need must be asked
	// for here or the app stays in "learning" until unrelated mail arrives.
	stopLayer1 := startLayer1Reconcile(a, shutdown, warmDone)
	defer stopLayer1()
	stopStoreMaintenance := startStoreMaintenance(a, shutdown, warmDone)
	defer stopStoreMaintenance()
	if desktopUI != nil {
		if _, err := desktopUI(srv.BootstrapURL(), shutdown); err != nil {
			// A failed window (the Windows exe needs the WebView2 runtime)
			// must not leave a headless process with no message: report the
			// cause natively, then open the same app in the default browser
			// so the owner is never stranded. The bootstrap URL carries the
			// token; the plain URL logged on a fallback failure does not.
			log.Warn("cannot open the desktop interface", "url", srv.URL(), "err", logx.RedactedError(err))
			report(paths.DataDir, fmt.Errorf(
				"cannot open the Mailsense window: %w\n\nInstall the Microsoft Edge WebView2 runtime, or open %s in your browser.",
				err, srv.URL()))
			if openErr := desktop.Open(srv.BootstrapURL()); openErr != nil {
				log.Warn("cannot open the browser fallback", "url", srv.URL(), "err", openErr)
			}
		}
	}
	<-shutdown.Done()
	// Cancel first: a handler waiting on a job can then finish during the
	// API join instead of waiting for the API deadline to expire.
	a.jobs.CancelAll()
	return code
}

// app is what the API serves: every wired service, nothing discarded.
type app struct {
	cfg      *config.Config
	bus      *events.Bus
	logs     *logx.Service
	st       *store.Store
	sec      secrets.Store
	jobs     *jobs.Manager
	guard    *resource.Guard
	sidecar  *sidecar.Manager
	registry *source.Registry
	engines  *triage.Engines
	labels   *feedback.Labeler
	embedder *embed.Source

	learning *learning.Machine
	syncer   *syncer.Syncer
	triage   *triage.Triager
	search   *search.Searcher
	priority *priority.Service
	agenda   *agenda.Service
	qa       *qa.Service
	index    *index.Indexer
	evidence *evidence.Engine
	layer1   *layer1.Runner
	// views joins the detached read-model rebuilds the API adapters own (the
	// hub, the companies explorer, the conversation pages, the open loops and
	// the Brief). They read the store after the request that started them
	// returned, so the API's handler join cannot see them; stop joins them
	// before the store closes. An app wired without the API has none.
	views []cacheCloser
	stop  func() bool
}

// shutdownRequest is the one non-blocking shutdown signal.
type shutdownRequest struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	// unjoined records that a background worker outlived its bounded join.
	// The store and the instance lock then stay open: closing either under a
	// live reader is worse than leaving the process to exit with its files
	// intact.
	unjoined atomic.Bool
}

func newShutdownRequest() *shutdownRequest { return &shutdownRequest{done: make(chan struct{})} }

func (s *shutdownRequest) Request(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return api.ErrShutdown
	}
	s.closed = true
	close(s.done)
	return nil
}

func (s *shutdownRequest) Done() <-chan struct{} { return s.done }

// MarkUnjoined records one worker that did not finish inside its join bound.
func (s *shutdownRequest) MarkUnjoined() { s.unjoined.Store(true) }

// Unjoined reports whether any worker outlived its join bound.
func (s *shutdownRequest) Unjoined() bool { return s.unjoined.Load() }

// requestContext is cancelled when shutdown is requested. Background work
// started after the API is announced uses it so it ends with the app.
func (s *shutdownRequest) requestContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// wireEngines opens the secret store, validates the triage engines,
// wires the semantic side, the learning machine (the app-wide gate), the
// source registry, the triager and the searcher behind it; stop ends
// their jobs, runtimes, watchers and connectors.
func wireEngines(cfg *config.Config, environ []string, bus *events.Bus, st *store.Store, dataDir string, shutdown *shutdownRequest) (*app, error) {
	sec, err := secrets.Open(cfg, environ, bus, nil)
	if err != nil {
		return nil, fmt.Errorf("secret store: %w", err)
	}
	mon := engine.NewMonitor(bus, st, nil)
	// The resource guard is created with the manager it protects: every job
	// starts and reports through the one admission and pacing choke point.
	guard := resource.NewGuard(cfg, dataDir, nil)
	jm := jobs.NewManager(cfg, bus, nil, jobs.WithGuard(guard))
	sc, err := sidecar.New(sidecar.Options{Config: cfg, Bus: bus, Jobs: jm, Probe: embed.Probe(cfg)})
	if err != nil {
		return nil, fmt.Errorf("local runtime manager: %w", err)
	}
	runtimeUp := false
	defer func() {
		if runtimeUp {
			return
		}
		_ = jm.Shutdown(context.Background())
		sc.Stop()
	}()
	engines, err := checkTriage(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: st, Locator: sc, Monitor: mon})
	if err != nil {
		return nil, fmt.Errorf("triage: %w", err)
	}

	sem, err := wireSemantic(cfg, bus, sec, st, jm, sc, mon)
	if err != nil {
		return nil, fmt.Errorf("semantic search: %w", err)
	}
	ev, unev, err := wireEvidence(cfg, bus, st, sem)
	if err != nil {
		sem.stop()
		return nil, fmt.Errorf("evidence: %w", err)
	}
	l1, unl1, err := wireLayer1(cfg, bus, st, sem, ev, sec, mon)
	if err != nil {
		unev()
		sem.stop()
		return nil, fmt.Errorf("layer 1: %w", err)
	}
	machine, unwatch, err := wireLearning(cfg, bus, st, sem.ix, ev, l1)
	if err != nil {
		unl1()
		unev()
		sem.stop()
		return nil, fmt.Errorf("learning: %w", err)
	}
	learner := installprofile.New(cfg, st, nil)
	// The install profile scans the whole mailbox, so it obeys the same host
	// ceilings as every other heavy job through the app's one adapter.
	learner.SetAdmission(priorityAdmission{guard: guard})
	unlearn, learnDone, err := learner.AfterSync(bus) // the install profile follows the mail
	if err != nil {
		unwatch()
		unl1()
		unev()
		sem.stop()
		return nil, fmt.Errorf("install profile: %w", err)
	}
	// cleanup reports whether every worker it stops joined. The caller marks
	// the run unjoined on a miss, which keeps the store and the lock open.
	cleanup := func() bool {
		unlearn()
		joined := stopWorker(shutdown, learnDone, shutdownJoinTimeout)
		unwatch()
		unl1()
		unev()
		sem.stop()
		return joined
	}
	registry, err := newSourceRegistry(context.Background(), cfg, bus, st)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("mail sources: %w", err)
	}
	tr, err := triage.New(cfg, st, bus, engines, machine, nil)
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("triage: %w", err)
	}
	stopTriage, err := tr.AfterSync(bus, jm)
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("triage trigger: %w", err)
	}
	tr.UseEvidence(ev) // its facts join the states while triage.facts.evidence is on
	// The route key is the one configuration identity of the stored route
	// rows. Triage, priority and the label queue read the same value; layer1
	// writes it. The closure keeps one owner and lets each reader compute it
	// under its own cache policy.
	routeKey := func() (string, error) { return layer1.RouteKey(cfg) }
	tr.UseRoute(st, triage.RouteKeyFunc(routeKey)) // the app's answers are the certified route, never the paid cache
	l1.UseModelState(tr)                           // the judge's label-free state (safe before the gate opens)
	stopAuto, err := l1.AutoLabels(bus, jm)
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("automatic labels: %w", err)
	}
	baseCleanup := cleanup
	cleanup = func() bool {
		stopAuto()
		return baseCleanup()
	}
	sr, err := search.New(cfg, search.Deps{Store: st, Embedder: sem.src, Coverage: sem.ix, Triage: tr, Gate: machine, Bus: bus,
		// A waiting question triggers the index catch-up itself, so a message
		// that arrived moments ago is indexed before the answer, not after.
		Ensure: func(ctx context.Context) error {
			_, err := sem.ix.Ensure(ctx, jm)
			return err
		}})
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("search: %w", err)
	}
	pr, err := priority.New(cfg, priority.Deps{Store: st, Gate: machine, Config: cfg, Threads: st, RouteKey: routeKey})
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("priority: %w", err)
	}
	ags := agenda.New(agenda.Deps{Store: st, Threads: pr, DefaultHorizon: time.Duration(cfg.Int(agenda.SettingDefaultHours)) * time.Hour})
	qaSrc := qa.NewSource(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: mon, Monitor: mon})
	qs, err := qa.New(cfg, qa.Deps{Store: st, Priority: pr, Search: sr, Agenda: ags, Gate: machine, Generator: qaSrc})
	if err != nil {
		registry.Close()
		cleanup()
		return nil, fmt.Errorf("qa: %w", err)
	}
	runtimeUp = true
	application := &app{cfg: cfg, bus: bus, logs: nil, st: st, sec: sec, jobs: jm, guard: guard, sidecar: sc, registry: registry,
		engines: engines, labels: feedback.NewLabeler(st, bus), embedder: sem.src,
		learning: machine, syncer: syncer.New(st, cfg, bus, nil), triage: tr, search: sr, priority: pr, agenda: ags, qa: qs,
		index: sem.ix, evidence: ev, layer1: l1}
	application.stop = func() bool {
		// Priority owns the one cold ranking build; stop it before the
		// store closes under it, and report whether every worker was
		// joined. A miss keeps the store and the lock open.
		stopTriage()
		joined := pr.Close()
		if !cleanup() {
			joined = false
		}
		// The read models' detached rebuilds are the last store readers left:
		// they outlive the requests that started them, so the API's handler
		// join never saw them. Join them after the workers above stopped (so
		// no new rebuild starts behind this join) and before the store
		// closes, under the one shutdown bound.
		ctx, cancel := context.WithTimeout(context.Background(), shutdownJoinTimeout)
		defer cancel()
		if !joinReadModels(ctx, application.views) {
			joined = false
		}
		registry.Close()
		return joined
	}
	return application, nil
}

// checkTriage builds the engines, validating the provider catalog, the
// decider, the triage profile and rules at start (every problem
// reported). Deciders are built again per run, so later changes to
// triage.decider or the providers file apply without a restart.
func checkTriage(deps engine.Deps) (*triage.Engines, error) {
	engines := triage.NewEngines(deps)
	if _, _, err := engines.Decider(deps.Config); err != nil {
		// Unconfigured is not fatal: the app must start so the owner can
		// configure providers; the triage action reports the same error.
		logx.For("main").Warn("triage decider is not ready", "err", logx.RedactedError(err))
	}
	if _, err := taxonomy.Load(deps.Config); err != nil {
		return nil, err
	}
	if _, err := triage.LoadRules(deps.Config); err != nil {
		return nil, err
	}
	logProviderRoles(deps.Config)
	return engines, nil
}

// logProviderRoles reports the state of the four role selectors at startup
// (never fatal): an empty or deleted record is an explicit blocker the
// owner fixes in the provider settings.
func logProviderRoles(cfg *config.Config) {
	log := logx.For("main")
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		log.Warn("provider file is invalid: no provider can be used until it is fixed", "err", logx.RedactedError(err))
		return
	}
	// The startup has no provider health yet, so only the configuration and
	// catalog answer here; the API adds the key presence from the monitor.
	for _, st := range engine.RoleStatuses(cfg, cat, nil) {
		switch {
		case st.Provider == "":
			log.Warn("provider role is not configured", "setting", st.Role.Setting, "role", st.Role.Label)
		case !st.Configured:
			log.Warn("provider role is not usable", "setting", st.Role.Setting, "role", st.Role.Label, "provider", st.Provider, "err", st.Detail)
		default:
			log.Info("provider role ready", "setting", st.Role.Setting, "role", st.Role.Label, "provider", st.Provider)
		}
	}
}

// semantic is the wired semantic side.
type semantic struct {
	src  *embed.Source
	ix   *index.Indexer
	jobs *jobs.Manager
	stop func()
}

// wireSemantic builds the local runtime manager's embedder and the indexer
// (started after every successful sync). The manager itself is owned by
// wireEngines so the API and the jobs UI share it. An embedder that cannot
// be built is not a startup error: either this machine has no engine or an
// embed.* setting the model does not allow, and the app must start so the
// owner can fix it. The embeddings stage reports the same error as a
// blocker with its action (index.Stage), so learning — and with it the gate
// in front of search — waits for one. Once learned, an embedder that
// becomes unreachable degrades search to full text, disclosed with every
// answer.
func wireSemantic(cfg *config.Config, bus *events.Bus, sec secrets.Store, st *store.Store, jm *jobs.Manager, sc *sidecar.Manager, mon *engine.Monitor) (*semantic, error) {
	log := logx.For("main")
	src := embed.NewSource(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: mon, Locator: sc, Monitor: mon})
	if _, err := src.Embedder(); err != nil {
		log.Warn("embedding is not ready: learning waits for it (embeddings stage blocked)", "err", logx.RedactedError(err))
	}
	ix := index.New(cfg, st, bus, src, nil)
	unwatch, err := ix.AfterSync(bus, jm) // a sync run is followed by an index run (index.after_sync)
	if err != nil {
		return nil, err
	}
	// The unavailable-embedder wait ends at the shared recovery points: any
	// effective setting change, the configured provider reporting healthy,
	// or the managed runtime becoming ready.
	unwatchChanges, err := ix.AfterChanges(bus)
	if err != nil {
		unwatch()
		return nil, err
	}
	unwatchRuntime, err := watchRuntimeReady(ix, bus, cfg.EventBuffer())
	if err != nil {
		unwatchChanges()
		unwatch()
		return nil, err
	}
	return &semantic{src: src, ix: ix, jobs: jm, stop: func() {
		unwatchRuntime()
		unwatchChanges()
		unwatch()
	}}, nil
}

// wireEvidence builds the evidence engine (the registry, its bins and
// rules are checked: a broken one is a startup error) over the embedder's
// vector space (template_matches), started after every successful sync
// and every index run that committed work (evidence.after_runs).
func wireEvidence(cfg *config.Config, bus *events.Bus, st *store.Store, sem *semantic) (*evidence.Engine, func(), error) {
	space := func() (string, error) {
		e, err := sem.src.Embedder()
		if err != nil {
			return "", err
		}
		return e.Space(), nil
	}
	ev, err := evidence.New(evidence.Deps{Config: cfg, Store: st, Bus: bus, Space: space})
	if err != nil {
		return nil, nil, err
	}
	stop, err := ev.AfterRuns(bus, sem.jobs)
	if err != nil {
		return nil, nil, err
	}
	return ev, stop, nil
}

// wireLayer1 builds the local classifier over the evidence and the
// embedder's vector space, trained after every evidence run and every
// committed index run, label change and change of a setting or data file
// it reads (layer1.after_runs) when its inputs changed; a broken teachers
// or requirements file is a blocker of the layer1 stage, never fatal.
func wireLayer1(cfg *config.Config, bus *events.Bus, st *store.Store, sem *semantic, ev *evidence.Engine, sec secrets.Store, mon *engine.Monitor) (*layer1.Runner, func(), error) {
	src := judge.NewSource(engine.Deps{Config: cfg, Bus: bus, Secrets: sec, Sink: mon, Monitor: mon})
	r, err := layer1.New(layer1.Deps{Config: cfg, Store: st, Evidence: ev, Embedder: sem.src, Bus: bus,
		Judge: src.Judge, JudgeCost: src.Cost})
	if err != nil {
		return nil, nil, err
	}
	if _, err := layer1.NewPlan(cfg); err != nil {
		// never fatal (rules review #4: the app has no CLI to fix it from):
		// the layer1 stage shows the problem as a blocker with its remedy
		logx.For("main").Warn("layer 1 configuration has problems", "err", logx.RedactedError(err))
	}
	stopRuns, err := r.AfterRuns(bus, sem.jobs)
	if err != nil {
		return nil, nil, err
	}
	// every data file's edit becomes a change of its setting (app-wide;
	// layer 1, evidence and triage re-run on their own files' edits)
	stopFiles := cfg.WatchDataFiles()
	return r, func() { stopFiles(); stopRuns() }, nil
}

// wireLearning builds the learning machine over every stage, in order;
// the stages whose modules are not built yet are pending, so the gate
// stays closed. The status (which the gate answers from) is refreshed
// after every sync, discovery or selection change, index run and settings
// change. Index batches also invalidate it through a bounded progress
// subscription, so /api/learning shows the live run instead of the status
// cached before it started; index.finished still rebuilds from the final
// coverage. The first status build belongs to the startup warm: a stage
// that cannot report is a warm warning and is reported by /api/learning,
// not a fatal startup error.
func wireLearning(cfg *config.Config, bus *events.Bus, st *store.Store, ix *index.Indexer, ev *evidence.Engine, l1 *layer1.Runner) (*learning.Machine, func(), error) {
	syncStage := syncer.SyncStage(cfg, st)
	m, err := learning.New(func() string { return source.Scope(cfg.Snapshot()) }, st, bus, nil,
		syncer.MailboxesStage(cfg, st),
		learning.MachineStage(cfg, nil),
		syncStage,
		ev.Stage(),
		ix.Stage(),
		l1.LabelsStage(syncStage),
		l1.Stage(ev.Stage(), ix.Stage()),
		l1.RouteStage(l1.Stage(ev.Stage(), ix.Stage())),
		l1.QuestionsStage(),
	)
	if err != nil {
		return nil, nil, err
	}
	// The explicit, reversible test preview: Allow unlocks the automatic
	// answer screens while Open, Status, the latch and every stage keep
	// reporting the real (closed) gate.
	m.SetPreview(cfg.Preview)
	buffer := cfg.EventBuffer()
	var stops []func()
	stopAll := func() {
		for _, s := range stops {
			s()
		}
	}
	for _, watch := range []func() (func(), error){
		func() (func(), error) { return learning.RefreshOn(m, bus, syncer.TopicFinished, buffer) },
		func() (func(), error) { return learning.RefreshOn(m, bus, syncer.TopicMailboxes, buffer) },
		func() (func(), error) {
			return learning.RefreshOnProgress(m, bus, index.TopicProgress, buffer, learning.ProgressRefreshInterval)
		},
		func() (func(), error) {
			return learning.RefreshOnProgress(m, bus, layer1.TopicProgress, buffer, learning.ProgressRefreshInterval)
		},
		func() (func(), error) { return learning.RefreshOn(m, bus, index.TopicFinished, buffer) },
		func() (func(), error) { return learning.RefreshOn(m, bus, evidence.TopicFinished, buffer) },
		func() (func(), error) {
			// A judge run writes one label per message. Coalesce the status
			// rebuild exactly like a running index: at most one per interval,
			// and the final status still arrives on the trailing tick.
			return learning.RefreshOnProgress(m, bus, feedback.TopicLabel, buffer, learning.ProgressRefreshInterval)
		},
		func() (func(), error) { return learning.RefreshOn(m, bus, layer1.TopicFinished, buffer) },
		func() (func(), error) { return learning.RefreshOn(m, bus, config.TopicChanged, buffer) },
	} {
		s, err := watch()
		if err != nil {
			stopAll()
			return nil, nil, err
		}
		stops = append(stops, s)
	}
	return m, stopAll, nil
}
