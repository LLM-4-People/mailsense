package decision

import (
	"encoding/json"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// codec is one wire protocol: request encoding and strict response decoding.
type codec interface {
	encode(p *engine.Provider, state string, qs []engine.Question) ([]byte, error)
	decode(body []byte, qs []engine.Question) (decoded, error)
}

// decoded is one response: an answer for every requested question.
type decoded struct {
	model    string
	answers  map[string]engine.Answer
	input    int64
	output   int64
	hasUsage bool
	// billed is the charge the provider reported for the call (Example provider:
	// cost.request_cost_usd, energy-metered), nil when it reports none.
	billed *float64
}

// codecs maps protocol names to codecs (the registry's protocols).
var codecs = map[string]codec{
	"systemone":        systemone{},
	"openai_decisions": openAI{},
}

// unwrap removes a response envelope. Cloudflare wraps the answer in
// {"result": …, "success": bool, "errors": […]}; success false is an error.
func unwrap(envelope string, body []byte, limit int) ([]byte, error) {
	if envelope != engine.EnvelopeCloudflare {
		return body, nil
	}
	var env struct {
		Result  json.RawMessage `json:"result"`
		Success *bool           `json:"success"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, contractError("response", "not a JSON envelope: %v", err)
	}
	if env.Success == nil || !*env.Success {
		_, msg := describe(body, limit)
		return nil, contractError("response", "envelope success is not true: %s", msg)
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return nil, contractError("response", "envelope has no result")
	}
	return env.Result, nil
}

// usageOf reads {"input_tokens", "output_tokens"} (non-negative integers).
func usageOf(v any, d *decoded) error {
	u, ok := v.(map[string]any)
	if !ok {
		return nil // absent usage: the adapter falls back to its estimate
	}
	in, okIn := jsonx.Int(u["input_tokens"])
	if !okIn || in < 0 {
		return contractError("usage", "input_tokens is not a non-negative integer")
	}
	out, okOut := jsonx.Int(u["output_tokens"])
	if _, present := u["output_tokens"]; present && (!okOut || out < 0) {
		return contractError("usage", "output_tokens is not a non-negative integer")
	}
	d.input, d.output, d.hasUsage = in, max(out, 0), true
	return nil
}

// billingOf reads the optional provider-reported charge {"cost":
// {"request_cost_usd"}} (a finite, non-negative number) and rejects a
// response that says the state was truncated: its answers are about a
// state the model did not fully see (the context budget is enforced
// before sending, so this means that budget is wrong).
func billingOf(doc map[string]any, d *decoded) error {
	if t, _ := doc["truncated"].(bool); t {
		return contractError("response", "the provider truncated the state")
	}
	cost, ok := doc["cost"].(map[string]any)
	if !ok {
		return nil
	}
	raw, present := cost["request_cost_usd"]
	if !present {
		return nil
	}
	usd, ok := jsonx.Finite(raw)
	if !ok || usd < 0 {
		return contractError("cost", "request_cost_usd is not a non-negative number")
	}
	d.billed = &usd
	return nil
}

// topObject decodes a response body that must be one JSON object.
func topObject(body []byte) (map[string]any, error) {
	v, err := jsonx.DecodeOne(body)
	if err != nil {
		return nil, contractError("response", "%v", err)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, contractError("response", "expected an object, got %s", jsonx.Kind(v))
	}
	return doc, nil
}

func modelOf(doc map[string]any) string {
	s, _ := doc["model"].(string)
	return s
}

// byName indexes questions; duplicate names are an input error.
func byName(qs []engine.Question) (map[string]*engine.Question, error) {
	out := make(map[string]*engine.Question, len(qs))
	for i := range qs {
		if _, dup := out[qs[i].Name]; dup {
			return nil, fmt.Errorf("%w: question %q appears twice", ErrInput, qs[i].Name)
		}
		out[qs[i].Name] = &qs[i]
	}
	return out, nil
}
