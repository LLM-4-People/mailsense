package render

import (
	stdhtml "html"
	"regexp"
	"strings"
)

// linkPattern finds the two families of links a plain-text mail may carry.
// Anything else (javascript:, file:, data:) never becomes an anchor.
var linkPattern = regexp.MustCompile(`(?i)\b(?:https?://|mailto:)[^\s<>"']+`)

// Text renders the canonical plain body as a safe fragment. Blank lines,
// indentation and quoting are preserved inside one <pre>; http(s) and mailto
// addresses become anchors and every other byte is escaped. It never parses
// the text as markup.
func Text(plain string) string {
	if strings.TrimSpace(plain) == "" {
		return ""
	}
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	plain = strings.ReplaceAll(plain, "\r", "\n")
	var b strings.Builder
	b.WriteString(`<pre class="mail-text" dir="auto">`)
	for i, line := range strings.Split(plain, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		quoted := strings.HasPrefix(strings.TrimLeft(line, " \t"), ">")
		if quoted {
			b.WriteString(`<span class="mail-quoted">`)
		}
		writeLinkified(&b, line)
		if quoted {
			b.WriteString(`</span>`)
		}
	}
	b.WriteString(`</pre>`)
	return b.String()
}

// writeLinkified writes one line, escaping text and turning only validated
// URLs into anchors.
func writeLinkified(b *strings.Builder, line string) {
	for {
		loc := linkPattern.FindStringIndex(line)
		if loc == nil {
			b.WriteString(stdhtml.EscapeString(line))
			return
		}
		b.WriteString(stdhtml.EscapeString(line[:loc[0]]))
		raw := line[loc[0]:loc[1]]
		url, tail := trimLink(raw)
		if link, ok := safeLink(url); ok {
			b.WriteString(`<a href="` + stdhtml.EscapeString(link) + `" target="_blank" rel="noopener noreferrer nofollow">`)
			b.WriteString(stdhtml.EscapeString(url))
			b.WriteString(`</a>`)
			b.WriteString(stdhtml.EscapeString(tail))
		} else {
			b.WriteString(stdhtml.EscapeString(raw))
		}
		line = line[loc[1]:]
	}
}

// trimLink separates sentence punctuation from a URL. An unmatched closing
// bracket belongs to the sentence, not to the address.
func trimLink(s string) (url, tail string) {
loop:
	for len(s) > 0 {
		switch c := s[len(s)-1]; c {
		case '.', ',', ';', ':', '!', '?', '\'', '"':
			tail = string(c) + tail
			s = s[:len(s)-1]
		case ')':
			if strings.Count(s, "(") >= strings.Count(s, ")") {
				break loop
			}
			tail = string(c) + tail
			s = s[:len(s)-1]
		case ']':
			if strings.Count(s, "[") >= strings.Count(s, "]") {
				break loop
			}
			tail = string(c) + tail
			s = s[:len(s)-1]
		case '}':
			if strings.Count(s, "{") >= strings.Count(s, "}") {
				break loop
			}
			tail = string(c) + tail
			s = s[:len(s)-1]
		default:
			break loop
		}
	}
	return s, tail
}
