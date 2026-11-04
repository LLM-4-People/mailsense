package feedback

import (
	"maps"
	"math"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// EscalationSuggestion is a calibrated escalation threshold of one role.
type EscalationSuggestion struct {
	Role         taxonomy.Role `json:"role"`
	Question     string        `json:"question"`
	Field        string        `json:"field"` // min_confidence
	Current      float64       `json:"current"`
	Suggested    float64       `json:"suggested"`
	AccuracyKept float64       `json:"accuracy_kept"` // labelled answers at or above the threshold
	FlaggedShare float64       `json:"flagged_share"` // judged answers of the role below it
	Labels       int           `json:"labels"`
}

// CalibrateEscalation suggests, for each escalation role with at least
// minLabels labelled answers, the lowest min_confidence (a multiple of
// step) whose kept answers reach the target accuracy: the largest
// most-confident share at that precision (internal/selective: the one
// confidence definition and ranking — a choice's or score's confidence,
// a yes/no answer's probability of the side its cutoff chooses; a score
// is right within half a level). A suggestion from the point estimate,
// shown with its label count: never a certification. Only Reference labels
// calibrate: a judge answer never moves the suggestion.
func CalibrateEscalation(judged Judged, labels []Label, p *taxonomy.Profile, target, step float64, minLabels int) []EscalationSuggestion {
	labels = Reference(labels)
	var out []EscalationSuggestion
	for _, role := range slices.Sorted(maps.Keys(p.Escalation)) {
		q, ok := p.RoleQuestion(role)
		if !ok {
			continue
		}
		if s, ok := calibrateRole(judged, labels, p, role, q, target, step, minLabels); ok {
			out = append(out, s)
		}
	}
	return out
}

func calibrateRole(judged Judged, labels []Label, p *taxonomy.Profile, role taxonomy.Role, q *engine.Question, target, step float64, minLabels int) (EscalationSuggestion, bool) {
	var rows []selective.Ranked
	for _, l := range labels {
		v, ok := judged[l.MessageID][q.Name]
		if !ok || l.Question != q.Name {
			continue
		}
		c, known := selective.Confidence(v.Answer(), p.Cutoff(q.Name))
		if !known {
			continue
		}
		rows = append(rows, selective.Ranked{ID: l.MessageID, Correct: correct(p, q, &l, v), Confidence: c})
	}
	if len(rows) < minLabels {
		return EscalationSuggestion{}, false
	}
	rows = selective.Rank(rows)
	tp := selective.Targets(rows, []float64{target}, 1)[0]
	if tp.Threshold == selective.NoThreshold {
		return EscalationSuggestion{}, false
	}
	// the lowest multiple of step above the first answer left out keeps the same answers
	below := 0.0
	if tp.Covered < len(rows) {
		below = rows[tp.Covered].Confidence
	}
	suggested := mathx.Round((math.Floor(mathx.Round(below/step, 9))+1)*step, 9)
	if below == 0 {
		suggested = 0
	}
	s := EscalationSuggestion{Role: role, Question: q.Name, Field: "min_confidence", Labels: len(rows), Suggested: mathx.Round(min(suggested, tp.Threshold), 2),
		AccuracyKept: round3(mathx.Ratio(tp.Covered-tp.Errors, tp.Covered)), Current: p.Threshold(taxonomy.ThresholdLowConfidence)}
	if rule := p.Escalation[role]; rule.MinConfidence != nil {
		s.Current = *rule.MinConfidence
	}
	s.FlaggedShare = flaggedShare(judged, p, q, s.Suggested)
	return s, true
}

func correct(p *taxonomy.Profile, q *engine.Question, l *Label, v taxonomy.Value) bool {
	switch q.Type {
	case engine.Choice:
		return v.Choice == l.Value.Choice
	case engine.Noul:
		return (v.Model >= p.Cutoff(q.Name)) == l.Positive(p)
	}
	return math.Abs(v.Model-l.Value.Number) <= 0.5
}

// flaggedShare is the share of the role's answered judged values a
// threshold escalates (unknown confidence escalates). A refusal or
// abstention is a deliberate non-answer: it is neither in the denominator
// nor flagged.
func flaggedShare(judged Judged, p *taxonomy.Profile, q *engine.Question, t float64) float64 {
	n, flagged := 0, 0
	for _, values := range judged {
		if v, ok := values[q.Name]; ok && v.Answered() {
			n++
			if c, known := selective.Confidence(v.Answer(), p.Cutoff(q.Name)); !known || c < t {
				flagged++
			}
		}
	}
	return round3(mathx.Ratio(flagged, n))
}
