// Package embed is the embedder adapter for OpenAI-compatible
// /v1/embeddings endpoints (the managed llama-server sidecar, or any
// server the user configures): the model's task templates, the input
// token budget, batching, retries through httpx, strict vector validation
// at the trust boundary (finite, native length, unit norm), Matryoshka
// truncation with re-normalisation, and usage accounting.
package embed

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Protocol is the wire format this adapter speaks.
const Protocol = engine.ProtocolOpenAIEmbeddings

// Register adds the protocol to the registry.
func Register(r *engine.Registry) {
	r.RegisterEmbedder(Protocol, func(p engine.Provider, deps engine.Deps) (engine.Embedder, error) {
		return New(p, deps)
	})
}

// Adapter is an Embedder for one provider instance.
type Adapter struct {
	p        engine.Provider
	spec     engine.EmbeddingSpec
	deps     engine.Deps
	client   *http.Client
	clock    clock.Clock
	log      *slog.Logger
	dims     int
	batch    int
	maxChars int
	maxBytes int
	space    string
	policy   func(*httpx.Policy)
}

// New builds the adapter; embed.dimensions must be a length the model
// allows (0 = native) and embed.max_input_tokens fit its context (a
// managed runtime's context and model revision come from its catalog
// through the Locator). opts customise the HTTP client (tests inject
// transports).
func New(p engine.Provider, deps engine.Deps, opts ...httpx.Option) (*Adapter, error) {
	if p.Protocol != Protocol || p.Embedding == nil {
		return nil, fmt.Errorf("embed: provider %s is not an %s embedder", p.ID, Protocol)
	}
	info, err := modelInfo(&p, deps)
	if err != nil {
		return nil, err
	}
	s := deps.Config.Snapshot()
	dims, budget := s.Int("embed.dimensions"), s.Int("embed.max_input_tokens")
	if dims == 0 {
		dims = p.Embedding.Dimensions
	}
	if !p.Embedding.Allows(dims) {
		return nil, fmt.Errorf("embed: embed.dimensions %d is not a length of %s (native %d, Matryoshka %v)", dims, p.Model, p.Embedding.Dimensions, p.Embedding.Matryoshka)
	}
	if budget > info.ContextTokens {
		return nil, fmt.Errorf("embed: embed.max_input_tokens %d exceeds the %d-token context of provider %s", budget, info.ContextTokens, p.ID)
	}
	clk := clock.Or(deps.Clock)
	client, err := httpx.New(deps.Config, deps.Bus, "embed."+p.ID, append([]httpx.Option{httpx.WithClock(clk)}, opts...)...)
	if err != nil {
		return nil, err
	}
	a := &Adapter{p: p, spec: *p.Embedding, deps: deps, client: client, clock: clk, dims: dims,
		batch: min(s.Int("embed.batch_size"), p.Embedding.MaxBatch), maxChars: int(float64(budget) * p.CharsPerToken),
		maxBytes: info.ContextTokens - p.Embedding.ReservedTokens, policy: p.PolicyHook(deps.Config),
		log: logx.For("engine.embed").With("provider", p.ID, "model", p.Model)}
	a.space = spaceOf(&p, dims, info.Revision)
	return a, nil
}

// modelInfo is the model's context and revision: the runtime catalog's
// for a managed runtime, the provider's own context otherwise.
func modelInfo(p *engine.Provider, deps engine.Deps) (engine.ModelInfo, error) {
	if p.Runtime == "" {
		return engine.ModelInfo{ContextTokens: p.ContextTokens}, nil
	}
	if deps.Locator == nil {
		return engine.ModelInfo{}, fmt.Errorf("%w: runtime %s is not available in this process", ErrNoServer, p.Runtime)
	}
	return deps.Locator.Model(p.Runtime, p.Model)
}

// revisionDigits is how much of a model file's SHA-256 names its space.
const revisionDigits = 12

// spaceOf names the vector space: protocol, model, length, a digest of
// the task templates, the record fingerprint and, for a managed model, its
// file's revision. The record id and label are display data: renaming a
// record keeps its vectors, while a model, endpoint, template or dimension
// change starts a new space (old vectors stay incomparable).
func spaceOf(p *engine.Provider, dims int, revision string) string {
	e := p.Embedding
	tmpl := digest.Hex([]byte(e.QueryTemplate+"\x00"+e.DocumentTemplate+"\x00"+e.EmptyTitle), 8)
	parts := []string{p.Protocol, p.Model, strconv.Itoa(dims), tmpl, p.Fingerprint()}
	if revision != "" {
		parts = append(parts, revision[:min(revisionDigits, len(revision))])
	}
	return strings.Join(parts, "/")
}

// Name is the provider record id.
func (a *Adapter) Name() string { return a.p.ID }

// Model is the configured model.
func (a *Adapter) Model() string { return a.p.Model }

// Space is the vector space of every returned vector.
func (a *Adapter) Space() string { return a.space }

// MaxBatch is the effective request size: the configured batch bounded by
// the provider's documented maximum. The index sizes one batch to it so a
// failed batch does not throw away earlier sub-batch vectors.
func (a *Adapter) MaxBatch() int { return a.batch }

// Dimensions is the length of every returned vector.
func (a *Adapter) Dimensions() int { return a.dims }

// Input is the exact text sent: whitespace runs collapsed, cut to the
// token budget (title first), inside the model's task template, and never
// longer in UTF-8 bytes than the server's per-input context (less the
// tokens it adds itself): the budget is an estimate, but a tokenizer that
// falls back to bytes never makes more tokens than bytes, so the server
// can never refuse an input as too large.
func (a *Adapter) Input(t engine.Text) string {
	in, _ := a.input(t)
	return in
}

func (a *Adapter) input(t engine.Text) (string, bool) {
	body := strings.Join(mail.Words(t.Body), " ")
	if t.Purpose == engine.PurposeQuery {
		return a.fit(a.spec.QueryTemplate, "", body, mail.Head(body, a.maxChars))
	}
	title := mail.Head(strings.Join(mail.Words(t.Title), " "), a.maxChars)
	cut := mail.Head(body, a.maxChars-mail.Runes(title))
	if title == "" {
		title = a.spec.EmptyTitle
	}
	return a.fit(a.spec.DocumentTemplate, title, body, cut)
}

// fit fills the template and, past maxBytes, shortens the text (then the
// title) on character boundaries. cut reports whether body was shortened.
func (a *Adapter) fit(tmpl, title, body, text string) (string, bool) {
	fill := func(title, text string) string {
		return strings.NewReplacer("{title}", title, "{text}", text).Replace(tmpl)
	}
	out := fill(title, text)
	if over := len(out) - a.maxBytes; over > 0 {
		text = headBytes(text, len(text)-over)
		out = fill(title, text)
	}
	if over := len(out) - a.maxBytes; over > 0 {
		out = fill(headBytes(title, len(title)-over), text)
	}
	return out, text != body
}

// headBytes is the longest prefix of s of at most n bytes that ends on a
// character boundary.
func headBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Embed embeds texts in batches of embed.batch_size; any failed batch
// fails the call (vectors already received are discarded, never mixed).
func (a *Adapter) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	var out engine.Embeddings
	inputs := make([]string, len(texts))
	for i, t := range texts {
		if t.Purpose != engine.PurposeQuery && t.Purpose != engine.PurposeDocument {
			return out, fmt.Errorf("%w: text %d has purpose %q", ErrInput, i, t.Purpose)
		}
		var cut bool
		inputs[i], cut = a.input(t)
		if cut {
			out.Truncated++
		}
	}
	ctx = httpx.WithPolicy(ctx, a.policy)
	for start := 0; start < len(inputs); start += a.batch {
		vecs, usage, err := a.call(ctx, inputs[start:min(start+a.batch, len(inputs))])
		out.Usage.Add(usage)
		if err != nil {
			return engine.Embeddings{Usage: out.Usage}, err
		}
		out.Vectors = append(out.Vectors, vecs...)
	}
	return out, nil
}
