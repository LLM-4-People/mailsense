package layer1

import (
	"math"
	"testing"
)

// A λ step with no scored selection label has an infinite NLL. It is a
// diagnostic, and an infinite float cannot be stored as JSON: recording it
// failed a whole run's commit (reproduced on a single-class question). The
// path omits it and the λ choice falls back to the default.
func TestPathNLLOmitsUnscoredLambdas(t *testing.T) {
	t.Parallel()
	p := &lambdaPath{rep: &QuestionReport{}}
	p.record(0.01, math.Inf(1))
	p.record(0.02, math.NaN())
	if len(p.rep.PathNLL) != 0 {
		t.Fatalf("PathNLL = %v, want no unmeasurable step", p.rep.PathNLL)
	}
	p.record(0.1, 0.5)
	if got := p.rep.PathNLL["0.1"]; got != 0.5 {
		t.Fatalf("PathNLL[0.1] = %v, want 0.5", got)
	}
}
