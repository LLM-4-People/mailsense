// Package configtest builds real configurations for tests: the embedded
// schema with a temp data directory, no ambient environment, and optional
// overrides applied through the normal Set path.
package configtest

import (
	"maps"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
)

// New returns a Config rooted in a fresh temp data directory.
func New(t testing.TB, overrides map[string]any) *config.Config {
	t.Helper()
	return NewWithBus(t, nil, overrides)
}

// NewWithBus is New with change notifications on bus.
func NewWithBus(t testing.TB, bus *events.Bus, overrides map[string]any) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: t.TempDir()}, Bus: bus})
	if err != nil {
		t.Fatalf("configtest: load: %v", err)
	}
	for _, key := range slices.Sorted(maps.Keys(overrides)) { // a stable order: constraints see the same steps every run
		if err := cfg.Set(key, overrides[key]); err != nil {
			t.Fatalf("configtest: set %s: %v", key, err)
		}
	}
	return cfg
}
