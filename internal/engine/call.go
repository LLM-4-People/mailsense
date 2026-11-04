package engine

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// PaidCall is the one accounting path of an adapter request that may have been
// billed. The adapter owns its wire format, its decode and the token counts;
// this owns the order every adapter must follow: start the clock, send, build
// the base usage, classify a transport failure or a non-200 by the one rule,
// apply AccountedUsage with the one estimated rule, and report the call once
// through Deps.Record and the adapter's log.
//
// The estimated rule is why this cannot be per-adapter: a transport failure
// after the request was written may be billed, and a status the provider did
// not explicitly decline is an estimate; missing either silently under-reports
// spend.
type PaidCall struct {
	Deps     Deps
	Provider *Provider
	Clock    clock.Clock
	Log      *slog.Logger
	// Label is the log message of a successful call ("decision call"); a
	// failure logs the same label with " failed" appended.
	Label string
	// Transport classifies a transport failure. Nil keeps the error the
	// adapter's PostPolicy produced.
	Transport func(context.Context, error) error
	// Extra returns the adapter-only log attributes of one call (the
	// generator logs output tokens, the decider the billed flag).
	Extra func(Usage, bool) []any
}

// Post sends one request and accounts it. A transport failure or a non-200
// answer is classified, accounted and reported here; ok is false and the
// returned usage is final. On a 200 ok is true and the adapter decodes Raw,
// applies the reported or estimated token counts and calls Report.
func (c PaidCall) Post(ctx context.Context, client *http.Client, spec PostSpec, policy PostPolicy, body []byte) (status int, raw []byte, usage Usage, err error, ok bool) {
	start := c.Clock.Now()
	status, raw, err = PostJSON(ctx, c.Deps.Config, client, c.Provider, spec, policy)
	usage = Usage{Calls: 1, Duration: c.Clock.Now().Sub(start)}
	if err != nil {
		if c.Transport != nil {
			err = c.Transport(ctx, err)
		}
		estimated := errors.Is(err, httpx.ErrAfterWrite)
		usage = c.Provider.AccountedUsage(usage, body, estimated)
		c.Report(ctx, usage, 0, err, estimated, false)
		return status, raw, usage, err, false
	}
	if status != http.StatusOK {
		apiErr := Classify(c.Provider.ID, status, raw, c.Provider.Retry.QuotaCodes, c.Deps.ErrorTextChars())
		estimated := !c.Provider.Declined(status, c.Deps.Config)
		usage = c.Provider.AccountedUsage(usage, body, estimated)
		c.Report(ctx, usage, status, apiErr, estimated, false)
		return status, raw, usage, apiErr, false
	}
	return status, raw, usage, nil, true
}

// Report publishes, persists and logs one accounted call. estimated says the
// token counts are the adapter's estimate (the provider reported none) and
// billed says EstimatedUSD is the charge the provider reported rather than
// tokens times the catalog price.
func (c PaidCall) Report(ctx context.Context, u Usage, status int, callErr error, estimated, billed bool) {
	rec := UsageRecord{Provider: c.Provider.ID, Model: c.Provider.Model, Usage: u,
		Failed: callErr != nil, Status: status, Estimated: estimated, Billed: billed}
	if callErr != nil {
		rec.Error = callErr.Error()
	}
	c.Deps.Record(ctx, rec)
	attrs := []any{"status", status, "input_tokens", u.InputTokens}
	if c.Extra != nil {
		attrs = append(attrs, c.Extra(u, billed)...)
	}
	attrs = append(attrs, "usd", u.EstimatedUSD, "duration", u.Duration)
	if callErr != nil {
		c.Log.Warn(c.Label+" failed", append(attrs, "err", strings.TrimSpace(callErr.Error()))...)
		return
	}
	c.Log.Info(c.Label, attrs...)
}
