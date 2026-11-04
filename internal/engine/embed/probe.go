package embed

import (
	"context"
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Probe is the sidecar's prober: the embedder provider served by
// (runtime, model), bound to one endpoint per call, embedding texts as
// documents with every check of the adapter (finite, native length, unit
// norm). The adapter is built once per settings and providers-file
// version (and model); the endpoint travels in the call's context. It
// records no usage: probes are the runtime's own checks.
func Probe(cfg *config.Config) func(ctx context.Context, runtime, model string, ep engine.Endpoint, info engine.ModelInfo, texts []string) ([][]float32, int64, error) {
	var mu sync.Mutex
	cache := map[string]*Adapter{}
	return func(ctx context.Context, runtime, model string, ep engine.Endpoint, info engine.ModelInfo, texts []string) ([][]float32, int64, error) {
		key := fmt.Sprintf("%s|%s|%s|%+v", cfg.VersionKey(engine.ProvidersFileSetting), runtime, model, info)
		mu.Lock()
		a := cache[key]
		if a == nil {
			var err error
			if a, err = probeAdapter(cfg, runtime, model, info); err != nil {
				mu.Unlock()
				return nil, 0, err
			}
			clear(cache) // older versions are never asked for again
			cache[key] = a
		}
		mu.Unlock()
		in := make([]engine.Text, len(texts))
		for i, t := range texts {
			in[i] = engine.Text{Purpose: engine.PurposeDocument, Body: t}
		}
		res, err := a.Embed(context.WithValue(ctx, probeEndpoint{}, ep), in)
		return res.Vectors, res.Usage.InputTokens, err
	}
}

func probeAdapter(cfg *config.Config, runtime, model string, info engine.ModelInfo) (*Adapter, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, err
	}
	p, ok := servedBy(cat, runtime, model)
	if !ok {
		return nil, fmt.Errorf("embed: no %s provider is served by runtime %s with model %s", Protocol, runtime, model)
	}
	return New(p, engine.Deps{Config: cfg, Locator: probeLocator{info}})
}

// servedBy is the first embedder (by name) a runtime serves with a model.
func servedBy(cat *engine.Catalog, runtime, model string) (engine.Provider, bool) {
	for _, name := range cat.Names(engine.KindEmbedder) {
		p, _ := cat.Get(name)
		if p.Runtime == runtime && p.Model == model && p.Protocol == Protocol {
			return p, true
		}
	}
	return engine.Provider{}, false
}

type probeEndpoint struct{}

// probeLocator hands out the endpoint of the probe call (its context).
type probeLocator struct{ info engine.ModelInfo }

func (l probeLocator) Locate(ctx context.Context, _, _ string) (engine.Endpoint, error) {
	ep, ok := ctx.Value(probeEndpoint{}).(engine.Endpoint)
	if !ok {
		return engine.Endpoint{}, fmt.Errorf("%w: a probe without an endpoint", ErrNoServer)
	}
	return ep, nil
}

// Failed: a probe is the runtime's own check; its caller sees the error.
func (probeLocator) Failed(string, string, engine.Endpoint) {}

func (l probeLocator) Model(string, string) (engine.ModelInfo, error) { return l.info, nil }
