package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// vulkanWorld is the Windows shape (one Vulkan build, no cpu) whose
// catalog entry declares the preflight, over an injected probe.
type vulkanTestWorld struct {
	w           *world
	jobs        *jobs.Manager
	m           *Manager
	seen        func() map[State]bool
	startMarker string
}

func newVulkanTestWorld(t *testing.T, variant string, probe VulkanProbe) *vulkanTestWorld {
	t.Helper()
	starts := filepath.Join(t.TempDir(), "started.txt")
	vars := map[string]fakeVariant{"vulkan": {gpu: true, requires: RequirementVulkan,
		env: map[string]string{"FAKE_ENV_DUMP": starts}}}
	w := newWorld(t, vars, map[string]any{"sidecar.variant": variant, "sidecar.threads": 1}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	jm := jobs.NewManager(w.cfg, w.bus, nil)
	return &vulkanTestWorld{w: w, jobs: jm, m: w.managerWith(jm, probe), seen: w.states(), startMarker: starts}
}

func (v *vulkanTestWorld) installs() int {
	n := 0
	for _, info := range v.jobs.List() {
		if info.Kind == "sidecar.install" {
			n++
		}
	}
	return n
}

// Round 25: a machine with no Vulkan device must fail before the model or
// the runtime archive is requested, for both auto and the named build.
func TestNoVulkanFailsBeforeAnyDownload(t *testing.T) {
	for _, variant := range []string{VariantAuto, RequirementVulkan} {
		t.Run(variant, func(t *testing.T) {
			var probes atomic.Int64
			v := newVulkanTestWorld(t, variant, func() error {
				probes.Add(1)
				return ErrNoVulkan
			})
			_, err := v.w.locate(v.m)
			if !errors.Is(err, ErrNoVulkan) {
				t.Fatalf("locate error %v, want ErrNoVulkan", err)
			}
			if got := v.w.srv.count(v.w.model.Name); got != 0 {
				t.Errorf("model requests = %d, want 0", got)
			}
			if got := v.w.srv.count(v.w.rt.Name); got != 0 {
				t.Errorf("archive requests = %d, want 0", got)
			}
			if got := v.w.srv.served(); got != 0 {
				t.Errorf("served bytes = %d, want 0", got)
			}
			if got := v.installs(); got != 0 {
				t.Errorf("install jobs = %d, want 0", got)
			}
			states := v.seen()
			for _, forbidden := range []State{StateInstalling, StateProbing, StateStarting, StateReady} {
				if states[forbidden] {
					t.Errorf("state %s was published on a machine with no Vulkan device (%v)", forbidden, states)
				}
			}
			st := v.m.Status()[0]
			if st.State != StateFailed || !strings.Contains(strings.ToLower(st.Detail), "vulkan") {
				t.Errorf("status is not an explicit failure: %+v", st)
			}
			if st.PID != 0 || st.Restarts != 0 {
				t.Errorf("nothing ran: pid %d, restarts %d", st.PID, st.Restarts)
			}
			if _, err := os.Stat(v.startMarker); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the runtime start marker exists: %v", err)
			}
			for range 2 {
				builds, err := v.m.Builds()
				if err != nil {
					t.Fatal(err)
				}
				if len(builds) != 1 || builds[0].Unavailable != RequirementVulkan || builds[0].Installed {
					t.Fatalf("builds: %+v", builds)
				}
				if builds[0].Download != v.w.model.Size+v.w.rt.Size {
					t.Errorf("download disclosure %d, want the catalog total %d", builds[0].Download, v.w.model.Size+v.w.rt.Size)
				}
			}
			// A missing device is not cached: the failed start and each
			// Builds read probe again, so the card's Refresh re-probes.
			beforeReprobe := probes.Load()
			if beforeReprobe != 3 {
				t.Errorf("probe calls after start + 2 Builds = %d, want 3 (a missing device is re-probed)", beforeReprobe)
			}
			if err := v.m.Reprobe(rtName); err != nil {
				t.Fatal(err)
			}
			if _, err := v.m.Builds(); err != nil {
				t.Fatal(err)
			}
			if got := probes.Load(); got != 4 {
				t.Errorf("probe calls after Reprobe + Builds = %d, want 4", got)
			}
			t.Logf("round25 %s: model=0 archive=0 served=0 install_jobs=0 states=%v probes_before_reprobe=%d probes_after_reprobe=%d",
				variant, states, beforeReprobe, probes.Load())
		})
	}
}

// Control: the preflight is data, not a blanket GPU rule. A Vulkan build
// that declares requires and a machine that passes installs normally.
func TestVulkanPresentInstallsNormally(t *testing.T) {
	var probes atomic.Int64
	v := newVulkanTestWorld(t, "vulkan", func() error {
		probes.Add(1)
		return nil
	})
	if _, err := v.w.locate(v.m); err != nil {
		t.Fatal(err)
	}
	if v.w.srv.count(v.w.model.Name) != 1 || v.w.srv.count(v.w.rt.Name) != 1 || v.w.srv.served() != v.w.model.Size+v.w.rt.Size {
		t.Fatalf("model %d, archive %d, bytes %d", v.w.srv.count(v.w.model.Name), v.w.srv.count(v.w.rt.Name), v.w.srv.served())
	}
	if got := v.installs(); got != 1 {
		t.Fatalf("install jobs = %d, want 1", got)
	}
	if _, err := os.Stat(v.startMarker); err != nil {
		t.Fatalf("the runtime did not start: %v", err)
	}
	builds, err := v.m.Builds()
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0].Unavailable != "" || !builds[0].Installed {
		t.Fatalf("builds: %+v", builds)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe calls after start + Builds = %d, want 1 (a confirmed device is cached)", got)
	}
	if st := v.m.Status()[0]; st.State != StateReady || st.PID == 0 {
		t.Fatalf("status: %+v", st)
	}
	t.Logf("round25 vulkan present: model=1 archive=1 served=%d install_jobs=1 probes=%d (cached)", v.w.srv.served(), probes.Load())
}

// A probe that itself fails is retryable and must not be reported as a
// missing device, nor cached as one.
func TestVulkanProbeErrorIsExplicit(t *testing.T) {
	var probes atomic.Int64
	v := newVulkanTestWorld(t, "vulkan", func() error {
		probes.Add(1)
		return errors.New("loader exploded")
	})
	job, err := v.m.Install(rtName, modelName, "vulkan")
	if job != nil || !errors.Is(err, ErrVulkanProbe) || errors.Is(err, ErrNoVulkan) {
		t.Fatalf("Install = %v, %v; want nil, ErrVulkanProbe", job, err)
	}
	if got := v.w.srv.served(); got != 0 {
		t.Errorf("served bytes = %d, want 0", got)
	}
	if got := v.installs(); got != 0 {
		t.Errorf("install jobs = %d, want 0", got)
	}
	builds, err := v.m.Builds()
	if err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0].Unavailable != UnavailableProbe || builds[0].Installed {
		t.Fatalf("builds: %+v", builds)
	}
	if _, err := v.m.Install(rtName, modelName, "vulkan"); !errors.Is(err, ErrVulkanProbe) {
		t.Fatalf("a second Install: %v", err)
	}
	if got := probes.Load(); got != 3 {
		t.Errorf("probe calls = %d, want 3 (a probe failure is never cached)", got)
	}
	t.Logf("round25 probe error: served=0 install_jobs=0 probes=%d", probes.Load())
}

// The install action is refused before a job exists.
func TestInstallActionRefusesNoVulkan(t *testing.T) {
	v := newVulkanTestWorld(t, "vulkan", func() error { return ErrNoVulkan })
	job, err := v.m.Install(rtName, modelName, "vulkan")
	if job != nil || !errors.Is(err, ErrNoVulkan) {
		t.Fatalf("Install = %v, %v; want nil, ErrNoVulkan", job, err)
	}
	if v.w.srv.served() != 0 || v.installs() != 0 {
		t.Fatalf("served %d bytes, %d install jobs", v.w.srv.served(), v.installs())
	}
}
