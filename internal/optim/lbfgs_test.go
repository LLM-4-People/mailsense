package optim

import (
	"context"
	"errors"
	"math"
	"testing"
)

var opts = Options{Memory: 10, MaxIterations: 500, Tolerance: 1e-10}

// quadratic is ½ Σ aᵢ(xᵢ − cᵢ)²: minimum at c.
func quadratic(a, c []float64) Func {
	return func(x, g []float64) float64 {
		var f float64
		for i := range x {
			d := x[i] - c[i]
			f += 0.5 * a[i] * d * d
			g[i] = a[i] * d
		}
		return f
	}
}

func rosenbrock(x, g []float64) float64 {
	a, b := 1-x[0], x[1]-x[0]*x[0]
	g[0] = -2*a - 400*x[0]*b
	g[1] = 200 * b
	return a*a + 100*b*b
}

func TestQuadraticExactMinimum(t *testing.T) {
	a, c := []float64{1, 10, 100, 1000}, []float64{3, -2, 0.5, 7}
	x0 := []float64{0, 0, 0, 0}
	res, err := Minimize(t.Context(), quadratic(a, c), x0, opts)
	if err != nil || !res.Converged {
		t.Fatalf("%+v %v", res, err)
	}
	for i := range c {
		if math.Abs(res.X[i]-c[i]) > 1e-8 {
			t.Errorf("x[%d] = %v, want %v", i, res.X[i], c[i])
		}
	}
	if x0[0] != 0 {
		t.Error("x0 modified")
	}
}

func TestRosenbrock(t *testing.T) {
	res, err := Minimize(t.Context(), rosenbrock, []float64{-1.2, 1}, opts)
	if err != nil || !res.Converged || math.Abs(res.X[0]-1) > 1e-6 || math.Abs(res.X[1]-1) > 1e-6 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDeterministic(t *testing.T) {
	a, _ := Minimize(t.Context(), rosenbrock, []float64{-1.2, 1}, opts)
	b, _ := Minimize(t.Context(), rosenbrock, []float64{-1.2, 1}, opts)
	if a.X[0] != b.X[0] || a.X[1] != b.X[1] || a.Iterations != b.Iterations {
		t.Fatal("two runs differ")
	}
}

func TestNonFiniteObjective(t *testing.T) {
	nan := func(x, g []float64) float64 { g[0] = 0; return math.NaN() }
	if _, err := Minimize(t.Context(), nan, []float64{1}, opts); !errors.Is(err, ErrNotFinite) {
		t.Fatalf("NaN start: %v", err)
	}
	infGrad := func(x, g []float64) float64 { g[0] = math.Inf(1); return 1 }
	if _, err := Minimize(t.Context(), infGrad, []float64{1}, opts); !errors.Is(err, ErrNotFinite) {
		t.Fatalf("infinite gradient: %v", err)
	}
	// A function that is NaN away from the start: the line search rejects
	// the NaN points and the minimiser stops without accepting one.
	cliff := func(x, g []float64) float64 {
		if x[0] < 0.9 {
			g[0] = math.NaN()
			return math.NaN()
		}
		g[0] = 1
		return x[0]
	}
	res, err := Minimize(t.Context(), cliff, []float64{1}, opts)
	if err != nil || res.X[0] < 0.9 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCancelReturnsTheLastPoint(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	f := func(x, g []float64) float64 {
		calls++
		if calls == 5 {
			cancel()
		}
		return rosenbrock(x, g)
	}
	res, err := Minimize(ctx, f, []float64{-1.2, 1}, opts)
	if !errors.Is(err, ErrCanceled) || res.X == nil || res.Iterations == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	g := make([]float64, 2)
	if got := rosenbrock(res.X, g); got != res.F {
		t.Errorf("returned F %v is not f(X) %v", res.F, got)
	}
}

func TestMaxIterations(t *testing.T) {
	o := opts
	o.MaxIterations = 3
	res, err := Minimize(t.Context(), rosenbrock, []float64{-1.2, 1}, o)
	if err != nil || res.Converged || res.Iterations != 3 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestOptionsMisuse(t *testing.T) {
	for _, o := range []Options{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {1, 1, math.NaN()}} {
		if _, err := Minimize(t.Context(), rosenbrock, []float64{0, 0}, o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestNonPositiveCurvatureIsSkipped(t *testing.T) {
	m := newMemory(3, 1)
	m.add([]float64{0}, []float64{1}, []float64{1}, []float64{0.5}, 1) // sᵀy = -0.5
	if m.len() != 0 {
		t.Fatal("a pair with negative curvature was kept")
	}
	m.add([]float64{0}, []float64{1}, []float64{1}, []float64{2}, 1)
	if m.len() != 1 {
		t.Fatal("a pair with positive curvature was dropped")
	}
	for range 5 {
		m.add([]float64{0}, []float64{1}, []float64{1}, []float64{2}, 1)
	}
	if m.len() != 3 {
		t.Fatalf("memory holds %d pairs, cap 3", m.len())
	}
}

// A non-convex function with a negative-curvature region still finds a
// local minimum (the skipped pairs keep the direction a descent one).
func TestNonConvex(t *testing.T) {
	f := func(x, g []float64) float64 { // x⁴ − 3x² + x: local minima near ±1.2
		g[0] = 4*x[0]*x[0]*x[0] - 6*x[0] + 1
		return x[0]*x[0]*x[0]*x[0] - 3*x[0]*x[0] + x[0]
	}
	o := opts
	o.Tolerance = 1e-7
	res, err := Minimize(t.Context(), f, []float64{0.1}, o)
	g := []float64{0}
	f(res.X, g)
	if err != nil || !res.Converged || math.Abs(g[0]) > 1e-6 || 12*res.X[0]*res.X[0]-6 <= 0 {
		t.Fatalf("%+v %v grad %v", res, err, g[0])
	}
}
