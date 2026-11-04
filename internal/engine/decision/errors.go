package decision

import (
	"errors"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Error classes; every adapter error wraps one (errors.Is). The provider
// classes are the engine's (shared with the generator adapter); the
// question-contract class is this adapter's.
var (
	ErrInsufficientCredits = engine.ErrInsufficientCredits
	ErrAuth                = engine.ErrAuth
	ErrQuota               = engine.ErrQuota
	ErrRejected            = engine.ErrRejected
	ErrTransient           = engine.ErrTransient
	ErrHTTP                = engine.ErrHTTP
	ErrContract            = errors.New("decision: answer violates the question contract")
	ErrContextBudget       = engine.ErrContextBudget
	ErrNoKey               = engine.ErrNoKey
	ErrInput               = engine.ErrInput
)

// APIError is a non-200 answer of a provider.
type APIError = engine.APIError

// contractError builds an ErrContract for one answer field.
func contractError(question, format string, args ...any) error {
	return engine.ContractError(ErrContract, question, format, args...)
}

// classify turns a failed response into an APIError (the engine's status
// and quota-code rule).
func classify(provider string, status int, body []byte, quotaCodes []string, limit int) *APIError {
	return engine.Classify(provider, status, body, quotaCodes, limit)
}

// describe reads every known provider error shape (the engine's rule).
func describe(body []byte, limit int) (codes []string, message string) {
	return engine.DescribeError(body, limit)
}
