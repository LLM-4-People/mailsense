package selective

import "testing"

// TestPrecisionComesFromTheCounts: finish and AnsweredPrecision derive the one
// ratio from the counts. A second finish must recompute instead of returning the
// previous precision.
func TestPrecisionComesFromTheCounts(t *testing.T) {
	c := &Certification{Answered: 10, Errors: 2}
	c.finish(0.8, 1.96)
	if c.Precision != 0.8 {
		t.Fatalf("precision = %v, want 0.8", c.Precision)
	}
	c.Answered, c.Errors = 4, 1
	c.finish(0.6, 1.96)
	if c.Precision != 0.75 {
		t.Fatalf("precision after a second finish = %v, want 0.75", c.Precision)
	}
	if got := answeredPrecision(0, 0); got != 0 {
		t.Fatalf("answeredPrecision(0, 0) = %v, want 0", got)
	}
	if got := (Certification{Precision: 0.9, Answered: 3, Errors: 1}).AnsweredPrecision(); got != 0.9 {
		t.Fatalf("a stored precision was not returned: %v", got)
	}
}
