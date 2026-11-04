// Package generate is the generator adapter for OpenAI-compatible
// /chat/completions endpoints (the judge's Gemma layer): one system+user
// request, optional strict JSON mode, the context budget enforced before
// sending, retries through httpx (billing-aware, per-provider overrides),
// strict response decoding at the trust boundary and usage accounting.
package generate

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Protocol is the wire format this adapter speaks.
const Protocol = engine.ProtocolOpenAIChat

// Register adds the protocol to the registry.
func Register(r *engine.Registry) {
	r.RegisterGenerator(Protocol, func(p engine.Provider, deps engine.Deps) (engine.Generator, error) {
		return New(p, deps)
	})
}

// Adapter is a Generator for one provider instance.
type Adapter struct {
	p      engine.Provider
	url    string
	deps   engine.Deps
	client *http.Client
	clock  clock.Clock
	log    *slog.Logger
	policy func(*httpx.Policy)
}

// New builds the adapter of a provider; its protocol must be Protocol and it
// must be a catalog-validated generator. opts customise the HTTP client
// (tests inject transports).
func New(p engine.Provider, deps engine.Deps, opts ...httpx.Option) (*Adapter, error) {
	if p.Protocol != Protocol {
		return nil, fmt.Errorf("generate: provider %s: unknown protocol %q", p.ID, p.Protocol)
	}
	url, err := p.URL()
	if err != nil {
		return nil, err
	}
	clk := clock.Or(deps.Clock)
	client, err := httpx.New(deps.Config, deps.Bus, "generate."+p.ID, append([]httpx.Option{httpx.WithClock(clk)}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &Adapter{p: p, url: url, deps: deps, client: client, clock: clk, policy: p.PolicyHook(deps.Config),
		log: logx.For("engine.generate").With("provider", p.ID, "model", p.Model)}, nil
}

// Name is the provider record id.
func (a *Adapter) Name() string { return a.p.ID }

// Model is the configured model.
func (a *Adapter) Model() string { return a.p.Model }

// Plan estimates one Generate without sending anything; an error means the
// request cannot be sent (invalid input, or over the context budget).
func (a *Adapter) Plan(system, user string) (engine.Estimate, error) {
	body, err := a.encode(system, user)
	if err != nil {
		return engine.Estimate{PriceKnown: a.p.PriceKnown()}, err
	}
	if err := a.checkContext(body); err != nil {
		return engine.Estimate{PriceKnown: a.p.PriceKnown()}, err
	}
	tokens := a.p.Tokens(body)
	return engine.Estimate{Calls: 1, InputTokens: tokens,
		EstimatedUSD: a.p.Cost(tokens, int64(a.p.ReserveOutputTokens)), PriceKnown: a.p.PriceKnown()}, nil
}

// Generate sends one request. The context budget is checked before the
// key is read or anything is sent; when the context carries a run's
// engine.Reserver, the call's ceiling is reserved before it is sent
// (engine.ErrBudget when the budget cannot cover it) and settled with its
// usage after. On error, Generation.Usage holds what the call spent.
func (a *Adapter) Generate(ctx context.Context, system, user string, opts ...engine.GenerateOptions) (engine.Generation, error) {
	gen := engine.Generation{Model: a.p.Model}
	body, err := a.encode(system, user, opts...)
	if err != nil {
		return gen, err
	}
	if err := a.checkContext(body); err != nil {
		return gen, err
	}
	key, err := a.p.APIKey(a.deps.Secrets)
	if err != nil {
		return gen, err
	}
	ctx = httpx.WithPolicy(ctx, a.policy)
	gen, usage, err := engine.Charge(ctx, a.p.Ceiling(body), func() (engine.Generation, engine.Usage, error) {
		return a.call(ctx, key, body)
	})
	gen.Usage = usage
	return gen, err
}

// checkContext refuses a request whose input plus the output reserve would
// exceed the provider's context (never silently truncate).
func (a *Adapter) checkContext(body []byte) error {
	tokens, output := a.p.Tokens(body), int64(a.p.ReserveOutputTokens)
	if tokens+output > int64(a.p.ContextTokens) {
		return fmt.Errorf("%w: ~%d input + %d output tokens, provider %s allows %d", engine.ErrContextBudget, tokens, output, a.p.ID, a.p.ContextTokens)
	}
	return nil
}
