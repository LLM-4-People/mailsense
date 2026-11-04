package decision

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Adapter is a Decider for one provider instance.
type Adapter struct {
	p      engine.Provider
	codec  codec
	url    string
	deps   engine.Deps
	client *http.Client
	clock  clock.Clock
	log    *slog.Logger
	policy func(*httpx.Policy)
}

// New builds the adapter of a provider (its protocol must be one of codecs).
// opts customise the HTTP client (tests inject transports).
func New(p engine.Provider, deps engine.Deps, opts ...httpx.Option) (*Adapter, error) {
	c, ok := codecs[p.Protocol]
	if !ok {
		return nil, fmt.Errorf("decision: provider %s: unknown protocol %q", p.ID, p.Protocol)
	}
	url, err := p.URL()
	if err != nil {
		return nil, err
	}
	clk := clock.Or(deps.Clock)
	client, err := httpx.New(deps.Config, deps.Bus, "decision."+p.ID, append([]httpx.Option{httpx.WithClock(clk)}, opts...)...)
	if err != nil {
		return nil, err
	}
	return &Adapter{p: p, codec: c, url: url, deps: deps, client: client, clock: clk, policy: p.PolicyHook(deps.Config),
		log: logx.For("engine.decision").With("provider", p.ID, "model", p.Model)}, nil
}

// Name is the provider record id.
func (a *Adapter) Name() string { return a.p.ID }

// Model is the configured model.
func (a *Adapter) Model() string { return a.p.Model }

// Fingerprint identifies the endpoint behind the record.
func (a *Adapter) Fingerprint() string { return a.p.Fingerprint() }

// Plan estimates one Decide without sending anything; an error means the
// request cannot be sent (invalid, or over the context budget).
func (a *Adapter) Plan(state string, qs []engine.Question) (engine.Estimate, error) {
	bodies, err := a.prepare(state, qs)
	est := engine.Estimate{Calls: len(bodies), PriceKnown: a.p.PriceKnown()}
	for _, b := range bodies {
		est.InputTokens += a.p.Tokens(b)
	}
	est.EstimatedUSD = a.p.Cost(est.InputTokens, 0)
	return est, err
}

// Decide answers qs about state: one call per chunk of at most
// max_questions (same state each time, billed each time), answers merged,
// usage summed. Every chunk is checked against the context budget before
// the first call, so a refused request costs nothing. When the context
// carries a run's engine.Reserver, each call's ceiling is reserved before
// it is sent (engine.ErrBudget when the budget cannot cover it) and
// settled with its usage after. On error the returned Result still
// carries the usage spent and the answers of the chunks that succeeded
// (paid for: the caller keeps them).
func (a *Adapter) Decide(ctx context.Context, state string, qs []engine.Question) (engine.Result, error) {
	res := engine.Result{Model: a.p.Model, Answers: map[string]engine.Answer{}}
	bodies, err := a.prepare(state, qs)
	if err != nil {
		return res, err
	}
	key, err := a.p.APIKey(a.deps.Secrets)
	if err != nil {
		return res, err
	}
	ctx = httpx.WithPolicy(ctx, a.policy)
	for i, chunk := range chunks(qs, a.p.MaxQuestions) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		d, usage, err := engine.Charge(ctx, a.p.Ceiling(bodies[i]), func() (decoded, engine.Usage, error) {
			return a.call(ctx, key, bodies[i], chunk)
		})
		res.Usage.Add(usage)
		if err != nil {
			return res, err
		}
		if d.model != "" {
			res.Model = d.model
		}
		for name, ans := range d.answers {
			res.Answers[name] = ans
		}
	}
	return res, nil
}

// prepare validates the input and encodes every chunk, enforcing the
// context budget (Clef silently truncates an oversized state).
func (a *Adapter) prepare(state string, qs []engine.Question) ([][]byte, error) {
	if err := validateInput(state, qs); err != nil {
		return nil, err
	}
	var bodies [][]byte
	for _, chunk := range chunks(qs, a.p.MaxQuestions) {
		body, err := a.codec.encode(&a.p, state, chunk)
		if err != nil {
			return nil, fmt.Errorf("%w: encode: %v", ErrInput, err)
		}
		if t := a.p.Tokens(body); t > int64(a.p.ContextTokens) {
			return nil, fmt.Errorf("%w: ~%d tokens for %d question(s), provider %s allows %d", ErrContextBudget, t, len(chunk), a.p.ID, a.p.ContextTokens)
		}
		bodies = append(bodies, body)
	}
	return bodies, nil
}

func validateInput(state string, qs []engine.Question) error {
	if mail.Strip(state) == "" {
		return fmt.Errorf("%w: state is empty", ErrInput)
	}
	if len(qs) == 0 {
		return fmt.Errorf("%w: no questions", ErrInput)
	}
	if _, err := byName(qs); err != nil {
		return err
	}
	for i := range qs {
		if err := qs[i].Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInput, err)
		}
	}
	return nil
}

// chunks splits qs in order into groups of at most n.
func chunks(qs []engine.Question, n int) [][]engine.Question {
	var out [][]engine.Question
	for start := 0; start < len(qs); start += n {
		out = append(out, qs[start:min(start+n, len(qs))])
	}
	return out
}
