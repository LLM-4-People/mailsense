package engine

import "context"

// Generator is a chat model: one request of a system and a user message,
// one text answer (the judge's Gemma layer). Plan estimates one Generate
// without sending anything; an error means the request cannot be sent
// (invalid input, over the provider's context budget).
type Generator interface {
	// Name is the provider instance name (catalog key).
	Name() string
	// Model is the model the provider is configured with.
	Model() string
	// Plan estimates one Generate (the budget guard and the action's cost).
	Plan(system, user string) (Estimate, error)
	// Generate sends one request. On error, Generation.Usage holds what
	// the call spent (a call that reached the provider may be billed).
	// A paid generator reserves each call's ceiling through the context's
	// Reserver before sending it and settles its usage after, so a run's
	// budget and usage are exact. opts adjust this call's contract; none
	// keeps the provider record's configuration.
	Generate(ctx context.Context, system, user string, opts ...GenerateOptions) (Generation, error)
}

// GenerateOptions adjusts one call without changing the provider record.
// The zero value keeps the record's contract.
type GenerateOptions struct {
	// Prose asks for a plain-text answer even when the provider record is
	// configured for strict JSON mode. A conversation summary is prose, not
	// a plan; the planner and the judge keep the record's JSON contract.
	Prose bool
}

// Generation is one generator answer.
type Generation struct {
	Model   string `json:"model"`
	Content string `json:"content"`
	Usage   Usage  `json:"usage"`
}
