package rfc5322

import (
	"html"
	"strings"
)

// blockTags end a line of the text form of HTML.
var blockTags = map[string]bool{"br": true, "p": true, "div": true, "li": true, "tr": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "blockquote": true, "table": true, "hr": true}

// htmlText is the text of an HTML body: tags removed (script and style
// with their content), block elements ending lines, entities decoded. A
// text form for search and the model, not a renderer.
func htmlText(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		j := strings.IndexByte(s, '>')
		if j < 0 {
			break // an unterminated tag: the rest is markup
		}
		tag := tagName(s[1:j])
		s = s[j+1:]
		if tag == "script" || tag == "style" {
			if k := indexASCIIFold(s, "</"+tag); k >= 0 {
				s = s[k:]
			}
			continue
		}
		if blockTags[tag] {
			b.WriteByte('\n')
		}
	}
	lines := strings.Split(html.UnescapeString(b.String()), "\n")
	out := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// tagName is the lower-case name of a tag's inside ("/p", "br/", "div class=x").
func tagName(inside string) string {
	t := strings.TrimLeft(inside, "/!")
	if end := strings.IndexAny(t, " \t\r\n/"); end >= 0 {
		t = t[:end]
	}
	return strings.ToLower(t)
}

// indexASCIIFold is the index of sub (ASCII) in s, ignoring ASCII case,
// on s itself: lower-casing s first would move its offsets (Ⱥ lower-cases
// to a longer encoding).
func indexASCIIFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i] == sub[0] && strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}
