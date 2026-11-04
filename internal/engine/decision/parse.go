package decision

import (
	"math"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// boundTolerance absorbs float noise at an upper bound (a probability of
// 1.0000000001); the value is clamped to the bound.
const boundTolerance = 1e-9

// bounded reads a number in [lo, hi] at the trust boundary (jsonx.Finite:
// finite JSON numbers only, never a boolean or a string).
func bounded(question, field string, v any, lo, hi float64) (float64, error) {
	f, ok := jsonx.Finite(v)
	if !ok {
		return 0, contractError(question, "%s is not a finite number: %s", field, jsonx.Kind(v))
	}
	if f < lo || f > hi+boundTolerance {
		return 0, contractError(question, "%s = %v is outside [%v, %v]", field, f, lo, hi)
	}
	return min(f, hi), nil
}

func probability(question, field string, v any) (float64, error) {
	return bounded(question, field, v, 0, 1)
}

func confidence(question string, v any) (*float64, error) {
	c, err := probability(question, "confidence", v)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// levelKeys is the set of probability keys of a score question.
func levelKeys(q *engine.Question) map[string]bool {
	keys := map[string]bool{}
	for i := range q.Levels {
		keys[engine.LevelKey(i)] = true
	}
	return keys
}

// optionKeys is the set of option names of a choice question.
func optionKeys(q *engine.Question) map[string]bool {
	keys := map[string]bool{}
	for _, o := range q.Options {
		keys[o.Name] = true
	}
	return keys
}

// checkChoice validates a decoded choice answer (shared by both codecs):
// the choice is an option and is among the returned probabilities.
func checkChoice(q *engine.Question, a *engine.Answer) error {
	if !q.HasOption(a.Choice) {
		return contractError(q.Name, "unknown choice %q", a.Choice)
	}
	if _, ok := a.Probabilities[a.Choice]; !ok {
		return contractError(q.Name, "choice %q is missing from probabilities", a.Choice)
	}
	return nil
}

// putProbability adds one probability, refusing unknown or repeated keys.
func putProbability(q *engine.Question, probs map[string]float64, allowed map[string]bool, key string, v any) error {
	if !allowed[key] {
		return contractError(q.Name, "probability for unknown option %q", key)
	}
	if _, dup := probs[key]; dup {
		return contractError(q.Name, "probability for %q given twice", key)
	}
	p, err := probability(q.Name, "probabilities."+key, v)
	probs[key] = p
	return err
}

// consistent checks an answer's probabilities against itself, as the
// vendors define them (provider.example.test: "a probability for every option",
// score "probability-weighted"; every one of 335,870 legacy SubQ answers
// complied): every option or level has a probability, they sum to 1, the
// choice is the most probable option, a score is the expected level.
func consistent(q *engine.Question, a *engine.Answer, tol float64) error {
	if a.Refused || q.Type == engine.Noul {
		return nil
	}
	want := len(q.Options)
	if q.Type == engine.Score {
		want = len(q.Levels)
	}
	if len(a.Probabilities) != want {
		return contractError(q.Name, "probabilities for %d of %d options", len(a.Probabilities), want)
	}
	sum, best, expected := 0.0, 0.0, 0.0
	for k, p := range a.Probabilities {
		sum, best = sum+p, max(best, p)
		if q.Type == engine.Score {
			level, _ := strconv.Atoi(k)
			expected += float64(level) * p
		}
	}
	switch {
	case math.Abs(sum-1) > tol:
		return contractError(q.Name, "probabilities sum to %.4f, not 1", sum)
	case q.Type == engine.Choice && a.Probabilities[a.Choice] < best-tol:
		return contractError(q.Name, "choice %q is not the most probable option", a.Choice)
	case q.Type == engine.Score && math.Abs(a.Value-expected) > tol*float64(q.Top()):
		return contractError(q.Name, "score %.4f is not the expected level %.4f", a.Value, expected)
	}
	return nil
}
