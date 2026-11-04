package sidecar

import (
	"fmt"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// learned is what the manager measures once and keeps across starts
// (state.json in sidecar.dir): the thread tuning per build and machine,
// the canary reference vector per model (the model's pinned vector or one
// measured on a cpu build), the auto choice per runtime, model and
// machine with every probe, and the builds found broken (a GPU build that
// failed its canary, its start or kept crashing) with the reason. Keys
// contain the pins, so a new pin is measured again.
type learned struct {
	Tuning  map[string]Tuning    `json:"tuning"`
	Canary  map[string][]float32 `json:"canary"`
	Choices map[string]Choice    `json:"choices"`
	Broken  map[string]Probe     `json:"broken"`
}

// Tuning is the outcome of one thread measurement.
type Tuning struct {
	Threads int `json:"threads"`
	// TokensPerSecond per measured thread count.
	TokensPerSecond map[string]float64 `json:"tokens_per_second"`
	MeasuredAt      string             `json:"measured_at"`
}

// Choice is what sidecar.variant = auto settled on, and why.
type Choice struct {
	Variant   string  `json:"variant"`
	Reason    string  `json:"reason"`
	CPU       float64 `json:"cpu_tokens_per_second,omitempty"` // 0 without a cpu build
	Probes    []Probe `json:"probes"`
	DecidedAt string  `json:"decided_at"`
}

// Probe is one GPU build's trial: its canary cosine and throughput
// against the cpu build (0 without one), or why it failed.
type Probe struct {
	Variant         string  `json:"variant"`
	Canary          float64 `json:"canary,omitempty"`
	TokensPerSecond float64 `json:"tokens_per_second,omitempty"`
	Speedup         float64 `json:"speedup,omitempty"`
	Error           string  `json:"error,omitempty"`
	At              string  `json:"at"`
}

// stateFile guards the learned state and its file.
type stateFile struct {
	mu      sync.Mutex
	path    string
	replace fsx.Replace
	data    learned
}

// loadState reads the file; a missing or unreadable one starts empty (it
// is only measurements: they are taken again).
func loadState(path string, replace fsx.Replace) (*stateFile, error) {
	s := &stateFile{path: path, replace: replace, data: learned{}}
	raw, ok, err := fsx.ReadOptional(path)
	if err == nil && ok {
		if derr := jsonx.DecodeStrict(raw, &s.data); derr != nil {
			s.data, err = learned{}, fmt.Errorf("sidecar: %s is unreadable (measured again): %w", path, derr)
		}
	}
	s.data.init()
	return s, err
}

func (d *learned) init() {
	if d.Tuning == nil {
		d.Tuning = map[string]Tuning{}
	}
	if d.Canary == nil {
		d.Canary = map[string][]float32{}
	}
	if d.Choices == nil {
		d.Choices = map[string]Choice{}
	}
	if d.Broken == nil {
		d.Broken = map[string]Probe{}
	}
}

// read runs fn on the state under the lock.
func (s *stateFile) read(fn func(*learned)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
}

// update changes the state and writes the file.
func (s *stateFile) update(fn func(*learned)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.data)
	return fsx.WriteJSON(s.path, s.data, fsx.FilePerm, s.replace)
}

// forget drops everything measured whose key starts with prefix (an
// explicit re-probe).
func (s *stateFile) forget(prefix string) error {
	return s.update(func(d *learned) {
		dropPrefix(d.Choices, prefix)
		dropPrefix(d.Broken, prefix)
		dropPrefix(d.Tuning, prefix)
		dropPrefix(d.Canary, prefix)
	})
}

func dropPrefix[V any](m map[string]V, prefix string) {
	for k := range m {
		if strings.HasPrefix(k, prefix) {
			delete(m, k)
		}
	}
}
