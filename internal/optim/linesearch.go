package optim

import "github.com/LLM-4-People/mailsense/internal/mathx"

// search backtracks from step 1 (scaled on the first steepest-descent
// step) until f(x + t·d) ≤ f(x) + c₁·t·∇fᵀd; xn and gn receive the point
// and its gradient. ok is false when d is not a descent direction or no
// halving decreased f enough.
func search(f Func, x []float64, fx float64, g, d, xn, gn []float64) (fn, t float64, evals int, ok bool) {
	slope := dot(g, d)
	if !(slope < 0) {
		return 0, 0, 0, false
	}
	t = 1
	for range maxHalving {
		for i := range x {
			xn[i] = x[i] + t*d[i]
		}
		fn = f(xn, gn)
		evals++
		if mathx.IsFinite(fn) && fn <= fx+armijo*t*slope {
			return fn, t, evals, true
		}
		t *= shrink
	}
	return 0, 0, evals, false
}
