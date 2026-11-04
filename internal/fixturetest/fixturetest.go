// Package fixturetest imports a legacy JSONL export (the 33,842-message
// fixture, or a subset of it) into a fresh store, for fixture-tagged tests
// and local benchmarks. It is test support: no production code imports it.
package fixturetest

import (
	"os"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixtureconf"
	"github.com/LLM-4-People/mailsense/internal/source/archive"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// Path is the export named by FIXTURE_JSONL; the test is skipped without it.
func Path(t testing.TB) string {
	t.Helper()
	p := os.Getenv("FIXTURE_JSONL")
	if p == "" {
		t.Skip("FIXTURE_JSONL not set (scripts/fixture-import.sh sets it)")
	}
	return p
}

// Import syncs the export at path into a store in a fresh data directory,
// with the fixture's source configuration (fixtureconf: its mailbox
// picked, its owner declared) under overrides.
func Import(t testing.TB, path string, overrides map[string]any) (*config.Config, *store.Store) {
	t.Helper()
	settings, err := fixtureconf.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range overrides {
		settings[k] = v
	}
	cfg := configtest.New(t, settings)
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	conn, err := archive.New(fixtureconf.SourceID, path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := syncer.New(st, cfg, nil, nil).Run(t.Context(), conn, nil); err != nil || rep.Error != "" {
		t.Fatalf("fixture import: %v %s", err, rep.Error)
	}
	return cfg, st
}
