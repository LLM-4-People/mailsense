package layer1

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// HeldItem is one evaluation label's prediction by a model that never
// trained on that message (calibrated probabilities).
type HeldItem struct {
	ID    int64     `json:"id"`
	Probs []float64 `json:"p"`
}

// Holdout are a model's predictions of the evaluation labels it was
// measured on: the selection part out of fold (the fold models, which
// never saw their own fold) and the certification part by the model
// itself (trained on every target but the certification part's
// messages). Kept with the model so that its verdict is re-scored against
// the current labels without training (a corrected label changes the
// verdict at once).
type Holdout struct {
	Select  []HeldItem `json:"select"`
	Certify []HeldItem `json:"certify"`
}

// evalLabel is a message's current evaluation label of one question.
type evalLabel struct {
	class  int
	source string // the teacher source that gave it (manual, judge, …)
}

// certify re-scores a holdout against labels: the operating threshold is
// chosen on the selection part and tested once on the certification
// part's labels of the requirement's certify_sources (selective.Certify).
// Items whose message has no label any more are left out. Each row carries
// the label and the predicted class, so the certification reports which
// classes the threshold never answered.
func (h *Holdout) certify(t engine.QuestionType, labels map[int64]evalLabel, cutoff float64, req Requirement) selective.Certification {
	if h == nil {
		return selective.Certification{Target: req.TargetAccuracy, Threshold: selective.NoThreshold}
	}
	rank := func(items []HeldItem, keep func(evalLabel) bool) []selective.Ranked {
		var out []selective.Ranked
		for _, it := range items {
			l, ok := labels[it.ID]
			if !ok || !keep(l) {
				continue
			}
			c, conf := selective.Decide(t, it.Probs, cutoff)
			out = append(out, selective.Ranked{ID: it.ID, Correct: c == l.class, Confidence: conf,
				HasClass: true, Label: l.class, Predicted: c})
		}
		return out
	}
	sel := rank(h.Select, func(evalLabel) bool { return true })
	cert := rank(h.Certify, func(l evalLabel) bool { return slices.Contains(req.CertifySources, l.source) })
	return selective.Certify(sel, cert, req.TargetAccuracy, req.EffectiveZ())
}

// threshold is the operating threshold a certification gives a model:
// its chosen threshold when certified, NoThreshold (every answer
// abstains) otherwise.
func threshold(c selective.Certification) float64 {
	if c.Certified {
		return c.Threshold
	}
	return selective.NoThreshold
}
