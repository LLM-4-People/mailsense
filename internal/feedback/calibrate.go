package feedback

import (
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Suggestion is a threshold the owner's labels support better.
type Suggestion struct {
	Question     string             `json:"question"`
	ThresholdKey taxonomy.Threshold `json:"threshold_key"`
	Current      float64            `json:"current"`
	Suggested    float64            `json:"suggested"`
	F1           float64            `json:"f1"`
	Accuracy     float64            `json:"accuracy"`
	Labels       int                `json:"labels"`
}

// Calibrate sweeps each yes/no role's threshold from 0 to 1 by step and
// suggests the one maximising (F1, accuracy); ties keep the lowest
// threshold. A question needs minLabels labelled, judged messages. Only
// Reference labels calibrate: a judge answer never moves the suggestion.
// Only an answered judged value is a sweep input: a refusal or abstention
// is a deliberate non-answer, not a "no".
func Calibrate(judged Judged, labels []Label, p *taxonomy.Profile, step float64, minLabels int) []Suggestion {
	labels = Reference(labels)
	var out []Suggestion
	steps := mathx.RoundInt(1 / step)
	for _, c := range taxonomy.RoleCutoffs {
		q, ok := p.RoleQuestion(c.Role)
		if !ok || q.Type != engine.Noul {
			continue
		}
		type pair struct {
			actual bool
			value  float64
		}
		var pairs []pair
		for _, l := range labels {
			if v, ok := judged[l.MessageID][q.Name]; ok && l.Question == q.Name && v.Answered() {
				pairs = append(pairs, pair{l.Positive(p), v.Number})
			}
		}
		if len(pairs) < minLabels {
			continue
		}
		best := Suggestion{Question: q.Name, ThresholdKey: c.Key, Current: p.Threshold(c.Key), Labels: len(pairs), F1: -1}
		for i := 0; i <= steps; i++ {
			threshold := min(1, mathx.Round(float64(i)*step, 9)) // no float drift: 12·0.05 must be 0.6
			bools := make([][2]bool, len(pairs))
			for k, pr := range pairs {
				bools[k] = [2]bool{pr.actual, pr.value >= threshold}
			}
			_, _, f1, acc := count(bools).scores()
			if f1 > best.F1 || (f1 == best.F1 && acc > best.Accuracy) {
				best.F1, best.Accuracy, best.Suggested = f1, acc, mathx.Round(threshold, 2)
			}
		}
		best.F1, best.Accuracy = round3(best.F1), round3(best.Accuracy)
		out = append(out, best)
	}
	return out
}
