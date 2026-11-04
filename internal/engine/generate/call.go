package generate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// ErrContract is a provider answer that violates the chat contract (no
// choice, no message, no content, malformed usage): the request may have
// been billed, so its usage is still recorded.
var ErrContract = errors.New("generate: answer violates the chat contract")

// contractError builds an ErrContract for one response field.
func contractError(field, format string, args ...any) error {
	return engine.ContractError(ErrContract, field, format, args...)
}

// message is one chat message.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// responseFormat is the OpenAI-compatible strict JSON mode.
type responseFormat struct {
	Type string `json:"type"`
}

// request is the wire body: the model, the system and user messages, the
// provider's JSON mode when configured, a deterministic temperature and
// the output cap (the same cap the budget reserves).
type request struct {
	Model           string          `json:"model"`
	Messages        []message       `json:"messages"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Temperature     float64         `json:"temperature"`
	MaxTokens       int             `json:"max_tokens"`
}

// encode builds one request; empty prompts are refused before any call.
// A prose call omits the provider's strict JSON mode: a summary is prose,
// and forcing JSON on it would either truncate the answer or make the
// summary contract reject a valid JSON wrapper.
func (a *Adapter) encode(system, user string, opts ...engine.GenerateOptions) ([]byte, error) {
	if mail.Strip(system) == "" || mail.Strip(user) == "" {
		return nil, fmt.Errorf("%w: system and user must be non-empty", engine.ErrInput)
	}
	prose := false
	for _, opt := range opts {
		prose = prose || opt.Prose
	}
	r := request{Model: a.p.Model, Messages: []message{{Role: "system", Content: system}, {Role: "user", Content: user}},
		ReasoningEffort: a.p.ReasoningEffort, Temperature: 0, MaxTokens: a.p.ReserveOutputTokens}
	if a.p.ResponseFormat != "" && !prose {
		r.ResponseFormat = &responseFormat{Type: a.p.ResponseFormat}
	}
	return json.Marshal(r)
}

// call sends one request and decodes it strictly. Usage is recorded for
// every call that reached the provider, successful or not.
func (a *Adapter) call(ctx context.Context, key string, body []byte) (engine.Generation, engine.Usage, error) {
	gen := engine.Generation{Model: a.p.Model}
	status, raw, usage, err, ok := a.paid().Post(ctx, a.client,
		engine.PostSpec{URL: a.url, Key: key, Body: body, Accept: true},
		engine.SentinelPolicy(a.p.ID, engine.ErrInput, engine.ErrHTTP), body)
	if !ok {
		return gen, usage, err
	}
	d, err := decode(raw)
	if d.gen.Model == "" {
		d.gen.Model = a.p.Model
	}
	usage.InputTokens, usage.OutputTokens = d.input, d.output
	estimated := !d.hasUsage
	if estimated {
		usage.InputTokens = a.p.Tokens(body)
		usage.OutputTokens = a.contentTokens(d.gen.Content)
	}
	usage = a.p.AccountedUsage(usage, body, false)
	a.paid().Report(ctx, usage, status, err, estimated, false)
	return d.gen, usage, err
}

// paid is this adapter's slice of the shared call accounting: its labels and
// the one log attribute only the generator has.
func (a *Adapter) paid() engine.PaidCall {
	return engine.PaidCall{Deps: a.deps, Provider: &a.p, Clock: a.clock, Log: a.log, Label: "generate call",
		Extra: func(u engine.Usage, _ bool) []any { return []any{"output_tokens", u.OutputTokens} }}
}

// decoded is the parsed answer plus what the response reported.
type decoded struct {
	gen      engine.Generation
	input    int64
	output   int64
	hasUsage bool
}

// decode reads choices[0].message.content and usage. Provider responses
// carry extra fields, so only the known shape is checked; malformed
// required fields are contract errors (never a default).
func decode(raw []byte) (decoded, error) {
	d := decoded{gen: engine.Generation{}}
	v, err := jsonx.DecodeOne(raw)
	if err != nil {
		return d, contractError("response", "%v", err)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return d, contractError("response", "expected an object, got %s", jsonx.Kind(v))
	}
	if model, ok := doc["model"].(string); ok && model != "" {
		d.gen.Model = model
	}
	choices, ok := doc["choices"].([]any)
	if !ok || len(choices) == 0 {
		return d, contractError("response", "choices is %s, not a non-empty array", jsonx.Kind(doc["choices"]))
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return d, contractError("response", "choices[0] is %s, not an object", jsonx.Kind(choices[0]))
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return d, contractError("response", "choices[0].message is %s, not an object", jsonx.Kind(choice["message"]))
	}
	content, ok := msg["content"].(string)
	if !ok {
		keys := make([]string, 0, len(msg))
		for k := range msg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		finish, _ := choice["finish_reason"].(string)
		return d, contractError("response", "choices[0].message.content is %s, not a string (finish_reason=%q, message keys=%v)", jsonx.Kind(msg["content"]), finish, keys)
	}
	if mail.Strip(content) == "" {
		return d, contractError("response", "choices[0].message.content is empty")
	}
	d.gen.Content = content
	if u, ok := doc["usage"].(map[string]any); ok {
		if d.input, err = tokensOf("prompt_tokens", u["prompt_tokens"]); err != nil {
			return d, err
		}
		if d.output, err = tokensOf("completion_tokens", u["completion_tokens"]); err != nil {
			return d, err
		}
		d.hasUsage = true
	}
	return d, nil
}

// tokensOf reads one usage counter: a non-negative integer literal.
func tokensOf(field string, v any) (int64, error) {
	n, ok := jsonx.Int(v)
	if !ok || n < 0 {
		return 0, contractError("usage", "%s is not a non-negative integer", field)
	}
	return n, nil
}

// contentTokens estimates an answer's tokens when the provider reported
// none (chars_per_token, like the request estimate).
func (a *Adapter) contentTokens(content string) int64 {
	return a.p.Tokens([]byte(content))
}
