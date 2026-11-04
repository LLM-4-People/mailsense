package selective

import (
	"slices"
	"testing"
)

// classed is n rows of one labelled class: predicted is the rule's answer,
// so Correct follows from label == predicted.
func classed(base int64, n, label, predicted int, conf float64) []Ranked {
	out := make([]Ranked, n)
	for i := range out {
		out[i] = Ranked{ID: base + int64(i), Correct: label == predicted, Confidence: conf,
			HasClass: true, Label: label, Predicted: predicted}
	}
	return out
}

// TestCertificationReportsClassRecall: the pooled verdict can reach the
// target on the majority class while a rare class is never answered. The
// report names that class and sets ClassCollapsed, but this round's
// Certified still follows the pooled rule; a balanced fixture does not
// collapse.
func TestCertificationReportsClassRecall(t *testing.T) {
	const target, z = 0.9, 1.96
	sel := perfect(0, 40, 0.9)
	majority := classed(1000, 97, 0, 0, 0.9)
	minority := classed(2000, 3, 1, 0, 0.5) // below the chosen .9 cut: never answered
	cert := append(slices.Clone(majority), minority...)

	c := Certify(sel, cert, target, z)
	if !c.Certified || c.Answered != 97 || c.Errors != 0 || c.Eligible != 100 {
		t.Fatalf("the pooled rule did not follow its own test: %+v", c)
	}
	want := []ClassRecall{
		{Class: 0, Eligible: 97, Answered: 97, Correct: 97},
		{Class: 1, Eligible: 3, Answered: 0, Correct: 0},
	}
	if !slices.Equal(c.ClassRecall, want) {
		t.Fatalf("class recall %+v, want %+v", c.ClassRecall, want)
	}
	if !c.ClassCollapsed {
		t.Fatalf("the unmeasured minority class did not collapse: %+v", c)
	}
	// The class facts are report-only: the same rows without them take the
	// same pooled decision.
	plain := slices.Clone(cert)
	for i := range plain {
		plain[i].HasClass, plain[i].Label, plain[i].Predicted = false, 0, 0
	}
	pooled := Certify(sel, plain, target, z)
	if pooled.Certified != c.Certified || pooled.Answered != c.Answered ||
		pooled.Errors != c.Errors || pooled.Lower != c.Lower || pooled.Coverage != c.Coverage {
		t.Fatalf("classes moved the pooled verdict:\n%+v\n%+v", c, pooled)
	}

	// The same counts, with the minority rows answered: no collapse.
	balanced := append(slices.Clone(majority), classed(2000, 3, 1, 1, 0.9)...)
	b := Certify(sel, balanced, target, z)
	if !b.Certified || b.ClassCollapsed {
		t.Fatalf("a balanced fixture collapsed: %+v", b)
	}
	if !slices.Equal(b.ClassRecall, []ClassRecall{
		{Class: 0, Eligible: 97, Answered: 97, Correct: 97},
		{Class: 1, Eligible: 3, Answered: 3, Correct: 3},
	}) {
		t.Fatalf("balanced class recall %+v", b.ClassRecall)
	}

	// The brief's 99/1 shape is reported but does not collapse: one
	// eligible label is below classMin, so the report shows it and leaves
	// the pooled verdict in force.
	one := append(classed(1000, 99, 0, 0, 0.9), classed(2000, 1, 1, 0, 0.5)...)
	cOne := Certify(sel, one, target, z)
	if !cOne.Certified || cOne.ClassCollapsed {
		t.Fatalf("99/1 collapsed or failed the pooled rule: %+v", cOne)
	}
	if !slices.Equal(cOne.ClassRecall, []ClassRecall{
		{Class: 0, Eligible: 99, Answered: 99, Correct: 99},
		{Class: 1, Eligible: 1, Answered: 0, Correct: 0},
	}) {
		t.Fatalf("99/1 class recall %+v", cOne.ClassRecall)
	}

	// A class below classMin is reported but cannot collapse on its own.
	tiny := append(slices.Clone(majority), classed(3000, classMin-1, 2, 0, 0.5)...)
	s := Certify(sel, tiny, target, z)
	if s.ClassCollapsed {
		t.Fatalf("a class with %d labels collapsed: %+v", classMin-1, s)
	}
	if !slices.Equal(s.ClassRecall, []ClassRecall{
		{Class: 0, Eligible: 97, Answered: 97, Correct: 97},
		{Class: 2, Eligible: classMin - 1, Answered: 0, Correct: 0},
	}) {
		t.Fatalf("tiny class recall %+v", s.ClassRecall)
	}
}

// TestCertificationWithoutClassesSkipsTheReport: a caller that supplies no
// class facts gets no per-class report and no collapse, and the pooled
// result is the one it always was.
func TestCertificationWithoutClassesSkipsTheReport(t *testing.T) {
	const target, z = 0.9, 1.96
	c := Certify(perfect(0, 40, 0.9), perfect(1000, 100, 0.9), target, z)
	if len(c.ClassRecall) != 0 || c.ClassCollapsed {
		t.Fatalf("rows without classes reported one: %+v", c)
	}
	if !c.Certified || c.Answered != 100 || c.Errors != 0 || c.Coverage != 1 {
		t.Fatalf("the pooled result changed: %+v", c)
	}
}

// TestCertifyChoosesThePointThreshold: the threshold is the point
// estimate on the selection part, not its Wilson lower bound. The fixture
// has 85 correct answers at .9 then 15 wrong at .8: the point estimate
// reaches .8 over all 100, while the lower bound only reaches it over the
// perfect 85.
func TestCertifyChoosesThePointThreshold(t *testing.T) {
	const target, z = 0.8, 1.96
	var sel []Ranked
	for i := range 85 {
		sel = append(sel, Ranked{ID: int64(i), Correct: true, Confidence: 0.9})
	}
	for i := range 15 {
		sel = append(sel, Ranked{ID: int64(100 + i), Correct: false, Confidence: 0.8})
	}
	tp := Targets(Rank(sel), []float64{target}, z)[0]
	if tp.Threshold == tp.ThresholdLB {
		t.Fatalf("the fixture does not distinguish the two cuts: %+v", tp)
	}
	if tp.Threshold != 0.8 || tp.ThresholdLB != 0.9 {
		t.Fatalf("fixture cuts changed: point %v lower bound %v", tp.Threshold, tp.ThresholdLB)
	}
	c := Certify(sel, perfect(1000, 100, 0.9), target, z)
	if c.Threshold != tp.Threshold {
		t.Fatalf("Certify chose %v, the point threshold is %v", c.Threshold, tp.Threshold)
	}
	if c.Threshold == tp.ThresholdLB {
		t.Fatalf("Certify chose the lower-bound threshold %v", c.Threshold)
	}
}

// A fixed rule that answers no row of a class must report that class as
// collapsed: the certified class report is the only place the operator can
// see that the cascade never reached it.
func TestCertifyFixedReportsUnansweredClassAsCollapsed(t *testing.T) {
	var cert []Ranked
	for i := range 100 {
		cert = append(cert, Ranked{ID: int64(i + 1), Correct: true, Confidence: 0.9,
			HasClass: true, Label: 0, Predicted: 0})
	}
	for i := range 3 {
		cert = append(cert, Ranked{ID: int64(1000 + i), HasClass: true, Label: 1, Abstained: true})
	}
	c := CertifyFixed(cert, len(cert), 0.9, 1.96)
	if !c.Certified {
		t.Fatalf("the answered class must still certify: %+v", c)
	}
	if c.Answered != 100 || c.N != 103 || c.Errors != 0 {
		t.Fatalf("abstained rows must not count as answers: %+v", c)
	}
	want := []ClassRecall{
		{Class: 0, Eligible: 100, Answered: 100, Correct: 100},
		{Class: 1, Eligible: 3, Answered: 0, Correct: 0},
	}
	if !slices.Equal(c.ClassRecall, want) {
		t.Fatalf("class recall %+v, want %+v", c.ClassRecall, want)
	}
	if !c.ClassCollapsed {
		t.Fatalf("a class with 3 eligible rows and no answer is collapsed: %+v", c)
	}
}
