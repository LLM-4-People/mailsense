package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

var log = logx.For("engine")

// Deps are what an adapter needs from the app. Bus, Sink, Clock and
// Locator may be nil (no events, no persisted counters, wall clock, no
// managed runtimes).
type Deps struct {
	Config  *config.Config
	Bus     *events.Bus
	Secrets secrets.Store
	Sink    UsageSink
	Clock   clock.Clock
	Locator Locator
	// Monitor is the app-wide provider health registry when one exists.
	// An engine source without one builds its own.
	Monitor *Monitor
}

// DeciderFactory builds a Decider for a provider of its protocol.
type DeciderFactory func(p Provider, deps Deps) (Decider, error)

// EmbedderFactory builds an Embedder for a provider of its protocol.
type EmbedderFactory func(p Provider, deps Deps) (Embedder, error)

// GeneratorFactory builds a Generator for a provider of its protocol.
type GeneratorFactory func(p Provider, deps Deps) (Generator, error)

// Registry maps protocols to adapters. Each adapter package registers its
// protocols explicitly (decision.Register, embed.Register,
// generate.Register) — no init-time magic.
type Registry struct {
	deciders   map[string]DeciderFactory
	embedders  map[string]EmbedderFactory
	generators map[string]GeneratorFactory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{deciders: map[string]DeciderFactory{}, embedders: map[string]EmbedderFactory{}, generators: map[string]GeneratorFactory{}}
}

// RegisterDecider adds a decider protocol; registering one twice panics
// (a wiring bug).
func (r *Registry) RegisterDecider(protocol string, f DeciderFactory) {
	if _, dup := r.deciders[protocol]; dup || f == nil {
		panic(fmt.Sprintf("engine: decider protocol %q registered twice or without a factory", protocol))
	}
	r.deciders[protocol] = f
}

// RegisterEmbedder adds an embedder protocol; registering one twice panics.
func (r *Registry) RegisterEmbedder(protocol string, f EmbedderFactory) {
	if _, dup := r.embedders[protocol]; dup || f == nil {
		panic(fmt.Sprintf("engine: embedder protocol %q registered twice or without a factory", protocol))
	}
	r.embedders[protocol] = f
}

// RegisterGenerator adds a generator protocol; registering one twice panics.
func (r *Registry) RegisterGenerator(protocol string, f GeneratorFactory) {
	if _, dup := r.generators[protocol]; dup || f == nil {
		panic(fmt.Sprintf("engine: generator protocol %q registered twice or without a factory", protocol))
	}
	r.generators[protocol] = f
}

// Protocols lists the registered protocols of a kind.
func (r *Registry) Protocols(kind Kind) []string {
	switch kind {
	case KindEmbedder:
		return slices.Sorted(maps.Keys(r.embedders))
	case KindGenerator:
		return slices.Sorted(maps.Keys(r.generators))
	default:
		return slices.Sorted(maps.Keys(r.deciders))
	}
}

// Decider builds the named decider of the catalog.
func (r *Registry) Decider(cat *Catalog, id string, deps Deps) (Decider, error) {
	p, f, err := lookup(r, cat, id, KindDecider, r.deciders, deps)
	if err != nil {
		return nil, err
	}
	d, err := f(p, deps)
	if err != nil {
		return nil, err
	}
	log.Info("decider ready", "provider", id, "protocol", p.Protocol, "model", p.Model,
		"max_questions", p.MaxQuestions, "context_tokens", p.ContextTokens)
	return d, nil
}

// Embedder builds the named embedder of the catalog.
func (r *Registry) Embedder(cat *Catalog, id string, deps Deps) (Embedder, error) {
	p, f, err := lookup(r, cat, id, KindEmbedder, r.embedders, deps)
	if err != nil {
		return nil, err
	}
	e, err := f(p, deps)
	if err != nil {
		return nil, err
	}
	log.Info("embedder ready", "provider", id, "protocol", p.Protocol, "model", p.Model, "runtime", p.Runtime,
		"dimensions", e.Dimensions(), "space", e.Space())
	return e, nil
}

// Generator builds the named generator of the catalog.
func (r *Registry) Generator(cat *Catalog, id string, deps Deps) (Generator, error) {
	p, _, err := lookup(r, cat, id, KindGenerator, r.generators, deps)
	if err != nil {
		return nil, err
	}
	return r.GeneratorFor(p, deps)
}

// GeneratorFor builds a generator from an explicit provider record (the
// catalog's, possibly with a caller's limit adjustment such as the Q&A
// output cap). It is the one construction path of Generator.
func (r *Registry) GeneratorFor(p Provider, deps Deps) (Generator, error) {
	if p.Kind() != KindGenerator {
		return nil, fmt.Errorf("engine: provider %q is a %s, not a %s", p.ID, p.Kind(), KindGenerator)
	}
	f, ok := r.generators[p.Protocol]
	if !ok {
		return nil, fmt.Errorf("engine: provider %q uses protocol %q; supported: %v", p.ID, p.Protocol, r.Protocols(KindGenerator))
	}
	if deps.Config == nil || deps.Secrets == nil {
		return nil, fmt.Errorf("engine: provider %q needs a config and a secret store", p.ID)
	}
	g, err := f(p, deps)
	if err != nil {
		return nil, err
	}
	log.Info("generator ready", "provider", p.ID, "protocol", p.Protocol, "model", p.Model,
		"context_tokens", p.ContextTokens)
	return g, nil
}

// ProviderTestResult is the generic answer of a provider test: no key, no
// mail and no answer content is ever returned. DetailCode is the stable
// machine code the UI translates; Detail stays the English fallback.
type ProviderTestResult struct {
	OK           bool              `json:"ok"`
	LatencyMS    int64             `json:"latency_ms"`
	Model        string            `json:"model"`
	Detail       string            `json:"detail"`
	DetailCode   string            `json:"detail_code,omitempty"`
	DetailParams map[string]string `json:"detail_params,omitempty"`
}

// TestProvider sends the protocol's probe (decider), one tiny chat call
// (generator) or one short embedding (embedder) through the named record.
// The key is checked before any call; its value is never returned.
func (r *Registry) TestProvider(ctx context.Context, cat *Catalog, id string, deps Deps) (ProviderTestResult, error) {
	p, err := cat.Select(id, "")
	if err != nil {
		return ProviderTestResult{}, err
	}
	if deps.Secrets == nil {
		return ProviderTestResult{}, fmt.Errorf("%w: record %q needs a secret store", ErrTestFailed, id)
	}
	if p.APIKeySecret != "" {
		value, err := deps.Secrets.Get(p.APIKeySecret)
		if err != nil || value == "" {
			return ProviderTestResult{}, fmt.Errorf("%w: record %q has no usable key", ErrNoKey, id)
		}
	}
	res := ProviderTestResult{Model: p.Model}
	start := time.Now()
	switch p.Kind() {
	case KindDecider:
		proto, ok := ProtocolOf(p.Protocol)
		if !ok || proto.Probe == nil {
			return res, fmt.Errorf("%w: protocol %q has no probe", ErrTestFailed, p.Protocol)
		}
		d, err := r.Decider(cat, id, deps)
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		decided, err := d.Decide(ctx, proto.Probe.State, []Question{proto.Probe.Question})
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		if decided.Model != "" {
			res.Model = decided.Model
		}
		if _, ok := decided.Answers[proto.Probe.Question.Name]; !ok {
			return res, fmt.Errorf("%w: the decider returned no probe answer", ErrTestFailed)
		}
		res.Detail = "decider answered the probe"
		res.DetailCode = "decider_answered"
	case KindGenerator:
		g, err := r.Generator(cat, id, deps)
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		gen, err := g.Generate(ctx, "You are a connection test.", "Reply with the single word OK.")
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		if strings.TrimSpace(gen.Content) == "" {
			return res, fmt.Errorf("%w: the generator returned empty content", ErrTestFailed)
		}
		if gen.Model != "" {
			res.Model = gen.Model
		}
		if p.ResponseFormat == ResponseFormatJSON && !json.Valid([]byte(strings.TrimSpace(gen.Content))) {
			return res, fmt.Errorf("%w: the generator did not return valid JSON in JSON mode", ErrTestFailed)
		}
		res.Detail = "generator answered"
		res.DetailCode = "generator_answered"
	case KindEmbedder:
		e, err := r.Embedder(cat, id, deps)
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		emb, err := e.Embed(ctx, []Text{{Purpose: PurposeDocument, Body: "connection test"}})
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			return res, fmt.Errorf("%w: %w", ErrTestFailed, err)
		}
		if len(emb.Vectors) != 1 {
			return res, fmt.Errorf("%w: the embedder returned %d vectors for one text", ErrTestFailed, len(emb.Vectors))
		}
		vec := emb.Vectors[0]
		if len(vec) != e.Dimensions() {
			return res, fmt.Errorf("%w: the embedder returned %d dimensions, want %d", ErrTestFailed, len(vec), e.Dimensions())
		}
		tolerance := p.Embedding.NormTolerance
		norm := 0.0
		for _, x := range vec {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return res, fmt.Errorf("%w: the embedder returned a non-finite vector", ErrTestFailed)
			}
			norm += float64(x) * float64(x)
		}
		if math.Abs(math.Sqrt(norm)-1) > tolerance {
			return res, fmt.Errorf("%w: the embedder returned a vector whose norm is not 1", ErrTestFailed)
		}
		res.Detail = fmt.Sprintf("embedder returned one %d-dimensional unit vector", len(vec))
		res.DetailCode = "embedder_answered"
		res.DetailParams = map[string]string{"dimensions": strconv.Itoa(len(vec))}
	default:
		return res, fmt.Errorf("%w: record %q has no engine kind", ErrTestFailed, id)
	}
	res.OK = true
	return res, nil
}

// lookup finds a provider of a kind and the factory of its protocol.
func lookup[F any](r *Registry, cat *Catalog, id string, kind Kind, factories map[string]F, deps Deps) (Provider, F, error) {
	var zero F
	p, err := cat.Select(id, kind)
	if err != nil {
		return p, zero, err
	}
	f, ok := factories[p.Protocol]
	if !ok {
		return p, zero, fmt.Errorf("engine: provider %q uses protocol %q; supported: %v", id, p.Protocol, r.Protocols(kind))
	}
	if deps.Config == nil || deps.Secrets == nil {
		return p, zero, fmt.Errorf("engine: provider %q needs a config and a secret store", id)
	}
	return p, f, nil
}

// ErrorTextChars is http.error_text_chars: how much of a server's error body
// an error quotes and the quota probe reads.
func (d Deps) ErrorTextChars() int { return errorTextChars(d.Config) }

// errorTextChars is the one reader of the setting. It never clamps: mail.Head
// treats a non-positive limit as an empty string, as every caller expects.
func errorTextChars(cfg *config.Config) int { return cfg.Int("http.error_text_chars") }

// Record publishes one provider call on TopicUsage and persists it through
// the Sink (a failure to persist is logged): the one usage path of every
// adapter.
func (d Deps) Record(ctx context.Context, rec UsageRecord) {
	if d.Bus != nil {
		events.Publish(d.Bus, TopicUsage, rec)
	}
	if d.Sink != nil {
		if err := d.Sink.AddUsage(context.WithoutCancel(ctx), rec); err != nil {
			log.Error("usage counters not saved", "provider", rec.Provider, "err", err)
		}
	}
}
