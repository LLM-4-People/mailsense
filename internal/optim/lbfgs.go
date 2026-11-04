// Package optim minimises smooth functions: limited-memory BFGS with an
// Armijo backtracking line search (Nocedal & Wright, Numerical
// Optimization, 2nd ed., 2006: Algorithm 7.4, the two-loop recursion, with
// the initial matrix γI, γ = sᵀy/yᵀy, eq. 7.20; Algorithm 3.1). Pure and
// deterministic: the same function and start give the same steps.
package optim

import (
	"context"
	"errors"
	"fmt"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"math"
)

// Func evaluates the objective at x and writes its gradient into grad
// (len(grad) == len(x)); it must not keep either slice.
type Func func(x, grad []float64) float64

// Options tune Minimize.
type Options struct {
	// Memory is the number of correction pairs kept (m ≥ 1).
	Memory int
	// MaxIterations caps the iterations (≥ 1).
	MaxIterations int
	// Tolerance stops when ‖∇f‖∞ ≤ Tolerance·max(1, |f|) (> 0).
	Tolerance float64
}

// Result is where Minimize stopped.
type Result struct {
	X           []float64
	F           float64
	Iterations  int
	Evaluations int
	// Converged is true when the gradient test held; false after
	// MaxIterations or when no step along the search direction (nor the
	// steepest descent) decreased f any more.
	Converged bool
}

// Errors of Minimize.
var (
	// ErrCanceled: the context ended; Result holds the last accepted x.
	ErrCanceled = errors.New("optim: canceled")
	// ErrNotFinite: f or its gradient was NaN or infinite at an accepted
	// point (the start included).
	ErrNotFinite = errors.New("optim: the objective or its gradient is not finite")
)

// Line-search constants (Nocedal & Wright: c₁ = 1e-4; halving).
const (
	armijo     = 1e-4
	shrink     = 0.5
	maxHalving = 50
	// curvature skips a correction pair whose sᵀy ≤ curvature·‖s‖‖y‖:
	// the inverse Hessian estimate must stay positive definite.
	curvature = 1e-10
)

func (o Options) check() error {
	if o.Memory < 1 || o.MaxIterations < 1 || !(o.Tolerance > 0) {
		return fmt.Errorf("optim: memory %d, max iterations %d and tolerance %g must be positive", o.Memory, o.MaxIterations, o.Tolerance)
	}
	return nil
}

// Minimize runs L-BFGS from x0 (not modified). The context is checked
// every iteration: on cancellation it returns the last accepted point
// with ErrCanceled.
func Minimize(ctx context.Context, f Func, x0 []float64, o Options) (Result, error) {
	if err := o.check(); err != nil {
		return Result{}, err
	}
	n := len(x0)
	x, g := append([]float64(nil), x0...), make([]float64, n)
	fx := f(x, g)
	res := Result{X: x, F: fx, Evaluations: 1}
	if !mathx.IsFinite(fx) || !allFinite(g) {
		return res, ErrNotFinite
	}
	mem := newMemory(o.Memory, n)
	d, xn, gn := make([]float64, n), make([]float64, n), make([]float64, n)
	for res.Iterations < o.MaxIterations {
		if norm(g) <= o.Tolerance*math.Max(1, math.Abs(fx)) {
			res.Converged = true
			break
		}
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("%w: %w", ErrCanceled, err)
		}
		mem.direction(g, d)
		fn, step, evals, ok := search(f, x, fx, g, d, xn, gn)
		res.Evaluations += evals
		if !ok && mem.len() > 0 { // the quasi-Newton direction failed: steepest descent once
			mem.reset()
			mem.direction(g, d)
			fn, step, evals, ok = search(f, x, fx, g, d, xn, gn)
			res.Evaluations += evals
		}
		if !ok {
			break // no decrease along the steepest descent: as far as f can be minimised here
		}
		if !mathx.IsFinite(fn) || !allFinite(gn) {
			return res, ErrNotFinite
		}
		mem.add(x, xn, g, gn, step)
		copy(x, xn)
		copy(g, gn)
		fx = fn
		res.F = fx
		res.Iterations++
	}
	if !res.Converged && norm(g) <= o.Tolerance*math.Max(1, math.Abs(fx)) {
		res.Converged = true
	}
	return res, nil
}

// memory holds the last m correction pairs (s = Δx, y = Δ∇f).
type memory struct {
	m      int
	s, y   [][]float64
	rho    []float64
	alpha  []float64
	first  bool // no pair yet: the first step is scaled by 1/‖g‖
	sx, yx []float64
}

func newMemory(m, n int) *memory {
	return &memory{m: m, first: true, alpha: make([]float64, m), sx: make([]float64, n), yx: make([]float64, n)}
}

func (m *memory) len() int { return len(m.s) }

func (m *memory) reset() { m.s, m.y, m.rho, m.first = nil, nil, nil, true }

// add keeps the pair of a step unless its curvature is not positive.
func (m *memory) add(x, xn, g, gn []float64, step float64) {
	for i := range x {
		m.sx[i], m.yx[i] = xn[i]-x[i], gn[i]-g[i]
	}
	sy := dot(m.sx, m.yx)
	if !(sy > curvature*math.Sqrt(dot(m.sx, m.sx)*dot(m.yx, m.yx))) {
		return // skipped: the estimate stays positive definite
	}
	var s, y []float64
	if len(m.s) == m.m { // reuse the oldest pair's storage
		s, y = m.s[0], m.y[0]
		m.s, m.y, m.rho = m.s[1:], m.y[1:], m.rho[1:]
	} else {
		s, y = make([]float64, len(x)), make([]float64, len(x))
	}
	copy(s, m.sx)
	copy(y, m.yx)
	m.s, m.y, m.rho = append(m.s, s), append(m.y, y), append(m.rho, 1/sy)
	m.first = false
}

// direction writes d = −H∇f (two-loop recursion).
func (m *memory) direction(g, d []float64) {
	for i := range g {
		d[i] = -g[i]
	}
	k := len(m.s)
	if k == 0 {
		if m.first {
			if gn := math.Sqrt(dot(g, g)); gn > 0 {
				scale(d, 1/gn)
			}
		}
		return
	}
	for i := k - 1; i >= 0; i-- {
		m.alpha[i] = m.rho[i] * dot(m.s[i], d)
		axpy(-m.alpha[i], m.y[i], d)
	}
	scale(d, dot(m.s[k-1], m.y[k-1])/dot(m.y[k-1], m.y[k-1]))
	for i := range k {
		b := m.rho[i] * dot(m.y[i], d)
		axpy(m.alpha[i]-b, m.s[i], d)
	}
}

func dot(a, b []float64) float64 {
	var s float64
	for i, x := range a {
		s += x * b[i]
	}
	return s
}

func axpy(a float64, x, y []float64) {
	for i, v := range x {
		y[i] += a * v
	}
}

func scale(x []float64, a float64) {
	for i := range x {
		x[i] *= a
	}
}

// norm is the infinity norm.
func norm(g []float64) float64 {
	var m float64
	for _, x := range g {
		m = math.Max(m, math.Abs(x))
	}
	return m
}

func allFinite(v []float64) bool {
	for _, x := range v {
		if !mathx.IsFinite(x) {
			return false
		}
	}
	return true
}
