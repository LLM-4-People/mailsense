package qa

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
)

// Source is the production Q&A generator factory. It builds the catalog and
// the generator protocol registry once, then caches the configured generator
// until the configuration snapshot (or the providers file) changes. A
// missing record or key is ErrUnavailable, never a panic and never a paid
// call.
type Source struct {
	deps engine.Deps
	reg  *engine.Registry

	mu  sync.Mutex
	key string
	gen Generator
}

// NewSource returns a source over deps (Config and Secrets are required to
// build a generator; Bus and Sink carry its usage).
func NewSource(deps engine.Deps) *Source {
	reg := engine.NewRegistry()
	generate.Register(reg)
	return &Source{deps: deps, reg: reg}
}

// Generator builds the configured qa.provider with the Q&A output cap: the
// answer budget plus, for a reasoning model, the effort's reasoning
// allowance, so the call cannot spend its whole cap thinking and return a
// null content. An empty selector, an unknown record, the wrong protocol
// kind or a missing key is wrapped in ErrUnavailable; the service turns that
// into an explicit retrieval answer.
func (s *Source) Generator() (Generator, error) {
	if s.deps.Config == nil || s.deps.Secrets == nil {
		return nil, fmt.Errorf("%w: a config and secret store are required", ErrUnavailable)
	}
	cfg := s.deps.Config
	id := cfg.String("qa.provider")
	if id == "" {
		return nil, fmt.Errorf("%w: no Q&A generator is configured (set qa.provider)", ErrUnavailable)
	}
	key := cfg.VersionKey(engine.ProvidersFileSetting) + "|" + id +
		"|" + strconv.Itoa(cfg.Int("qa.max_answer_tokens"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != nil && s.key == key {
		return s.gen, nil
	}
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: provider catalog: %v", ErrUnavailable, err)
	}
	p, err := cat.Select(id, engine.KindGenerator)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	p.ReserveOutputTokens = p.AnswerOutputTokens(cfg.Int("qa.max_answer_tokens"))
	if p.APIKeySecret != "" {
		value, err := s.deps.Secrets.Get(p.APIKeySecret)
		if err != nil || value == "" {
			return nil, fmt.Errorf("%w: the key of %q is not configured", ErrUnavailable, id)
		}
	}
	gen, err := s.reg.GeneratorFor(p, s.deps)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.key, s.gen = key, gen
	return gen, nil
}

// ensure the source is a GeneratorSource.
var _ GeneratorSource = (*Source)(nil)
