package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// ellipsis marks a cut snippet edge.
const ellipsis = "…"

// snippetTerm is one query term prepared for snippet matching. A word-mode
// term keeps the folded FTS tokens it must match adjacently; a
// substring/scan term keeps its folded text.
type snippetTerm struct {
	mode  store.TermMode
	text  string
	words []string
}

// snippetPlan prepares the query terms of one page. Word terms match whole
// FTS tokens (a multi-token term, quoted phrase or punctuation-joined word,
// matches adjacent tokens); substring and scan terms keep substring
// matching. A one-rune scan term is a last resort: it is dropped when any
// longer term exists.
func snippetPlan(terms []store.TermMatch) []snippetTerm {
	out := make([]snippetTerm, 0, len(terms))
	long := false
	for _, t := range terms {
		text := mail.Fold(t.Term)
		switch t.Mode {
		case store.ModeWord:
			words := tokenizeFTS(text)
			if len(words) == 0 {
				continue
			}
			out = append(out, snippetTerm{mode: t.Mode, text: text, words: words})
			long = long || utf8.RuneCountInString(text) > 1
		case store.ModeSubstring, store.ModeScan:
			if text == "" {
				continue
			}
			out = append(out, snippetTerm{mode: t.Mode, text: text})
			long = long || utf8.RuneCountInString(text) > 1
		}
	}
	if !long {
		return out
	}
	kept := out[:0]
	for _, t := range out {
		if t.mode == store.ModeScan && utf8.RuneCountInString(t.text) == 1 {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return out
	}
	return kept
}

// snippetToken is one FTS-like token (letters, numbers and private-use
// characters) and its byte offset in the folded text.
type snippetToken struct {
	text string
	at   int
}

func tokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r)
}

// tokenize splits folded text the way the shipped word index does
// (unicode61): runs of letters, numbers and private-use characters are
// tokens; marks, whitespace, underscores and punctuation separate them.
func tokenize(s string) []snippetToken {
	var out []snippetToken
	start := -1
	for i, r := range s {
		if tokenRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, snippetToken{text: s[start:i], at: start})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, snippetToken{text: s[start:], at: start})
	}
	return out
}

func tokenizeFTS(s string) []string {
	tokens := tokenize(s)
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.text
	}
	return out
}

// findTokens returns the byte offset in folded of the first adjacent
// occurrence of words, or -1.
func findTokens(tokens []snippetToken, words []string) int {
	if len(words) == 0 {
		return -1
	}
	for i := 0; i+len(words) <= len(tokens); i++ {
		ok := true
		for j := range words {
			if tokens[i+j].text != words[j] {
				ok = false
				break
			}
		}
		if ok {
			return tokens[i].at
		}
	}
	return -1
}

// snippet cuts about n code points of body around the first match of the
// plan (whole FTS tokens in word mode, a substring in substring/scan mode),
// accent- and case-insensitively as in the index. It reports whether an
// anchor was found: a full-text hit whose indexed fields carry the term but
// whose body does not must not show an unrelated body beginning.
func snippet(body string, plan []snippetTerm, n int) (string, bool) {
	if len(plan) == 0 {
		return "", false
	}
	flat := strings.Join(mail.Words(body), " ")
	if flat == "" {
		return "", false
	}
	folded, offsets := mail.FoldMap(flat)
	tokens := tokenize(folded)
	at := -1
	for _, t := range plan {
		i := -1
		switch t.mode {
		case store.ModeWord:
			i = findTokens(tokens, t.words)
		default:
			i = strings.Index(folded, t.text)
		}
		if i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 || at >= len(offsets) {
		return "", false
	}
	return cut(flat, offsets[at], n), true
}

// preview cuts the cleaned beginning of a body. It is the semantic-only
// result preview, not a matched excerpt.
func preview(body string, n int) string {
	return cut(strings.Join(mail.Words(mail.CleanBody(body)), " "), 0, n)
}

// hitSnippet is the snippet contract of one hydrated hit: a semantic-only
// hit keeps a preview, a full-text hit gets a body excerpt anchored at a
// query term, or an empty string when the body carries no match (a
// subject-, sender- or recipient-only hit).
func hitSnippet(sum store.Summary, plan []snippetTerm, textRank, n int) string {
	if textRank == 0 {
		return preview(sum.Body, n)
	}
	s, _ := snippet(sum.Body, plan, n)
	return s
}

// cut returns n code points of s starting about a third of n before the
// byte offset at (on a word boundary), with ellipses where it was cut.
func cut(s string, at, n int) string {
	runes := []rune(s)
	pos := len([]rune(s[:at]))
	start := max(0, pos-n/3)
	for start > 0 && start < pos && runes[start-1] != ' ' {
		start++
	}
	end := min(len(runes), start+n)
	out := string(runes[start:end])
	if start > 0 {
		out = ellipsis + out
	}
	if end < len(runes) {
		out += ellipsis
	}
	return out
}
