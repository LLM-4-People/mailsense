package sidecar

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

const (
	rtName    = "llama-server"
	modelName = "embeddinggemma-2"
)

type world struct {
	t     *testing.T
	cfg   *config.Config
	bus   *events.Bus
	srv   *artifactServer
	rt    Artifact
	model Artifact
	vars  map[string]fakeVariant
	auto  []string
}

func newWorld(t *testing.T, vars map[string]fakeVariant, overrides map[string]any, auto ...string) *world {
	t.Helper()
	w := &world{t: t, bus: events.New(), srv: newArtifactServer(t), vars: vars, auto: auto}
	t.Cleanup(w.bus.Close)
	zipped, err := fakeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	w.rt = w.srv.add("fake-runtime.zip", zipped)
	w.model = w.srv.add("model.gguf", bytes.Repeat([]byte("gguf"), 4096))
	set := map[string]any{"sidecar.start_poll": "10ms", "sidecar.health.interval": "100ms", "sidecar.tune.cpu_percent": []int{cpuPercent(1), cpuPercent(2)},
		"sidecar.tune.inputs": 4, "sidecar.restart.base_delay": "10ms", "sidecar.restart.max_delay": "100ms",
		"sidecar.stop_timeout": "2s", "sidecar.start_timeout": "30s", "http.retry.max_attempts": 2}
	for k, v := range overrides {
		set[k] = v
	}
	w.cfg = configtest.New(t, set)
	embedtest.InstallRuntime(t, w.cfg, rtName, modelName)
	w.catalog()
	return w
}

// cpuPercent is the percentage of this machine's CPUs that tunes t
// threads (the candidates are relative to the hardware).
func cpuPercent(t int) int { return min(100, int(math.Ceil(float64(t*100)/float64(runtime.NumCPU())))) }

func (w *world) catalog() { writeCatalog(w.t, w.cfg.Paths().DataDir, w.rt, w.model, w.vars, w.auto...) }

func (w *world) manager() *Manager {
	w.t.Helper()
	return w.managerWithJobs(jobs.NewManager(w.cfg, w.bus, nil))
}

// managerWithJobs is the manager over an explicit job manager: the round 25
// measurements count the install jobs it started.
func (w *world) managerWithJobs(jm *jobs.Manager) *Manager {
	w.t.Helper()
	return w.managerWith(jm, nil)
}

// managerWith injects the capability probe (nil = the platform's own).
func (w *world) managerWith(jm *jobs.Manager, probe VulkanProbe) *Manager {
	w.t.Helper()
	m, err := New(Options{Config: w.cfg, Bus: w.bus, Jobs: jm, Probe: embed.Probe(w.cfg), VulkanProbe: probe})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(m.Stop)
	return m
}

func (w *world) locate(m *Manager) (engine.Endpoint, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), 60*time.Second)
	defer cancel()
	return m.Locate(ctx, rtName, modelName)
}

// embedder is the managed embedder over m (the app's path).
func (w *world) embedder(m *Manager) engine.Embedder {
	w.t.Helper()
	sec, _ := secrets.Open(w.cfg, nil, nil, nil)
	e, err := embed.NewSource(engine.Deps{Config: w.cfg, Secrets: sec, Locator: m}).Embedder()
	if err != nil {
		w.t.Fatal(err)
	}
	return e
}

func doc(body string) []engine.Text {
	return []engine.Text{{Purpose: engine.PurposeDocument, Title: "t", Body: body}}
}

// builtinCatalog is the embedded catalog: the fake world overrides its
// runtimes and model but inherits the canary sentence.
func builtinCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// pinCanary gives the fake model the pinned reference vector of the
// catalog's canary sentence (the vector the real cpu build measured); a
// fake GPU build embeds the same text through embedtest.Vector.
func (w *world) pinCanary(t *testing.T, v []float32) {
	t.Helper()
	w.model.CanaryVector = v
	w.model.CanarySentenceSHA256 = digest.Hex([]byte(builtinCatalog(t).Canary), 0)
	w.catalog()
}

// noCPUVars is a platform with only the Vulkan build (the Windows shape).
func noCPUVars(env ...map[string]string) map[string]fakeVariant {
	v := fakeVariant{gpu: true}
	if len(env) > 0 {
		v.env = env[0]
	}
	return map[string]fakeVariant{"vulkan": v}
}

// states collects the states published from now on.
func (w *world) states() func() map[State]bool {
	sub, _ := events.Subscribe(w.bus, TopicState, 256)
	return func() map[State]bool {
		seen := map[State]bool{}
		for len(sub.C) > 0 {
			seen[(<-sub.C).State] = true
		}
		return seen
	}
}

// eventually waits for a real child process to reach a condition.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond) // polling a real child process
	}
}

var cpuOnly = map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_SLOW_THREADS": "2"}}}

func TestLocateInstallsTunesStartsAndServes(t *testing.T) {
	w := newWorld(t, cpuOnly, nil)
	m := w.manager()
	seen := w.states()
	ep, err := w.locate(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ep.URL, "http://127.0.0.1:") || len(ep.Key) != 64 {
		t.Fatalf("a loopback endpoint with a 32-byte key: %+v", ep)
	}
	if _, err := w.embedder(m).Embed(t.Context(), doc("hello")); err != nil {
		t.Fatal(err)
	}
	st := m.Status()[0]
	if st.State != StateReady || st.Variant != VariantCPU || st.Requested != VariantAuto || st.Threads != 2 || st.PID == 0 ||
		!strings.Contains(st.Fallback, "no GPU build") {
		t.Fatalf("ready on cpu (auto has nothing to try) with the faster thread count: %+v", st)
	}
	got := seen()
	for _, s := range []State{StateInstalling, StateTuning, StateStarting, StateReady} {
		if !got[s] {
			t.Errorf("state %s was not published (%v)", s, got)
		}
	}
	m.Stop()
	if !processGone(st.PID) {
		t.Fatalf("Stop ends the child %d", st.PID)
	}
	again := w.manager()
	if _, err := w.locate(again); err != nil || w.srv.count(w.rt.Name) != 1 || w.srv.count(w.model.Name) != 1 {
		t.Fatalf("an installed runtime is verified, not downloaded again: %v (%d, %d)", err, w.srv.count(w.rt.Name), w.srv.count(w.model.Name))
	}
	if st := again.Status()[0]; st.Threads != 2 {
		t.Fatalf("the measured thread count is kept: %+v", st)
	}
}

func TestInterruptedDownloadResumes(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	w.srv.cut[w.model.Name] = 5000
	if _, err := w.locate(w.manager()); err != nil {
		t.Fatal(err)
	}
	if w.srv.count(w.model.Name) != 2 || len(w.srv.ranges) != 1 || w.srv.ranges[0] != "bytes=5000-" {
		t.Fatalf("the second request asks for the rest only: %d %v", w.srv.count(w.model.Name), w.srv.ranges)
	}
}

// Rules review 2b §1 (failed first: the shifted bytes were appended and
// only the hash caught it after a second full download): a 206 that does
// not start where the part ends starts the part over.
func TestARangeAnswerFromElsewhereRestartsThePart(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	w.srv.cut[w.model.Name] = 5000
	w.srv.shift[w.model.Name] = 1000
	if _, err := w.locate(w.manager()); err != nil {
		t.Fatal(err)
	}
	if n := w.srv.count(w.model.Name); n != 3 || len(w.srv.ranges) != 1 {
		t.Fatalf("cut, refused range, full download: %d requests, ranges %v", n, w.srv.ranges)
	}
}

func TestWrongHashIsNeverInstalled(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	w.model.SHA256 = strings.Repeat("0", 64)
	w.catalog()
	m := w.manager()
	_, err := w.locate(m)
	if !errors.Is(err, engine.ErrUnavailable) || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("refused: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(m.layout.root, "models", "*"))
	if len(matches) != 0 || m.Status()[0].State != StateFailed {
		t.Fatalf("nothing kept, the runtime failed: %v %+v", matches, m.Status())
	}
}

func TestTamperedOrExtendedInstallIsDetectedBeforeLaunch(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1, "sidecar.auto_install": false})
	m := w.manager()
	j, err := m.Install(rtName, modelName, VariantCPU) // the action works without auto_install
	if err != nil {
		t.Fatal(err)
	}
	if <-j.Done(); j.Info().Status != runstate.Succeeded {
		t.Fatalf("install action: %+v", j.Info())
	}
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	m.Stop()
	b, _ := m.build(rtName, modelName, VariantCPU)
	planted := filepath.Join(m.layout.build(b), "fake", "evil.dll")
	if err := os.WriteFile(planted, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.locate(w.manager()); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "not part of the installed build") {
		t.Fatalf("an unlisted file is refused: %v", err)
	}
	_ = os.Remove(planted)
	// Replaced (written next to it and renamed over: right after its
	// process exits, Linux can still refuse to open it for writing).
	if err := os.WriteFile(m.layout.exe(b)+".new", []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(m.layout.exe(b)+".new", m.layout.exe(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.locate(w.manager()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("a changed file is not launched: %v", err)
	}
	_ = w.cfg.Set("sidecar.auto_install", true)
	if _, err := w.locate(w.manager()); err != nil || w.srv.count(w.rt.Name) != 2 {
		t.Fatalf("auto_install installs it again: %v (%d downloads)", err, w.srv.count(w.rt.Name))
	}
}

func TestBrokenGPUBuildFallsBackToCPUAndIsRemembered(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {}, "cuda": {gpu: true, env: map[string]string{"FAKE_MODE": "half-norm"}}}
	w := newWorld(t, vars, map[string]any{"sidecar.variant": "cuda", "sidecar.threads": 1})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	st := m.Status()[0]
	if st.Variant != VariantCPU || st.Requested != "cuda" || !strings.Contains(st.Fallback, "canary") {
		t.Fatalf("the fallback is disclosed: %+v", st)
	}
	m.Stop()
	seen := w.states()
	m2 := w.manager()
	if _, err := w.locate(m2); err != nil {
		t.Fatal(err)
	}
	if got := seen(); got[StateCanary] || !strings.Contains(m2.Status()[0].Fallback, "re-probe") {
		t.Fatalf("a broken build is not tried at every start: %v %+v", got, m2.Status()[0])
	}
	if _, _, broken := m2.Learned(); len(broken) != 1 {
		t.Fatalf("persisted: %+v", broken)
	}
}

func TestGoodGPUBuildPassesTheCanary(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {}, "vulkan": {gpu: true}}
	w := newWorld(t, vars, map[string]any{"sidecar.variant": "vulkan", "sidecar.threads": 1})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	if st := m.Status()[0]; st.Variant != "vulkan" || st.Fallback != "" || st.Canary < 0.9999 {
		t.Fatalf("vulkan serves after matching the cpu reference: %+v", st)
	}
}

// The Windows shape: one Vulkan build, no cpu anywhere. The model's pinned
// vector is the canary reference, so no cpu build is needed.
func TestAPinnedCanaryPassesWithoutACPU(t *testing.T) {
	w := newWorld(t, noCPUVars(), map[string]any{"sidecar.variant": "vulkan"}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	st := m.Status()[0]
	if st.Variant != "vulkan" || st.Canary < 0.9999 || st.Threads != -1 || st.Fallback != "" {
		t.Fatalf("vulkan serves against the pinned vector with the runtime's own threads: %+v", st)
	}
	if builds, err := m.Builds(); err != nil || len(builds) != 1 || !builds[0].GPU || builds[0].Variant != "vulkan" {
		t.Fatalf("the platform has exactly the vulkan build: %+v %v", builds, err)
	}
}

// A pinned vector that does not match the current sentence (or is far
// from what the build produces) fails the canary; without a cpu build
// there is nothing to fall back to.
func TestAPinnedCanaryFailsWithAFarVectorAndNoCPU(t *testing.T) {
	w := newWorld(t, noCPUVars(), map[string]any{"sidecar.variant": "vulkan", "sidecar.health.failures": 1}, "vulkan")
	far := make([]float32, embedtest.Dims)
	far[0] = 1
	w.pinCanary(t, far)
	m := w.manager()
	_, err := w.locate(m)
	if err == nil || !strings.Contains(err.Error(), "canary") {
		t.Fatalf("a far vector is refused: %v", err)
	}
	st := m.Status()[0]
	if st.State != StateFailed || st.Variant == VariantCPU {
		t.Fatalf("failed without a cpu fallback: %+v", st)
	}
	if builds, _ := m.Builds(); len(builds) != 1 {
		t.Fatalf("no cpu build exists: %+v", builds)
	}
}

// A broken named build is not retried without a cpu fallback: the error
// says a re-probe is required.
func TestABrokenNamedBuildWithoutACPUNeedsAReprobe(t *testing.T) {
	w := newWorld(t, noCPUVars(), map[string]any{"sidecar.variant": "vulkan", "sidecar.health.failures": 1}, "vulkan")
	far := make([]float32, embedtest.Dims)
	far[0] = 1
	w.pinCanary(t, far)
	m := w.manager()
	if _, err := w.locate(m); err == nil {
		t.Fatal("the far vector passed")
	}
	m.Stop()
	_, err := w.locate(w.manager())
	if err == nil || !strings.Contains(err.Error(), "re-probe") || !strings.Contains(err.Error(), "no cpu build") {
		t.Fatalf("a broken named build without cpu: %v", err)
	}
}

// Even when a cpu build exists, a pinned vector is the reference: the
// cpu is not installed or launched (its archive here is unreachable).
func TestAPinnedCanaryDoesNotLaunchTheCPU(t *testing.T) {
	missing := Artifact{Name: "missing-cpu.zip", URL: "http://127.0.0.1:9/missing-cpu.zip",
		SHA256: strings.Repeat("0", 64), Size: 1, Unpack: UnpackZip}
	vars := map[string]fakeVariant{"cpu": {artifact: &missing}, "vulkan": {gpu: true}}
	w := newWorld(t, vars, map[string]any{"sidecar.variant": "vulkan", "sidecar.threads": 1})
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	cpu, err := m.build(rtName, modelName, VariantCPU)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.layout.build(cpu)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the cpu build was installed or launched: %v", err)
	}
}

func TestAutoChoosesTheVulkanBuildWithoutACPU(t *testing.T) {
	w := newWorld(t, noCPUVars(), nil, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	st := m.Status()[0]
	if st.Variant != "vulkan" || st.Requested != VariantAuto || st.Fallback != "" {
		t.Fatalf("auto serves vulkan: %+v", st)
	}
	_, choices, _ := m.Learned()
	if len(choices) != 1 {
		t.Fatalf("one choice: %+v", choices)
	}
	for _, c := range choices {
		if c.Variant != "vulkan" || c.CPU != 0 || len(c.Probes) != 1 || c.Probes[0].Speedup != 0 || c.Probes[0].TokensPerSecond <= 0 {
			t.Fatalf("the probe has no cpu comparison: %+v", c)
		}
	}
	if builds, _ := m.Builds(); len(builds) != 1 {
		t.Fatalf("only vulkan is installed: %+v", builds)
	}
}

func TestAutoFailsWithoutACPUWhenTheCanaryFails(t *testing.T) {
	w := newWorld(t, noCPUVars(map[string]string{"FAKE_MODE": "half-norm"}),
		map[string]any{"sidecar.health.failures": 1}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()
	_, err := w.locate(m)
	if err == nil || !strings.Contains(err.Error(), "canary") {
		t.Fatalf("a failed canary is an error, not a fallback: %v", err)
	}
	if st := m.Status()[0]; st.State != StateFailed || st.Variant == VariantCPU {
		t.Fatalf("failed without a cpu fallback: %+v", st)
	}
	if _, choices, _ := m.Learned(); len(choices) != 0 {
		t.Fatalf("a failed auto probe is not persisted as a choice: %+v", choices)
	}
}

// Without a pinned vector and without a cpu build the canary cannot run:
// the error names the model and both ways to fix it.
func TestMissingPinnedVectorWithoutACPUIsExplicit(t *testing.T) {
	w := newWorld(t, noCPUVars(), map[string]any{"sidecar.health.failures": 1}, "vulkan")
	m := w.manager()
	_, err := w.locate(m)
	if err == nil || !strings.Contains(err.Error(), modelName) || !strings.Contains(err.Error(), "pinned canary vector") ||
		!strings.Contains(err.Error(), "cpu build") {
		t.Fatalf("the missing reference is explicit: %v", err)
	}
}

func TestModelAndThreadsWorkWithoutACPU(t *testing.T) {
	w := newWorld(t, noCPUVars(), nil, "vulkan")
	m := w.manager()
	info, err := m.Model(rtName, modelName)
	if err != nil || info.Revision != w.model.SHA256 || info.ContextTokens != 2048 {
		t.Fatalf("model metadata without a cpu build: %+v %v", info, err)
	}
	in, err := m.instance(rtName, modelName)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.build(rtName, modelName, "vulkan")
	if err != nil {
		t.Fatal(err)
	}
	if threads, err := in.threadsFor(t.Context(), b); err != nil || threads != -1 {
		t.Fatalf("a GPU build without cpu leaves the thread count to the runtime: %d %v", threads, err)
	}
}

// A crash-looping Vulkan build is marked broken; without a cpu build the
// next start reports the re-probe error instead of starting it again.
func TestACrashLoopingVulkanWithoutACPUIsReported(t *testing.T) {
	w := newWorld(t, noCPUVars(map[string]string{"FAKE_EXIT_AFTER": "2"}),
		map[string]any{"sidecar.restart.max_delay": "5s"}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		st := m.Status()[0]
		return st.State == StateFailed && strings.Contains(st.Detail, "re-probe")
	})
	if st := m.Status()[0]; st.Variant == VariantCPU {
		t.Fatalf("there is no cpu fallback: %+v", st)
	}
	_, choices, broken := m.Learned()
	if len(broken) != 1 {
		t.Fatalf("the build is marked broken: %+v", broken)
	}
	for _, c := range choices {
		if c.Variant != "vulkan" {
			t.Fatalf("markBroken must not choose cpu when the platform has none: %+v", c)
		}
	}
}

// Coordinator decision (review 2b, auto): a GPU build of the auto list
// wins only when it passes the canary AND beats cpu by
// sidecar.auto.min_speedup; the choice and every probe are kept, and a
// second start does not probe again.
func TestAutoPicksAFasterGPUAndRemembersIt(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_DELAY_MS": "8"}}, "vulkan": {gpu: true, env: map[string]string{"FAKE_DELAY_MS": "1"}}}
	w := newWorld(t, vars, map[string]any{"sidecar.threads": 1}, "vulkan")
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	_, choices, _ := m.Learned()
	if st := m.Status()[0]; st.Variant != "vulkan" || len(choices) != 1 {
		t.Fatalf("vulkan chosen: %+v %+v", st, choices)
	}
	for _, c := range choices {
		if c.Variant != "vulkan" || len(c.Probes) != 1 || c.Probes[0].Speedup < 1.1 || c.Probes[0].Canary < 0.9999 || c.CPU <= 0 ||
			c.DecidedAt < c.Probes[0].At { // found live: the choice was stamped before its probes
			t.Fatalf("the probe is kept: %+v", c)
		}
	}
	m.Stop()
	seen := w.states()
	m2 := w.manager()
	if _, err := w.locate(m2); err != nil || m2.Status()[0].Variant != "vulkan" {
		t.Fatalf("second start: %v %+v", err, m2.Status()[0])
	}
	if seen()[StateProbing] {
		t.Fatal("the second start probed again")
	}
	if err := m2.Reprobe(rtName); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return seen()[StateProbing] })
}

func TestAutoKeepsCPUWhenTheGPUIsSlowerOrBroken(t *testing.T) {
	for name, gpu := range map[string]map[string]string{
		"slower": {"FAKE_DELAY_MS": "8"},
		"broken": {"FAKE_MODE": "half-norm"},
	} {
		vars := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_DELAY_MS": "1"}}, "vulkan": {gpu: true, env: gpu}}
		w := newWorld(t, vars, map[string]any{"sidecar.threads": 1}, "vulkan")
		m := w.manager()
		if _, err := w.locate(m); err != nil {
			t.Fatal(err)
		}
		_, choices, _ := m.Learned()
		st := m.Status()[0]
		for _, c := range choices {
			if c.Variant != VariantCPU || len(c.Probes) != 1 || c.Probes[0].Error == "" || st.Variant != VariantCPU || !strings.HasPrefix(st.Fallback, "auto:") {
				t.Errorf("%s: cpu kept with the probe's reason: %+v %+v", name, c, st)
			}
		}
		m.Stop()
	}
}

// Review 2b #3 (failed first: 12 restarts in 6 s, still vulkan): a GPU
// build that keeps dying soon after its start is marked broken and cpu
// takes over.
func TestAGPUBuildInACrashLoopFallsBackToCPU(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {}, "vulkan": {gpu: true, env: map[string]string{"FAKE_EXIT_AFTER": "2"}}}
	w := newWorld(t, vars, map[string]any{"sidecar.variant": "vulkan", "sidecar.threads": 1, "sidecar.restart.max_delay": "5s"})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { st := m.Status()[0]; return st.Variant == VariantCPU && st.State == StateReady })
	if st := m.Status()[0]; !strings.Contains(st.Fallback, "stopped 3 times") {
		t.Fatalf("the reason is disclosed: %+v", st)
	}
}

func TestCrashedRuntimeIsRestarted(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_EXIT_AFTER": "3"}}}
	w := newWorld(t, vars, map[string]any{"sidecar.threads": 1})
	svc, err := logx.Setup(w.cfg, w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	m := w.manager()
	first, err := w.locate(m)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Status()[0].Restarts > 0 })
	var restartWarn bool
	for _, e := range svc.Ring().Snapshot() {
		if e.Component == "sidecar" && e.Level == "WARN" && strings.HasPrefix(e.Message, "runtime restarting") {
			restartWarn = true
		}
	}
	if !restartWarn {
		t.Fatal("the supervisor's restart WARN is missing")
	}
	second, err := w.locate(m)
	if err != nil || second.URL == first.URL {
		t.Fatalf("restarted on a new port: %v %+v %+v", err, first, second)
	}
}

// The health probe's expected 503 (a runtime still loading) is debug: it must
// not fill the default-info log with warnings, while a real 5xx stays warn.
func TestHealthProbe503IsQuiet(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_HEALTH_503": "3"}}}
	w := newWorld(t, vars, map[string]any{
		"sidecar.variant": "cpu",
		"sidecar.threads": 1,
		"log.components":  []string{"httpx=debug"},
	})
	svc, err := logx.Setup(w.cfg, w.bus)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	var debug503, warn503 int
	for _, e := range svc.Ring().Snapshot() {
		if e.Component == "httpx" && e.Attrs["status"] == int64(503) {
			switch e.Level {
			case "DEBUG":
				debug503++
			case "WARN":
				warn503++
			}
		}
	}
	if debug503 == 0 {
		t.Fatal("the expected health 503 was not logged at DEBUG")
	}
	if warn503 != 0 {
		t.Fatalf("%d expected health 503 entries were logged at WARN", warn503)
	}
}

// Review 2b #2 (failed first: ErrUnavailable right after the crash, the
// index run stopped): a runtime that dies between calls is restarted at
// once and the call goes to the new one; an input that crashes it every
// time fails as a server error, never as an unavailable embedder.
func TestACrashBetweenCallsIsNotAnUnavailableEmbedder(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_EXIT_AFTER_EMBED": "1", "FAKE_DIE_ON": "poison"}}}
	w := newWorld(t, vars, map[string]any{"sidecar.threads": 1, "sidecar.health.interval": "10s"})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	e := w.embedder(m)
	if _, err := e.Embed(t.Context(), doc("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(t.Context(), doc("again")); err != nil {
		t.Fatalf("the runtime died on the call and its restart answered it: %v", err)
	}
	_, err := e.Embed(t.Context(), doc("poison"))
	if err == nil || errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("a crashing input is a server error: %v", err)
	}
}

// Review 2b #5 (failed first: 5/5 rounds failed on the shared .part):
// concurrent installs of two builds share the model download safely.
func TestConcurrentInstallsShareTheModelSafely(t *testing.T) {
	w := newWorld(t, map[string]fakeVariant{"cpu": {}, "vulkan": {gpu: true}}, nil)
	w.model = w.srv.add("model.gguf", bytes.Repeat([]byte("gguf"), 2<<20))
	w.catalog()
	m := w.manager()
	j1, err1 := m.Install(rtName, modelName, VariantCPU)
	j2, err2 := m.Install(rtName, modelName, "vulkan")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	<-j1.Done()
	<-j2.Done()
	if j1.Info().Error != "" || j2.Info().Error != "" || w.srv.count("model.gguf") != 1 {
		t.Fatalf("both installed, the model downloaded once: %q %q (%d)", j1.Info().Error, j2.Info().Error, w.srv.count("model.gguf"))
	}
}

// Review 2b #7 (failed first: MAILSENSE_SECRET_* reached the child): the
// child sees only env_allow, its variant's env and the key.
func TestTheRuntimeGetsOnlyTheAllowedEnvironment(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "env.txt")
	t.Setenv("MAILSENSE_SECRET_LEAK", "s3cret")
	t.Setenv("LLAMA_ARG_THREADS", "99")
	w := newWorld(t, map[string]fakeVariant{"cpu": {env: map[string]string{"FAKE_ENV_DUMP": dump}}}, map[string]any{"sidecar.threads": 1})
	if _, err := w.locate(w.manager()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dump)
	for _, name := range strings.Split(string(raw), "\n") {
		up := strings.ToUpper(name)
		if strings.HasPrefix(up, "MAILSENSE_SECRET") || up == "LLAMA_ARG_THREADS" {
			t.Errorf("%s reached the runtime", name)
		}
	}
	if !strings.Contains(string(raw), "LLAMA_API_KEY") {
		t.Fatal("the key is passed")
	}
}

func TestBuildsVerifyAndRemove(t *testing.T) {
	w := newWorld(t, map[string]fakeVariant{"cpu": {}, "vulkan": {gpu: true}}, map[string]any{"sidecar.threads": 1}, "vulkan")
	m := w.manager()
	if j, _ := m.Install(rtName, modelName, "vulkan"); j != nil {
		<-j.Done()
	}
	builds, err := m.Builds()
	if err != nil || len(builds) != 2 || builds[1].Variant != "vulkan" || !builds[1].Installed || !builds[1].Auto || builds[0].Installed || builds[1].SizeBytes == 0 {
		t.Fatalf("builds: %+v %v", builds, err)
	}
	// The disclosed download is the whole from-scratch install: runtime
	// archives plus the model, not the runtime alone.
	cat, err := LoadCatalog(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	build, err := cat.Resolve(rtName, "vulkan", m.platform, modelName)
	if err != nil {
		t.Fatal(err)
	}
	wantDownload := build.ModelFile.Size
	for _, a := range build.Spec.Artifacts {
		wantDownload += a.Size
	}
	if builds[0].Download != wantDownload || builds[1].Download != wantDownload {
		t.Fatalf("download bytes %d/%d, want %d (model %d)", builds[0].Download, builds[1].Download, wantDownload, build.ModelFile.Size)
	}
	if builds[1].Download <= build.ModelFile.Size {
		t.Fatal("the download disclosure omits the model")
	}
	for _, variant := range []string{"auto", "vulkan"} {
		if err := m.ValidateVariant(variant); err != nil {
			t.Fatalf("ValidateVariant(%q): %v", variant, err)
		}
	}
	if err := m.ValidateVariant("cuda"); err == nil {
		t.Fatal("a build the platform does not have was accepted")
	}
	if err := m.Verify(rtName, modelName, "vulkan"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(rtName, "vulkan"); err != nil {
		t.Fatal(err)
	}
	if builds, _ = m.Builds(); builds[1].Installed {
		t.Fatal("removed")
	}
	if err := m.Remove(rtName, "rocm"); err == nil {
		t.Fatal("unknown build")
	}
}

func TestUnsupportedPlatformAndUnknownVariant(t *testing.T) {
	w := newWorld(t, cpuOnly, nil)
	m, err := New(Options{Config: w.cfg, Jobs: jobs.NewManager(w.cfg, nil, nil), Probe: embed.Probe(w.cfg), Platform: "plan9/386"})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	if _, err := w.locate(m); !errors.Is(err, ErrUnsupported) || !errors.Is(err, engine.ErrUnavailable) {
		t.Fatalf("no build: %v", err)
	}
	if err := w.cfg.Set("sidecar.variant", "rocm"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Config: w.cfg, Jobs: jobs.NewManager(w.cfg, nil, nil), Probe: embed.Probe(w.cfg)}); err == nil || !strings.Contains(err.Error(), "rocm") {
		t.Fatalf("a variant the catalog does not have is refused at start: %v", err)
	}
}

func TestLocateReturnsAReadyEndpointDespiteACanceledCaller(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	m := w.manager()
	defer m.Stop()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	// The latch is resolved once the runtime is ready. A caller whose
	// context expires at that moment must still get the usable endpoint,
	// never a misleading ErrStarting with state "ready".
	for i := range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		ep, err := m.Locate(ctx, rtName, modelName)
		if err != nil || ep.URL == "" || ep.Key == "" {
			t.Fatalf("call %d: endpoint %+v err %v", i, ep, err)
		}
	}
}

func TestLocateTimesOutWhileStartingAndKeepsGoing(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	m := w.manager()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.Locate(ctx, rtName, modelName); !errors.Is(err, ErrStarting) {
		t.Fatalf("a caller that cannot wait: %v", err)
	}
	if _, err := w.locate(m); err != nil {
		t.Fatalf("the start went on: %v", err)
	}
}

// TestLocateReportsStartingNotNullIdle: a caller that asked for the runtime
// must never see "idle" while it is on its way. The transition is published
// at admission, before the supervisor goroutine is scheduled, so a short
// interactive budget reports an honest reason instead of a state that says
// nothing was requested.
func TestLocateReportsStartingNotNullIdle(t *testing.T) {
	w := newWorld(t, cpuOnly, map[string]any{"sidecar.threads": 1})
	m := w.manager()
	for range 20 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := m.Locate(ctx, rtName, modelName)
		if !errors.Is(err, ErrStarting) {
			t.Fatalf("a caller that cannot wait: %v", err)
		}
		if strings.Contains(err.Error(), " idle") {
			t.Fatalf("a requested runtime still reports idle: %v", err)
		}
		if !strings.Contains(err.Error(), string(StateStarting)) {
			t.Fatalf("a requested runtime does not report starting: %v", err)
		}
	}
	if _, err := w.locate(m); err != nil {
		t.Fatalf("the start went on: %v", err)
	}
}

func TestNewRefusesMissingCollaboratorsAndBadCatalogs(t *testing.T) {
	cfg := configtest.New(t, nil)
	embedtest.InstallRuntime(t, cfg, rtName, modelName)
	if _, err := New(Options{Config: cfg}); err == nil {
		t.Fatal("no jobs/prober")
	}
	for _, bad := range []string{`{"models": {"x": {"name": "../x"}}}`, `{"runtimes": {"llama-server": {"version": "../../x"}}}`} {
		if err := os.WriteFile(filepath.Join(cfg.Paths().DataDir, "runtimes.json"), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Options{Config: cfg, Jobs: jobs.NewManager(cfg, nil, nil), Probe: embed.Probe(cfg)}); err == nil {
			t.Fatalf("a bad override file is a startup error: %s", bad)
		}
	}
}

func TestChangingTheVariantRestartsTheRuntime(t *testing.T) {
	vars := map[string]fakeVariant{"cpu": {}, "vulkan": {gpu: true}}
	w := newWorld(t, vars, map[string]any{"sidecar.threads": 1, "sidecar.variant": VariantCPU})
	m := w.manager()
	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set("sidecar.variant", "vulkan"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { st := m.Status()[0]; return st.Variant == "vulkan" && st.State == StateReady })
}
