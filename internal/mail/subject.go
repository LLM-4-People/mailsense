package mail

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

var spaceRunRE = regexp.MustCompile(`\s+`)

// SubjectNormalizer removes reply/forward prefixes (re:, aw:, 回复：, رد: …),
// collapses whitespace and lower-cases, for subject-based thread keys. The
// prefix words are data (the shipped multilingual list and the ones this
// install learned); the matching is structural: compatibility forms are
// folded first (NFKC: full-width ＲＥ：, the full-width and small colons),
// bidi and other format controls are ignored, a counter may follow the
// word (RE[2]:, AW(3):, Re^2:), and up to max stacked prefixes go.
type SubjectNormalizer struct {
	prefix *regexp.Regexp
	max    int
}

// NewSubjectNormalizer builds the normalizer from prefix word lists (any
// script; joined, de-duplicated) and how many stacked prefixes to remove.
func NewSubjectNormalizer(maxPrefixes int, lists ...[]string) (*SubjectNormalizer, error) {
	var words []string
	for _, list := range lists {
		for _, p := range list {
			if w := CompatView(p); w != "" && !slices.Contains(words, w) {
				words = append(words, w)
			}
		}
	}
	if len(words) == 0 || maxPrefixes < 1 {
		return nil, errors.New("mail: subject normalizer needs prefixes and maxPrefixes ≥ 1")
	}
	// Longest first: "fwd" before "fw", "rif" before "r".
	slices.SortStableFunc(words, func(a, b string) int { return len(b) - len(a) })
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = regexp.QuoteMeta(w)
	}
	re, err := regexp.Compile(`(?i)^\s*(` + strings.Join(quoted, "|") + `)\s*(\[\d+\]|\(\d+\)|\^\d+)?\s*:\s*`)
	if err != nil {
		return nil, err
	}
	return &SubjectNormalizer{prefix: re, max: maxPrefixes}, nil
}

// HasPrefix reports whether the subject starts with a reply/forward prefix.
func (n *SubjectNormalizer) HasPrefix(subject string) bool {
	return n.prefix.MatchString(CompatView(subject))
}

// Normalize returns the thread-comparison form of subject.
func (n *SubjectNormalizer) Normalize(subject string) string {
	subject = CompatView(subject)
	for range n.max {
		loc := n.prefix.FindStringIndex(subject)
		if loc == nil {
			break
		}
		subject = subject[loc[1]:]
	}
	return strings.ToLower(strings.TrimSpace(spaceRunRE.ReplaceAllString(subject, " ")))
}

// LeadingWord is the text before the first colon of a folded subject when
// it is short enough to be a prefix (the learner's candidate), else "".
func LeadingWord(subject string, maxRunes int) string {
	s := CompatView(subject)
	w, _, ok := strings.Cut(s, ":")
	w = strings.TrimSpace(w)
	if !ok || w == "" || Runes(w) > maxRunes {
		return ""
	}
	return strings.ToLower(w)
}
