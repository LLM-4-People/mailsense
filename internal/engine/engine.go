// Package engine is the provider layer: the engine kinds (Decider,
// Embedder and Generator), the typed question/answer contract every
// decision model shares, provider records as data (the settings-owned
// provider file), usage accounting, protocol descriptors and the registry
// that builds a configured provider.
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// Kind is what an engine does.
type Kind string

// Engine kinds. Each kind has an adapter (decision, embed, generate); the
// kind of a record derives from its protocol, never from a record field.
const (
	KindDecider   Kind = "decider"
	KindEmbedder  Kind = "embedder"
	KindGenerator Kind = "generator"
)

func (k Kind) valid() bool { return k == KindDecider || k == KindEmbedder || k == KindGenerator }

// ErrUnavailable marks errors that will fail every further request the
// same way (no credits, rejected key, quota, no key configured): a run
// stops at the first one instead of failing each message.
var ErrUnavailable = errors.New("provider unavailable")

// Provider configuration errors. ErrNotConfigured means the owner has not
// selected a usable record (empty selector, deleted record or wrong kind):
// it wraps ErrUnavailable so startup and actions surface an explicit
// blocker instead of crashing or inventing a default.
var (
	ErrNotConfigured   = fmt.Errorf("provider not configured: %w", ErrUnavailable)
	ErrUnknownProvider = errors.New("engine: unknown provider")
	ErrTestFailed      = errors.New("engine: provider test failed")
)

// Decider answers typed questions about a state (decision models: SubQ,
// Example provider Jev, Cloudflare Clef, OpenAI Decisions).
type Decider interface {
	// Name is the provider record id.
	Name() string
	// Model is the model the provider is configured with.
	Model() string
	// Fingerprint identifies protocol, endpoint, model and embedding spec:
	// paid caches key on it so a model or endpoint change invalidates them.
	Fingerprint() string
	// Plan estimates calls and cost of one Decide (the budget guard). An
	// error means the request cannot be sent (invalid input, over the
	// provider's context budget): Decide would fail without a call.
	Plan(state string, questions []Question) (Estimate, error)
	// Decide answers every question about state. On error, Answers holds
	// the questions already answered by earlier chunks (paid for: callers
	// keep them) and Usage what was spent.
	Decide(ctx context.Context, state string, questions []Question) (Result, error)
}

// Estimate is the predicted cost of one Decide.
type Estimate struct {
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	EstimatedUSD float64 `json:"estimated_usd"`
	PriceKnown   bool    `json:"price_known"`
}

// Add sums o into e (PriceKnown only while both are priced).
func (e *Estimate) Add(o Estimate) {
	e.Calls += o.Calls
	e.InputTokens += o.InputTokens
	e.EstimatedUSD += o.EstimatedUSD
	e.PriceKnown = e.PriceKnown && o.PriceKnown
}

// Sub removes o from e (a reservation that settled).
func (e *Estimate) Sub(o Estimate) {
	e.Calls -= o.Calls
	e.InputTokens -= o.InputTokens
	e.EstimatedUSD -= o.EstimatedUSD
}

// Usage is what calls consumed (summed over chunks).
type Usage struct {
	Calls        int           `json:"calls"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	EstimatedUSD float64       `json:"estimated_usd"`
	Duration     time.Duration `json:"duration"`
}

// Add sums o into u.
func (u *Usage) Add(o Usage) {
	u.Calls += o.Calls
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.EstimatedUSD += o.EstimatedUSD
	u.Duration += o.Duration
}

// Result is the answer set of one Decide.
type Result struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// UsageRecord is one provider call, published on TopicUsage and handed
// to the UsageSink (persisted counters).
type UsageRecord struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Usage
	Failed bool `json:"failed"`
	// Estimated is true when the tokens are the adapter's estimate: the
	// provider reported none although the call may have been billed (a
	// failure after the request was written, a 5xx it did not decline).
	Estimated bool `json:"estimated,omitempty"`
	// Billed is true when EstimatedUSD is the charge the provider reported
	// (metered pricing) rather than tokens × the catalog price.
	Billed bool   `json:"billed,omitempty"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TopicUsage carries every provider call (the UI's cost meter).
var TopicUsage = events.NewTopic[UsageRecord]("engine.usage")

// UsageSink persists usage counters (store.Store implements it).
type UsageSink interface {
	AddUsage(ctx context.Context, rec UsageRecord) error
}
