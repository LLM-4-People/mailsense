package layer1

import (
	"math"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
)

// The route's key participant is the best firing key, with the rule's
// soft distribution: the highest Wilson lower bound, ties by rule id,
// non-keys, uninformative answers and classes the question does not have
// left out.
func TestKeyAnswerPicksTheBestKey(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	noul := q(engine.Noul)
	rules := []RuleInfo{
		{ID: "b", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}, Stat: evidence.RuleStat{Key: true, WilsonLow: 0.9}},
		{ID: "a", Question: "n", Answer: evidence.RuleAnswer{Truth: &no}, Stat: evidence.RuleStat{Key: true, WilsonLow: 0.9}},
		{ID: "c", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}, Stat: evidence.RuleStat{Key: true, WilsonLow: 0.95}},
		{ID: "d", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}, Stat: evidence.RuleStat{Key: false, WilsonLow: 0.99}},
		{ID: "e", Question: "n", Answer: evidence.RuleAnswer{Option: "no-such-option"}, Stat: evidence.RuleStat{Key: true, WilsonLow: 0.99}},
		{ID: "all", Question: "s", Answer: evidence.RuleAnswer{Min: 0, Max: 3, Range: true}, Stat: evidence.RuleStat{Key: true, WilsonLow: 0.99}},
	}
	fires := map[int64][]int{1: {0, 1, 2, 3, 4}, 2: {0, 1}, 3: {5}}
	r, p, ok := keyAnswer(noul, rules, fires, 1)
	if !ok || r.ID != "c" || len(p) != 2 || math.Abs(p[1]-0.95) > 1e-12 || math.Abs(p[0]-0.05) > 1e-12 {
		t.Fatalf("the highest lower bound: %+v %v %v", r, p, ok)
	}
	r, p, ok = keyAnswer(noul, rules, fires, 2)
	if !ok || r.ID != "a" || math.Abs(p[0]-0.9) > 1e-12 {
		t.Fatalf("a tie goes to the lower rule id: %+v %v %v", r, p, ok)
	}
	sq := q(engine.Score)
	if _, _, ok := keyAnswer(sq, rules, fires, 3); ok {
		t.Fatal("an uninformative key offered an answer")
	}
	if _, _, ok := keyAnswer(noul, rules, fires, 99); ok {
		t.Fatal("a message no key fires on offered an answer")
	}
}
