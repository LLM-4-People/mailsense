package taxonomy

import (
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// EscalationRule is when a role's answer is too uncertain: its confidence
// (internal/selective, the one definition: a choice's or score's stated
// confidence, a yes/no answer's probability of the side its cutoff
// chooses) below MinConfidence (absent: thresholds.low_confidence).
type EscalationRule struct {
	MinConfidence *float64 `json:"min_confidence,omitempty"`
}

// Profile is a validated triage profile.
type Profile struct {
	Name            string                  `json:"name"`
	BodyChars       int                     `json:"body_chars"`
	StateTemplate   string                  `json:"state_template"`
	Questions       []engine.Question       `json:"questions"` // in document order (chunking order)
	Roles           map[Role]string         `json:"roles"`     // role → question name (dropped roles absent)
	Thresholds      map[Threshold]float64   `json:"thresholds"`
	PriorBlend      map[Role]float64        `json:"prior_blend"` // weight of the MODEL in the blend
	PriorMinSamples int                     `json:"prior_min_samples"`
	IncludeHistory  bool                    `json:"include_history"`
	IncludeExamples bool                    `json:"include_examples"`
	IncludeFacts    bool                    `json:"include_facts"`
	MaxExamples     int                     `json:"max_examples"`
	Escalation      map[Role]EscalationRule `json:"escalation"`
	// SuspiciousCategories are category options that count as a suspicious
	// label; KnownRelationships the relationship options whose domains are
	// known for the impersonation check.
	SuspiciousCategories []string `json:"suspicious_categories"`
	KnownRelationships   []string `json:"known_relationships"`
	Fingerprint          string   `json:"fingerprint"`
	// Source is the overrides file merged in ("" = built-in only).
	Source   string `json:"source"`
	template *Template
	roleOf   map[string]Role
}

// Question returns a question by name.
func (p *Profile) Question(name string) (*engine.Question, bool) {
	for i := range p.Questions {
		if p.Questions[i].Name == name {
			return &p.Questions[i], true
		}
	}
	return nil, false
}

// QuestionName returns the name of the question playing role ("" when the
// role is dropped). It is the one projection from a role to its question, so
// a profile override that renames or removes a question reaches every
// consumer instead of leaving a literal lookup behind.
func (p *Profile) QuestionName(role Role) string {
	if q, ok := p.RoleQuestion(role); ok {
		return q.Name
	}
	return ""
}

// RoleQuestion returns the question playing role, if the role is kept.
func (p *Profile) RoleQuestion(role Role) (*engine.Question, bool) {
	name, ok := p.Roles[role]
	if !ok {
		return nil, false
	}
	return p.Question(name)
}

// RoleOf returns the role a question plays ("" when none; validation
// guarantees at most one).
func (p *Profile) RoleOf(question string) Role { return p.roleOf[question] }

// Template is the parsed state template.
func (p *Profile) Template() *Template { return p.template }

// UsesField reports whether the state template uses a placeholder.
func (p *Profile) UsesField(name string) bool {
	for _, f := range p.template.Fields {
		if f == name {
			return true
		}
	}
	return false
}

// Threshold returns a threshold (validation guarantees every known key).
func (p *Profile) Threshold(k Threshold) float64 { return p.Thresholds[k] }

// Cutoff is the yes/no threshold of a noul question: its role's own
// threshold, else noul_default. A value at or above it is "yes".
func (p *Profile) Cutoff(question string) float64 {
	role := p.RoleOf(question)
	for _, c := range RoleCutoffs {
		if c.Role == role {
			return p.Threshold(c.Key)
		}
	}
	return p.Threshold(ThresholdNoulDefault)
}

// Uncertain lists the escalation roles' questions whose answer is too
// uncertain, in question order: a refusal, a below-target abstention, or
// a usable answer without a confidence or below the role's
// min_confidence. A route-level abstention (not_certified,
// below_coverage, no_answer) is a deliberate non-answer, not an
// uncertainty, and is skipped.
func (p *Profile) Uncertain(values map[string]Value) []string {
	var out []string
	for _, q := range p.Questions {
		rule, ok := p.Escalation[p.RoleOf(q.Name)]
		v, has := values[q.Name]
		if !ok || !has {
			continue
		}
		a := v.Answer()
		if a.Uncertain() {
			out = append(out, q.Name)
		} else if a.Abstain != "" {
			continue
		} else if c, known := selective.Confidence(a, p.Cutoff(q.Name)); !known || c < p.minConfidence(rule) {
			out = append(out, q.Name)
		}
	}
	return out
}

func (p *Profile) minConfidence(r EscalationRule) float64 {
	if r.MinConfidence != nil {
		return *r.MinConfidence
	}
	return p.Threshold(ThresholdLowConfidence)
}

// QuestionNames is the set of question names.
func (p *Profile) QuestionNames() map[string]bool {
	out := make(map[string]bool, len(p.Questions))
	for _, q := range p.Questions {
		out[q.Name] = true
	}
	return out
}
