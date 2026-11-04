package layer1

import "math"

// ordinal is the cumulative-logit head of a score question (proportional
// odds, McCullagh 1980): one output s = wᵀx and K−1 cut points
// c₀ < c₁ < … < c_{K−2}, P(y ≤ k) = σ(c_k − s). The cut points are kept
// ordered by construction: c_k = θ₀ + Σ_{j=1..k} exp(δ_j); extra holds
// [θ₀, δ₁, …, δ_{K−2}].
type ordinal struct{ k int }

func (o ordinal) kind() Kind   { return Ordinal }
func (o ordinal) classes() int { return o.k }
func (o ordinal) outputs() int { return 1 }
func (o ordinal) extra() int   { return o.k - 1 }

// init places the cut points at the logits of the prior's cumulative
// distribution (gaps of at least minGap).
func (o ordinal) init(prior []float64, bias, extra []float64) {
	bias[0] = 0 // s carries no intercept: θ₀ is it
	var cum float64
	prev := 0.0
	for k := 0; k < o.k-1; k++ {
		cum += prior[k]
		p := math.Min(math.Max(cum, 1e-4), 1-1e-4)
		c := math.Log(p / (1 - p))
		if k == 0 {
			extra[0] = c
		} else {
			extra[k] = math.Log(math.Max(c-prev, minGap))
			c = prev + math.Exp(extra[k])
		}
		prev = c
	}
}

// minGap is the smallest initial distance between two cut points.
const minGap = 0.05

// cuts writes the cut points.
func (o ordinal) cuts(extra, c []float64) {
	c[0] = extra[0]
	for k := 1; k < o.k-1; k++ {
		c[k] = c[k-1] + math.Exp(extra[k])
	}
}

// interval is log P(lo ≤ y ≤ hi) and its derivatives with respect to
// a = c_hi − s and b = c_{lo−1} − s (∞ and −∞ at the ends).
func interval(a, b float64) (logp, da, db float64) {
	switch {
	case math.IsInf(a, 1) && math.IsInf(b, -1):
		return 0, 0, 0
	case math.IsInf(b, -1): // P = σ(a)
		return logSigmoid(a), sigmoid(-a), 0
	case math.IsInf(a, 1): // P = 1 − σ(b) = σ(−b)
		return logSigmoid(-b), 0, -sigmoid(b)
	}
	// P = σ(a) − σ(b) = σ(a)·σ(−b)·(1 − e^{b−a}), a > b
	gap := math.Max(a-b, 1e-12)
	logp = logSigmoid(a) + logSigmoid(-b) + math.Log(-math.Expm1(-gap))
	// d log P/da = σ(a)σ(−a)/P, d log P/db = −σ(b)σ(−b)/P
	da = math.Exp(logSigmoid(a) + logSigmoid(-a) - logp)
	db = -math.Exp(logSigmoid(b) + logSigmoid(-b) - logp)
	return logp, da, db
}

func (o ordinal) loss(z, extra []float64, t Target, dz, dextra []float64, w float64) float64 {
	var cbuf [16]float64
	c := cbuf[:o.k-1]
	if o.k-1 > len(cbuf) {
		c = make([]float64, o.k-1)
	}
	o.cuts(extra, c)
	var dcbuf [16]float64
	dc := dcbuf[:o.k-1]
	if o.k-1 > len(dcbuf) {
		dc = make([]float64, o.k-1)
	}
	s := z[0]
	var l, ds float64
	for _, p := range t {
		if p.W == 0 {
			continue
		}
		a, b := math.Inf(1), math.Inf(-1)
		if p.Hi < o.k-1 {
			a = c[p.Hi] - s
		}
		if p.Lo > 0 {
			b = c[p.Lo-1] - s
		}
		lp, da, db := interval(a, b)
		l -= p.W * math.Max(lp, minLogP)
		ds += p.W * (da + db) // ∂(−log P)/∂s = da + db (a and b fall as s rises)
		if p.Hi < o.k-1 {
			dc[p.Hi] -= p.W * da
		}
		if p.Lo > 0 {
			dc[p.Lo-1] -= p.W * db
		}
	}
	dz[0] = ds
	// chain to θ₀ and δ: ∂c_k/∂θ₀ = 1, ∂c_k/∂δ_j = e^{δ_j} for j ≤ k
	var tail float64
	for k := o.k - 2; k >= 1; k-- {
		tail += dc[k]
		dextra[k] += w * tail * math.Exp(extra[k])
	}
	tail += dc[0]
	dextra[0] += w * tail
	return l
}

func (o ordinal) logProbs(z, extra []float64, out []float64) {
	c := make([]float64, o.k-1)
	o.cuts(extra, c)
	for k := range o.k {
		a, b := math.Inf(1), math.Inf(-1)
		if k < o.k-1 {
			a = c[k] - z[0]
		}
		if k > 0 {
			b = c[k-1] - z[0]
		}
		lp, _, _ := interval(a, b)
		out[k] = math.Max(lp, minLogP)
	}
}
