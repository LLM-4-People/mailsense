// Package judge is the Gemma layer: one chat model call labels one
// message for every question still missing, against one versioned prompt
// and the label-free model view (header, facts, body). It validates the
// model's answer strictly (requested questions only, no duplicates, every
// value through taxonomy.ParseLabel) and never invents a default: a null
// value is a refusal and no label. The app-side caller is layer1
// (RunJudge); the package has no store or gate dependency, so a judge can
// label before the learning gate opens.
package judge

import (
	"context"
	"errors"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// SettingProvider names the catalog generator a judge run uses
// (internal/config/schema.json).
const SettingProvider = "judge.provider"

// ErrContract is an answer that violates the labelling contract (not
// JSON, an unknown or duplicate question, a value no question accepts, or
// an oversized answer). A contract error for one question never becomes a
// label or a default.
var ErrContract = errors.New("judge: the answer violates the labelling contract")

// ErrInput is an invalid request (no questions, an unknown question, an
// empty state): nothing is sent.
var ErrInput = engine.ErrInput

// ErrTimeout is one call that exceeded the judge's call timeout. It is a
// transient failure of one message (the provider accepted the request and
// stopped answering), not a provider-wide outage: the caller counts the
// message as failed and its own consecutive-failure limit decides when to
// stop.
var ErrTimeout = errors.New("judge: call timed out")

// Judge labels one message per call.
type Judge interface {
	// Name is the provider instance name (catalog key).
	Name() string
	// Model is the model the provider is configured with.
	Model() string
	// PromptDigest covers every input of one prompt (the template, the
	// profile's decision identity and the ordered question set qs): a label
	// written under another digest is another task.
	PromptDigest(qs []engine.Question) (string, error)
	// Plan estimates one Label without sending anything (the budget guard
	// and the run's pre-flight refusal).
	Plan(state string, qs []engine.Question) (engine.Estimate, error)
	// Label answers the questions about state. Labels has an entry for
	// every question the model answered validly: nil means the model
	// refused (no label, never a target); a question absent from Labels
	// failed the contract (the error names it). A paid implementation
	// reserves each call's ceiling through the context's engine.Reserver
	// and settles its usage after.
	Label(ctx context.Context, state string, qs []engine.Question) (Result, error)
}

// Result is one judge call's parsed answer and what it spent.
type Result struct {
	Labels map[string]*taxonomy.Label
	Usage  engine.Usage
}

// Refused counts the questions the model answered null.
func (r Result) Refused() int {
	n := 0
	for _, l := range r.Labels {
		if l == nil {
			n++
		}
	}
	return n
}
