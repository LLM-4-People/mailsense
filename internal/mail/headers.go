package mail

import "strings"

// HeaderValues returns every value of the named header in a raw header
// block (case-insensitive name, folded lines unfolded, values trimmed), in
// order. Parsing is line based, so a preamble line, a malformed or
// colon-less line or CRLF/LF endings never hide a header.
func HeaderValues(block, name string) []string { return ParseHeaders(block).Values(name) }

// HeaderValue is the first value of the named header ("" when absent).
func HeaderValue(block, name string) string { return ParseHeaders(block).Value(name) }

// Headers is a header block parsed once (logical lines split at their
// first colon), for readers of several headers.
type Headers []struct{ name, value string }

// ParseHeaders splits a raw header block (see HeaderValues).
func ParseHeaders(block string) Headers { return parseLines(unfoldHeaders(block)) }

// ParseCutHeaders splits a header block its source cut at an arbitrary
// point (mail.AltHeadersTruncated): its last logical line is dropped, as
// what the cut left of it ("Auto-Submitted: auto-gen", "dmarc=fa") would
// read as a complete, different value.
func ParseCutHeaders(block string) Headers {
	lines := unfoldHeaders(block)
	return parseLines(lines[:max(len(lines)-1, 0)])
}

func parseLines(lines []string) Headers {
	if len(lines) == 0 {
		return nil
	}
	var out Headers
	for _, line := range lines {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out = append(out, struct{ name, value string }{strings.TrimRight(k, " \t"), strings.TrimSpace(v)})
		}
	}
	return out
}

// Values returns every value of the named header (case-insensitive), in order.
func (h Headers) Values(name string) []string {
	var out []string
	for _, l := range h {
		if strings.EqualFold(l.name, name) {
			out = append(out, l.value)
		}
	}
	return out
}

// Value is the first value of the named header ("" when absent).
func (h Headers) Value(name string) string {
	for _, l := range h {
		if strings.EqualFold(l.name, name) {
			return l.value
		}
	}
	return ""
}

// unfoldHeaders splits a header block into logical lines: a line that
// starts with a space or tab continues the previous one (RFC 5322 §2.2.3).
func unfoldHeaders(block string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n") {
		if l == "" {
			continue
		}
		if (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += " " + strings.TrimSpace(l)
			continue
		}
		out = append(out, l)
	}
	return out
}
