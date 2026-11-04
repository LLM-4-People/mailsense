package layer1

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// labelClass is the class a stored label stands for at its question's
// cutoff: a choice's option index, a yes/no side by selective.Decide at the
// profile's cutoff, or a level rounded half-up. It is the one class rule of
// a stored label: the training target, the evaluation labels and the review
// suggestion all read it.
func labelClass(q *engine.Question, l taxonomy.Label, cutoff float64) int {
	switch q.Type {
	case engine.Choice:
		return slices.IndexFunc(q.Options, func(o engine.Option) bool { return o.Name == l.Choice })
	case engine.Noul:
		class, _ := selective.Decide(engine.Noul, []float64{1 - l.Number, l.Number}, cutoff)
		return class
	}
	return mathx.RoundInt(l.Number)
}

// labelTarget is a stored label's target and class at the profile's cutoff:
// an option, yes/no (a probability label is soft; its class is
// selective.Decide's side at the cutoff) or a level (rounded half-up).
func labelTarget(q *engine.Question, raw string, cutoff float64) (Target, int, error) {
	l, err := taxonomy.DecodeLabel(q, raw)
	if err != nil {
		return nil, 0, err
	}
	class := labelClass(q, l, cutoff)
	switch q.Type {
	case engine.Choice:
		return OneHot(class), class, nil
	case engine.Noul:
		t, err := Soft([]float64{1 - l.Number, l.Number}, exactSum)
		return t, class, err
	}
	return OneHot(class), class, nil
}

// answerTarget is a decider answer's soft target: a choice's and a
// score's probabilities (an option or level the answer leaves out counts
// 0; a key the question does not have makes the answer invalid), a yes/no
// question's value.
func answerTarget(q *engine.Question, a engine.Answer, tolerance float64) (Target, error) {
	if a.Type != q.Type {
		return nil, fmt.Errorf("%w: a %s answer to a %s question", ErrBadTarget, a.Type, q.Type)
	}
	switch q.Type {
	case engine.Noul:
		if math.IsNaN(a.Value) || a.Value < 0 || a.Value > 1 {
			return nil, fmt.Errorf("%w: yes/no value %v", ErrBadTarget, a.Value)
		}
		return Soft([]float64{1 - a.Value, a.Value}, exactSum)
	case engine.Choice:
		p := make([]float64, len(q.Options))
		for k, v := range a.Probabilities {
			i := slices.IndexFunc(q.Options, func(o engine.Option) bool { return o.Name == k })
			if i < 0 {
				return nil, fmt.Errorf("%w: unknown option %q", ErrBadTarget, k)
			}
			p[i] = v
		}
		return Soft(p, tolerance)
	}
	p := make([]float64, len(q.Levels))
	for k, v := range a.Probabilities {
		i := -1
		for l := range q.Levels {
			if engine.LevelKey(l) == k {
				i = l
			}
		}
		if i < 0 {
			return nil, fmt.Errorf("%w: unknown level %q", ErrBadTarget, k)
		}
		p[i] = v
	}
	return Soft(p, tolerance)
}

// ruleTarget pools (averages: rules are correlated, a product would count
// one fact several times) the targets of the rules of the question that
// fire on the message and teach (a rule whose answer names every class is
// uninformative and skipped: bugs review #10): precision π (the Wilson lower bound
// measured on the evaluation labels, when it reaches min_lower_bound; a
// rule that fired on none: unmeasured_precision when set) on the rule's
// answer, 1 − π spread evenly over the other classes.
func (qs *questionSet) ruleTarget(s Source, g *gathered, id int64) (Target, bool) {
	var parts Target
	n := 0
	for _, ri := range g.fires[id] {
		r := g.rules[ri]
		if r.Question != qs.q.Name {
			continue
		}
		pi, ok := rulePrecision(s, r)
		if !ok {
			continue
		}
		p, ok := rulePart(qs.q, r.Answer, pi)
		if !ok {
			continue
		}
		parts = append(parts, p...)
		n++
	}
	if n == 0 {
		return nil, false
	}
	for i := range parts {
		parts[i].W /= float64(n)
	}
	return parts, true
}

// rulePart is one rule's soft target: precision π on the classes its
// answer names (a level range spreads π evenly), 1 − π over the other
// classes. ok is false for an uninformative answer (it names every class:
// it says nothing, and its target would not sum to 1) and for an answer
// naming no class of the question.
func rulePart(q *engine.Question, a evidence.RuleAnswer, pi float64) (Target, bool) {
	classes := classesOf(q)
	lo, hi := a.Classes(q)
	if lo < 0 || hi < lo || hi >= classes {
		return nil, false
	}
	if lo <= 0 && hi >= classes-1 {
		return nil, false
	}
	parts := Target{{W: pi, Lo: lo, Hi: hi}}
	rest := classes - (hi - lo + 1) // > 0: the answer does not name every class
	for c := range classes {
		if c < lo || c > hi {
			parts = append(parts, Part{W: (1 - pi) / float64(rest), Lo: c, Hi: c})
		}
	}
	return parts, true
}

// rulePrecision is the precision a rule teaches with (ok false: it does
// not teach).
func rulePrecision(s Source, r RuleInfo) (float64, bool) {
	if r.Stat.Fired == 0 {
		if s.UnmeasuredPrecision == nil {
			return 0, false
		}
		return *s.UnmeasuredPrecision, true
	}
	if r.Stat.WilsonLow < s.MinLowerBound {
		return 0, false
	}
	return r.Stat.WilsonLow, true
}

// evalLabels are the evaluation labels as message → question → value
// (the rules are measured on them).
func evalLabels(sources []Source, g *gathered) map[int64]map[string]json.RawMessage {
	out := map[int64]map[string]json.RawMessage{}
	for _, s := range sources {
		if !s.Eval {
			continue
		}
		for id, qs := range g.labels[s.LabelSource] {
			for q, v := range qs {
				if out[id] == nil {
					out[id] = map[string]json.RawMessage{}
				}
				if _, has := out[id][q]; !has {
					out[id][q] = json.RawMessage(v)
				}
			}
		}
	}
	return out
}
