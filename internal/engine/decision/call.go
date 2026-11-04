package decision

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// call sends one chunk and decodes it strictly. Usage is recorded for
// every call that reached the provider, successful or not.
func (a *Adapter) call(ctx context.Context, key string, body []byte, qs []engine.Question) (decoded, engine.Usage, error) {
	status, raw, usage, err, ok := a.paid().Post(ctx, a.client,
		engine.PostSpec{URL: a.url, Key: key, Body: body, Accept: true},
		engine.SentinelPolicy(a.p.ID, ErrInput, ErrHTTP), body)
	if !ok {
		return decoded{}, usage, err
	}
	d, err := a.decodeOK(raw, qs)
	usage.InputTokens, usage.OutputTokens = d.input, d.output
	if !d.hasUsage {
		usage.InputTokens = a.p.Tokens(body) // billed anyway: account the estimate
	}
	usage = a.p.AccountedUsage(usage, body, false)
	if d.billed != nil {
		usage.EstimatedUSD = *d.billed // what the provider charged (metered pricing)
	}
	a.paid().Report(ctx, usage, status, err, !d.hasUsage, d.billed != nil)
	return d, usage, err
}

// paid is this adapter's slice of the shared call accounting: its labels, its
// transport classification and the one log attribute only the decider has.
func (a *Adapter) paid() engine.PaidCall {
	return engine.PaidCall{Deps: a.deps, Provider: &a.p, Clock: a.clock, Log: a.log, Label: "decision call",
		Transport: transportError,
		Extra:     func(engine.Usage, bool) []any { return []any{"billed", false} }}
}

// transportError marks a transport failure that survived the HTTP retry
// policy as transient: every message would fail the same way, so a run
// stops instead of charging the failure to each one. A cancelled or expired
// context is the run stopping for another reason, and an input error is this
// adapter's, not the provider's; both keep their class.
func transportError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrInput):
		return err
	}
	return fmt.Errorf("%w: %w", engine.ErrTransient, err)
}

func (a *Adapter) decodeOK(raw []byte, qs []engine.Question) (decoded, error) {
	inner, err := unwrap(a.p.Envelope, raw, a.deps.ErrorTextChars())
	if err != nil {
		return decoded{}, err
	}
	d, err := a.codec.decode(inner, qs)
	if err == nil && len(d.answers) != len(qs) {
		err = contractError("response", "%d answers for %d questions", len(d.answers), len(qs))
	}
	for i := range qs {
		if err != nil {
			break
		}
		ans := d.answers[qs[i].Name]
		err = consistent(&qs[i], &ans, a.p.ProbabilityTolerance)
	}
	if err != nil {
		d.answers = nil // a contradictory chunk is not kept
	}
	return d, err
}
