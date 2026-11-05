package evidence

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// RuleAnswers applies the definitional rule key to stored evidence:
// message → question → the deterministic rule answer. It is the read-time
// fallback for a question the route abstained on as not certified; it
// writes nothing and never changes the route, coverage or the learning gate.
//
// Only a definitional rule answers here. A measured estimate is served by
// the route's own key, not by this read-time fallback; an unmeasured
// estimate is evidence only. The gold dev table is why: most estimates were
// not accurate enough to be an answer (a question mark is not a request
// count, a named date is not always a deadline).
//
// A question is answered only when its firing rules agree. For a choice or
// noul question every firing rule must name the same option/truth. For a
// score question the narrowest firing range wins; only an exact range
// (min == max) is served, because a range is not one level. A conflict is
// no answer, never a guess.
func (e *Engine) RuleAnswers(ctx context.Context, ids []int64) (map[int64]map[string]engine.Answer, error) {
	if e == nil || e.rules == nil || len(ids) == 0 {
		return nil, nil
	}
	records, err := e.Records(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("evidence: rule answers: %w", err)
	}
	out := make(map[int64]map[string]engine.Answer)
	for id, rec := range records {
		byQuestion := map[string][]*Rule{}
		for i := range e.rules.Rules {
			r := &e.rules.Rules[i]
			if !r.Definitional {
				continue
			}
			if r.Fires(rec) {
				byQuestion[r.Question] = append(byQuestion[r.Question], r)
			}
		}
		for name, fired := range byQuestion {
			q, ok := e.rules.Question(name)
			if !ok {
				continue
			}
			a, _, ok := agreedRuleAnswer(q, fired)
			if !ok {
				continue
			}
			if out[id] == nil {
				out[id] = map[string]engine.Answer{}
			}
			out[id][name] = a
		}
	}
	return out, nil
}

// agreedRuleAnswer resolves one question's firing rules. The rule id is the
// provenance of the answer; a tie or a range that names more than one level
// answers nothing.
func agreedRuleAnswer(q *engine.Question, fired []*Rule) (engine.Answer, string, bool) {
	if q == nil || len(fired) == 0 {
		return engine.Answer{}, "", false
	}
	if q.Type == engine.Score {
		var best *Rule
		bestWidth := 0
		for _, r := range fired {
			ra := r.Parsed()
			if !ra.Range || ra.Min > ra.Max {
				return engine.Answer{}, "", false
			}
			width := ra.Max - ra.Min
			switch {
			case best == nil || width < bestWidth:
				best, bestWidth = r, width
			case width == bestWidth:
				if !sameRuleAnswer(best.Parsed(), ra) {
					return engine.Answer{}, "", false
				}
			}
		}
		if bestWidth != 0 {
			return engine.Answer{}, "", false
		}
		a, ok := ruleEngineAnswer(q, best.Parsed())
		a.AutoRule = best.ID
		return a, best.ID, ok
	}
	var agreed *Rule
	for _, r := range fired {
		if agreed == nil {
			agreed = r
			continue
		}
		if !sameRuleAnswer(agreed.Parsed(), r.Parsed()) {
			return engine.Answer{}, "", false
		}
	}
	a, ok := ruleEngineAnswer(q, agreed.Parsed())
	a.AutoRule = agreed.ID
	return a, agreed.ID, ok
}

// sameRuleAnswer compares the answer a rule names, independent of its id.
func sameRuleAnswer(a, b RuleAnswer) bool {
	if a.Range != b.Range || a.Option != b.Option || a.Min != b.Min || a.Max != b.Max {
		return false
	}
	switch {
	case a.Truth == nil || b.Truth == nil:
		return a.Truth == nil && b.Truth == nil
	default:
		return *a.Truth == *b.Truth
	}
}

// ruleEngineAnswer builds the typed answer of one resolved rule. The
// distribution is one-hot, so a consumer that reads probabilities sees the
// same class as the displayed value; a range that is not one level is
// refused.
func ruleEngineAnswer(q *engine.Question, ra RuleAnswer) (engine.Answer, bool) {
	if q == nil {
		return engine.Answer{}, false
	}
	switch q.Type {
	case engine.Choice:
		if ra.Option == "" {
			return engine.Answer{}, false
		}
		probs := make(map[string]float64, len(q.Options))
		found := false
		for _, o := range q.Options {
			probs[o.Name] = 0
			if o.Name == ra.Option {
				found = true
			}
		}
		if !found {
			return engine.Answer{}, false
		}
		probs[ra.Option] = 1
		return engine.Answer{Type: q.Type, Choice: ra.Option, Probabilities: probs}, true
	case engine.Noul:
		if ra.Truth == nil {
			return engine.Answer{}, false
		}
		yes := 0.0
		if *ra.Truth {
			yes = 1
		}
		return engine.Answer{Type: q.Type, Value: yes}, true
	case engine.Score:
		if !ra.Range || ra.Min != ra.Max || ra.Min < 0 || ra.Min >= len(q.Levels) {
			return engine.Answer{}, false
		}
		probs := make(map[string]float64, len(q.Levels))
		for i := range q.Levels {
			probs[engine.LevelKey(i)] = 0
		}
		probs[engine.LevelKey(ra.Min)] = 1
		return engine.Answer{Type: q.Type, Value: float64(ra.Min), Probabilities: probs}, true
	}
	return engine.Answer{}, false
}
