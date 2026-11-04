package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/httpx"
)

// DiscoveryMaxBodyBytes bounds the official model-list response. A larger
// body is refused before it can be parsed.
const DiscoveryMaxBodyBytes = 2 << 20

// DiscoveryDefaultProtocol is the protocol a discovery request without one
// uses for the default path and model type inference.
const DiscoveryDefaultProtocol = ProtocolOpenAIChat

// Model discovery errors. The API maps every one of them to a stable code;
// none may carry an API key or a response body.
var (
	ErrDiscoveryInvalidRequest = errors.New("engine: invalid model discovery request")
	ErrDiscoveryAuth           = errors.New("engine: model discovery credentials rejected")
	ErrDiscoveryNotFound       = errors.New("engine: model list endpoint not found")
	ErrDiscoveryFailed         = errors.New("engine: model discovery failed")
	ErrDiscoveryInvalid        = errors.New("engine: model discovery response is invalid")
	ErrDiscoveryEmpty          = errors.New("engine: model discovery returned no models")
)

// DiscoverRequest is one official model-list lookup. APIKey is the plaintext
// value from the settings form; APIKeySecret names a stored secret and is
// only read when APIKey is empty. Neither is ever returned or logged.
type DiscoverRequest struct {
	BaseURL      string `json:"base_url"`
	Protocol     string `json:"protocol,omitempty"`
	APIKey       string `json:"api_key,omitempty"`
	APIKeySecret string `json:"api_key_secret,omitempty"`
}

// ProviderModel is one normalized model of the provider's official list.
// Missing values stay zero or omitted; the discovery code never invents
// context limits, output caps, prices or capabilities.
type ProviderModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	Type             string   `json:"type,omitempty"`
	ContextTokens    int      `json:"context_tokens,omitempty"`
	MaxOutputTokens  int      `json:"max_output_tokens,omitempty"`
	PriceInput       *float64 `json:"price_input_per_mtok,omitempty"`
	PriceOutput      *float64 `json:"price_output_per_mtok,omitempty"`
	StructuredOutput bool     `json:"structured_output,omitempty"`
	Reasoning        bool     `json:"reasoning,omitempty"`
	Tools            bool     `json:"tools,omitempty"`
	Streaming        bool     `json:"streaming,omitempty"`
	Endpoints        []string `json:"endpoints,omitempty"`
	Path             string   `json:"path"`
}

// NormalizeDiscoverRequest validates the request boundary and applies the
// defaults: a trimmed base URL without its trailing slash and an explicit
// protocol (openai_chat when absent). It is idempotent.
func NormalizeDiscoverRequest(req DiscoverRequest) (DiscoverRequest, error) {
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		return req, fmt.Errorf("%w: base_url is required", ErrDiscoveryInvalidRequest)
	}
	if err := endpointURLError(base); err != nil {
		return req, fmt.Errorf("%w: base_url %v", ErrDiscoveryInvalidRequest, err)
	}
	req.BaseURL = base
	req.Protocol = strings.TrimSpace(req.Protocol)
	if req.Protocol == "" {
		req.Protocol = DiscoveryDefaultProtocol
	}
	return req, nil
}

// DiscoverModels fetches the provider's official GET {base_url}/models
// through the shared HTTP client and returns its models sorted by id.
func DiscoverModels(ctx context.Context, deps Deps, req DiscoverRequest) ([]ProviderModel, error) {
	req, err := NormalizeDiscoverRequest(req)
	if err != nil {
		return nil, err
	}
	if deps.Config == nil {
		return nil, fmt.Errorf("%w: no configuration", ErrDiscoveryFailed)
	}
	key, err := discoveryKey(deps, req)
	if err != nil {
		return nil, err
	}

	modelsURL := req.BaseURL + "/models"
	u, err := url.Parse(modelsURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid model list URL", ErrDiscoveryInvalidRequest)
	}
	host := u.Host

	httpReq, err := httpx.NewRequest(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDiscoveryInvalidRequest, host, err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpx.SetBearer(httpReq, key)

	client, err := httpx.New(deps.Config, deps.Bus, "provider.discover")
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrDiscoveryFailed, host, err)
	}
	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrDiscoveryFailed, host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, discoveryStatusError(host, resp.StatusCode)
	}
	raw, err := readDiscoveryBody(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s answered HTTP %d: %v", ErrDiscoveryFailed, host, resp.StatusCode, err)
	}
	payload, err := decodeDiscoveryPayload(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s answered HTTP %d", ErrDiscoveryInvalid, host, resp.StatusCode)
	}

	models := make([]ProviderModel, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if model, ok := normalizeProviderModel(entry, req.Protocol); ok {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%w: %s answered HTTP %d", ErrDiscoveryEmpty, host, resp.StatusCode)
	}
	slices.SortFunc(models, func(a, b ProviderModel) int { return strings.Compare(a.ID, b.ID) })
	log.Info("provider models discovered", "method", http.MethodGet, "host", host,
		"status", resp.StatusCode, "models", len(models), "latency_ms", time.Since(start).Milliseconds())
	return models, nil
}

// discoveryKey resolves the key precedence: plaintext form value, then the
// named stored secret, then no authentication.
func discoveryKey(deps Deps, req DiscoverRequest) (string, error) {
	if req.APIKey != "" {
		return req.APIKey, nil
	}
	name := strings.TrimSpace(req.APIKeySecret)
	if name == "" {
		return "", nil
	}
	if deps.Secrets == nil {
		return "", fmt.Errorf("%w: secret %s needs a secret store", ErrNoKey, name)
	}
	value, err := deps.Secrets.Get(name)
	if err != nil || value == "" {
		return "", fmt.Errorf("%w: secret %s has no value", ErrNoKey, name)
	}
	return value, nil
}

// discoveryStatusError names an upstream failure without quoting its body.
func discoveryStatusError(host string, status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s answered HTTP %d", ErrDiscoveryAuth, host, status)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s answered HTTP %d", ErrDiscoveryNotFound, host, status)
	default:
		return fmt.Errorf("%w: %s answered HTTP %d", ErrDiscoveryFailed, host, status)
	}
}

// readDiscoveryBody reads at most DiscoveryMaxBodyBytes and rejects a
// larger response explicitly instead of truncating it.
func readDiscoveryBody(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, DiscoveryMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > DiscoveryMaxBodyBytes {
		return nil, fmt.Errorf("response larger than %d MiB", DiscoveryMaxBodyBytes>>20)
	}
	return raw, nil
}

type discoveryPayload struct {
	Object string           `json:"object"`
	Data   []discoveryModel `json:"data"`
}

// discoveryModel is the official OpenAI shape plus the extended fields a
// provider may add. Unknown fields are ignored.
type discoveryModel struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Type                string                 `json:"type"`
	ContextLength       int                    `json:"context_length"`
	ContextTokens       int                    `json:"context_tokens"`
	ContextWindow       int                    `json:"context_window"`
	MaxOutputTokens     int                    `json:"max_output_tokens"`
	MaxCompletionTokens int                    `json:"max_completion_tokens"`
	Endpoints           []string               `json:"endpoints"`
	Capabilities        *discoveryCapabilities `json:"capabilities"`
	Pricing             *discoveryPricing      `json:"pricing"`
}

type discoveryCapabilities struct {
	StructuredOutput bool `json:"structured_output"`
	Reasoning        bool `json:"reasoning"`
	Tools            bool `json:"tools"`
	Streaming        bool `json:"streaming"`
}

type discoveryPricing struct {
	Input  any    `json:"input"`
	Output any    `json:"output"`
	Unit   string `json:"unit"`
}

// decodeDiscoveryPayload decodes exactly one JSON document with number
// tokens preserved, so pricing may be a JSON number or a numeric string.
func decodeDiscoveryPayload(raw []byte) (discoveryPayload, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var payload discoveryPayload
	if err := dec.Decode(&payload); err != nil {
		return payload, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return payload, errors.New("trailing data")
	}
	return payload, nil
}

// normalizeProviderModel converts one official entry. Entries without an
// id are skipped (ok=false).
func normalizeProviderModel(entry discoveryModel, protocol string) (ProviderModel, bool) {
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return ProviderModel{}, false
	}
	endpoints := cleanEndpoints(entry.Endpoints)
	model := ProviderModel{
		ID:              id,
		Name:            strings.TrimSpace(entry.Name),
		Type:            modelType(entry.Type, endpoints, protocol),
		ContextTokens:   firstPositive(entry.ContextLength, entry.ContextTokens, entry.ContextWindow),
		MaxOutputTokens: firstPositive(entry.MaxOutputTokens, entry.MaxCompletionTokens),
		Endpoints:       endpoints,
		Path:            modelPath(endpoints, protocol),
	}
	if caps := entry.Capabilities; caps != nil {
		model.StructuredOutput = caps.StructuredOutput
		model.Reasoning = caps.Reasoning
		model.Tools = caps.Tools
		model.Streaming = caps.Streaming
	}
	if pricing := entry.Pricing; pricing != nil {
		if factor, ok := priceFactor(pricing.Unit); ok {
			model.PriceInput = pricePerMtok(pricing.Input, factor)
			model.PriceOutput = pricePerMtok(pricing.Output, factor)
		}
	}
	return model, true
}

func cleanEndpoints(endpoints []string) []string {
	out := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			out = append(out, endpoint)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hasEndpoint matches the official short endpoint names and their path or
// absolute-URL spellings, but the normalized path never carries the base.
func hasEndpoint(endpoints []string, want string) bool {
	for _, endpoint := range endpoints {
		endpoint = strings.TrimSuffix(strings.TrimSpace(endpoint), "/")
		endpoint = strings.TrimPrefix(endpoint, "/")
		if endpoint == want || strings.HasSuffix(endpoint, "/"+want) {
			return true
		}
	}
	return false
}

func modelType(raw string, endpoints []string, protocol string) string {
	if typ := strings.TrimSpace(raw); typ != "" {
		return typ
	}
	switch {
	case hasEndpoint(endpoints, "chat/completions"):
		return "chat"
	case hasEndpoint(endpoints, "embeddings"):
		return "embedding"
	default:
		return protocolType(protocol)
	}
}

func modelPath(endpoints []string, protocol string) string {
	switch {
	case hasEndpoint(endpoints, "chat/completions"):
		return "/chat/completions"
	case hasEndpoint(endpoints, "embeddings"):
		return "/embeddings"
	default:
		return defaultPath(protocol)
	}
}

func protocolType(protocol string) string {
	proto, ok := ProtocolOf(protocol)
	if !ok {
		return ""
	}
	if proto.Kind == KindEmbedder {
		return "embedding"
	}
	return "chat"
}

func defaultPath(protocol string) string {
	if protocolType(protocol) == "embedding" {
		return "/embeddings"
	}
	return "/chat/completions"
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// priceFactor converts a provider unit to per-million tokens. Unknown
// units are not guessed.
func priceFactor(unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "per_1m_tokens":
		return 1, true
	case "per_1k_tokens":
		return 1_000, true
	case "per_token":
		return 1_000_000, true
	default:
		return 0, false
	}
}

// pricePerMtok accepts a JSON number or a numeric string and returns a
// price converted to per-million tokens. Invalid, negative or non-finite
// values are omitted.
func pricePerMtok(value any, factor float64) *float64 {
	price, ok := numericValue(value)
	if !ok {
		return nil
	}
	price *= factor
	if math.IsNaN(price) || math.IsInf(price, 0) {
		return nil
	}
	return &price
}

func numericValue(value any) (float64, bool) {
	var number float64
	switch v := value.(type) {
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = v
	case int:
		number = float64(v)
	case int64:
		number = float64(v)
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	return number, true
}
