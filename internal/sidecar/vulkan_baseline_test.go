package sidecar

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// TestGPUBuildWithoutRequirementInstallsAndStarts is the round 25 control:
// a GPU variant whose catalog entry has no `requires` field is the shape
// every fake catalog and every pre-round custom catalog has. It must keep
// downloading, installing and starting after the Vulkan preflight exists,
// because the preflight is opt-in data, never a blanket GPU rule.
//
// It also records the synthetic install path's exact cost (requests, body
// bytes, install jobs, started processes and states) so the guarded no-
// Vulkan measurement is compared against a measured control. The sizes are
// the fake artifact server's, not the real catalog's 342,039,075 B.
func TestGPUBuildWithoutRequirementInstallsAndStarts(t *testing.T) {
	starts := filepath.Join(t.TempDir(), "started.txt")
	w := newWorld(t, noCPUVars(map[string]string{"FAKE_ENV_DUMP": starts}),
		map[string]any{"sidecar.variant": "vulkan", "sidecar.threads": 1}, "vulkan")
	w.pinCanary(t, embedtest.Vector(builtinCatalog(t).Canary, embedtest.Dims))
	jm := jobs.NewManager(w.cfg, w.bus, nil)
	m := w.managerWithJobs(jm)
	seen := w.states()
	order, err := events.Subscribe(w.bus, TopicState, 256)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.locate(m); err != nil {
		t.Fatal(err)
	}

	installs := 0
	for _, info := range jm.List() {
		if info.Kind == "sidecar.install" {
			installs++
		}
	}
	states := seen()
	stateList := make([]State, 0, len(states))
	for state := range states {
		stateList = append(stateList, state)
	}
	slices.Sort(stateList)
	var sequence []State
	for len(order.C) > 0 {
		sequence = append(sequence, (<-order.C).State)
	}

	_, started := os.Stat(starts)
	modelCalls, archiveCalls := w.srv.count(w.model.Name), w.srv.count(w.rt.Name)
	t.Logf("round25 control (synthetic): model_requests=%d archive_requests=%d served_bytes=%d model_size=%d archive_size=%d install_jobs=%d start_marker=%v states=%v state_order=%v",
		modelCalls, archiveCalls, w.srv.served(), w.model.Size, w.rt.Size, installs, started == nil, stateList, sequence)

	if modelCalls != 1 || archiveCalls != 1 {
		t.Errorf("the control downloads the model and the archive once: %d, %d", modelCalls, archiveCalls)
	}
	if want := w.model.Size + w.rt.Size; w.srv.served() != want {
		t.Errorf("served %d body bytes, want the two artifacts' %d", w.srv.served(), want)
	}
	if installs != 1 {
		t.Errorf("the control starts %d install jobs, want 1", installs)
	}
	if started != nil {
		t.Errorf("the control's runtime did not start: %v", started)
	}
	if st := m.Status()[0]; st.State != StateReady || st.PID == 0 {
		t.Errorf("the control's runtime is not ready: %+v", st)
	}
	for _, state := range []State{StateInstalling, StateStarting, StateCanary, StateReady} {
		if !states[state] {
			t.Errorf("the control did not publish %s (%v)", state, stateList)
		}
	}
}

// TestFakeArtifactsAreNotTheRealCatalogTotal keeps the synthetic measurement
// honest: the fake server serves small files, so its byte counts must never
// be reported as the real download.
func TestFakeArtifactsAreNotTheRealCatalogTotal(t *testing.T) {
	cat, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cat.Resolve(rtName, "vulkan", "windows/amd64", modelName)
	if err != nil {
		t.Fatal(err)
	}
	total := b.ModelFile.Size
	for _, a := range b.Spec.Artifacts {
		total += a.Size
	}
	const realTotal = int64(342_039_075)
	if total != realTotal {
		t.Fatalf("the real Windows install is %d B, want %d", total, realTotal)
	}
	fakeModel := int64(4 * 4096)
	if fakeModel >= realTotal {
		t.Fatalf("the fake model (%d B) is not distinguishable from the real install", fakeModel)
	}
	t.Logf("real Windows catalog total %d B (model %d + archive %d), the fake model is %d B",
		total, b.ModelFile.Size, b.Spec.Artifacts[0].Size, fakeModel)
}
