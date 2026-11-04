package layer1

import "github.com/LLM-4-People/mailsense/internal/engine"

// keyAnswer is the layer-0 key answering a message's question: among the
// key rules (evidence.RuleStat.Key) that fire on the message and whose
// answer is informative, the one with the highest Wilson lower bound
// (ties: the lower rule id), with its soft class distribution (the
// rule's precision on its answer, the rest spread over the other
// classes: rulePart). ok is false when no key offers an answer.
func keyAnswer(q *engine.Question, rules []RuleInfo, fires map[int64][]int, id int64) (RuleInfo, []float64, bool) {
	var best RuleInfo
	var bestTarget Target
	found := false
	for _, ri := range fires[id] {
		r := rules[ri]
		if r.Question != q.Name || !r.Stat.Key {
			continue
		}
		t, ok := rulePart(q, r.Answer, r.Stat.WilsonLow)
		if !ok {
			continue
		}
		if !found || r.Stat.WilsonLow > best.Stat.WilsonLow ||
			(r.Stat.WilsonLow == best.Stat.WilsonLow && r.ID < best.ID) {
			best, bestTarget, found = r, t, true
		}
	}
	if !found {
		return RuleInfo{}, nil, false
	}
	return best, bestTarget.Mass(classesOf(q)), true
}
