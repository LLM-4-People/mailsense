package triage

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/decision"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
)

// DeciderSource builds the decider triage.decider names, per run, so a
// changed setting or providers file takes effect without a restart.
type DeciderSource interface {
	Decider(cfg *config.Config) (engine.Decider, *engine.Catalog, error)
}

// ErrUnknownProvider marks a provider test naming a record that does not
// exist. A non-decider is no longer an error: the test dispatches on the
// record's kind.
var ErrUnknownProvider = errors.New("triage: unknown provider")

// ProviderState is one catalog provider with its live health.
type ProviderState struct {
	Provider engine.Provider
	Status   engine.ProviderStatus
}

// Engines is the production DeciderSource and the provider admin surface:
// the protocol registry of every engine kind, the adapters' dependencies,
// and the one provider health monitor every call reports to (it forwards
// usage to the store's counters).
type Engines struct {
	registry *engine.Registry
	deps     engine.Deps
	monitor  *engine.Monitor
}

// NewEngines wires the registry and the monitor; bus, sink, locator and
// clk may be nil. deps.Monitor, when set, is the app-wide health registry.
func NewEngines(deps engine.Deps) *Engines {
	reg := engine.NewRegistry()
	decision.Register(reg)
	embed.Register(reg)
	generate.Register(reg)
	deps = engine.WithMonitor(deps)
	return &Engines{registry: reg, deps: deps, monitor: deps.Monitor}
}

// Deps returns the adapter dependencies (the provider test uses them).
func (e *Engines) Deps() engine.Deps { return e.deps }

// errNoDecider is the one "no decider configured" problem: the catalog has no
// chosen decider, and the fix names the setting.
func errNoDecider() error {
	return fmt.Errorf("%w: no triage decider is configured (set %s)", engine.ErrNotConfigured, DeciderSetting)
}

// Decider loads the catalog and builds the configured decider.
func (e *Engines) Decider(cfg *config.Config) (engine.Decider, *engine.Catalog, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, nil, err
	}
	id := cfg.String(DeciderSetting)
	if id == "" {
		return nil, cat, errNoDecider()
	}
	d, err := e.registry.Decider(cat, id, e.deps)
	return d, cat, err
}

// Providers lists every provider of the catalog with its health.
func (e *Engines) Providers(cfg *config.Config) ([]ProviderState, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderState, 0)
	for _, p := range cat.List() {
		out = append(out, ProviderState{Provider: p, Status: e.monitor.Status(p, e.deps.Secrets)})
	}
	return out, nil
}

// StatusOf reports one provider's key presence and health.
func (e *Engines) StatusOf(cfg *config.Config, id string) (engine.ProviderStatus, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return engine.ProviderStatus{}, err
	}
	p, err := cat.Select(id, "")
	if err != nil {
		if errors.Is(err, engine.ErrUnknownProvider) {
			return engine.ProviderStatus{}, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
		}
		return engine.ProviderStatus{}, err
	}
	return e.monitor.Status(p, e.deps.Secrets), nil
}

// TestNamed tests the named record with the probe of its kind (decider,
// generator or embedder): the backend never restricts tests to deciders.
func (e *Engines) TestNamed(ctx context.Context, cfg *config.Config, id string) (engine.ProviderTestResult, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return engine.ProviderTestResult{}, err
	}
	if _, err := cat.Select(id, ""); err != nil {
		if errors.Is(err, engine.ErrUnknownProvider) {
			return engine.ProviderTestResult{}, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
		}
		return engine.ProviderTestResult{}, err
	}
	return e.registry.TestProvider(ctx, cat, id, e.deps)
}

// Status reports the configured provider (key present, health).
func (e *Engines) Status(cfg *config.Config) (engine.ProviderStatus, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return engine.ProviderStatus{}, err
	}
	id := cfg.String(DeciderSetting)
	if id == "" {
		return engine.ProviderStatus{}, errNoDecider()
	}
	p, err := cat.Select(id, engine.KindDecider)
	if err != nil {
		return engine.ProviderStatus{}, err
	}
	return e.monitor.Status(p, e.deps.Secrets), nil
}

// TestProvider tests the configured decider (the live check).
func (e *Engines) TestProvider(ctx context.Context, cfg *config.Config) (engine.ProviderTestResult, error) {
	id := cfg.String(DeciderSetting)
	if id == "" {
		return engine.ProviderTestResult{}, errNoDecider()
	}
	return e.TestNamed(ctx, cfg, id)
}

// Monitor is the app-wide health registry, shared by the API's provider
// list and every engine source.
func (e *Engines) Monitor() *engine.Monitor { return e.monitor }
