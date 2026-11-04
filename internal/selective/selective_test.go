package selective

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

func choiceQ() *engine.Question {
	return &engine.Question{Name: "c", Type: engine.Choice, Options: []engine.Option{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
}

func TestTargetsCutOnlyBetweenConfidences(t *testing.T) {
	rows := Rank([]Ranked{{ID: 1, Correct: true, Confidence: 0.9}, {ID: 2, Correct: true, Confidence: 0.9}, {ID: 3, Correct: false, Confidence: 0.9}, {ID: 4, Correct: true, Confidence: 0.8}, {ID: 5, Correct: true, Confidence: 0.7}})
	tp := Targets(rows, []float64{1}, 1.96)[0]
	if tp.Covered != 0 || tp.Threshold != NoThreshold {
		t.Fatalf("a tie with an error cannot be split: %+v", tp)
	}
	if c, e := Answered(rows, 0.8); c != 4 || e != 1 {
		t.Fatalf("answered %d %d", c, e)
	}
}

func TestCertifyTestsTheThresholdOnTheOtherPart(t *testing.T) {
	var sel, cert []Ranked
	for i := range 500 {
		sel = append(sel, Ranked{ID: int64(i), Correct: true, Confidence: 0.9 + float64(i%10)/100})
		cert = append(cert, Ranked{ID: int64(1000 + i), Correct: true, Confidence: 0.9 + float64(i%10)/100})
	}
	c := Certify(sel, cert, 0.99, 1.96)
	if !c.Certified || c.Coverage != 1 || c.Answered != 500 || c.Threshold != 0.9 {
		t.Fatalf("%+v", c)
	}
	// errors in the certification part above the chosen threshold
	for i := range 10 {
		cert[i].Correct = false
	}
	if c := Certify(sel, cert, 0.99, 1.96); c.Certified || c.Coverage != 0 || c.Errors != 10 {
		t.Fatalf("errors certified: %+v", c)
	}
	if c := Certify(nil, cert, 0.99, 1.96); c.Certified || c.Threshold != NoThreshold {
		t.Fatalf("no selection labels: %+v", c)
	}
	if c := Certify(sel, nil, 0.99, 1.96); c.Certified {
		t.Fatalf("no certification labels: %+v", c)
	}
	if n := Needed(0.99, 1.96); n != 381 {
		t.Fatalf("needed %d", n)
	}
}

// Finding (bugs #13, rules C): scanning every cut point for the Wilson
// bound over-certifies — at a true precision just under the target
// 3–11 % of trials certified 0.99 against a nominal 2.5 %. The split
// method tests one threshold once: it certifies at most at the nominal
// rate.
func TestSplitCertificationIsNotOptimistic(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	const trials, n, truth = 400, 1500, 0.989
	scan, split := 0, 0
	draw := func(base int64) []Ranked {
		out := make([]Ranked, n)
		for i := range out {
			out[i] = Ranked{ID: base + int64(i), Correct: r.Float64() < truth, Confidence: r.Float64()}
		}
		return out
	}
	for k := range trials {
		all := draw(int64(k) * 10 * n)
		if Targets(Rank(all), []float64{0.99}, 1.96)[0].CoverageLB > 0 {
			scan++
		}
		if Certify(all[:n/2], all[n/2:], 0.99, 1.96).Certified {
			split++
		}
	}
	t.Logf("certified 0.99 at true precision %.3f: scan every cut %d/%d, split %d/%d", truth, scan, trials, split, trials)
	if float64(split)/trials > 0.025 {
		t.Fatalf("the split method certified %d of %d", split, trials)
	}
	if scan <= split {
		t.Fatalf("the scan was expected to be the optimistic one (%d vs %d)", scan, split)
	}
}

func TestInCertifyPartIsStableAndNearTheFraction(t *testing.T) {
	in := 0
	for i := range 10000 {
		g := "t" + string(rune('a'+i%26)) + string(rune(i))
		if InCertifyPart(g, 0.3) {
			in++
		}
		if InCertifyPart(g, 0.3) != InCertifyPart(g, 0.3) || (InCertifyPart(g, 0.3) && !InCertifyPart(g, 0.6)) {
			t.Fatal("not stable or not monotone in the fraction")
		}
	}
	if math.Abs(float64(in)/10000-0.3) > 0.02 {
		t.Fatalf("share %d of 10000", in)
	}
}

func TestAnswerAbstainsWithoutAUsableValue(t *testing.T) {
	q := choiceQ()
	a := Answer(q, []float64{0.2, 0.7, 0.1}, 0.5, 0.6)
	if a.Abstain != "" || a.Choice != "b" || *a.Confidence != 0.7 {
		t.Fatalf("%+v", a)
	}
	a = Answer(q, []float64{0.2, 0.7, 0.1}, 0.5, 0.8)
	if a.Abstain != engine.AbstainBelowTarget || a.Choice != "" || a.Value != 0 || a.Confidence != nil || a.Probabilities["b"] != 0.7 {
		t.Fatalf("abstained %+v", a)
	}
	yn := &engine.Question{Name: "y", Type: engine.Noul}
	a = Answer(yn, []float64{0.45, 0.55}, 0.6, 0.5) // cutoff 0.6 chooses no, with probability 0.45
	if a.Abstain == "" || a.Value != 0 || a.Probabilities[engine.NoulYes] != 0.55 {
		t.Fatalf("yes/no below its threshold %+v", a)
	}
	if c, ok := Confidence(engine.Answer{Type: engine.Noul, Value: 0.55}, 0.6); !ok || math.Abs(c-0.45) > 1e-12 {
		t.Fatalf("yes/no confidence %v", c)
	}
	if _, ok := Confidence(a, 0.6); ok {
		t.Fatal("an abstained answer has no confidence")
	}
	if _, ok := Confidence(engine.Answer{Type: engine.Choice, Refused: true}, 0.5); ok {
		t.Fatal("a refused answer has no confidence")
	}
	// Route-level abstentions are the route's wire reasons, not engine
	// constants: they are unusable here too.
	if _, ok := Confidence(engine.Answer{Type: engine.Choice, Abstain: "not_certified"}, 0.5); ok {
		t.Fatal("a route-level abstention has no confidence")
	}
	if _, ok := Confidence(engine.Answer{Probabilities: map[string]float64{"a": 0.9, "b": 0.1}}, 0.5); ok {
		t.Fatal("an answer without a type is not usable")
	}
	sc := &engine.Question{Name: "s", Type: engine.Score, Levels: []string{"0", "1", "2"}}
	a = Answer(sc, []float64{0.1, 0.3, 0.6}, 0.5, 0.5)
	if math.Abs(a.Value-1.5) > 1e-12 || *a.Confidence != 0.6 {
		t.Fatalf("score %+v", a)
	}
	if c, ok := Confidence(engine.Answer{Type: engine.Score, Probabilities: map[string]float64{"0": 0.2, "1": 0.8}}, 0.5); !ok || c != 0.8 {
		t.Fatalf("score without a stated confidence %v", c)
	}
}

func TestProbsReadsBackEveryForm(t *testing.T) {
	q := choiceQ()
	for _, th := range []float64{0.5, 0.9} { // given, abstained
		p, ok := Probs(q, Answer(q, []float64{0.2, 0.7, 0.1}, 0.5, th))
		if !ok || p[1] != 0.7 || p[0] != 0.2 {
			t.Fatalf("threshold %v: %v %v", th, p, ok)
		}
	}
	yn := &engine.Question{Name: "y", Type: engine.Noul}
	for _, th := range []float64{0.5, 0.9} {
		p, ok := Probs(yn, Answer(yn, []float64{0.3, 0.7}, 0.5, th))
		if !ok || math.Abs(p[1]-0.7) > 1e-12 {
			t.Fatalf("yes/no %v: %v", th, p)
		}
	}
	if _, ok := Probs(q, engine.Answer{Type: engine.Choice, Choice: "a", Probabilities: map[string]float64{"zz": 1}}); ok {
		t.Fatal("foreign keys read back")
	}
}

func TestCoverageAtPrecisionTargets(t *testing.T) {
	// 100 answers: the 60 most confident right, then one wrong, then 39
	// mixed; two answers tie at the cut
	var rows []Ranked
	for i := range 100 {
		rows = append(rows, Ranked{ID: int64(i), Correct: i < 60 || (i > 60 && i%2 == 0), Confidence: 1 - float64(i)/200})
	}
	rows[59].Confidence = rows[60].Confidence // the 60th right one ties with the wrong one: no cut between them
	pts := Targets(Rank(rows), []float64{1, 0.98, 0.5}, 1.96)
	if pts[0].Covered != 59 || pts[0].Errors != 0 || pts[0].Coverage != 0.59 {
		t.Fatalf("precision 1: %+v", pts[0])
	}
	if pts[1].Covered != 61 || pts[1].Errors != 1 { // 60/61 = 0.984
		t.Fatalf("precision 0.98: %+v", pts[1])
	}
	if pts[0].CoverageLB != 0 || pts[0].ThresholdLB != NoThreshold {
		t.Fatalf("a Wilson bound never reaches 1: %+v", pts[0])
	}
	if pts[2].CoverageLB != 1 {
		t.Fatalf("precision 0.5 is certified over everything: %+v", pts[2])
	}
	if c, e := Answered(Rank(rows), pts[0].Threshold); c != 59 || e != 0 {
		t.Fatalf("answered at the threshold: %d %d", c, e)
	}
}
