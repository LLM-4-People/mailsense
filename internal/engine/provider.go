package engine

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/httpx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

// Provider is one provider record. The record id is the JSON key of the
// provider file; the other fields are the complete definition of the
// provider (endpoint, model, key name, limits, prices and adapter data).
type Provider struct {
	// ID is the immutable record id: the top-level key of the provider
	// file. It is only set on API request bodies; the file writer clears
	// it (the object key is the id).
	ID string `json:"id,omitempty"`
	// Label is display-only ("" = show the id).
	Label string `json:"label,omitempty"`
	// Protocol is the record's wire protocol (ProtocolSystemOne, ...);
	// the engine kind and auth scheme derive from it.
	Protocol string `json:"protocol"`
	// BaseURL is absolute http(s); it may contain {model}. It is empty
	// only for a managed runtime embedder.
	BaseURL string `json:"base_url"`
	// Path is the request path, starting with /; it may contain {model}.
	Path string `json:"path"`
	// Model is the provider's model id.
	Model string `json:"model"`
	// APIKeySecret is the secret-store name holding this record's key.
	// It is required for a remote decider/generator and empty for a
	// managed runtime (the runtime generates its own key) or a local
	// endpoint without auth.
	APIKeySecret string `json:"api_key_secret,omitempty"`
	// Runtime names the managed sidecar serving the model (embedders
	// only; BaseURL is then empty and the sidecar knows its port and key).
	Runtime string `json:"runtime,omitempty"`

	ContextTokens int     `json:"context_tokens,omitempty"`  // budget enforced before sending
	CharsPerToken float64 `json:"chars_per_token,omitempty"` // conservative estimate (no tokenizer)
	// ReserveTokensPerByte is the worst case a budget reserves before each
	// call (deciders and generators): tokens per UTF-8 byte of the request.
	ReserveTokensPerByte float64 `json:"reserve_tokens_per_byte,omitempty"`
	// ReserveOutputTokens is the most output one call may bill (deciders
	// with an output price must set it: the ceiling covers it; a generator
	// sends it as max_tokens).
	ReserveOutputTokens int `json:"reserve_output_tokens,omitempty"`
	// MaxQuestions is the most typed questions one decision call may carry.
	MaxQuestions int `json:"max_questions,omitempty"`
	// ProbabilityTolerance bounds the drift allowed between an answer's
	// probabilities and its own value (they sum to 1, the choice is the
	// most probable option, a score is their expected level).
	ProbabilityTolerance float64 `json:"probability_tolerance,omitempty"`
	// ResponseFormat is the chat response format a generator requests
	// ("" = none; ResponseFormatJSON = the provider's strict JSON mode).
	ResponseFormat string `json:"response_format,omitempty"`
	// ReasoningEffort is the generator's reasoning budget when the provider
	// supports one ("" = the provider's default).
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	PriceInput      *float64 `json:"price_input_per_mtok"`
	PriceOutput     *float64 `json:"price_output_per_mtok"`
	Retry           Retry    `json:"retry,omitzero"`
	Envelope        string   `json:"envelope,omitempty"`
	NoulFold        string   `json:"noul_fold,omitempty"`
	// Embedding is the model data of an embedder.
	Embedding *EmbeddingSpec `json:"embedding,omitempty"`
}

// Retry overrides the http.retry.* policy for one provider.
type Retry struct {
	MaxAttempts *int  `json:"max_attempts,omitempty"`
	Statuses    []int `json:"statuses,omitempty"`
	// QuotaCodes are provider error codes/types that mean "out of credit"
	// even on a normally retryable status (a quota 429 is never retried).
	QuotaCodes []string `json:"quota_codes,omitempty"`
}

// Envelopes and response formats.
const (
	EnvelopeCloudflare = "cloudflare"

	// ResponseFormatJSON is the OpenAI-compatible strict JSON answer mode.
	ResponseFormatJSON = "json_object"
)

// reasoningEfforts are the accepted values of reasoning_effort: "" keeps the
// provider's default, the rest name the provider's reasoning budget. The
// validator and the record schema read this one list.
var reasoningEfforts = []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}

// maxTokensPerByte bounds reserve_tokens_per_byte (a sanity limit on data).
const maxTokensPerByte = 4

// limits are the schema rules a provider's retry overrides must keep
// (http.retry.statuses not_in, http.retry.max_attempts bounds).
type limits struct {
	never                    []int
	minAttempts, maxAttempts int
}

var (
	placeholderRE = regexp.MustCompile(`\{([^{}]*)\}`)
	// providerIDRE: immutable record ids are lower-case slugs.
	providerIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

// ProviderIDValid reports whether id is a valid immutable record id.
func ProviderIDValid(id string) bool { return providerIDRE.MatchString(id) }

// Kind is the engine kind the record's protocol provides.
func (p Provider) Kind() Kind {
	proto, ok := ProtocolOf(p.Protocol)
	if !ok {
		return ""
	}
	return proto.Kind
}

// NeedsKey reports whether the record's protocol requires an API key.
func (p Provider) NeedsKey() bool {
	proto, ok := ProtocolOf(p.Protocol)
	return ok && proto.KeyRequired && p.Runtime == ""
}

// reasoningAnswerTokens is the output a reasoning model may spend before it
// writes the answer, by the provider's reasoning_effort. It is an allowance,
// not a target: "low" spent about 350 tokens on a three-message thread, and
// the provider's declared output reserve is always the ceiling. "none" and an
// empty effort add nothing.
var reasoningAnswerTokens = map[string]int{
	"minimal": 1024,
	"low":     4096,
	"medium":  8192,
	"high":    16384,
	"xhigh":   32768,
	"max":     32768,
}

// AnswerOutputTokens returns the request's output cap for an answer budget.
// A reasoning generator spends output tokens on its reasoning before the
// answer, so the cap must cover both; reserving only the answer truncates the
// call with a null content. A provider without reasoning keeps the answer
// budget exactly, and the record's declared output reserve is the ceiling.
func (p Provider) AnswerOutputTokens(answer int) int {
	if answer < 1 {
		return answer
	}
	cap := answer + reasoningAnswerTokens[p.ReasoningEffort]
	if p.ReserveOutputTokens > 0 && cap > p.ReserveOutputTokens {
		cap = p.ReserveOutputTokens
	}
	return cap
}

// Fingerprint identifies the model endpoint behind a record: protocol,
// endpoint, path, model and embedding spec. Display data (id, label) and
// the secret name never change it, so a rename keeps derived caches while a
// model or endpoint change invalidates them.
func (p Provider) Fingerprint() string {
	parts := []string{"protocol=" + p.Protocol, "base_url=" + p.BaseURL, "path=" + p.Path, "model=" + p.Model}
	if p.Embedding != nil {
		parts = append(parts, "embedding="+strings.Join(p.Embedding.fingerprintParts(), ","))
	}
	return digest.Hex([]byte(strings.Join(parts, "\x00")), 16)
}

// Key is the readable paid-cache identity of a record: id, model and
// fingerprint. A rename alone changes the key, which is correct for
// decision caches keyed by the configured provider.
func (p Provider) Key() string { return DeciderKey(p.ID, p.Model, p.Fingerprint()) }

// DeciderKey is the one identity of a configured decider: provider id, model
// and fingerprint. Paid caches key on it, so a model or endpoint change
// invalidates them; the provider record and the live decider share the shape.
func DeciderKey(name, model, fingerprint string) string {
	return name + "/" + model + "/" + fingerprint
}

// Validate reports every problem of the record set, using the current
// retry bounds. The caller writes nothing when it returns an error.
func (c *Catalog) Validate(l limits) error {
	var problems []string
	for _, id := range c.order {
		p := c.byID[id]
		problems = append(problems, p.problems(l)...)
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return &ValidationError{Problems: problems}
	}
	return nil
}

// ValidationError carries every problem of a provider document.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("engine: invalid providers:\n  - %s", strings.Join(e.Problems, "\n  - "))
}

// problems lists every problem of p (catalog load reports them all).
func (p *Provider) problems(l limits) []string {
	var out []string
	bad := func(field, format string, args ...any) {
		out = append(out, fmt.Sprintf("providers/%s/%s: %s", p.ID, field, fmt.Sprintf(format, args...)))
	}
	if !ProviderIDValid(p.ID) {
		bad("id", "must match %s", providerIDRE)
	}
	proto, known := ProtocolOf(p.Protocol)
	switch {
	case p.Protocol == "":
		bad("protocol", "is required")
	case !known:
		bad("protocol", "must be one of %s, got %q", protocolNames(), p.Protocol)
	}
	if p.Model == "" {
		bad("model", "is required")
	}
	p.endpointProblems(proto, known, bad)
	if p.ContextTokens < 1 && p.Runtime == "" {
		bad("context_tokens", "must be ≥ 1")
	}
	if p.Runtime != "" && p.ContextTokens != 0 {
		bad("context_tokens", "comes from the runtime catalog for a managed runtime (leave it out)")
	}
	if p.CharsPerToken <= 0 {
		bad("chars_per_token", "must be > 0")
	}
	for _, price := range []*float64{p.PriceInput, p.PriceOutput} {
		if price != nil && (*price < 0 || !finite(*price)) {
			bad("price", "must be a finite number ≥ 0 or null")
		}
	}
	if p.Runtime != "" {
		if !proto.RuntimeAllowed {
			bad("runtime", "is only for an embedder protocol")
		}
		if p.APIKeySecret != "" {
			bad("api_key_secret", "must be empty for a managed runtime (it generates its key)")
		}
	} else if proto.KeyRequired {
		if p.APIKeySecret == "" {
			bad("api_key_secret", "is required for protocol %q", p.Protocol)
		} else if err := secrets.ValidName(p.APIKeySecret); err != nil {
			bad("api_key_secret", "%v", err)
		}
	} else if p.APIKeySecret != "" {
		if err := secrets.ValidName(p.APIKeySecret); err != nil {
			bad("api_key_secret", "%v", err)
		}
	}
	if known {
		switch proto.Kind {
		case KindDecider:
			p.deciderProblems(bad)
		case KindEmbedder:
			p.embedderProblems(bad)
		case KindGenerator:
			p.generatorProblems(bad)
		}
	} else {
		p.rejectKindFields(bad)
	}
	if len(p.Retry.Statuses) > 0 || p.Retry.MaxAttempts != nil || len(p.Retry.QuotaCodes) > 0 {
		p.retryProblems(l, bad)
	}
	return out
}

// validEndpointURL is the one URL rule for provider records and discovery:
// an absolute http(s) URL with no embedded credentials, query or fragment.
// Credentials belong in the secret store, and a query or fragment would be
// persisted and echoed by the provider view and its validation error.
func validEndpointURL(raw string) bool { return endpointURLError(raw) == nil }

// endpointURLError explains why raw is not a valid provider base URL. It is
// the single endpoint rule; the transport-level policy (httpx.ValidateURL)
// is also applied to every request, so a record or a pasted URL cannot pass
// here and still reach the network.
func endpointURLError(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not an absolute URL")
	}
	if u.Host == "" {
		return errors.New("has no host")
	}
	if u.User != nil {
		return errors.New("must not carry credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not carry a query or fragment")
	}
	return httpx.ValidateURL(u)
}

func (p *Provider) endpointProblems(proto Protocol, known bool, bad func(string, string, ...any)) {
	if p.Runtime != "" {
		if p.BaseURL != "" {
			bad("base_url", "must be empty for a managed runtime (%s chooses the port)", p.Runtime)
		}
		if known && !proto.RuntimeAllowed {
			return
		}
	} else if !validEndpointURL(p.BaseURL) {
		// Never echo the value: it may contain a credential the owner pasted.
		bad("base_url", "must be an absolute http(s) URL without credentials, query, fragment or metadata host")
	}
	if !strings.HasPrefix(p.Path, "/") {
		bad("path", "must start with /, got %q", p.Path)
	}
	for _, m := range placeholderRE.FindAllStringSubmatch(p.BaseURL+p.Path, -1) {
		if m[1] != "model" {
			bad("path", "unknown placeholder {%s} (known: model)", m[1])
		}
	}
	if strings.Contains(p.BaseURL+p.Path, "{model}") && p.Model == "" {
		bad("model", "is required when the endpoint contains {model}")
	}
}

func (p *Provider) deciderProblems(bad func(string, string, ...any)) {
	if p.MaxQuestions < 1 || p.ProbabilityTolerance <= 0 || p.ProbabilityTolerance >= 1 ||
		p.ReserveTokensPerByte <= 0 || p.ReserveTokensPerByte > maxTokensPerByte {
		bad("limits", "max_questions must be ≥ 1, probability_tolerance in (0, 1) and reserve_tokens_per_byte in (0, %d]", maxTokensPerByte)
	}
	if p.PriceOutput != nil && *p.PriceOutput > 0 && p.ReserveOutputTokens < 1 {
		bad("reserve_output_tokens", "is required with an output price (the budget reserves it before each call)")
	}
	if p.Envelope != "" && p.Envelope != EnvelopeCloudflare {
		bad("envelope", "must be empty or %q", EnvelopeCloudflare)
	}
	if p.Runtime != "" || p.Embedding != nil || p.ResponseFormat != "" || p.ReasoningEffort != "" {
		bad("generator/embedder fields", "response_format, reasoning_effort, runtime and embedding are not decider fields")
	}
}

func (p *Provider) embedderProblems(bad func(string, string, ...any)) {
	if p.Embedding == nil {
		bad("embedding", "is required for an embedder")
	} else if err := p.Embedding.Validate(); err != nil {
		bad("embedding", "%v", err)
	}
	if p.MaxQuestions != 0 || p.ProbabilityTolerance != 0 || p.Envelope != "" || p.NoulFold != "" ||
		p.ResponseFormat != "" || p.ReasoningEffort != "" || p.ReserveTokensPerByte != 0 {
		bad("decider/generator fields", "max_questions, probability_tolerance, envelope, noul_fold, response_format, reasoning_effort and reserve_tokens_per_byte are not embedder fields")
	}
}

// generatorProblems checks the limits a paid chat generator needs: a
// worst-case reservation per call (the budget's ceiling), an output cap
// (the request's max_tokens) and both prices (real cost accounting). A
// generator is remote: a managed runtime is an embedder's concern.
func (p *Provider) generatorProblems(bad func(string, string, ...any)) {
	if p.ReserveTokensPerByte <= 0 || p.ReserveTokensPerByte > maxTokensPerByte || p.ReserveOutputTokens < 1 {
		bad("limits", "reserve_tokens_per_byte must be in (0, %d] and reserve_output_tokens must be ≥ 1 for a generator (the request's max_tokens and the budget's ceiling)", maxTokensPerByte)
	} else if p.ContextTokens > 0 && p.ReserveOutputTokens >= p.ContextTokens {
		bad("reserve_output_tokens", "must leave room for input (it must be < context_tokens)")
	}
	if p.PriceInput == nil || p.PriceOutput == nil {
		bad("price", "a generator needs price_input_per_mtok and price_output_per_mtok (real cost accounting)")
	}
	if p.ResponseFormat != "" && p.ResponseFormat != ResponseFormatJSON {
		bad("response_format", "must be empty or %q", ResponseFormatJSON)
	}
	if p.ReasoningEffort != "" && !slices.Contains(reasoningEfforts, p.ReasoningEffort) {
		bad("reasoning_effort", "must be empty or %s, got %q", strings.Join(reasoningEfforts[1:], ", "), p.ReasoningEffort)
	}
	if p.MaxQuestions != 0 || p.ProbabilityTolerance != 0 || p.Envelope != "" || p.NoulFold != "" {
		bad("decider fields", "max_questions, probability_tolerance, envelope and noul_fold are decider fields")
	}
	if p.Runtime != "" || p.Embedding != nil {
		bad("runtime/embedding", "are embedder fields")
	}
}

// rejectKindFields reports the fields of an unknown protocol that cannot be
// classified; the unknown protocol itself is already reported.
func (p *Provider) rejectKindFields(bad func(string, string, ...any)) {
	if p.Embedding != nil {
		bad("embedding", "is only for an embedder protocol")
	}
	if p.Runtime != "" {
		bad("runtime", "is only for an embedder protocol")
	}
}

func (p *Provider) retryProblems(l limits, bad func(string, string, ...any)) {
	if a := p.Retry.MaxAttempts; a != nil && (*a < l.minAttempts || *a > l.maxAttempts) {
		bad("retry/max_attempts", "must be %d-%d (http.retry.max_attempts bounds)", l.minAttempts, l.maxAttempts)
	}
	for _, s := range p.Retry.Statuses {
		if s < 400 || s > 599 || slices.Contains(l.never, s) {
			bad("retry/statuses", "%d cannot be retried (allowed: 4xx/5xx except %v)", s, l.never)
		}
	}
}

// URL is the endpoint with {model} filled in.
func (p *Provider) URL() (string, error) {
	full := p.BaseURL + p.Path
	if strings.Contains(full, "{model}") && p.Model == "" {
		return "", fmt.Errorf("engine: provider %s has no model for {model} (set it in engine.providers.file)", p.ID)
	}
	return strings.ReplaceAll(full, "{model}", url.PathEscape(p.Model)), nil
}

// Authorize applies the protocol's auth scheme to a request. A record with
// no key sends none (a local embedder).
func (p *Provider) Authorize(req *http.Request, key string) error {
	proto, ok := ProtocolOf(p.Protocol)
	if !ok {
		return fmt.Errorf("engine: provider %s: unknown protocol %q", p.ID, p.Protocol)
	}
	switch proto.Auth {
	case AuthBearer:
		httpx.SetBearer(req, key)
	default:
		return fmt.Errorf("engine: provider %s: unsupported auth scheme %q", p.ID, proto.Auth)
	}
	return nil
}

// PriceKnown reports whether the input price is known.
func (p *Provider) PriceKnown() bool { return p.PriceInput != nil }

// Cost is the estimated USD of a token count (0 when the price is unknown).
func (p *Provider) Cost(input, output int64) float64 {
	usd := 0.0
	if p.PriceInput != nil {
		usd += float64(input) * *p.PriceInput / 1e6
	}
	if p.PriceOutput != nil {
		usd += float64(output) * *p.PriceOutput / 1e6
	}
	return usd
}

// AccountedUsage prices one call. When billed, the provider may have
// charged the request without reporting what it used (the request failed
// after it was written, or a 5xx it did not decline), so the input tokens
// are the request estimate and the output is unknown (0). Otherwise the
// recorded token counts are priced as they stand.
func (p *Provider) AccountedUsage(u Usage, body []byte, billed bool) Usage {
	if billed {
		u.InputTokens = p.Tokens(body)
		u.OutputTokens = 0
	}
	u.EstimatedUSD = p.Cost(u.InputTokens, u.OutputTokens)
	return u
}

// APIKey reads the provider's key from the secret store. A missing, empty or
// unreadable secret is the one ErrNoKey error, with the same message for every
// adapter, so a caller cannot get a different refusal depending on which
// adapter asked.
func (p *Provider) APIKey(sec secrets.Store) (string, error) {
	key, err := sec.Get(p.APIKeySecret)
	if err != nil || key == "" {
		return "", fmt.Errorf("%w: provider %s has no usable key: %v", ErrNoKey, p.ID, err)
	}
	return key, nil
}

// Tokens estimates a request's tokens from its characters (no tokenizer:
// the record's conservative chars_per_token). The count is code points,
// never bytes: a multi-byte character is one character.
func (p *Provider) Tokens(body []byte) int64 {
	return int64(math.Ceil(float64(utf8.RuneCount(body)) / p.CharsPerToken))
}

// Ceiling is the most one call can cost: reserve_tokens_per_byte of every
// byte of the request, plus reserve_output_tokens at the output price.
func (p *Provider) Ceiling(body []byte) Estimate {
	tokens := int64(math.Ceil(float64(len(body)) * p.ReserveTokensPerByte))
	return Estimate{Calls: 1, InputTokens: tokens,
		EstimatedUSD: p.Cost(tokens, int64(p.ReserveOutputTokens)), PriceKnown: p.PriceKnown()}
}

// PolicyHook returns the httpx.WithPolicy hook of this record: the live
// http.retry.* policy with the record's overrides, and a quota code in a
// retryable response makes it final. http.error_text_chars is read on every
// call because the owner may change it while a run is active.
func (p *Provider) PolicyHook(cfg *config.Config) func(*httpx.Policy) {
	return func(pol *httpx.Policy) {
		*pol = p.Policy(*pol, errorTextChars(cfg))
	}
}

// Declined reports whether a final status means the server declined the
// work (a retryable status: nothing billed) rather than possibly did it.
// Every status below 500 is a decline; a 5xx is one only while the policy
// lists it as retryable.
func (p *Provider) Declined(status int, cfg *config.Config) bool {
	pol := p.Policy(httpx.PolicyFromConfig(cfg.Snapshot()), errorTextChars(cfg))
	return status < 500 || slices.Contains(pol.Statuses, status)
}

// Policy applies the provider's retry overrides to base: the attempts and
// the retryable statuses it declares, and its quota codes (a retryable
// response carrying one is final). limit is http.error_text_chars, the
// most of an error body the quota probe reads.
func (p *Provider) Policy(base httpx.Policy, limit int) httpx.Policy {
	if p.Retry.MaxAttempts != nil {
		base.MaxAttempts = *p.Retry.MaxAttempts
	}
	if len(p.Retry.Statuses) > 0 {
		base.Statuses = append([]int(nil), p.Retry.Statuses...)
	}
	if quota := p.Retry.QuotaCodes; len(quota) > 0 {
		base.Final = func(_ int, body []byte) bool {
			codes, _ := DescribeError(body, limit)
			return IsQuota(codes, quota)
		}
	}
	return base
}

// SuggestSecretName is the generic secret name a new record gets when the
// API caller omits one: PROVIDER_<UPPER_ID>_API_KEY. It is derived from the
// record id, never from a vendor.
func SuggestSecretName(id string) string {
	upper := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(id))
	return "PROVIDER_" + upper + "_API_KEY"
}

// ProviderFieldType is the JSON type of one provider record field.
type ProviderFieldType string

// Provider field types.
const (
	ProviderFieldString ProviderFieldType = "string"
	ProviderFieldInt    ProviderFieldType = "int"
	ProviderFieldFloat  ProviderFieldType = "float"
	ProviderFieldBool   ProviderFieldType = "bool"
	ProviderFieldObject ProviderFieldType = "object"
)

// ProviderBounds is the numeric range the validator accepts for one field.
// A nil side is unbounded; an exclusive side is an open interval.
type ProviderBounds struct {
	Min          *float64 `json:"min,omitempty"`
	Max          *float64 `json:"max,omitempty"`
	MinExclusive bool     `json:"min_exclusive,omitempty"`
	MaxExclusive bool     `json:"max_exclusive,omitempty"`
}

// ProviderField is one field of the provider record as the wire describes
// it: the Go field name, its JSON key, its type, the kinds that accept it,
// its numeric bounds, its enum values, and whether it is required or
// advanced. Required means the validator rejects the record whenever the
// field is empty for every kind; a conditional requirement (a managed
// runtime, a key-required protocol) stays in the validator.
type ProviderField struct {
	Name     string            `json:"name"`
	JSON     string            `json:"json"`
	Type     ProviderFieldType `json:"type"`
	Kinds    []Kind            `json:"kinds"`
	Bounds   *ProviderBounds   `json:"bounds,omitempty"`
	Enum     []string          `json:"enum,omitempty"`
	Required bool              `json:"required"`
	Advanced bool              `json:"advanced"`
}

// providerFieldRule is the semantic half of one record field, keyed by Go
// field name. The reflection walk over Provider supplies the name, JSON key
// and type, so this table never restates them: it is the only copy of the
// kinds, bounds, enums, required and advanced flags.
type providerFieldRule struct {
	kinds    []Kind
	bounds   *ProviderBounds
	enum     []string
	required bool
	advanced bool
}

var (
	// providerAllKinds is every engine kind, in the kind declaration order.
	providerAllKinds = []Kind{KindDecider, KindGenerator, KindEmbedder}
	// providerPaidKinds are the kinds that carry a budget and a price.
	providerPaidKinds = []Kind{KindDecider, KindGenerator}
	// providerDeciderKinds, providerGeneratorKinds and providerEmbedderKinds
	// are the single-kind field sets of each adapter.
	providerDeciderKinds = []Kind{KindDecider}
	// providerGeneratorKinds is the generator-only field set.
	providerGeneratorKinds = []Kind{KindGenerator}
	// providerEmbedderKinds is the embedder-only field set.
	providerEmbedderKinds = []Kind{KindEmbedder}
)

// providerFieldRules is the one semantic description of the provider record.
// It mirrors the rules of Provider.problems: a bound appears only where the
// validator enforces an unconditional one (a conditional rule, such as
// context_tokens on a managed runtime, stays in the validator and is not
// stated as a bound here). Nested records (retry, embedding) keep their own
// validators and are described as objects.
var providerFieldRules = map[string]providerFieldRule{
	"ID":           {kinds: providerAllKinds, required: true},
	"Label":        {kinds: providerAllKinds},
	"Protocol":     {kinds: providerAllKinds, enum: ProtocolNames(), required: true},
	"BaseURL":      {kinds: providerAllKinds},
	"Path":         {kinds: providerAllKinds, required: true},
	"Model":        {kinds: providerAllKinds, required: true},
	"APIKeySecret": {kinds: providerAllKinds},
	"Runtime":      {kinds: providerEmbedderKinds, advanced: true},
	"ContextTokens": {kinds: providerAllKinds, advanced: true,
		bounds: &ProviderBounds{Min: providerNum(1)}},
	"CharsPerToken": {kinds: providerAllKinds, advanced: true,
		bounds: &ProviderBounds{Min: providerNum(0), MinExclusive: true}},
	"ReserveTokensPerByte": {kinds: providerPaidKinds, advanced: true,
		bounds: &ProviderBounds{Min: providerNum(0), MinExclusive: true, Max: providerNum(maxTokensPerByte)}},
	"ReserveOutputTokens": {kinds: providerPaidKinds, advanced: true},
	"MaxQuestions":        {kinds: providerDeciderKinds, advanced: true, bounds: &ProviderBounds{Min: providerNum(1)}},
	"ProbabilityTolerance": {kinds: providerDeciderKinds, advanced: true,
		bounds: &ProviderBounds{Min: providerNum(0), MinExclusive: true, Max: providerNum(1), MaxExclusive: true}},
	"ResponseFormat":  {kinds: providerGeneratorKinds, enum: []string{"", ResponseFormatJSON}, advanced: true},
	"ReasoningEffort": {kinds: providerGeneratorKinds, enum: reasoningEfforts, advanced: true},
	"PriceInput":      {kinds: providerPaidKinds, advanced: true, bounds: &ProviderBounds{Min: providerNum(0)}},
	"PriceOutput":     {kinds: providerPaidKinds, advanced: true, bounds: &ProviderBounds{Min: providerNum(0)}},
	"Retry":           {kinds: providerAllKinds, advanced: true},
	"Envelope":        {kinds: providerDeciderKinds, enum: []string{"", EnvelopeCloudflare}, advanced: true},
	"NoulFold":        {kinds: providerDeciderKinds, advanced: true},
	"Embedding":       {kinds: providerEmbedderKinds, advanced: true},
}

// ProviderFields describes the provider record next to the struct it
// describes: one entry per field of Provider, in declaration order. Names,
// JSON keys and types come from the struct's own tags; kinds, bounds, enums
// and the required and advanced flags come from providerFieldRules. A field
// with no rule is a defect (TestProviderSchemaCoversEveryRecordField fails),
// never a silently undocumented field.
func ProviderFields() []ProviderField {
	t := reflect.TypeFor[Provider]()
	out := make([]ProviderField, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		rule, ok := providerFieldRules[f.Name]
		if !ok {
			continue
		}
		out = append(out, ProviderField{
			Name: f.Name, JSON: providerJSONName(f), Type: providerFieldType(f.Type),
			Kinds: slices.Clone(rule.kinds), Bounds: providerBoundsCopy(rule.bounds),
			Enum: slices.Clone(rule.enum), Required: rule.required, Advanced: rule.advanced,
		})
	}
	return out
}

// providerBoundsCopy returns a deep copy of b so a caller cannot change the
// rule table through the schema it reads.
func providerBoundsCopy(b *ProviderBounds) *ProviderBounds {
	if b == nil {
		return nil
	}
	cp := *b
	if b.Min != nil {
		min := *b.Min
		cp.Min = &min
	}
	if b.Max != nil {
		max := *b.Max
		cp.Max = &max
	}
	return &cp
}

// providerJSONName is the record key of a struct field (its JSON tag without
// the options).
func providerJSONName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// providerFieldType maps a Go field type to the wire type of the record. A
// pointer is its element's type (a nullable value); the nested record types
// are objects, described by their own validators.
func providerFieldType(t reflect.Type) ProviderFieldType {
	switch t.Kind() {
	case reflect.Pointer:
		return providerFieldType(t.Elem())
	case reflect.String:
		return ProviderFieldString
	case reflect.Bool:
		return ProviderFieldBool
	case reflect.Int, reflect.Int64:
		return ProviderFieldInt
	case reflect.Float64:
		return ProviderFieldFloat
	case reflect.Struct, reflect.Slice, reflect.Map:
		return ProviderFieldObject
	}
	return ""
}

// providerNum is a bound literal.
func providerNum(v float64) *float64 { return &v }

func protocolNames() string {
	return strings.Join(ProtocolNames(), ", ")
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
