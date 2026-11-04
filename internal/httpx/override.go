package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

type policyKey struct{}

// WithPolicy returns a context whose requests use the http.retry.* policy
// as adjusted by adjust (per-provider overrides). The billing rule cannot
// be adjusted away: failures after the request was written stay terminal.
func WithPolicy(ctx context.Context, adjust func(*Policy)) context.Context {
	return context.WithValue(ctx, policyKey{}, adjust)
}

func adjusted(ctx context.Context, p Policy) Policy {
	never := p.Never
	if adjust, ok := ctx.Value(policyKey{}).(func(*Policy)); ok && adjust != nil {
		adjust(&p)
	}
	p.Never = never // the never-retry rule is not adjustable
	return p
}

// maxFinalPeek bounds how much of a retryable response is read to ask
// Policy.Final (error bodies are small; this is a memory safety limit).
const maxFinalPeek = 1 << 20

// finalByBody applies Policy.Final to a response the status rule would
// retry; the body stays readable for the caller.
func finalByBody(p Policy, resp *http.Response, d Decision) Decision {
	if !d.Retry || p.Final == nil || resp == nil || resp.Body == nil {
		return d
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, maxFinalPeek))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	if err == nil && p.Final(resp.StatusCode, head) {
		return Decision{Reason: d.Reason + "; the body says it is final (quota or credit)"}
	}
	return d
}
