package judge

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// costHeaderChars is the header and observed-facts allowance of the
// action's per-message cost estimate (the profile's body_chars is the
// body): the real run reserves each call's actual ceiling through the
// generator's budget guard.
const costHeaderChars = 2000

// errConfig marks a source built without what a judge needs.
var errConfig = errors.New("judge: a config and a secret store are required")

// Source is the production judge factory: the generator registry, the
// provider catalog and the profile of the configured install. It caches
// the per-message cost of the action's estimate until the configuration
// snapshot or one of its data files changes (the stage asks for it once
// per short question).
type Source struct {
	deps engine.Deps
	reg  *engine.Registry

	mu        sync.Mutex
	costKey   string
	perMsg    float64
	costKnown bool
}

// NewSource returns a source over deps (Config and Secrets are required to
// build a judge; Bus and Sink carry its usage).
func NewSource(deps engine.Deps) *Source {
	reg := engine.NewRegistry()
	generate.Register(reg)
	return &Source{deps: deps, reg: reg}
}

// Judge builds the judge of a provider record; "" is the configured
// judge.provider.
func (s *Source) Judge(provider string) (Judge, error) {
	j, _, err := s.build(provider)
	return j, err
}

// Cost estimates what judging count messages costs: one call per message
// with every profile question, the profile's body budget plus the header
// allowance as the state. It is the run_judge action's number; a run
// still refuses before spending (Budget.Refuse) and reserves every real
// call. Unknown when the provider has no price or cannot be built.
func (s *Source) Cost(count int) (float64, bool) {
	if count <= 0 {
		return 0, true
	}
	per, known := s.perMessage()
	if !known {
		return 0, false
	}
	return per * float64(count), true
}

// perMessage is one message's estimate with every profile question,
// cached per configuration version.
func (s *Source) perMessage() (float64, bool) {
	key := s.deps.Config.VersionKey(engine.ProvidersFileSetting, taxonomy.ProfileFileSetting)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.costKey == key {
		return s.perMsg, s.costKnown
	}
	s.costKey, s.costKnown, s.perMsg = key, false, 0
	j, profile, err := s.build("")
	if err != nil {
		return 0, false
	}
	state := strings.Repeat("x", profile.BodyChars+costHeaderChars)
	est, err := j.Plan(state, profile.Questions)
	if err != nil || !est.PriceKnown || est.Calls != 1 {
		return 0, false
	}
	s.perMsg, s.costKnown = est.EstimatedUSD, true
	return s.perMsg, true
}

// build is the one construction path of Judge and Cost.
func (s *Source) build(provider string) (Judge, *taxonomy.Profile, error) {
	if s.deps.Config == nil || s.deps.Secrets == nil {
		return nil, nil, errConfig
	}
	if provider == "" {
		provider = s.deps.Config.String(SettingProvider)
	}
	if provider == "" {
		return nil, nil, fmt.Errorf("judge: no chat generator is configured (set %s)", SettingProvider)
	}
	cat, err := engine.LoadCatalog(s.deps.Config)
	if err != nil {
		return nil, nil, err
	}
	if _, err := cat.Select(provider, engine.KindGenerator); err != nil {
		return nil, nil, fmt.Errorf("judge: %w", err)
	}
	profile, err := taxonomy.Load(s.deps.Config)
	if err != nil {
		return nil, nil, err
	}
	gen, err := s.reg.Generator(cat, provider, s.deps)
	if err != nil {
		return nil, nil, err
	}
	j, err := New(gen, profile, WithCallTimeout(s.deps.Config.Duration("judge.call_timeout")))
	return j, profile, err
}
