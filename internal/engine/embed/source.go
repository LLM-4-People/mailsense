package embed

import (
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Source builds the embedder embed.provider names from the catalog and
// the current settings, and keeps it while neither changed: the index
// and every search share one HTTP client (kept-alive connections), and a
// changed setting or providers file applies to the next call without a
// restart. It is also the provider monitor the adapter reports to.
type Source struct {
	deps     engine.Deps
	monitor  *engine.Monitor
	registry *engine.Registry
	mu       sync.Mutex
	stamp    string
	cur      engine.Embedder
}

// NewSource wires the registry and the health monitor (usage goes on to
// deps.Sink, the store's counters). deps.Monitor, when set, is the
// app-wide health registry.
func NewSource(deps engine.Deps) *Source {
	reg := engine.NewRegistry()
	Register(reg)
	deps = engine.WithMonitor(deps)
	return &Source{deps: deps, monitor: deps.Monitor, registry: reg}
}

// Embedder returns the configured embedder (built once per settings and
// providers-file version). An empty selector or a missing record is an
// explicit ErrUnavailable: the embeddings stage reports it as a blocker.
func (s *Source) Embedder() (engine.Embedder, error) {
	cfg := s.deps.Config
	id := cfg.String("embed.provider")
	if id == "" {
		return nil, fmt.Errorf("%w: no embedding provider is configured (set embed.provider)", engine.ErrUnavailable)
	}
	stamp := cfg.VersionKey(engine.ProvidersFileSetting) + "|" + id
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && s.stamp == stamp {
		return s.cur, nil
	}
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", engine.ErrUnavailable, err)
	}
	e, err := s.registry.Embedder(cat, id, s.deps)
	if err != nil {
		return nil, err
	}
	s.cur, s.stamp = e, stamp
	return e, nil
}

// Status reports the configured embedder's health (last call, failures).
func (s *Source) Status() (engine.ProviderStatus, error) {
	id := s.deps.Config.String("embed.provider")
	if id == "" {
		return engine.ProviderStatus{}, fmt.Errorf("%w: no embedding provider is configured (set embed.provider)", engine.ErrUnavailable)
	}
	cat, err := engine.LoadCatalog(s.deps.Config)
	if err != nil {
		return engine.ProviderStatus{}, err
	}
	p, err := cat.Select(id, engine.KindEmbedder)
	if err != nil {
		return engine.ProviderStatus{}, fmt.Errorf("embed: %w", err)
	}
	return s.monitor.Status(p, s.deps.Secrets), nil
}
