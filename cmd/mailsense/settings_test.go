package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

// A variant the catalog does not have on this platform must be refused by the
// settings write, before it can be persisted: otherwise the next launch fails
// in sidecar.New before the API exists, and the owner cannot undo the value
// from the UI.
func TestSettingsRejectsAnUnknownSidecarVariant(t *testing.T) {
	cfg := configtest.New(t, nil)
	m, err := sidecar.New(sidecar.Options{
		Config:   cfg,
		Jobs:     jobs.NewManager(cfg, nil, nil),
		Probe:    embed.Probe(cfg),
		Platform: "windows/amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Stop)
	settings := settingsAPI{&wire{app: &app{cfg: cfg, sidecar: m}}}

	before := cfg.String("sidecar.variant")
	_, err = settings.Set("sidecar.variant", json.RawMessage(`"cuda"`))
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Code != "invalid_value" {
		t.Fatalf("cuda on Windows: %v", err)
	}
	if got := cfg.String("sidecar.variant"); got != before {
		t.Fatalf("the rejected variant was saved: %q", got)
	}

	// A build the catalog does have is saved.
	view, err := settings.Set("sidecar.variant", json.RawMessage(`"vulkan"`))
	if err != nil {
		t.Fatalf("vulkan: %v", err)
	}
	if got := cfg.String("sidecar.variant"); got != "vulkan" {
		t.Fatalf("saved variant %q (view %+v)", got, view)
	}
	if _, err := settings.Set("sidecar.variant", json.RawMessage(`"auto"`)); err != nil {
		t.Fatalf("auto: %v", err)
	}
}
