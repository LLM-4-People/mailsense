package selective

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// InCertifyPart reports whether a group (a conversation) belongs to the
// certification part of the labels: a digest of the group, read as a
// number in [0, 1), below fraction. Stable: a conversation stays in its
// part whatever other labels are added, so the two parts never share a
// conversation.
func InCertifyPart(group string, fraction float64) bool {
	u, _ := strconv.ParseUint(digest.Hex([]byte(group), 16), 16, 64)
	return float64(u)/math.Exp2(64) < fraction
}

// Certification is a decision rule's test on the disjoint certification
// part: the answers it gives there, their errors and the Wilson lower
// bound of their precision. A threshold rule (Certify, CertifyEligible)
// chooses its cut on the selection part; a fixed rule (CertifyFixed) was
// cut elsewhere and is only tested here. Certified when that bound
// reaches the target; Coverage is then the answered share of the
// eligible certification labels (0 otherwise).
//
// ClassRecall is the same test per labelled class: a rule can reach the
// pooled target on the majority class while answering none of a rare
// one. It is report-only: this round it never changes Certified.
type Certification struct {
	Target    float64 `json:"target"`
	Threshold float64 `json:"threshold"` // chosen on the selection part (NoThreshold: none qualifies there; 0: a fixed rule chose no cut)
	Selected  int     `json:"selected"`  // labels of the selection part (0 for a fixed rule)
	N         int     `json:"n"`         // tested rows of the certification part (answers are Answered; a fixed rule's abstentions stay here)
	Eligible  int     `json:"eligible"`  // labelled certification messages: the coverage denominator (at least N)
	Answered  int     `json:"answered"`  // of the tested rows, the answers the rule gives (a fixed rule: every row)
	Errors    int     `json:"errors"`
	Lower     float64 `json:"lower_bound"` // Wilson lower bound of the answered precision
	// Precision is the observed answered precision (0 when nothing answered).
	Precision float64 `json:"precision"`
	// Tested distinguishes a failed test (Answered > 0, Lower may be 0) from
	// a rule that never ran on a certification row.
	Tested bool `json:"tested"`
	// CoverageLower is the Wilson lower bound of Answered/Eligible at z.
	CoverageLower float64 `json:"coverage_lower_bound"`
	Certified     bool    `json:"certified"`
	Coverage      float64 `json:"coverage"`
	// ClassRecall is empty when the caller supplied no classes.
	ClassRecall []ClassRecall `json:"class_recall"`
	// ClassCollapsed reports a class with at least classMin eligible
	// certification labels and no answered row: the pooled rule reached the
	// target while that class was never answered. It does not reject
	// certification yet; blocking is the next, separately approved rule.
	ClassCollapsed bool `json:"class_collapsed"`
}

// AnsweredPrecision is the observed answered precision of the certification.
// It returns the stored value when the report carries one and the ratio of the
// stored counts for a report written before that field existed (a legacy row
// with Precision 0). Nothing answered is 0: there is no precision to report.
//
// The certification writer and every reader (the requirement's label request,
// the learning blocker's detail and purchase plan) use this one accessor, so a
// report loaded from an older store cannot be read as 0 % precise while the
// same counts freshly certified are read as their real ratio.
func (c Certification) AnsweredPrecision() float64 {
	if c.Precision != 0 || c.Answered <= 0 {
		return c.Precision
	}
	return answeredPrecision(c.Answered, c.Errors)
}

// answeredPrecision is the one ratio: answered minus errors over answered, and
// zero when nothing was answered.
func answeredPrecision(answered, errors int) float64 {
	if answered <= 0 {
		return 0
	}
	return float64(answered-errors) / float64(answered)
}

// ClassRecall is one labelled class's coverage by a decision rule on the
// certification part: every classed row is eligible, Answered and Correct
// count the rows the rule actually answered.
type ClassRecall struct {
	Class    int `json:"class"`
	Eligible int `json:"eligible"`
	Answered int `json:"answered"`
	Correct  int `json:"correct"`
}

// classMin is the fewest eligible certification labels a class needs
// before zero answers is a collapse rather than an unmeasured class.
const classMin = 3

// classRecall reports each labelled class of cert. answered says whether
// the rule answers a row (a fixed rule answers every row it was given).
// Rows without a class are ignored, so a caller that supplies none gets an
// empty report and no collapse.
func classRecall(cert []Ranked, answered func(Ranked) bool) ([]ClassRecall, bool) {
	index := map[int]int{}
	var out []ClassRecall
	for _, r := range cert {
		if !r.HasClass {
			continue
		}
		i, ok := index[r.Label]
		if !ok {
			i = len(out)
			index[r.Label] = i
			out = append(out, ClassRecall{Class: r.Label})
		}
		out[i].Eligible++
		if !answered(r) {
			continue
		}
		out[i].Answered++
		if r.Correct {
			out[i].Correct++
		}
	}
	slices.SortFunc(out, func(a, b ClassRecall) int { return cmp.Compare(a.Class, b.Class) })
	collapsed := false
	for _, r := range out {
		if r.Eligible >= classMin && r.Answered == 0 {
			collapsed = true
		}
	}
	return out, collapsed
}

// Certify chooses the threshold on sel (the lowest confidence of the
// largest most-confident share whose point precision reaches target: the
// point estimate — any rule on sel is valid, the test on cert is what
// certifies) and tests it once on cert: certified when the Wilson lower
// bound (at z) of the answered precision reaches target. Choosing and
// testing on disjoint labels removes the optimism of scanning every cut
// of the labels the bound is computed on. Coverage is Answered/N: every
// certification row is eligible.
func Certify(sel, cert []Ranked, target, z float64) Certification {
	return CertifyEligible(sel, cert, len(cert), target, z)
}

// CertifyEligible is Certify with the coverage denominator made explicit:
// eligible is the number of labelled certification messages the caller
// counts, including the messages no tested answer exists for (the caller
// decides which labels are eligible). It is raised to len(cert) when the
// caller passes less, so Coverage = Answered/Eligible can never exceed 1.
// The threshold is still chosen on sel only and tested once on cert;
// CertifyEligible(sel, cert, len(cert), target, z) is exactly Certify.
func CertifyEligible(sel, cert []Ranked, eligible int, target, z float64) Certification {
	if eligible < len(cert) {
		eligible = len(cert)
	}
	c := Certification{Target: target, Threshold: NoThreshold, Selected: len(sel), N: len(cert), Eligible: eligible}
	report := func() {
		c.ClassRecall, c.ClassCollapsed = classRecall(cert, func(r Ranked) bool { return r.Confidence >= c.Threshold })
	}
	report()
	if len(sel) == 0 || len(cert) == 0 {
		return c
	}
	c.Threshold = Targets(Rank(sel), []float64{target}, z)[0].Threshold
	report()
	if c.Threshold == NoThreshold {
		return c
	}
	c.Tested = true
	c.Answered, c.Errors = Answered(cert, c.Threshold)
	c.finish(target, z)
	return c
}

// CertifyFixed certifies a decision rule whose cut was chosen elsewhere
// (the route's per-layer gates): it never scans the certification labels
// and chooses nothing. Every certification row the caller passes is
// eligible; the rows it answered (all of them unless Abstained is set) are
// N's answers, Errors the wrong answered rows, and the test is one Wilson
// bound (at z) on their precision. Eligible is the labelled certification
// population, raised to len(cert) so Coverage = Answered/Eligible cannot
// exceed 1. Threshold and Selected are 0: a fixed rule has no cut and no
// selection scan; read Certified, never Threshold. The class report covers
// every classed row, so a class the cascade never answered is visible as
// collapsed instead of silently absent.
func CertifyFixed(cert []Ranked, eligible int, target, z float64) Certification {
	if eligible < len(cert) {
		eligible = len(cert)
	}
	c := Certification{Target: target, N: len(cert), Eligible: eligible}
	c.Tested = len(cert) > 0
	c.ClassRecall, c.ClassCollapsed = classRecall(cert, func(r Ranked) bool { return !r.Abstained })
	for _, r := range cert {
		if r.Abstained {
			continue
		}
		c.Answered++
		if !r.Correct {
			c.Errors++
		}
	}
	c.finish(target, z)
	return c
}

// finish computes a certification's test from the counts its constructor
// filled in: the observed answered precision, the Wilson lower bounds (at z)
// of that precision and of the answered coverage, and the two verdict fields
// (Certified, and the certified Coverage). CertifyEligible and CertifyFixed
// both end here, so the threshold rule and the fixed rule cannot report the
// same counts under different numbers.
func (c *Certification) finish(target, z float64) {
	c.Precision = answeredPrecision(c.Answered, c.Errors)
	if lo, _, ok := mathx.Wilson(c.Answered, c.Eligible, z); ok {
		c.CoverageLower = lo
	}
	if lo, _, ok := mathx.Wilson(c.Answered-c.Errors, c.Answered, z); ok {
		c.Lower = lo
		c.Certified = lo >= target
	}
	if c.Certified {
		c.Coverage = float64(c.Answered) / float64(c.Eligible)
	}
}

// EffectiveCoverage is the coverage a certification is judged on: the Wilson
// lower bound of a tested report, or the point coverage of a report written
// before CoverageLower existed (Tested false and no lower bound recorded).
// The legacy fallback lives here only, so every surface reads the same
// coverage for the same stored report.
func (c Certification) EffectiveCoverage() float64 {
	if c.Tested || c.CoverageLower > 0 {
		return c.CoverageLower
	}
	return c.Coverage
}

// Answering is the one answer predicate of a certification: certified at the
// target, its effective coverage at or above the requirement's minimum.
// Route decisions, verdicts, the learning stages and their blockers all call
// it, so no surface can call a route answerable while another refuses to
// answer.
func Answering(c Certification, minCoverage float64) bool {
	return c.Certified && c.EffectiveCoverage() >= minCoverage
}

// Needed is the fewest answers without an error whose Wilson lower bound
// at z reaches target (n/n ≥ target ⇔ n ≥ z²·t/(1−t)): what a
// certification part must answer at least.
func Needed(target, z float64) int {
	return int(math.Ceil(z * z * target / (1 - target)))
}
