package judge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Generator is the Judge over one chat generator and one profile. It owns
// the prompt (one template), the digest (template + profile + question set),
// the strict answer boundary and the per-call deadline; the generator owns
// the HTTP call, the context budget and the usage.
type Generator struct {
	gen     engine.Generator
	profile *taxonomy.Profile
	prompt  Prompt
	timeout time.Duration
}

// Option customises a judge.
type Option func(*Generator)

// WithCallTimeout bounds one Label call. A provider that accepts the
// request and stops answering must not hold the caller (a labelling run,
// its job, or a validation harness) until the HTTP client's total timeout:
// past this the call fails with ErrTimeout and the message stays
// unlabelled. Zero disables the bound (tests only).
func WithCallTimeout(d time.Duration) Option {
	return func(g *Generator) { g.timeout = d }
}

// New builds a judge. A judge that is asked any question other than the
// profile's (or a different definition of one) would misdescribe its digest,
// so Label and PromptDigest refuse it.
func New(gen engine.Generator, profile *taxonomy.Profile, opts ...Option) (*Generator, error) {
	if gen == nil || profile == nil {
		return nil, errors.New("judge: a generator and a profile are required")
	}
	g := &Generator{gen: gen, profile: profile, prompt: builtinPrompt}
	for _, opt := range opts {
		opt(g)
	}
	if g.timeout < 0 {
		return nil, fmt.Errorf("%w: call timeout %s is negative", ErrInput, g.timeout)
	}
	return g, nil
}

// Name is the provider name.
func (g *Generator) Name() string { return g.gen.Name() }

// Model is the model the provider is configured with.
func (g *Generator) Model() string { return g.gen.Model() }

// PromptDigest covers every input of one prompt: the shipped template, the
// profile's decision identity (state template, body chars and fact switches
// included) and qs in canonical profile order. qs is a run's question set:
// the superset each message's request is drawn from, so a change to any of
// its questions re-buys the labels the old prompt produced.
func (g *Generator) PromptDigest(qs []engine.Question) (string, error) {
	ordered, err := g.orderedQuestions(qs)
	if err != nil {
		return "", err
	}
	profile, err := g.profile.Digest()
	if err != nil {
		return "", err
	}
	return g.prompt.digest(profile, ordered)
}

// Plan estimates one Label for one message's state and questions.
func (g *Generator) Plan(state string, qs []engine.Question) (engine.Estimate, error) {
	system, user, err := g.request(state, qs)
	if err != nil {
		return engine.Estimate{}, err
	}
	return g.gen.Plan(system, user)
}

// Label sends one call and parses the answer. The request is validated
// before anything is sent; on a contract error the valid labels of the
// answer are returned with it (they were paid for and are trustworthy),
// while the failed questions are absent from the map. The call is bounded
// by the judge's timeout: a provider that stops answering fails with
// ErrTimeout instead of holding the caller.
func (g *Generator) Label(ctx context.Context, state string, qs []engine.Question) (Result, error) {
	system, user, err := g.request(state, qs)
	if err != nil {
		return Result{}, err
	}
	parent := ctx
	if g.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, g.timeout)
		defer cancel()
	}
	gen, err := g.gen.Generate(ctx, system, user)
	res := Result{Usage: gen.Usage}
	if err != nil {
		if g.timeout > 0 && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("%w after %s: %w", ErrTimeout, g.timeout, err)
		} else if errors.Is(err, generate.ErrContract) {
			// A malformed provider answer is a failed label, not a failed
			// run: classify it with the judge's own contract error so the
			// batch caller counts it and continues (the usage stays).
			err = fmt.Errorf("%w: %w", ErrContract, err)
		}
		return res, err
	}
	labels, cerr := parseAnswer(gen.Content, qs)
	res.Labels = labels // may hold the valid answers even on a partial contract error
	if cerr != nil {
		return res, cerr
	}
	return res, nil
}

// request validates and renders one call: the state must carry text and
// every question must be one of the profile's questions (the same
// definition, in profile order) and no question may repeat.
func (g *Generator) request(state string, qs []engine.Question) (system, user string, err error) {
	if mail.Strip(state) == "" {
		return "", "", fmt.Errorf("%w: state is empty", ErrInput)
	}
	ordered, err := g.orderedQuestions(qs)
	if err != nil {
		return "", "", err
	}
	return g.prompt.render(ordered, state)
}

// orderedQuestions validates qs against the profile and returns them in
// profile order: the one canonical question set of a request and its
// digest.
func (g *Generator) orderedQuestions(qs []engine.Question) ([]engine.Question, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("%w: no questions", ErrInput)
	}
	want := make(map[string]bool, len(qs))
	for i := range qs {
		name := qs[i].Name
		if want[name] {
			return nil, fmt.Errorf("%w: question %q is asked twice", ErrInput, name)
		}
		want[name] = true
		pq, ok := g.profile.Question(name)
		if !ok {
			return nil, fmt.Errorf("%w: question %q is not in the profile", ErrInput, name)
		}
		if !reflect.DeepEqual(qs[i], *pq) {
			return nil, fmt.Errorf("%w: question %q differs from the profile's definition", ErrInput, name)
		}
	}
	ordered := make([]engine.Question, 0, len(qs))
	for i := range g.profile.Questions {
		q := g.profile.Questions[i]
		if want[q.Name] {
			ordered = append(ordered, q)
		}
	}
	return ordered, nil
}
