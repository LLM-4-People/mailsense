package feedback

import (
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Prior is a label-derived rate and the labels behind it.
type Prior struct {
	Rate    float64 `json:"rate"`
	Samples int     `json:"samples"`
}

// SuspiciousPrior is the label-derived suspicious prior of a message
// (feedback.suspicious_priors, made causal): labels on other messages,
// made before it (Label.Before), from its address (at least addressMin)
// or else its domain (at least domainMin). A suspicious label at or above
// its cutoff, or a category label of a suspicious category, is positive.
// labels must already be feedback.Teaching-filtered: the triage core filters
// once at load, so this per-message selector never scans or copies the whole
// label set; a judge answer never reaches it.
func SuspiciousPrior(labels []Label, p *taxonomy.Profile, sender string, messageID int64, before time.Time, addressMin, domainMin int,
	domains mail.Domains) (Prior, bool) {
	key := mail.NormalizeAddress(sender)
	domain := domains.Of(key) // never a shared free-mail domain: one label must not taint every sender of it
	var byAddr, byDomain []bool
	for _, l := range labels {
		positive, counts := suspiciousLabel(&l, p)
		if !counts || l.MessageID == messageID || !l.Before(before) || l.Sender == "" {
			continue
		}
		if l.Sender == key {
			byAddr = append(byAddr, positive)
		}
		if domain != "" && domains.Of(l.Sender) == domain {
			byDomain = append(byDomain, positive)
		}
	}
	switch {
	case len(byAddr) >= addressMin && len(byAddr) > 0:
		return rate(byAddr), true
	case len(byDomain) >= domainMin && len(byDomain) > 0:
		return rate(byDomain), true
	}
	return Prior{}, false
}

func suspiciousLabel(l *Label, p *taxonomy.Profile) (positive, counts bool) {
	susp, _ := p.RoleQuestion(taxonomy.RoleSuspicious)
	cat, _ := p.RoleQuestion(taxonomy.RoleCategory)
	switch {
	case susp != nil && l.Question == susp.Name && l.Type == engine.Noul:
		return l.Positive(p), true
	case cat != nil && l.Question == cat.Name && slices.Contains(p.SuspiciousCategories, l.Value.Choice):
		return true, true
	}
	return false, false
}

func rate(outcomes []bool) Prior {
	pos := 0
	for _, b := range outcomes {
		if b {
			pos++
		}
	}
	return Prior{Rate: float64(pos) / float64(len(outcomes)), Samples: len(outcomes)}
}
