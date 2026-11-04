package selective

import (
	"math"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// perfect is n correct answers at confidence conf (ids from base).
func perfect(base int64, n int, conf float64) []Ranked {
	out := make([]Ranked, n)
	for i := range out {
		out[i] = Ranked{ID: base + int64(i), Correct: true, Confidence: conf}
	}
	return out
}

// TestNoThresholdIsAboveEveryRealGate: the route's cuts are offer
// confidences (at most 1), so the sentinel must be above every one and a
// real gate at 1.0 must stay distinguishable from it.
func TestNoThresholdIsAboveEveryRealGate(t *testing.T) {
	if NoThreshold <= 1 {
		t.Fatalf("NoThreshold %v is not above a confidence", NoThreshold)
	}
	rows := Rank([]Ranked{{ID: 1, Correct: true, Confidence: 0.5}, {ID: 2, Correct: true, Confidence: 1}})
	if tp := Targets(rows, []float64{0.5}, 1.96)[0]; tp.Threshold != 0.5 || tp.Covered != 2 {
		t.Fatalf("a real gate at 1.0: %+v", tp)
	}
	if tp := Targets(nil, []float64{0.5}, 1.96)[0]; tp.Threshold != NoThreshold {
		t.Fatalf("an empty ranking: %+v", tp)
	}
}

// TestNeededIsTheSmallestCertifiableCount: with zero errors, Needed-1
// answers can never certify and Needed answers always do, at the targets
// the requirements use (docs/LAYER1.md).
func TestNeededIsTheSmallestCertifiableCount(t *testing.T) {
	const z = 1.96
	sel := perfect(0, 400, 0.9)
	for _, target := range []float64{0.95, 0.99} {
		n := Needed(target, z)
		if c := Certify(sel, perfect(1000, n-1, 0.9), target, z); c.Certified || c.Coverage != 0 || c.Answered != n-1 {
			t.Errorf("target %v: %d perfect answers certified: %+v", target, n-1, c)
		}
		c := Certify(sel, perfect(1000, n, 0.9), target, z)
		if !c.Certified || c.Answered != n || c.Errors != 0 || c.Coverage != 1 {
			t.Errorf("target %v: %d perfect answers did not certify: %+v", target, n, c)
		}
	}
}

// TestCoverageIsAnsweredOverEligible: the coverage denominator is the
// labelled certification population, not the rows a layer happened to
// answer, so a handful of answers cannot report full coverage. An
// eligible count below the rows is raised, so coverage never exceeds 1.
func TestCoverageIsAnsweredOverEligible(t *testing.T) {
	sel := perfect(0, 100, 0.9)
	cert := perfect(1000, 3, 0.9)
	c := CertifyEligible(sel, cert, 100, 0.4, 1.96)
	if !c.Certified || c.N != 3 || c.Eligible != 100 || c.Answered != 3 {
		t.Fatalf("certification: %+v", c)
	}
	if math.Abs(c.Coverage-0.03) > 1e-12 {
		t.Fatalf("coverage %v, want 3/100", c.Coverage)
	}
	// fewer eligible labels than rows: raised to the rows, coverage 1
	if c := CertifyEligible(sel, cert, 1, 0.4, 1.96); c.Eligible != 3 || c.Coverage != 1 {
		t.Fatalf("eligible below the rows: %+v", c)
	}
	for _, eligible := range []int{-5, 0, 1, 3, 4, 100} {
		c := CertifyEligible(sel, cert, eligible, 0.4, 1.96)
		if c.Eligible < len(cert) {
			t.Errorf("eligible %d not raised to the %d rows", eligible, len(cert))
		}
		if c.Coverage < 0 || c.Coverage > 1 {
			t.Errorf("eligible %d: coverage %v outside [0,1]", eligible, c.Coverage)
		}
	}
	// uncertified: coverage is 0 whatever the denominator
	if c := CertifyEligible(sel, cert, 100, 0.5, 1.96); c.Certified || c.Coverage != 0 {
		t.Fatalf("uncertified coverage: %+v", c)
	}
	// Certify is CertifyEligible with every row eligible: bit-identical
	if a, b := Certify(sel, cert, 0.4, 1.96), CertifyEligible(sel, cert, len(cert), 0.4, 1.96); !reflect.DeepEqual(a, b) {
		t.Fatalf("Certify and CertifyEligible differ:\n%+v\n%+v", a, b)
	}
}

// A rule that never saw a certification row is untested, not zero
// precision; a rule that answered every row wrong is tested with a zero
// lower bound. The two must not share a report.
func TestCertificationDistinguishesUntestedFromZero(t *testing.T) {
	empty := CertifyFixed(nil, 0, 0.95, 1.96)
	if empty.Tested || empty.Answered != 0 || empty.Precision != 0 || empty.Lower != 0 {
		t.Fatalf("an empty rule was tested: %+v", empty)
	}
	wrong := perfect(1000, 73, 0.9)
	for i := range wrong {
		wrong[i].Correct = false
	}
	c := CertifyFixed(wrong, len(wrong), 0.95, 1.96)
	if !c.Tested || c.Answered != 73 || c.Precision != 0 || c.Lower != 0 || c.Certified {
		t.Fatalf("a tested zero-precision rule: %+v", c)
	}
	sel := perfect(0, 100, 0.9)
	all := Certify(sel, perfect(1000, 73, 0.9), 0.95, 1.96)
	if !all.Tested || all.Precision != 1 || !all.Certified {
		t.Fatalf("a perfect tested rule: %+v", all)
	}
}

// Coverage has its own lower bound: 40 of 73 is point 0.548 but lower
// 0.434, so a 0.5 minimum refuses it. A legacy report keeps its point
// coverage instead of losing every answered route on upgrade — through the
// same Answering the stage and the route call, never two predicates.
func TestCoverageLowerBound(t *testing.T) {
	lo, _, ok := mathx.Wilson(40, 73, 1.96)
	if !ok {
		t.Fatal("Wilson refused 40/73")
	}
	c := Certification{Certified: true, Tested: true, Coverage: 40.0 / 73, CoverageLower: lo}
	if Answering(c, 0.5) {
		t.Fatalf("point %v lower %v passed a 0.5 minimum", c.Coverage, c.CoverageLower)
	}
	if !Answering(c, 0.4) {
		t.Fatalf("lower %v failed a 0.4 minimum", c.CoverageLower)
	}
	legacy := Certification{Certified: true, Coverage: 0.548}
	if !Answering(legacy, 0.5) {
		t.Fatal("a legacy report lost its point coverage")
	}
}

// TestGivenIsTheAnswerShape pins the shape of each question type (the
// old body of Answer) and the delegation: Answer is the threshold check
// plus Given.
func TestGivenIsTheAnswerShape(t *testing.T) {
	noul := &engine.Question{Name: "n", Type: engine.Noul}
	choice := &engine.Question{Name: "c", Type: engine.Choice, Options: []engine.Option{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	score := &engine.Question{Name: "s", Type: engine.Score, Levels: []string{"0", "1", "2"}}
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name   string
		q      *engine.Question
		probs  []float64
		cutoff float64
		want   engine.Answer
	}{
		{"choice", choice, []float64{0.2, 0.7, 0.1}, 0.5,
			engine.Answer{Type: engine.Choice, Choice: "b", Confidence: f(0.7),
				Probabilities: map[string]float64{"a": 0.2, "b": 0.7, "c": 0.1}}},
		{"choice ignores the cutoff", choice, []float64{0.2, 0.7, 0.1}, 0.9,
			engine.Answer{Type: engine.Choice, Choice: "b", Confidence: f(0.7),
				Probabilities: map[string]float64{"a": 0.2, "b": 0.7, "c": 0.1}}},
		{"noul yes", noul, []float64{0.3, 0.7}, 0.5,
			engine.Answer{Type: engine.Noul, Value: 0.7}},
		{"noul no below the cutoff", noul, []float64{0.45, 0.55}, 0.6,
			engine.Answer{Type: engine.Noul, Value: 0.55}},
		{"score", score, []float64{0.1, 0.3, 0.6}, 0.5,
			engine.Answer{Type: engine.Score, Value: 1.5, Confidence: f(0.6),
				Probabilities: map[string]float64{"0": 0.1, "1": 0.3, "2": 0.6}}},
		{"score tie takes the lower level", score, []float64{0.4, 0.4, 0.2}, 0.5,
			engine.Answer{Type: engine.Score, Value: 0.8, Confidence: f(0.4),
				Probabilities: map[string]float64{"0": 0.4, "1": 0.4, "2": 0.2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Given(tt.q, tt.probs, tt.cutoff)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Given:\n got %+v\nwant %+v", got, tt.want)
			}
			if a := Answer(tt.q, tt.probs, tt.cutoff, 0); !reflect.DeepEqual(a, got) {
				t.Fatalf("Answer at threshold 0 differs from Given:\n got %+v\nwant %+v", a, got)
			}
			conf, ok := Confidence(got, tt.cutoff)
			if !ok {
				t.Fatal("a given answer has a confidence")
			}
			a := Answer(tt.q, tt.probs, tt.cutoff, conf+0.01)
			if a.Abstain != engine.AbstainBelowTarget || a.Choice != "" || a.Value != 0 || a.Confidence != nil {
				t.Fatalf("above the confidence did not abstain cleanly: %+v", a)
			}
			if !reflect.DeepEqual(a.Probabilities, Distribution(tt.q, tt.probs)) {
				t.Fatalf("abstained distribution %v", a.Probabilities)
			}
		})
	}
}
