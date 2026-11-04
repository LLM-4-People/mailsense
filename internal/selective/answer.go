package selective

import (
	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Decide is the class a distribution answers and its confidence: the
// most likely class (choice option, score level) and its probability;
// for a yes/no question yes when P(yes) reaches the question's cutoff,
// and the probability of the side chosen. The one confidence definition:
// every threshold compares it.
func Decide(t engine.QuestionType, probs []float64, cutoff float64) (class int, confidence float64) {
	if t == engine.Noul {
		if probs[1] >= cutoff {
			return 1, probs[1]
		}
		return 0, probs[0]
	}
	c := 0
	for i := range probs {
		if probs[i] > probs[c] {
			c = i
		}
	}
	return c, probs[c]
}

// Decided is the class an answer decides at cutoff: Decide's class over the
// answer's own distribution. It is the one owner of the class a surface
// displays and derives a score level from; an answer without a distribution
// has no decided class.
func Decided(q *engine.Question, a engine.Answer, cutoff float64) (int, bool) {
	probs, ok := Probs(q, a)
	if !ok {
		return 0, false
	}
	class, _ := Decide(q.Type, probs, cutoff)
	return class, true
}

// Confidence is an answer's confidence by Decide's definition (false: an
// unusable answer — no type, refused or abstained — or one without the
// data): a yes/no answer's probability of the side its value chooses at
// cutoff; a choice or score answer's stated confidence, else the
// probability of its most likely option or level.
func Confidence(a engine.Answer, cutoff float64) (float64, bool) {
	if !a.Answered() {
		return 0, false
	}
	if a.Type == engine.Noul {
		_, c := Decide(engine.Noul, []float64{1 - a.Value, a.Value}, cutoff)
		return c, true
	}
	if a.Confidence != nil {
		return *a.Confidence, true
	}
	best, ok := 0.0, false
	for _, p := range a.Probabilities {
		best, ok = max(best, p), true
	}
	return best, ok
}

// Distribution is a class distribution as an answer's probabilities: per
// option (choice), per level key (score), per engine.NoulNo/NoulYes
// (yes/no).
func Distribution(q *engine.Question, probs []float64) map[string]float64 {
	out := map[string]float64{}
	switch q.Type {
	case engine.Choice:
		for k, o := range q.Options {
			out[o.Name] = probs[k]
		}
	case engine.Noul:
		out[engine.NoulNo], out[engine.NoulYes] = probs[0], probs[1]
	default:
		for k := range q.Levels {
			out[engine.LevelKey(k)] = probs[k]
		}
	}
	return out
}

// Given is a class distribution as the triage contract's answer, without
// a threshold: a choice's option, a yes/no question's P(yes) (no
// probabilities or confidence, as the engine contract asks), a score's
// expected level, the probabilities and (choice, score) the confidence.
// It is the one owner of the answer shape; Answer adds the threshold and
// the abstention.
func Given(q *engine.Question, probs []float64, cutoff float64) engine.Answer {
	class, conf := Decide(q.Type, probs, cutoff)
	a := engine.Answer{Type: q.Type}
	switch q.Type {
	case engine.Noul:
		a.Value = probs[1]
		return a
	case engine.Choice:
		a.Choice = q.Options[class].Name
	case engine.Score:
		for k := range q.Levels {
			a.Value += float64(k) * probs[k]
		}
	}
	a.Probabilities = Distribution(q, probs)
	a.Confidence = &conf
	return a
}

// Answer is a class distribution as the triage contract's answer, given
// on its own when its confidence reaches threshold (Given). Below
// threshold it abstains (engine.AbstainBelowTarget): Abstained.
func Answer(q *engine.Question, probs []float64, cutoff, threshold float64) engine.Answer {
	if _, conf := Decide(q.Type, probs, cutoff); conf < threshold {
		return Abstained(q, probs, engine.AbstainBelowTarget)
	}
	return Given(q, probs, cutoff)
}

// Abstained is the abstained form of a distribution: no choice, value or
// confidence (nothing a consumer could take for an answer), only the
// reason and the distribution for diagnostics and the next layer.
func Abstained(q *engine.Question, probs []float64, reason string) engine.Answer {
	return engine.Answer{Type: q.Type, Abstain: reason, Probabilities: Distribution(q, probs)}
}

// Probs is the class distribution an answer carries (an answer of
// Answer or Abstained; false when it has none: refused, or keys that are
// not the question's).
func Probs(q *engine.Question, a engine.Answer) ([]float64, bool) {
	if a.Refused || a.Type != q.Type {
		return nil, false
	}
	if q.Type == engine.Noul && a.Abstain == "" {
		return []float64{1 - a.Value, a.Value}, true
	}
	var keys []string
	switch q.Type {
	case engine.Choice:
		for _, o := range q.Options {
			keys = append(keys, o.Name)
		}
	case engine.Noul:
		keys = []string{engine.NoulNo, engine.NoulYes}
	default:
		for k := range q.Levels {
			keys = append(keys, engine.LevelKey(k))
		}
	}
	out := make([]float64, len(keys))
	for i, k := range keys {
		p, ok := a.Probabilities[k]
		if !ok {
			return nil, false
		}
		out[i] = p
	}
	return out, true
}
