package taxonomy

import (
	"encoding/json"
	"errors"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// Digest is the canonical identity of a profile as a decision input: every
// exported field that changes what triage or layer 1 does, excluding Source
// (the overrides path is not a decision). Fingerprint remains the legacy
// question-spec hash; Digest covers the rest of the profile, so a threshold,
// role, template or prior change moves the route key without changing the
// legacy fingerprint or the paid-decision cache key.
func (p *Profile) Digest() (string, error) {
	if p == nil {
		return "", errors.New("taxonomy: nil profile")
	}
	q := *p
	q.Source = ""
	raw, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	return digest.Hex(raw, 32), nil
}
