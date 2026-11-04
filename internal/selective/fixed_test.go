package selective

import (
	"math"
	"testing"
)

// wrong flips n copies of rows (ids from base): the same count, the other
// labelling.
func wrong(base int64, n int, conf float64) []Ranked {
	out := perfect(base, n, conf)
	for i := range out {
		out[i].Correct = false
	}
	return out
}

// TestFixedCertificationNeverUsesItsOwnLabelsToChoose: a fixed decision
// rule brings its cut with it, so CertifyFixed has nothing to choose and
// cannot scan the rows it tests. The threshold and the selection count
// stay 0 whatever the rows say; only the counts, the bound and the
// verdict move. Its counts are the rule's: every row is an answer, and
// the coverage denominator is the caller's eligible population.
func TestFixedCertificationNeverUsesItsOwnLabelsToChoose(t *testing.T) {
	const z = 1.96
	needed := Needed(0.99, z)
	good := perfect(0, needed, 0.9)
	bad := wrong(1000, needed, 0.9)
	oneShort := perfect(2000, needed-1, 0.9)

	tests := []struct {
		name      string
		cert      []Ranked
		eligible  int
		target    float64
		certified bool
		errors    int
		eligibleN int
		coverage  float64
	}{
		{"needed perfect answers certify", good, needed, 0.99, true, 0, needed, 1},
		{"one answer short does not", oneShort, needed, 0.99, false, 0, needed, 0},
		{"every row wrong never certifies", bad, needed, 0.99, false, needed, needed, 0},
		{"unanswered eligible labels lower the coverage", good, 2 * needed, 0.99, true, 0, 2 * needed, 0.5},
		{"fewer eligible than rows is raised to the rows", good, 3, 0.99, true, 0, needed, 1},
		{"no rows, eligible labels, no certification", nil, 100, 0.4, false, 0, 100, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := CertifyFixed(tc.cert, tc.eligible, tc.target, z)
			if c.Threshold != 0 || c.Selected != 0 {
				t.Fatalf("a fixed rule chose a cut or a selection row: %+v", c)
			}
			if c.N != len(tc.cert) || c.Answered != len(tc.cert) {
				t.Fatalf("N %d, Answered %d for %d rows", c.N, c.Answered, len(tc.cert))
			}
			if c.Eligible != tc.eligibleN || c.Errors != tc.errors {
				t.Fatalf("eligible %d errors %d, want %d and %d", c.Eligible, c.Errors, tc.eligibleN, tc.errors)
			}
			if c.Certified != tc.certified {
				t.Fatalf("certified %v, want %v: %+v", c.Certified, tc.certified, c)
			}
			if math.Abs(c.Coverage-tc.coverage) > 1e-12 {
				t.Fatalf("coverage %v, want %v", c.Coverage, tc.coverage)
			}
			wantCertified := c.Answered > 0 && c.Lower >= tc.target
			if c.Certified != wantCertified {
				t.Fatalf("certified %v does not follow the bound %v: %+v", c.Certified, c.Lower, c)
			}
		})
	}

	// The two labellings have the same shape; only the tested counts and
	// the verdict differ. Nothing the rule chose can move with them.
	a, b := CertifyFixed(good, 2*needed, 0.99, z), CertifyFixed(bad, 2*needed, 0.99, z)
	if a.Threshold != b.Threshold || a.Selected != b.Selected || a.Target != b.Target ||
		a.N != b.N || a.Answered != b.Answered || a.Eligible != b.Eligible {
		t.Fatalf("the labels moved a chosen field:\n%+v\n%+v", a, b)
	}
	if a.Lower <= b.Lower || a.Certified == b.Certified || a.Errors == b.Errors {
		t.Fatalf("the labels did not move only the test:\n%+v\n%+v", a, b)
	}
}
