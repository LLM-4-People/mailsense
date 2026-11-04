package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// PostSpec is one JSON POST a provider adapter sends.
type PostSpec struct {
	URL    string // endpoint of this call
	Key    string // authorization key ("" for a server that needs none)
	Body   []byte
	Accept bool // also ask for a JSON answer (the chat adapters do)
}

// PostPolicy is one adapter's error contract for a failed PostSpec. The
// shared step owns the HTTP mechanics and the order of the checks; the
// adapter owns what a failure means, because every adapter reports it with
// its own sentinels (the embedder's are not the chat adapters').
type PostPolicy struct {
	Input     func(error) error // a request that could not be built or authorized
	Transport func(error) error // an exchange that failed without an answer
	Read      func(error) error // an answer body that could not be read
}

// SentinelPolicy is the PostPolicy of an adapter that reports every
// exchange failure as one sentinel: input for a request it could not build,
// httpErr for anything the provider or the connection failed after that.
func SentinelPolicy(provider string, input, httpErr error) PostPolicy {
	return PostPolicy{
		Input:     func(err error) error { return fmt.Errorf("%w: %v", input, err) },
		Transport: func(err error) error { return fmt.Errorf("%w: %s: %w", httpErr, provider, err) },
		Read:      func(err error) error { return fmt.Errorf("%w: %s: read response: %v", httpErr, provider, err) },
	}
}

// PostJSON sends one JSON POST and reads its answer within
// http.max_response_mb (the one body reader of every adapter). A context
// cancelled during the exchange is the context's error, never a provider
// failure: the run stopped for another reason.
func PostJSON(ctx context.Context, cfg *config.Config, client *http.Client, p *Provider, spec PostSpec, policy PostPolicy) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.URL, bytes.NewReader(spec.Body))
	if err != nil {
		return 0, nil, policy.Input(err)
	}
	if err := p.Authorize(req, spec.Key); err != nil {
		return 0, nil, policy.Input(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if spec.Accept {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, policy.Transport(err)
	}
	defer resp.Body.Close()
	raw, err := httpx.ReadBody(cfg, resp)
	if err != nil {
		return resp.StatusCode, nil, policy.Read(err)
	}
	return resp.StatusCode, raw, nil
}
