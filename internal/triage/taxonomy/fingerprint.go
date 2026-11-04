package taxonomy

import (
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// fingerprintChars is the legacy fingerprint length (hex digits).
const fingerprintChars = 12

// Fingerprint is the legacy question-spec id: SHA-256 of Python's
// json.dumps(questions, sort_keys=True, ensure_ascii=True), first 12 hex
// digits. Computed over the questions section as the document holds it,
// so a Go cache and a legacy cache agree on the same spec.
func Fingerprint(questions jsonx.Object) (string, error) {
	canonical, err := jsonx.PyDumps(questions)
	if err != nil {
		return "", err
	}
	return digest.Hex(canonical, fingerprintChars), nil
}
