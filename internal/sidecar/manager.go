package sidecar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Prober embeds texts through a running runtime: the embedder adapter of
// the provider served by (runtime, model), bound to one endpoint. It
// validates what comes back (finite, native length, unit norm).
type Prober func(ctx context.Context, runtime, model string, ep engine.Endpoint, info engine.ModelInfo, texts []string) ([][]float32, int64, error)

// VulkanProbe checks that this machine can create a Vulkan instance and
// enumerate a physical device before a Vulkan build downloads anything. It
// returns ErrNoVulkan when the machine cannot run the build and
// ErrVulkanProbe when the check itself could not complete.
type VulkanProbe func() error

// Manager owns the managed runtimes of the app.
type Manager struct {
	cfg      *config.Config
	bus      *events.Bus
	jobs     *jobs.Manager
	clock    clock.Clock
	log      *slog.Logger
	layout   *layout
	dl       *downloader
	health   *http.Client
	prober   Prober
	platform string
	state    *stateFile
	key      string // every launch's API key (registered once for redaction)

	ctx    context.Context
	cancel context.CancelFunc
	// workers is the one bounded join of the supervisors this manager
	// started: Track refuses after Stop, Done retires, and Close waits
	// bounded by sidecar.stop_timeout plus the join slack.
	workers join.Group

	mu       sync.Mutex
	inst     map[string]*instance
	installs map[string]*jobs.Job

	// vulkanProbe runs the capability preflight. Only a confirmed device is
	// cached: a no-device result is re-probed on each status read so the
	// card's Refresh notices a newly installed driver, and a probe failure
	// is never cached.
	vulkanProbe VulkanProbe
	probeMu     sync.Mutex
	probed      bool
}

// Options are the manager's collaborators; Bus and Clock may be nil.
type Options struct {
	Config *config.Config
	Bus    *events.Bus
	Jobs   *jobs.Manager
	Clock  clock.Clock
	Probe  Prober
	// VulkanProbe overrides the platform's Vulkan check (tests); nil =
	// platformVulkanProbe.
	VulkanProbe VulkanProbe
	// Platform overrides GOOS/GOARCH (tests); "" = this machine.
	Platform string
}

// MachinePlatform is the catalog's platform key for this machine
// (GOOS/GOARCH). It is the one place the app derives it from the runtime.
func MachinePlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// New returns a manager; nothing starts until a runtime is located. The
// catalog and sidecar.variant are checked now (every problem reported).
func New(o Options) (*Manager, error) {
	if o.Config == nil || o.Jobs == nil || o.Probe == nil {
		return nil, errors.New("sidecar: a config, a job manager and a prober are required")
	}
	clk := clock.Or(o.Clock)
	m := &Manager{cfg: o.Config, bus: o.Bus, jobs: o.Jobs, clock: clk, log: logx.For("sidecar"), prober: o.Probe,
		layout: &layout{root: o.Config.Paths().Resolve(o.Config.String("sidecar.dir"))}, platform: o.Platform,
		inst: map[string]*instance{}, installs: map[string]*jobs.Job{}, vulkanProbe: o.VulkanProbe}
	if m.platform == "" {
		m.platform = MachinePlatform()
	}
	if m.vulkanProbe == nil {
		m.vulkanProbe = platformVulkanProbe
	}
	if err := m.checkVariant(); err != nil {
		return nil, err
	}
	var err error
	if m.key, err = randomKey(); err != nil {
		return nil, err
	}
	logx.RegisterSecret(m.key)
	if m.state, err = loadState(m.layout.state(), m.replace()); err != nil {
		m.log.Warn("runtime state not loaded", "err", err)
	}
	if m.dl, err = m.downloader(); err != nil {
		return nil, err
	}
	if m.health, err = httpx.New(o.Config, o.Bus, "sidecar.health", httpx.WithClock(clk), httpx.WithQuietSuccess(), httpx.WithQuietUnavailable()); err != nil {
		return nil, err
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m, nil
}

// checkVariant: sidecar.variant must be auto or a build every runtime has
// on this platform (the catalog decides, not the schema).
func (m *Manager) checkVariant() error {
	return m.ValidateVariant(m.cfg.String("sidecar.variant"))
}

// ValidateVariant reports whether value is auto or a build the catalog has
// for this platform. The settings write uses it before saving, so a stale
// cpu/cuda value cannot make the next launch fail before the API exists.
func (m *Manager) ValidateVariant(value string) error {
	// Load the catalog even for auto: an invalid override is a startup
	// error, and the settings write must reject it too.
	cat, err := LoadCatalog(m.cfg)
	if err != nil {
		return err
	}
	if value == VariantAuto {
		return nil
	}
	for name, rt := range cat.Runtimes {
		if p, ok := rt.Platforms[m.platform]; ok {
			if _, ok := p.Variants[value]; !ok {
				return fmt.Errorf("sidecar: sidecar.variant %q is not a %s build for %s (builds: %v, or auto)", value, name, m.platform, slices.Sorted(maps.Keys(p.Variants)))
			}
		}
	}
	return nil
}

func (m *Manager) downloader() (*downloader, error) {
	s := m.cfg.Snapshot()
	client, err := httpx.New(m.cfg, m.bus, "sidecar.download", httpx.WithClock(m.clock), httpx.WithTotalTimeout(s.Duration("sidecar.download.timeout")),
		httpx.WithCrossOriginRedirects())
	if err != nil {
		return nil, err
	}
	return &downloader{client: client, attempts: s.Int("sidecar.download.attempts"), replace: m.replace(), log: m.log}, nil
}

func (m *Manager) replace() fsx.Replace { return m.cfg.Snapshot().Replace(m.clock) }

// Locate returns the endpoint of the runtime serving model, starting it
// (and installing it, when sidecar.auto_install allows) if needed, and
// waiting until it is ready or ctx ends. The start goes on in the
// background when ctx ends first (a search that cannot wait).
func (m *Manager) Locate(ctx context.Context, runtime, model string) (engine.Endpoint, error) {
	in, err := m.instance(runtime, model)
	if err != nil {
		return engine.Endpoint{}, err
	}
	in.touch()
	l := in.ensure()
	// A resolved latch is the truth even when the caller's context has
	// just expired: the runtime is ready and the endpoint is usable. Check
	// it before the blocking select so a canceled context cannot turn a
	// ready endpoint into "runtime not ready yet".
	select {
	case <-l.ch:
		return l.ep, l.err
	default:
	}
	select {
	case <-l.ch:
		return l.ep, l.err
	case <-ctx.Done():
		st := in.snapshot()
		state := st.State
		if state == StateIdle || state == StateStopped {
			// The caller asked for this runtime, so a status that has not
			// left idle or stopped yet is a start in preparation (choosing
			// a build, checking the device), not a runtime nobody requested
			// or one that is still down. Reporting "idle"/"stopped" here
			// made a cold or post-idle start look like a no-op.
			state = StateStarting
		}
		return engine.Endpoint{}, fmt.Errorf("%w: %s %s (%s)", ErrStarting, runtime, state, st.Detail)
	}
}

// Failed is told by a caller that could not reach ep: unless the runtime
// already moved on, waiters get a new latch now and the supervisor checks
// the runtime at once (a crashed one restarts without waiting for the
// next health interval).
func (m *Manager) Failed(runtime, model string, ep engine.Endpoint) {
	m.mu.Lock()
	in := m.inst[runtime+"/"+model]
	m.mu.Unlock()
	if in != nil {
		in.failed(ep)
	}
}

// Model is the catalog's revision (file SHA-256) and per-input context of
// a model served by a runtime; nothing starts. It resolves the platform's
// default build (cpu when present, else Vulkan), so it works on Windows
// before any cpu build exists.
func (m *Manager) Model(runtime, model string) (engine.ModelInfo, error) {
	cat, err := LoadCatalog(m.cfg)
	if err != nil {
		return engine.ModelInfo{}, err
	}
	b, err := cat.DefaultBuild(runtime, m.platform, model)
	if err != nil {
		return engine.ModelInfo{}, err
	}
	return modelInfo(b), nil
}

func modelInfo(b Build) engine.ModelInfo {
	return engine.ModelInfo{Revision: b.ModelFile.SHA256, ContextTokens: b.RuntimeSpec.SlotContext}
}

// probe embeds texts on a running build.
func (m *Manager) probe(ctx context.Context, b Build, c *child, texts []string) ([][]float32, int64, error) {
	return m.prober(ctx, b.Runtime, b.Model, endpointOf(c), modelInfo(b), texts)
}

func (m *Manager) instance(runtime, model string) (*instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return nil, ErrStopped
	}
	key := runtime + "/" + model
	in := m.inst[key]
	if in == nil {
		in = newInstance(m, runtime, model)
		m.inst[key] = in
	}
	return in, nil
}

// supervisorJoinSlack is how long Stop waits past sidecar.stop_timeout for
// supervisors to return: a supervisor's own child stop is bounded by that
// setting, and the slack covers scheduling and its final status update. It
// is deliberately not a setting: shutdown must always finish.
const supervisorJoinSlack = 5 * time.Second

// startWorker admits and starts one supervisor on the manager's join group.
// Track refuses once Stop closed it, so no supervisor starts after the join
// began; the pool owns the one Done, so a supervisor that returns or panics
// always retires.
func (m *Manager) startWorker(fn func()) bool {
	if m.ctx.Err() != nil || !m.workers.Track() {
		return false
	}
	go func() {
		defer m.workers.Done()
		fn()
	}()
	return true
}

// Stop ends every runtime and waits for its supervisors, bounded by
// sidecar.stop_timeout plus supervisorJoinSlack. It is idempotent: the join
// group refuses new supervisors and every caller waits on the same drain. A
// supervisor that never returns is logged with the number left and abandoned,
// so shutdown reaches the store close and the data-directory lock release
// instead of hanging on it.
func (m *Manager) Stop() {
	m.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.Duration("sidecar.stop_timeout")+supervisorJoinSlack)
	defer cancel()
	if m.workers.Close(ctx) {
		return
	}
	if left := m.workers.Active(); left > 0 {
		m.log.Warn("sidecar supervisors did not stop before shutdown", "left", left)
	}
}

// build resolves a variant of a runtime for this machine.
func (m *Manager) build(runtime, model, variant string) (Build, error) {
	cat, err := LoadCatalog(m.cfg)
	if err != nil {
		return Build{}, err
	}
	return cat.Resolve(runtime, variant, m.platform, model)
}

// require is the one owner of a build's capability preflight: nothing is
// downloaded, verified or started when this machine cannot run the build.
// A confirmed device is cached (Reprobe forgets it); a missing device is
// checked again on every status read so Refresh sees a new driver, and a
// probe that itself failed is never cached.
func (m *Manager) require(b Build) error {
	switch b.Spec.Requires {
	case "":
		return nil
	case RequirementVulkan:
		m.probeMu.Lock()
		defer m.probeMu.Unlock()
		if m.probed {
			return nil
		}
		err := m.vulkanProbe()
		switch {
		case err == nil:
			m.probed = true
			return nil
		case errors.Is(err, ErrNoVulkan), errors.Is(err, ErrVulkanProbe):
			return err
		default:
			return fmt.Errorf("%w: %v", ErrVulkanProbe, err)
		}
	default:
		return fmt.Errorf("%w: build %s %s requires %q (known requirement: %q)", ErrUnsupported, b.Runtime, b.Variant, b.Spec.Requires, RequirementVulkan)
	}
}

// forgetRequirements drops the cached confirmed device (the re-probe
// action: e.g. after a GPU driver was installed).
func (m *Manager) forgetRequirements() {
	m.probeMu.Lock()
	m.probed = false
	m.probeMu.Unlock()
}

// Install installs a build as a job (the install action: it does not need
// sidecar.auto_install). A running install of the same build is returned.
func (m *Manager) Install(runtime, model, variant string) (*jobs.Job, error) {
	b, err := m.build(runtime, model, variant)
	if err != nil {
		return nil, err
	}
	if err := m.require(b); err != nil {
		return nil, err
	}
	return m.installJob(b)
}

func (m *Manager) installJob(b Build) (*jobs.Job, error) {
	key := m.layout.build(b)
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.installs[key]; j != nil && j.Info().Status == runstate.Running {
		return j, nil
	}
	j, err := m.jobs.Start("sidecar.install", b.Runtime+" "+b.RuntimeSpec.Version+" "+b.Variant, func(ctx context.Context, report func(jobs.Progress)) error {
		return m.install(ctx, b, func(done, total int64) {
			report(jobs.Progress{Done: done, Total: total, Unit: "bytes", Message: fmt.Sprintf("%s %s (%d%%)", b.Runtime, b.Variant, done*100/max(total, 1))})
		})
	})
	if err == nil {
		m.installs[key] = j
	}
	return j, err
}

// ensureInstalled is the check before every launch: the build and model
// verified against their pins; when they are missing or changed, an
// install (auto_install) whose end is awaited. installing is told when
// one starts.
func (m *Manager) ensureInstalled(ctx context.Context, b Build, installing func()) error {
	if err := m.require(b); err != nil {
		return err
	}
	verr := m.layout.verify(b)
	if verr == nil {
		return nil
	}
	if !m.cfg.Bool("sidecar.auto_install") {
		return fmt.Errorf("%w: %v", ErrNotInstalled, verr)
	}
	m.log.Info("runtime not installed or not as pinned: installing", "runtime", b.Runtime, "variant", b.Variant, "reason", verr)
	installing()
	j, err := m.installJob(b)
	if err != nil {
		return err
	}
	select {
	case <-j.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	if info := j.Info(); info.Status != runstate.Succeeded {
		return fmt.Errorf("%w: install %s: %s", ErrFailed, b.Variant, info.Error)
	}
	return nil
}
