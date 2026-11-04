package mail

import (
	"regexp"
	"strings"
)

var messageIDRE = regexp.MustCompile(`<[^<>\s]+>`)

// ThreadIDs returns the message's own Internet Message-ID and the ids it
// answers (In-Reply-To, then References), each as "<id>". The structured
// fields win; when both are empty they are read from the transport
// headers. Header extraction is line based (folded lines unfolded), so a
// preamble ("Microsoft Mail Internet Headers Version 2.0"), a malformed
// or colon-less line, or a leading space never hides them; a bare id
// (no angle brackets) counts too.
func ThreadIDs(m *Message) (self string, parents []string) {
	self = firstID(m.InternetMessageID)
	inReplyTo, refs := m.InReplyTo, m.References
	if inReplyTo == "" && refs == "" && m.Headers != "" {
		inReplyTo, refs = threadHeaders(m.Headers)
	}
	seen := map[string]bool{self: true}
	for _, id := range append(ids(inReplyTo), ids(refs)...) {
		if !seen[id] {
			seen[id] = true
			parents = append(parents, id)
		}
	}
	return self, parents
}

// threadHeaders returns the In-Reply-To and References values of a raw
// header block (all occurrences of each joined).
func threadHeaders(block string) (inReplyTo, refs string) {
	return strings.Join(HeaderValues(block, "In-Reply-To"), " "), strings.Join(HeaderValues(block, "References"), " ")
}

// ids lists the ids of a header value: bracketed ids, else bare tokens
// that look like ids (local@domain).
func ids(v string) []string {
	if found := messageIDRE.FindAllString(v, -1); len(found) > 0 {
		return found
	}
	var out []string
	for _, tok := range strings.Fields(v) {
		if id := firstID(tok); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// firstID is the first bracketed id of s, or s itself bracketed when it is
// a single bare token shaped like an id.
func firstID(s string) string {
	if id := messageIDRE.FindString(s); id != "" {
		return id
	}
	if s = strings.TrimSpace(s); s != "" && strings.Contains(s, "@") && !strings.ContainsAny(s, " \t<>,;") {
		return "<" + s + ">"
	}
	return ""
}
