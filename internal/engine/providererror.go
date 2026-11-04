package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Error classes of a provider call; every adapter wraps one (errors.Is).
// They are the provider layer's contract, shared by the decider and the
// generator adapters (an adapter's own input/contract errors stay local).
var (
	ErrInsufficientCredits = fmt.Errorf("engine: insufficient credits (%w)", ErrUnavailable)
	ErrAuth                = fmt.Errorf("engine: API key or model access rejected (%w)", ErrUnavailable)
	ErrQuota               = fmt.Errorf("engine: quota or spend limit reached (%w)", ErrUnavailable)
	ErrRejected            = errors.New("engine: request rejected by the provider")
	// ErrTransient marks a provider-wide temporary failure (rate limit,
	// gateway unavailable, request timeout, a remote server 500). Unlike
	// ErrUnavailable it may clear on its own, so a run stops and retries
	// later instead of charging the failure to individual messages.
	ErrTransient     = errors.New("engine: provider temporarily unavailable")
	ErrHTTP          = errors.New("engine: provider failed")
	ErrContextBudget = errors.New("engine: request exceeds the provider's context budget")
	ErrNoKey         = fmt.Errorf("engine: API key not configured (%w)", ErrUnavailable)
	ErrInput         = errors.New("engine: invalid request")
)

// ContractError builds an adapter's contract error for one answer field:
// its contract sentinel, the field and the detail. The formatting is the
// one every adapter uses, so a contract error reads the same wherever it
// was raised.
func ContractError(sentinel error, field, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", sentinel, field, fmt.Sprintf(format, args...))
}

// APIError is a non-200 answer of a provider.
type APIError struct {
	Provider string
	Status   int
	Codes    []string // provider error codes/types, as found in the body
	Message  string
	Class    error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%v: %s answered HTTP %d: %s", e.Class, e.Provider, e.Status, e.Message)
}

func (e *APIError) Unwrap() error { return e.Class }

// Classify turns a failed response into an APIError (status rule first,
// then quota codes found in the body).
func Classify(provider string, status int, body []byte, quotaCodes []string, limit int) *APIError {
	codes, msg := DescribeError(body, limit)
	e := &APIError{Provider: provider, Status: status, Codes: codes, Message: msg, Class: ErrHTTP}
	switch {
	case status == 402:
		e.Class = ErrInsufficientCredits
	case status == 401 || status == 403:
		e.Class = ErrAuth
	case IsQuota(codes, quotaCodes):
		e.Class = ErrQuota
	case status == 400 || status == 404 || status == 422:
		e.Class = ErrRejected
	case status == 408 || status == 429 || status == 500 || status == 502 || status == 503 || status == 504:
		// A rate limit, request timeout or gateway/server availability answer
		// is about the provider, not about these messages. A plain 500 is
		// provider-wide too: these consumers only talk to remote providers
		// (a managed runtime is embed-only), so the run stops instead of
		// charging one failure per message. The embed adapter keeps a
		// managed runtime's 500 message-scoped in its own classifier.
		e.Class = ErrTransient
	}
	return e
}

// IsQuota reports whether a provider error code means "out of credit".
func IsQuota(codes, quota []string) bool {
	for _, c := range codes {
		for _, q := range quota {
			if q != "" && strings.Contains(c, q) {
				return true
			}
		}
	}
	return false
}

// DescribeError reads every known provider error shape: SubQ
// {"detail":{error_type,message}}, Jev detail as object or FastAPI array,
// Cloudflare {"errors":[{code,message}]}, OpenAI
// {"error":{type,code,message}}; anything else is the trimmed body.
func DescribeError(body []byte, limit int) (codes []string, message string) {
	v, err := jsonx.DecodeOne(body)
	doc, ok := v.(map[string]any)
	if err != nil || !ok {
		return nil, mail.Head(strings.TrimSpace(string(body)), limit)
	}
	var msgs []string
	add := func(code any, msg any) {
		if s := text(code); s != "" {
			codes = append(codes, s)
		}
		if s := text(msg); s != "" {
			msgs = append(msgs, s)
		}
	}
	switch d := doc["detail"].(type) {
	case map[string]any:
		add(d["error_type"], d["message"])
	case []any:
		for _, item := range d {
			if m, ok := item.(map[string]any); ok {
				add(m["type"], m["msg"])
			}
		}
	case string:
		add(nil, d)
	}
	if list, ok := doc["errors"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				add(m["code"], m["message"])
			}
		}
	}
	if e, ok := doc["error"].(map[string]any); ok {
		add(e["code"], e["message"])
		add(e["type"], nil)
	}
	if len(msgs) == 0 {
		return codes, mail.Head(strings.TrimSpace(string(body)), limit)
	}
	return codes, mail.Head(strings.Join(msgs, "; "), limit)
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}
