package mail

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// TruncateHTML cuts raw HTML to at most n UTF-16 code units (the unit the
// source caps count in) without splitting a tag, an attribute or an entity.
// A cut never lands inside a complete token: a tag that does not fit is
// dropped whole and a text token is cut only between characters. The
// sanitizer repairs the unbalanced markup this can leave behind.
func TruncateHTML(s string, n int) (string, bool) {
	if n < 0 {
		n = 0
	}
	if UTF16Len(s) <= n {
		return s, false
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	used := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String(), true
		}
		raw := z.Raw()
		size := UTF16Len(string(raw))
		if used+size <= n {
			b.Write(raw)
			used += size
			continue
		}
		if tt == html.TextToken {
			b.Write(cutHTMLText(raw, n-used))
		}
		return b.String(), true
	}
}

// cutHTMLText is the longest prefix of raw that fits in units UTF-16 code
// units, ending on a rune boundary and never inside a character reference.
func cutHTMLText(raw []byte, units int) []byte {
	if units <= 0 {
		return nil
	}
	used, end := 0, 0
	for end < len(raw) {
		r, size := utf8.DecodeRune(raw[end:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if used+w > units {
			break
		}
		used += w
		end += size
	}
	cut := raw[:end]
	// A cut inside "&…;" would expose a broken reference. Move it before the
	// last '&' only when the remainder really is the start of a reference
	// (the raw token continues with a ';' after the cut).
	if i := bytes.LastIndexByte(cut, '&'); i >= 0 && !bytes.ContainsRune(cut[i:], ';') &&
		entityTail(cut[i+1:]) && bytes.IndexByte(raw[end:], ';') >= 0 {
		cut = cut[:i]
	}
	return cut
}

// entityTail reports whether b is the unfinished start of a character
// reference: an optional '#' and ASCII letters/digits.
func entityTail(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if b[0] == '#' {
		b = b[1:]
	}
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z') {
			return false
		}
	}
	return true
}
