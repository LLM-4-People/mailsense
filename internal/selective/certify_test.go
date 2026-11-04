package selective

import "testing"

// TestLegacyReportUsesTheOneAnswerPredicate: a report written before
// CoverageLower existed keeps its point coverage, and it keeps it through the
// one predicate every surface calls. The stage and the route cannot disagree
// about the same stored report, because there is no second rule to disagree
// with.
func TestLegacyReportUsesTheOneAnswerPredicate(t *testing.T) {
	legacy := Certification{Certified: true, Tested: false, Coverage: 0.8, CoverageLower: 0}
	if !Answering(legacy, 0.75) {
		t.Fatalf("a legacy report at point coverage %v refused 0.75: %+v", legacy.Coverage, legacy)
	}
	if Answering(legacy, 0.85) {
		t.Fatalf("a legacy report at point coverage %v answered 0.85: %+v", legacy.Coverage, legacy)
	}
	if got := legacy.EffectiveCoverage(); got != legacy.Coverage {
		t.Fatalf("effective coverage %v, want the legacy point coverage %v", got, legacy.Coverage)
	}
	// Certified is still the first half of the predicate: coverage alone
	// never answers.
	legacy.Certified = false
	if Answering(legacy, 0.75) {
		t.Fatalf("an uncertified legacy report answered: %+v", legacy)
	}
}

// TestTestedReportIgnoresPointCoverage: a tested report is judged on its
// Wilson lower bound alone; perfect point coverage cannot buy an answer when
// the bound misses the minimum.
func TestTestedReportIgnoresPointCoverage(t *testing.T) {
	c := Certification{Certified: true, Tested: true, Coverage: 1, CoverageLower: 0.3}
	if Answering(c, 0.5) {
		t.Fatalf("point coverage %v answered at lower bound %v: %+v", c.Coverage, c.CoverageLower, c)
	}
	if !Answering(c, 0.3) {
		t.Fatalf("lower bound %v refused its own minimum: %+v", c.CoverageLower, c)
	}
	if got := c.EffectiveCoverage(); got != c.CoverageLower {
		t.Fatalf("effective coverage %v, want the tested lower bound %v", got, c.CoverageLower)
	}
}

// TestCertificationFinishIsOneOwner: the threshold rule (CertifyEligible) and
// the fixed rule (CertifyFixed) report the same counts, eligible population,
// target and z through one finish method, so no formula can drift between
// the two constructors.
func TestCertificationFinishIsOneOwner(t *testing.T) {
	const z = 1.96
	tests := []struct {
		name      string
		answered  int
		errors    int
		eligible  int
		target    float64
		certified bool
	}{
		{"certified", 100, 1, 200, 0.9, true},
		{"precision below target", 100, 20, 100, 0.95, false},
		{"few answers, wide population", 73, 0, 200, 0.9, true},
		{"nothing answered", 0, 0, 50, 0.9, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cert := perfect(1000, tc.answered, 0.9)
			for i := 0; i < tc.errors; i++ {
				cert[i].Correct = false
			}
			// Every selection row answers, at the confidence the certification
			// rows carry: the threshold rule's cut is the one the fixed rule
			// was given, so both see the same answers.
			sel := perfect(0, 200, 0.9)
			fixed := CertifyFixed(cert, tc.eligible, tc.target, z)
			threshold := CertifyEligible(sel, cert, tc.eligible, tc.target, z)
			owner := Certification{Answered: tc.answered, Errors: tc.errors, Eligible: max(tc.eligible, tc.answered)}
			owner.finish(tc.target, z)

			if fixed.Answered != tc.answered || fixed.Errors != tc.errors || threshold.Answered != tc.answered || threshold.Errors != tc.errors {
				t.Fatalf("counts: fixed %d/%d, threshold %d/%d, want %d/%d",
					fixed.Answered, fixed.Errors, threshold.Answered, threshold.Errors, tc.answered, tc.errors)
			}
			if owner.Precision != fixed.Precision || owner.Lower != fixed.Lower ||
				owner.CoverageLower != fixed.CoverageLower || owner.Certified != fixed.Certified ||
				owner.Coverage != fixed.Coverage {
				t.Fatalf("a constructor does not report the finish's fields:\nowner %+v\nfixed %+v", owner, fixed)
			}
			if fixed.Precision != threshold.Precision || fixed.Lower != threshold.Lower ||
				fixed.CoverageLower != threshold.CoverageLower || fixed.Certified != threshold.Certified ||
				fixed.Coverage != threshold.Coverage {
				t.Fatalf("the two constructors report the same counts differently:\nfixed     %+v\nthreshold %+v", fixed, threshold)
			}
			if fixed.Certified != tc.certified {
				t.Fatalf("certified %v, want %v: %+v", fixed.Certified, tc.certified, fixed)
			}
			// The certified branch is the one coverage belongs to; the
			// uncertified branch must not report one.
			wantCoverage := 0.0
			if tc.certified {
				wantCoverage = float64(tc.answered) / float64(max(tc.eligible, tc.answered))
			}
			if fixed.Coverage != wantCoverage {
				t.Fatalf("coverage %v, want %v: %+v", fixed.Coverage, wantCoverage, fixed)
			}
		})
	}
}

// TestAnsweredPrecisionReadsLegacyCounts: a report written before the
// precision field existed carries its counts only. Reading it must give the
// same observed precision the certificate writer computes from those counts,
// or the same numbers would mean a below-target model on the requirement
// screen and an acceptable one on a fresh run.
func TestAnsweredPrecisionReadsLegacyCounts(t *testing.T) {
	legacy := Certification{Answered: 100, Errors: 3, Eligible: 100, Target: 0.9}
	fresh := Certification{Answered: 100, Errors: 3, Eligible: 100, Target: 0.9}
	fresh.finish(0.9, 1.96)
	if fresh.Precision == 0 {
		t.Fatal("a freshly certified report carries no precision")
	}
	if got, want := legacy.AnsweredPrecision(), fresh.Precision; got != want {
		t.Fatalf("legacy precision %v, want the freshly certified %v", got, want)
	}
	if got, want := legacy.AnsweredPrecision(), 0.97; got != want {
		t.Fatalf("legacy precision %v, want %v", got, want)
	}

	// Nothing answered: there is no precision to report.
	if got := (Certification{Answered: 0}).AnsweredPrecision(); got != 0 {
		t.Fatalf("unanswered precision %v", got)
	}
	// A stored value wins: the accessor never recomputes over a report that
	// carries one (the billed precision can differ from the raw ratio).
	if got := (Certification{Answered: 10, Errors: 5, Precision: 0.9}).AnsweredPrecision(); got != 0.9 {
		t.Fatalf("stored precision %v, want 0.9", got)
	}
}
