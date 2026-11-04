package triage

import (
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Priors are what the owner's history says about a sender, blended with
// the model's raw answers at read time (never cached). Like the state they
// are causal: computed from mail, replies and labels before the message,
// so a blended value never knows the message's own outcome (the legacy
// leak into reply values and their evaluation).
type Priors struct {
	history   *threads.History
	labels    []feedback.Label
	rules     *Rules
	scales    scales
	smoothing int
	addrMin   int
	domMin    int
	domains   mail.Domains
}

// standing is the sender's point at an instant, else its domain's.
func (pr *Priors) standing(sender string, at time.Time) (threads.Point, bool) {
	if pt, ok := pr.history.Sender(sender, at); ok {
		return pt, true
	}
	return pr.history.Domain(sender, at)
}

// prior returns the prior of a blend role for a message and its samples.
func (pr *Priors) prior(p *taxonomy.Profile, role taxonomy.Role, rowID int64, sender string, at time.Time) (float64, int, bool) {
	switch role {
	case taxonomy.RoleReply:
		pt, ok := pr.standing(sender, at)
		if !ok {
			return 0, 0, false
		}
		all, _ := pr.history.Overall(at)
		k := float64(max(1, pr.smoothing))
		return (float64(pt.Replied) + k*mathx.Ratio(all.Replied, all.Received)) / (float64(pt.Received) + k), pt.Received, true
	case taxonomy.RoleUrgency:
		pt, ok := pr.standing(sender, at)
		if !ok || pt.Bucket < 0 {
			return 0, 0, false
		}
		return pr.rules.Urgency.Levels[pr.scales.coarse(pt.Bucket, pr.scales.urgency)], pt.Received, true
	case taxonomy.RoleSuspicious:
		// SuspiciousPrior applies feedback.Teaching: a judge answer is no prior.
		prior, ok := feedback.SuspiciousPrior(pr.labels, p, sender, rowID, at, pr.addrMin, pr.domMin, pr.domains)
		return prior.Rate, prior.Samples, ok
	}
	return 0, 0, false
}

// annotate attaches the causal prior to the diagnostic field. It never
// changes Number: the prior is evidence about the owner's history, not a
// substitute for the answer the route certified. A display that
// re-thresholds a blended number can show a class the route never chose.
func (pr *Priors) annotate(p *taxonomy.Profile, raw map[string]engine.Answer, rowID int64, m *mail.Message) map[string]taxonomy.Value {
	at, dated := m.Instant()
	out := make(map[string]taxonomy.Value, len(raw))
	for name, a := range raw {
		v := taxonomy.FromAnswer(a)
		role := p.RoleOf(name)
		weight := p.PriorBlend[role]
		if dated && slices.Contains(taxonomy.BlendRoles, role) && weight > 0 &&
			a.Answered() && a.Type != engine.Choice {
			if prior, samples, ok := pr.prior(p, role, rowID, m.SenderAddress, at); ok && samples >= 1 {
				v.Prior = &prior
			}
		}
		out[name] = v
	}
	return out
}
