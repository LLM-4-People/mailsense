package judge

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed prompt.json
var promptJSON []byte

// Prompt is the one versioned template of the judge. The version changes
// whenever the wording (or the answer shape) changes: it is part of the
// prompt digest and therefore of every label's provenance.
type Prompt struct {
	Version int    `json:"version"`
	System  string `json:"system"`
	User    string `json:"user"`
}

// parsePrompt validates a template: both parts non-empty, the user part
// with the two placeholders, and a positive version.
func parsePrompt(data []byte) (Prompt, error) {
	var p Prompt
	if err := jsonx.DecodeStrict(data, &p); err != nil {
		return p, fmt.Errorf("judge: prompt: %w", err)
	}
	var problems []string
	if p.Version < 1 {
		problems = append(problems, "version must be ≥ 1")
	}
	if strings.TrimSpace(p.System) == "" {
		problems = append(problems, "system must be non-empty")
	}
	if !strings.Contains(p.User, "{questions}") || !strings.Contains(p.User, "{state}") {
		problems = append(problems, "user must contain {questions} and {state}")
	}
	if len(problems) > 0 {
		return p, fmt.Errorf("judge: prompt: %s", strings.Join(problems, "; "))
	}
	return p, nil
}

// builtinPrompt is the shipped template, validated once.
var builtinPrompt = func() Prompt {
	p, err := parsePrompt(promptJSON)
	if err != nil {
		panic(err) // an embedded file that does not parse is a build bug
	}
	return p
}()

// render fills the user part with the questions (JSON, profile order) and
// the state. The system part is fixed.
func (p Prompt) render(qs []engine.Question, state string) (system, user string, err error) {
	questions, err := json.Marshal(qs)
	if err != nil {
		return "", "", fmt.Errorf("%w: questions: %v", ErrInput, err)
	}
	user = strings.NewReplacer("{questions}", string(questions), "{state}", state).Replace(p.User)
	return p.System, user, nil
}

// promptIdentity is every input of one prompt: the shipped template, the
// profile's decision identity (including the state template, body budget and
// fact switches) and the run's questions in profile order (the superset each
// message's prompt draws from).
type promptIdentity struct {
	Prompt    Prompt            `json:"prompt"`
	Profile   string            `json:"profile"`
	Questions []engine.Question `json:"questions"`
}

// digest is the prompt's identity: the provenance of a label and the key
// that makes a re-run skip what was already bought under the same inputs.
func (p Prompt) digest(profileDigest string, qs []engine.Question) (string, error) {
	raw, err := json.Marshal(promptIdentity{Prompt: p, Profile: profileDigest, Questions: qs})
	if err != nil {
		return "", fmt.Errorf("%w: prompt identity: %v", ErrInput, err)
	}
	return digest.Hex(raw, 32), nil
}
