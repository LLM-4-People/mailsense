package layer1

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/optim"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// numGrad is the central finite difference of f at x.
func numGrad(f func(x []float64) float64, x []float64) []float64 {
	g := make([]float64, len(x))
	for i := range x {
		h := 1e-6 * math.Max(1, math.Abs(x[i]))
		old := x[i]
		x[i] = old + h
		fp := f(x)
		x[i] = old - h
		fm := f(x)
		x[i] = old
		g[i] = (fp - fm) / (2 * h)
	}
	return g
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol*math.Max(1, math.Abs(b)) }

// Every head's dℓ/dz and dℓ/dθ match finite differences on soft, range
// and one-hot targets.
func TestHeadGradientsMatchFiniteDifferences(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	for _, tc := range []struct {
		h       head
		targets []Target
	}{
		{multinomial{k: 4}, []Target{OneHot(2), {{W: 0.2, Lo: 0, Hi: 0}, {W: 0.8, Lo: 3, Hi: 3}}, {{W: 0.6, Lo: 1, Hi: 2}, {W: 0.4, Lo: 0, Hi: 0}}}},
		{logistic{}, []Target{OneHot(1), OneHot(0), {{W: 0.3, Lo: 0, Hi: 0}, {W: 0.7, Lo: 1, Hi: 1}}}},
		{ordinal{k: 5}, []Target{OneHot(0), OneHot(4), OneHot(2), {{W: 0.5, Lo: 1, Hi: 3}, {W: 0.5, Lo: 4, Hi: 4}}, {{W: 0.1, Lo: 0, Hi: 0}, {W: 0.9, Lo: 2, Hi: 2}}}},
	} {
		for _, tg := range tc.targets {
			z := make([]float64, tc.h.outputs())
			extra := make([]float64, tc.h.extra())
			for i := range z {
				z[i] = r.NormFloat64()
			}
			if tc.h.extra() > 0 {
				tc.h.init([]float64{0.1, 0.2, 0.3, 0.2, 0.2}, make([]float64, 1), extra)
			}
			dz, dx := make([]float64, len(z)), make([]float64, len(extra))
			tc.h.loss(z, extra, tg, dz, dx, 1)
			gz := numGrad(func(v []float64) float64 {
				return tc.h.loss(v, extra, tg, make([]float64, len(z)), make([]float64, len(extra)), 1)
			}, z)
			for i := range z {
				if !near(dz[i], gz[i], 1e-5) {
					t.Errorf("%s %v: dz[%d] = %v, numeric %v", tc.h.kind(), tg, i, dz[i], gz[i])
				}
			}
			if len(extra) > 0 {
				gx := numGrad(func(v []float64) float64 {
					return tc.h.loss(z, v, tg, make([]float64, len(z)), make([]float64, len(extra)), 1)
				}, extra)
				for i := range extra {
					if !near(dx[i], gx[i], 1e-5) {
						t.Errorf("%s %v: dθ[%d] = %v, numeric %v", tc.h.kind(), tg, i, dx[i], gx[i])
					}
				}
			}
		}
	}
}

// Probabilities of every head sum to 1 and the ordinal cut points stay
// ordered whatever δ.
func TestLogProbsAreDistributions(t *testing.T) {
	t.Parallel()
	for _, h := range []head{multinomial{k: 3}, logistic{}, ordinal{k: 6}} {
		for _, z0 := range []float64{-40, -1, 0, 3, 40} {
			z := make([]float64, h.outputs())
			for i := range z {
				z[i] = z0 * float64(i+1)
			}
			extra := make([]float64, h.extra())
			for i := range extra {
				extra[i] = -3 + float64(i) // tiny and large gaps
			}
			out := make([]float64, h.classes())
			h.logProbs(z, extra, out)
			var s float64
			for _, l := range out {
				s += math.Exp(l)
			}
			if math.Abs(s-1) > 1e-9 {
				t.Errorf("%s z=%v: probabilities sum to %v", h.kind(), z0, s)
			}
		}
	}
	o := ordinal{k: 5}
	c := make([]float64, 4)
	o.cuts([]float64{2, -50, 0, 50}, c)
	for k := 1; k < 4; k++ {
		if !(c[k] >= c[k-1]) {
			t.Fatalf("cut points out of order: %v", c)
		}
	}
}

// synthetic builds a design whose class depends on the embedding (and
// one evidence cell for class 0).
func synthetic(t *testing.T, n, dims, classes int) (*Design, []Example) {
	t.Helper()
	r := rand.New(rand.NewPCG(3, 4))
	b := NewDesign(dims, 4)
	var ex []Example
	for i := range n {
		c := i % classes
		v := make([]float32, dims)
		for j := range v {
			v[j] = float32(r.NormFloat64() * 0.3)
		}
		v[c] += 1.5
		u, _ := vector.Truncate(v, dims)
		q := vector.Quantize(u)
		cells := []int32{int32(r.IntN(2))}
		if c == 0 {
			cells = append(cells, 3)
		}
		if err := b.Add(int64(i+1), &q, cells); err != nil {
			t.Fatal(err)
		}
		ex = append(ex, Example{Row: i, Target: OneHot(c), Weight: 1})
	}
	d, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return d, ex
}

var testFit = FitOptions{Lambda: 1e-3, Optim: optim.Options{Memory: 10, MaxIterations: 300, Tolerance: 1e-7}, Workers: 3, ShardRows: 64}

// The whole objective's gradient (sharded, folded standardisation, L2)
// matches finite differences.
func TestObjectiveGradientMatchesFiniteDifferences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		k       Kind
		classes int
	}{{Multinomial, 3}, {Binary, 2}, {Ordinal, 4}} {
		d, ex := synthetic(t, 60, 8, tc.classes)
		h, _ := newHead(tc.k, tc.classes)
		ob := newObjective(d, h, ex, testFit, standardizerOf(d, ex))
		r := rand.New(rand.NewPCG(5, 6))
		x := make([]float64, h.outputs()*d.Cols()+h.outputs()+h.extra())
		for i := range x {
			x[i] = r.NormFloat64() * 0.3
		}
		g := make([]float64, len(x))
		ob.eval(x, g)
		num := numGrad(func(v []float64) float64 { return ob.eval(v, make([]float64, len(v))) }, x)
		for i := range x {
			if !near(g[i], num[i], 1e-4) {
				t.Errorf("%s: grad[%d] = %v, numeric %v", tc.k, i, g[i], num[i])
			}
		}
	}
}

func TestFitLearnsAndIsIndependentOfWorkers(t *testing.T) {
	t.Parallel()
	d, ex := synthetic(t, 300, 16, 3)
	h, _ := newHead(Multinomial, 3)
	p1, res, err := Fit(t.Context(), d, h, ex, nil, testFit)
	if err != nil || !res.Converged {
		t.Fatalf("%+v %v", res, err)
	}
	o := testFit
	o.Workers = 1
	p2, _, _ := Fit(t.Context(), d, h, ex, nil, o)
	for i := range p1.W {
		if p1.W[i] != p2.W[i] {
			t.Fatal("the fit depends on the number of workers")
		}
	}
	m := &Model{Kind: Multinomial, Classes: 3, Dims: d.Dims, Cells: d.Cells, Params: p1}
	m.fold(standardizerOf(d, ex))
	correct := 0
	out := make([]float64, 3)
	for _, e := range ex {
		m.logProbsRow(d, e.Row, out)
		if argmax(out) == e.Target.Class(3) {
			correct++
		}
	}
	if correct < 280 {
		t.Fatalf("training accuracy %d/300", correct)
	}
}

// Separable data with λ > 0 keeps finite, bounded weights.
func TestSeparableDataStaysBounded(t *testing.T) {
	t.Parallel()
	d, ex := synthetic(t, 100, 8, 2)
	h, _ := newHead(Binary, 2)
	for i := range ex {
		ex[i].Target = OneHot(ex[i].Row % 2)
	}
	p, _, err := Fit(t.Context(), d, h, ex, nil, testFit)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.W {
		if math.IsNaN(w) || math.Abs(w) > 1e3 {
			t.Fatalf("weight %v", w)
		}
	}
}

func TestFitMisuse(t *testing.T) {
	t.Parallel()
	d, ex := synthetic(t, 30, 4, 3)
	h, _ := newHead(Multinomial, 3)
	one := make([]Example, len(ex))
	for i := range ex {
		one[i] = Example{Row: ex[i].Row, Target: OneHot(1), Weight: 1}
	}
	if _, _, err := Fit(t.Context(), d, h, one, nil, testFit); !errors.Is(err, ErrSingleClass) {
		t.Errorf("single class: %v", err)
	}
	if _, _, err := Fit(t.Context(), d, h, nil, nil, testFit); err == nil {
		t.Error("no examples")
	}
	bad := append([]Example(nil), ex...)
	bad[0].Target = Target{{W: 1, Lo: 0, Hi: 7}}
	if _, _, err := Fit(t.Context(), d, h, bad, nil, testFit); !errors.Is(err, ErrBadTarget) {
		t.Errorf("bad target: %v", err)
	}
	bad[0] = Example{Row: 999, Target: OneHot(0), Weight: 1}
	if _, _, err := Fit(t.Context(), d, h, bad, nil, testFit); err == nil {
		t.Error("row out of range")
	}
	bad[0] = Example{Row: 0, Target: OneHot(0), Weight: 0}
	if _, _, err := Fit(t.Context(), d, h, bad, nil, testFit); err == nil {
		t.Error("zero weight")
	}
	for _, k := range []Kind{"x"} {
		if _, err := newHead(k, 3); err == nil {
			t.Error("unknown kind")
		}
	}
	if _, err := newHead(Binary, 3); err == nil {
		t.Error("binary with 3 classes")
	}
	if _, err := newHead(Multinomial, 1); err == nil {
		t.Error("one class")
	}
}

func TestTargets(t *testing.T) {
	t.Parallel()
	if _, err := Soft([]float64{0.5, 0.6}, 0.02); !errors.Is(err, ErrBadTarget) {
		t.Error("sum off")
	}
	if _, err := Soft([]float64{math.NaN(), 1}, 0.02); err == nil {
		t.Error("NaN")
	}
	if _, err := Soft([]float64{-0.1, 1.1}, 0.5); err == nil {
		t.Error("negative")
	}
	s, err := Soft([]float64{0.25, 0, 0.76}, 0.02)
	if err != nil || len(s) != 2 || s.Check(3) != nil || s.Class(3) != 2 {
		t.Fatalf("%v %v", s, err)
	}
	sharp := s.Sharpen(0.5)
	if !(sharp[1].W > s[1].W) || sharp.Check(3) != nil {
		t.Errorf("sharpen %v", sharp)
	}
	if m := (Target{{W: 1, Lo: 1, Hi: 2}}).Mass(4); m[1] != 0.5 || m[2] != 0.5 {
		t.Errorf("range mass %v", m)
	}
}
