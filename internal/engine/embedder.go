package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Purpose is what a text is embedded for: retrieval models are trained
// with a different task prefix for queries and for documents.
type Purpose string

// Purposes.
const (
	PurposeQuery    Purpose = "query"
	PurposeDocument Purpose = "document"
)

// Text is one input of an embedder.
type Text struct {
	Purpose Purpose
	Title   string // documents: the subject ("" = the model's empty title)
	Body    string
}

// Embedder turns texts into unit vectors (local llama-server, any
// OpenAI-compatible /v1/embeddings endpoint).
type Embedder interface {
	// Name is the provider instance name (catalog key).
	Name() string
	// Model is the configured model.
	Model() string
	// Space names the vector space: vectors are comparable only within one
	// (provider, model, dimensions, task templates).
	Space() string
	// Dimensions is the length of every returned vector.
	Dimensions() int
	// Input is the exact string sent for t (task template and the token
	// budget applied): the index hashes it to skip unchanged mail.
	Input(t Text) string
	// Embed returns one unit vector per text, in order. Nothing partial is
	// returned: a failed batch fails as a whole.
	Embed(ctx context.Context, texts []Text) (Embeddings, error)
}

// Embeddings is the answer of one Embed.
type Embeddings struct {
	Vectors [][]float32 `json:"-"`
	Usage   Usage       `json:"usage"`
	// Truncated counts the texts cut to the input token budget.
	Truncated int `json:"truncated"`
}

// Endpoint is where a managed runtime answers and the key it expects.
type Endpoint struct {
	URL string // scheme://host:port, no path
	Key string
}

// Locator finds (starting it when needed) the managed runtime serving a
// model: the sidecar implements it. An error that will repeat (not
// installed, unsupported platform) wraps ErrUnavailable.
type Locator interface {
	Locate(ctx context.Context, runtime, model string) (Endpoint, error)
	// Failed reports that a call could not reach ep (the runtime died or
	// moved): the next Locate waits for the restarted runtime instead of
	// handing out ep again.
	Failed(runtime, model string, ep Endpoint)
	// Model is what the runtime catalog pins for a model, without
	// starting anything.
	Model(runtime, model string) (ModelInfo, error)
}

// ModelInfo is a managed model's identity and limits.
type ModelInfo struct {
	// Revision identifies the exact model file (its SHA-256): a new file
	// is a new vector space.
	Revision string
	// ContextTokens is the per-input context of the runtime's slots.
	ContextTokens int
}

// EmbeddingSpec is the model data of an embedder provider.
type EmbeddingSpec struct {
	Dimensions int `json:"dimensions"` // native output length
	// Matryoshka lists the shorter lengths the model was trained to be cut
	// to (embed.dimensions must be the native length or one of them).
	Matryoshka []int `json:"matryoshka"`
	// QueryTemplate and DocumentTemplate are the model's task prefixes:
	// {text} is the input, {title} a document's title.
	QueryTemplate    string `json:"query_template"`
	DocumentTemplate string `json:"document_template"`
	EmptyTitle       string `json:"empty_title"`
	// NormTolerance bounds |length − 1| of a returned vector (the server
	// normalises; anything else is a broken backend, e.g. fp16 overflow).
	NormTolerance float64 `json:"norm_tolerance"`
	// MaxBatch is the most inputs one request may carry.
	MaxBatch int `json:"max_batch"`
	// ReservedTokens are the tokens the server adds to every input (BOS,
	// EOS): an input is at most context_tokens − reserved_tokens bytes.
	ReservedTokens int `json:"reserved_tokens"`
}

// Allows reports whether vectors may be cut to dims.
func (s *EmbeddingSpec) Allows(dims int) bool {
	return dims == s.Dimensions || slices.Contains(s.Matryoshka, dims)
}

// fingerprintParts is the embedding spec's canonical identity (every
// value that changes the vector space).
func (s *EmbeddingSpec) fingerprintParts() []string {
	dims := make([]string, len(s.Matryoshka))
	for i, d := range s.Matryoshka {
		dims[i] = strconv.Itoa(d)
	}
	return []string{
		"dimensions=" + strconv.Itoa(s.Dimensions),
		"matryoshka=" + strings.Join(dims, ","),
		"query_template=" + s.QueryTemplate,
		"document_template=" + s.DocumentTemplate,
		"empty_title=" + s.EmptyTitle,
		"norm_tolerance=" + strconv.FormatFloat(s.NormTolerance, 'g', -1, 64),
		"max_batch=" + strconv.Itoa(s.MaxBatch),
		"reserved_tokens=" + strconv.Itoa(s.ReservedTokens),
	}
}

// Validate reports every problem of the spec at once: the one rule set the
// provider catalog and the pinned sidecar data both use.
func (s *EmbeddingSpec) Validate() error {
	problems := s.problems()
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return errors.New(strings.Join(problems, "; "))
}

func (s *EmbeddingSpec) problems() []string {
	var out []string
	if s.Dimensions < 1 {
		out = append(out, "dimensions must be ≥ 1")
	}
	for _, d := range s.Matryoshka {
		if d < 1 || d >= s.Dimensions {
			out = append(out, fmt.Sprintf("matryoshka size %d must be in [1, dimensions)", d))
		}
	}
	if !strings.Contains(s.QueryTemplate, "{text}") || !strings.Contains(s.DocumentTemplate, "{text}") {
		out = append(out, "query_template and document_template must contain {text}")
	}
	if s.NormTolerance <= 0 || s.NormTolerance >= 1 || s.MaxBatch < 1 || s.ReservedTokens < 0 {
		out = append(out, "norm_tolerance must be in (0, 1), max_batch ≥ 1 and reserved_tokens ≥ 0")
	}
	return out
}
