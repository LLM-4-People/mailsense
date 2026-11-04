package installprofile

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// PrefixReport is the measurement behind the learned reply prefixes.
type PrefixReport struct {
	Prefixes []string `json:"prefixes"`
	Replies  int      `json:"replies"`  // messages answering another (In-Reply-To or References)
	Subjects int      `json:"subjects"` // subjects with a short leading word before a colon
}

// replyPrefixes learns the reply/forward prefixes this install's mail
// uses that the shipped list lacks: the short word before the first colon
// of a subject that marks replies — at least minCount replies start with
// it, and at least minShare of the subjects starting with it are replies
// (a "Ticket:" or "Invoice:" tag also starts new mail and is never one).
func replyPrefixes(a *threads.Analysis, shipped []string, minCount int, minShare float64, maxRunes int) PrefixReport {
	known, err := mail.NewSubjectNormalizer(1, shipped)
	replies, starts := map[string]int{}, map[string]int{}
	rep := PrefixReport{Prefixes: []string{}}
	for i := range a.Messages {
		m := &a.Messages[i]
		if len(m.Parents) > 0 {
			rep.Replies++
		}
		w := mail.LeadingWord(m.Subject, maxRunes)
		if w == "" || (err == nil && known.HasPrefix(m.Subject)) {
			continue
		}
		rep.Subjects++
		starts[w]++
		if len(m.Parents) > 0 {
			replies[w]++
		}
	}
	for w, n := range replies {
		if n >= minCount && float64(n) >= minShare*float64(starts[w]) {
			rep.Prefixes = append(rep.Prefixes, w)
		}
	}
	slices.Sort(rep.Prefixes)
	return rep
}
