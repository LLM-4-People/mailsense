// Package ngram is the one character n-gram rule of language
// identification, shared by the generator of the shipped profiles
// (scripts/datagen locale) and the detector (locale.Language), so both
// count exactly the same trigrams. No data, no state.
package ngram

import (
	"cmp"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Normalize is the text n-grams are counted on: NFKC, Unicode case
// folding, every character that is not a letter or a mark a space, runs
// of spaces one space, padded with one space at both ends (so word starts
// and ends are n-grams of their own). Diacritics are KEPT: they tell
// languages apart.
func Normalize(s string) string {
	s = cases.Fold().String(norm.NFKC.String(s))
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(' ')
	space := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsMark(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

// Count counts the n-grams of one to maxN consecutive characters of a
// normalized text (TextCat's profile: single characters and bigrams carry
// most of the signal in short and in spaceless text, trigrams the rest);
// an n-gram that is or holds a lone space between two characters is not
// one (it joins two words' edges).
func Count(normalized string, maxN int) map[string]int {
	rs := []rune(normalized)
	out := map[string]int{}
	for n := 1; n <= maxN; n++ {
		for i := 0; i+n <= len(rs); i++ {
			g := rs[i : i+n]
			if n == 1 && g[0] == ' ' || n == 3 && g[1] == ' ' {
				continue
			}
			out[string(g)]++
		}
	}
	return out
}

// Top ranks n-gram counts: most frequent first, ties in code-point
// order (deterministic), at most n.
func Top(counts map[string]int, n int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := cmp.Compare(counts[b], counts[a]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return keys[:min(n, len(keys))]
}
