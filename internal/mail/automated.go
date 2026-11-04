package mail

import (
	"slices"
	"strings"
)

// automatedRules are the transport headers that mark machine-sent mail
// (kinds.json automated).
type automatedRules struct {
	Doc string `json:"_doc"`
	// Present: headers whose presence alone marks it (any value).
	Present []string `json:"present"`
	// Values: headers whose (lower-case, trimmed) value is one of these.
	Values map[string][]string `json:"values"`
	// AutoSubmittedHuman: Auto-Submitted keywords that mean "not
	// automatic"; any other keyword marks it.
	AutoSubmittedHuman []string `json:"auto_submitted_human"`
}

// Automated reports whether a message was sent by a machine rather than
// written to the reader, from protocol data only (kinds.json): a
// non-ordinary kind (a report, a receipt, an automatic reply, a meeting
// item), a mailing-list or bulk header, or an Auto-Submitted keyword.
func Automated(m *Message) bool {
	if !KindOf(m).Ordinary() {
		return true
	}
	r := kinds().Automated
	for _, h := range r.Present {
		if HeaderValue(m.Headers, h) != "" {
			return true
		}
	}
	for h, values := range r.Values {
		if v := strings.ToLower(strings.TrimSpace(HeaderValue(m.Headers, h))); v != "" && slices.Contains(values, v) {
			return true
		}
	}
	a := autoSubmitted(m.Headers)
	return a != "" && !slices.Contains(r.AutoSubmittedHuman, a)
}
