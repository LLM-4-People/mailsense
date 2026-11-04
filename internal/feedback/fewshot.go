package feedback

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// tokenRE is Unicode-aware: letters and combining marks, digits, and the
// punctuation that appears inside addresses and identifiers. It is applied
// after fold, which decomposes accents and removes the marks, so "Réunion"
// and "reunion" are the same token for subject overlap.
var tokenRE = regexp.MustCompile(`[\p{L}\p{M}\p{N}@._-]+`)

// fold lowercases and strips combining marks. It never transliterates a
// script: Japanese, Cyrillic, Greek and Arabic words stay themselves, which
// is what a mailbox in any language needs.
func fold(s string) string {
	s = norm.NFD.String(strings.ToLower(s))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, s)
}

func tokens(subject string) map[string]bool {
	out := map[string]bool{}
	for _, t := range tokenRE.FindAllString(fold(subject), -1) {
		n := utf8.RuneCountInString(t)
		// Two-character ASCII words ("de", "IT") are noise; three-letter
		// acronyms ("VPN") and two-rune CJK words are meaningful.
		if n < 2 || (n == 2 && isASCII(t)) {
			continue
		}
		out[t] = true
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// SelectExamples returns the labels that may inform a message's state:
// labels on OTHER messages of the same sender (score 3) or, failing that,
// of the same domain (score 2), made before the message (Label.Before) —
// so a new label only ever changes the states of mail that arrives after
// it. The legacy subject overlap only orders labels within those groups
// (alone it selected labels across the whole archive). Best first, newest
// on ties, one per (message, question), at most limit.
// Domains are grouped by organisation (mail.Domains: never a shared
// free-mail domain). labels must already be feedback.Teaching-filtered: the
// triage core filters once at load, so this per-message selector never scans
// or copies the whole label set; a judge answer never reaches it.
func SelectExamples(labels []Label, messageID int64, sender, subject string, before time.Time, limit int, domains mail.Domains) []Label {
	if limit <= 0 || before.IsZero() {
		return nil
	}
	key := mail.NormalizeAddress(sender)
	domain := domains.Of(key)
	mine := tokens(subject)
	type scored struct {
		score float64
		l     Label
	}
	var list []scored
	for _, l := range labels {
		if l.MessageID == messageID || !l.Before(before) {
			continue
		}
		var s float64
		switch {
		case l.Sender != "" && l.Sender == key:
			s = 3
		case domain != "" && domains.Of(l.Sender) == domain:
			s = 2
		default:
			continue
		}
		list = append(list, scored{s + jaccard(mine, tokens(l.Subject)), l})
	}
	slices.SortStableFunc(list, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.score, a.score), strings.Compare(b.l.CreatedAt, a.l.CreatedAt))
	})
	type mark struct {
		id int64
		q  string
	}
	seen := map[mark]bool{}
	var out []Label
	for _, s := range list {
		if m := (mark{s.l.MessageID, s.l.Question}); !seen[m] {
			seen[m] = true
			out = append(out, s.l)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if b[t] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}
