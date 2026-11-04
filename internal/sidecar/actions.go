package sidecar

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// UnavailableProbe is BuildInfo.Unavailable when the machine check itself
// failed (ErrVulkanProbe): retryable, unlike a missing device.
const UnavailableProbe = "probe_failed"

// BuildInfo is what the UI shows about one build of this platform.
type BuildInfo struct {
	Runtime   string   `json:"runtime"`
	Version   string   `json:"version"`
	Variant   string   `json:"variant"`
	GPU       bool     `json:"gpu"`
	Auto      bool     `json:"auto"` // tried by sidecar.variant = auto
	Installed bool     `json:"installed"`
	SizeBytes int64    `json:"size_bytes"` // on disk (0 when not installed)
	Download  int64    `json:"download_bytes"`
	Pins      []string `json:"pins"` // the archives' SHA-256s
	// Unavailable names the missing capability (RequirementVulkan) or
	// UnavailableProbe; empty when the build may run here. It is reported
	// without downloading, installing or starting anything.
	Unavailable string `json:"unavailable,omitempty"`
	Broken      *Probe `json:"broken,omitempty"`
}

// Builds lists every build of this platform with its install state (the
// runtime list of the UI; Verify checks one in full).
func (m *Manager) Builds() ([]BuildInfo, error) {
	cat, err := LoadCatalog(m.cfg)
	if err != nil {
		return nil, err
	}
	var out []BuildInfo
	for _, name := range slices.Sorted(maps.Keys(cat.Runtimes)) {
		rt := cat.Runtimes[name]
		plat := rt.Platforms[m.platform]
		for _, v := range slices.Sorted(maps.Keys(plat.Variants)) {
			b := Build{Runtime: name, RuntimeSpec: rt, Variant: v, Spec: plat.Variants[v]}
			// The card installs the catalog's pinned model; include it so the
			// disclosed download is the real from-scratch size. A catalog
			// with exactly one model is unambiguous even without a pin.
			switch {
			case cat.LocalEmbedderPin != nil && cat.LocalEmbedderPin.Runtime == name:
				if model, ok := cat.Models[cat.LocalEmbedderPin.Model]; ok {
					b.Model, b.ModelFile = cat.LocalEmbedderPin.Model, model
				}
			case len(cat.Models) == 1:
				for id, model := range cat.Models {
					b.Model, b.ModelFile = id, model
				}
			}
			out = append(out, m.buildInfo(b, slices.Contains(plat.Auto, v)))
		}
	}
	return out, nil
}

func (m *Manager) buildInfo(b Build, auto bool) BuildInfo {
	bi := BuildInfo{Runtime: b.Runtime, Version: b.RuntimeSpec.Version, Variant: b.Variant, GPU: b.Spec.GPU, Auto: auto, Pins: pins(b)}
	switch err := m.require(b); {
	case errors.Is(err, ErrNoVulkan):
		bi.Unavailable = RequirementVulkan
	case errors.Is(err, ErrVulkanProbe):
		bi.Unavailable = UnavailableProbe
	}
	for _, a := range b.Spec.Artifacts {
		bi.Download += a.Size
	}
	// The model is part of a from-scratch install; the card must not quote
	// only the runtime archive (about a tenth of the real download).
	bi.Download += b.ModelFile.Size
	if man, err := readManifest(m.layout.build(b)); err == nil && slices.Equal(man.Artifacts, bi.Pins) {
		bi.Installed, bi.SizeBytes = true, dirSize(m.layout.build(b))
	}
	m.state.read(func(d *learned) {
		if p, ok := d.Broken[brokenKey(b)]; ok {
			bi.Broken = &p
		}
	})
	return bi
}

// Verify hashes a build and its model now (the verify action).
func (m *Manager) Verify(runtime, model, variant string) error {
	b, err := m.build(runtime, model, variant)
	if err != nil {
		return err
	}
	return m.layout.verify(b)
}

// Remove deletes an installed build (the remove action). A build in use
// is refused; the model is kept (every build shares it).
func (m *Manager) Remove(runtime, variant string) error {
	cat, err := LoadCatalog(m.cfg)
	if err != nil {
		return err
	}
	rt, ok := cat.Runtimes[runtime]
	v, ok2 := rt.Platforms[m.platform].Variants[variant]
	if !ok || !ok2 {
		return fmt.Errorf("%w: no %s %s build for %s", ErrUnsupported, runtime, variant, m.platform)
	}
	b := Build{Runtime: runtime, RuntimeSpec: rt, Variant: variant, Spec: v}
	for _, st := range m.Status() {
		if st.Runtime == runtime && st.Variant == variant && (st.State == StateReady || st.State == StateStarting) {
			return fmt.Errorf("sidecar: %s %s is in use", runtime, variant)
		}
	}
	m.log.Info("runtime build removed", "runtime", runtime, "variant", variant)
	return fsx.RemoveAll(m.layout.build(b), m.replace())
}

// Reprobe forgets a runtime's auto choice, broken builds, thread tuning
// and canary references (the re-probe action, e.g. after a driver update)
// and restarts it if it runs: the next start measures again.
func (m *Manager) Reprobe(runtime string) error {
	if err := m.state.forget(runtime + "|"); err != nil {
		return err
	}
	m.forgetRequirements()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.inst {
		if in.runtime == runtime {
			in.poke(true)
		}
	}
	return nil
}

// Learned is the persisted measurements (tuning, auto choices with every
// probe, broken builds) without the canary vectors.
func (m *Manager) Learned() (map[string]Tuning, map[string]Choice, map[string]Probe) {
	var t map[string]Tuning
	var c map[string]Choice
	var b map[string]Probe
	m.state.read(func(d *learned) { t, c, b = maps.Clone(d.Tuning), maps.Clone(d.Choices), maps.Clone(d.Broken) })
	return t, c, b
}

// Status lists every runtime/model pair of the catalog (idle when this
// process has not used it) with what it does now.
func (m *Manager) Status() []Status {
	cat, _ := LoadCatalog(m.cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	seen := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(m.inst)) {
		out, seen[key] = append(out, m.inst[key].snapshot()), true
	}
	if cat == nil {
		return out
	}
	for _, rt := range slices.Sorted(maps.Keys(cat.Runtimes)) {
		for _, model := range slices.Sorted(maps.Keys(cat.Models)) {
			if !seen[rt+"/"+model] {
				out = append(out, Status{Runtime: rt, Model: model, Requested: m.cfg.String("sidecar.variant"), State: StateIdle})
			}
		}
	}
	return out
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.Mode().Type() != os.ModeSymlink {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
