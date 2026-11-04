package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// Error classes; every adapter error wraps one (errors.Is).
var (
	ErrInput     = errors.New("embed: invalid input")
	ErrContract  = errors.New("embed: response violates the embeddings contract")
	ErrServer    = errors.New("embed: embeddings server failed")
	ErrRejected  = errors.New("embed: request rejected by the server")
	ErrTransient = fmt.Errorf("embed: embeddings provider temporarily unavailable (%w)", engine.ErrTransient)
	ErrAuth      = fmt.Errorf("embed: key rejected (%w)", engine.ErrUnavailable)
	ErrNoServer  = fmt.Errorf("embed: embeddings server unreachable (%w)", engine.ErrUnavailable)
	ErrNoKey     = fmt.Errorf("embed: API key not configured (%w)", engine.ErrUnavailable)
)

// call embeds one batch and records its usage.
func (a *Adapter) call(ctx context.Context, inputs []string) ([][]float32, engine.Usage, error) {
	start := a.clock.Now()
	status, vecs, tokens, err := a.request(ctx, inputs)
	usage := engine.Usage{Calls: 1, InputTokens: tokens, Duration: a.clock.Now().Sub(start)}
	usage = a.p.AccountedUsage(usage, nil, false) // the server reported the tokens
	rec := engine.UsageRecord{Provider: a.p.ID, Model: a.p.Model, Usage: usage, Failed: err != nil, Status: status}
	if err != nil {
		rec.Error = err.Error()
		a.log.Warn("embeddings call failed", "inputs", len(inputs), "status", status, "duration", usage.Duration, "err", err)
	} else {
		a.log.Debug("embeddings call", "inputs", len(inputs), "tokens", tokens, "duration", usage.Duration)
	}
	if ctx.Err() == nil {
		a.deps.Record(ctx, rec)
	}
	return vecs, usage, err
}

// request sends one batch. When a managed runtime cannot be reached (it
// died, or restarted on another port with another key) the Locator is
// told at once and the batch is sent once more to the runtime it hands
// out next (Locate waits for the restart): a crash the supervisor
// recovers from is not an unavailable embedder.
func (a *Adapter) request(ctx context.Context, inputs []string) (int, [][]float32, int64, error) {
	body, _ := json.Marshal(map[string]any{"model": a.p.Model, "input": inputs, "encoding_format": "float"})
	url, key, err := a.endpoint(ctx)
	if err != nil {
		return 0, nil, 0, err
	}
	status, vecs, tokens, err := a.send(ctx, url, key, body, len(inputs))
	if a.p.Runtime == "" || ctx.Err() != nil || !unreachable(err) {
		return status, vecs, tokens, err
	}
	a.log.Warn("runtime unreachable: waiting for its restart and sending once more", "runtime", a.p.Runtime, "err", err)
	a.deps.Locator.Failed(a.p.Runtime, a.p.Model, engine.Endpoint{URL: strings.TrimSuffix(url, a.p.Path), Key: key})
	if url, key, err = a.endpoint(ctx); err != nil {
		return 0, nil, 0, err
	}
	return a.send(ctx, url, key, body, len(inputs))
}

// unreachable: the request never got an answer from the server (refused,
// reset, cut mid-answer).
func unreachable(err error) bool {
	return errors.Is(err, ErrNoServer) || errors.Is(err, httpx.ErrAfterWrite)
}

func (a *Adapter) send(ctx context.Context, url, key string, body []byte, n int) (int, [][]float32, int64, error) {
	status, raw, err := engine.PostJSON(ctx, a.deps.Config, a.client, &a.p,
		engine.PostSpec{URL: url, Key: key, Body: body},
		engine.PostPolicy{
			Input:     func(err error) error { return fmt.Errorf("%w: %w", ErrInput, err) },
			Transport: func(err error) error { return transportError(ctx, a.p.ID, err) },
			Read:      func(err error) error { return fmt.Errorf("%w: %s: read response: %w", a.serverFailure(), a.p.ID, err) },
		})
	if err != nil {
		return status, nil, 0, err
	}
	if status != http.StatusOK {
		return status, nil, 0, a.statusError(status, httpx.ErrorText(a.deps.Config, raw))
	}
	vecs, tokens, err := decode(raw, n, &a.spec, a.dims)
	return status, vecs, tokens, err
}

// endpoint is the URL and key of the next call: the managed runtime's
// (started on demand by the Locator) or the provider's own.
func (a *Adapter) endpoint(ctx context.Context) (string, string, error) {
	if a.p.Runtime == "" {
		url, err := a.p.URL()
		if err != nil || a.p.APIKeySecret == "" {
			return url, "", err
		}
		key, err := a.deps.Secrets.Get(a.p.APIKeySecret)
		if err != nil || key == "" {
			return "", "", fmt.Errorf("%w: provider %s has no usable key: %v", ErrNoKey, a.p.ID, err)
		}
		return url, key, nil
	}
	if a.deps.Locator == nil {
		return "", "", fmt.Errorf("%w: runtime %s is not available in this process", ErrNoServer, a.p.Runtime)
	}
	ep, err := a.deps.Locator.Locate(ctx, a.p.Runtime, a.p.Model)
	if err != nil {
		return "", "", err
	}
	return ep.URL + a.p.Path, ep.Key, nil
}

// transportError: a request that never reached the server (after the
// httpx retries) means the server is not there; one that failed after
// being written is a server failure.
func transportError(ctx context.Context, name string, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, httpx.ErrAfterWrite):
		return fmt.Errorf("%w: %s: %w", ErrTransient, name, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrNoServer, name, err)
}

// serverFailure is the class of a 5xx (or an unreadable response body): a
// managed runtime's 500 is message-scoped because llama-server uses it for
// an input above its context; a remote provider's 5xx is about the
// provider, so the index stops after one call and quarantines nothing.
func (a *Adapter) serverFailure() error {
	if a.p.Runtime != "" {
		return ErrServer
	}
	return ErrTransient
}

// statusError classifies one non-200 answer.
func (a *Adapter) statusError(status int, msg string) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: %s answered HTTP %d: %s", ErrAuth, a.p.ID, status, msg)
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout:
		// A rate limit or a gateway/server availability answer is about the
		// provider, not about these messages: the run stops and retries
		// later.
		return fmt.Errorf("%w: %s answered HTTP %d: %s", ErrTransient, a.p.ID, status, msg)
	case status < 500:
		return fmt.Errorf("%w: %s answered HTTP %d: %s", ErrRejected, a.p.ID, status, msg)
	}
	// A remote provider's every 5xx, including a plain 500, is
	// provider-wide. A managed runtime's 500 is message-scoped: it is
	// llama-server's answer for an input above its context.
	return fmt.Errorf("%w: %s answered HTTP %d: %s", a.serverFailure(), a.p.ID, status, msg)
}
