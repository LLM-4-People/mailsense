package mail

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The text helpers below reproduce Python's str semantics (the legacy
// state and statistics were computed with them): whitespace is
// str.isspace(), lengths and cuts count code points.

// IsSpace is Python's str.isspace() for one character: Unicode White_Space
// plus the ASCII information separators U+001C..U+001F.
func IsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// Strip is Python's str.strip().
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// Words splits on runs of whitespace (Python's \S+).
func Words(s string) []string { return strings.FieldsFunc(s, IsSpace) }

// WordCount is len(Words(s)) without the slice: it counts runs of non-space
// runes (Python's \S+) in one pass and does not allocate.
func WordCount(s string) int {
	count, inWord := 0, false
	for _, r := range s {
		if IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			count++
		}
	}
	return count
}

// Runes is the length in code points (Python's len()).
func Runes(s string) int { return utf8.RuneCountInString(s) }

// Head returns the first n code points of s (Python's s[:n]; n ≤ 0 is "").
//
// Ownership: a truncated result is a fresh string that owns its bytes, so a
// retained Head keeps only the prefix, not the whole input. When all of s
// fits in n code points the result is s itself: it is the whole wanted value
// and no larger input is retained.
func Head(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return strings.Clone(s[:pos])
		}
		i++
	}
	return s
}

// Excerpt is the one quotation form of a body: stripped, line feeds as
// spaces, at most n code points.
//
// Ownership: the result is a fresh string that owns only the excerpt, never a
// substring of the body or of a full-body copy. A 240-rune quotation therefore
// retains 240 runes for as long as a warm analysis holds it.
func Excerpt(body string, n int) string {
	if n <= 0 {
		return ""
	}
	s := Strip(body)
	var b strings.Builder
	// Grow is bounded by the code-point budget, not the body size: the
	// builder allocates for the excerpt, never for the whole message.
	b.Grow(min(n, len(s)))
	written := 0
	for start := 0; start < len(s) && written < n; {
		r, width := utf8.DecodeRuneInString(s[start:])
		if r == '\n' {
			b.WriteByte(' ')
		} else {
			// Copy the original bytes so an invalid byte stays the same
			// byte instead of becoming U+FFFD.
			b.WriteString(s[start : start+width])
		}
		start += width
		written++
	}
	return b.String()
}

// IsWordRune is Python's \w for one character (letters, numbers, underscore).
func IsWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }

// CompatView is the folded form structural rules read: NFKC (width and
// compatibility forms — "：" is ":", "ＲＥ" is "RE", NBSP is a space,
// full-width digits are digits) without default-ignorable characters
// (bidi marks, zero-width joiners, variation selectors: the same set Fold
// removes). The one compatibility fold of subjects and bodies.
func CompatView(s string) string {
	return strings.Map(func(r rune) rune {
		if defaultIgnorable(r) {
			return -1
		}
		return r
	}, norm.NFKC.String(s))
}
