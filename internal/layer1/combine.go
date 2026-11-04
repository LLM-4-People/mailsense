package layer1

import (
	"context"
	"errors"
	"math"

	"github.com/LLM-4-People/mailsense/internal/optim"
)

// Combiner pools a head's log-probabilities with the kNN distribution and
// calibrates them: z_c = A·ℓ^lr_c + B·log p^knn_c, p = softmax(z). With
// B = 0 it is temperature scaling with T = 1/A (Guo et al., On
// Calibration of Modern Neural Networks, 2017); A and B are positive
// (softplus of the fitted values). No per-class bias: a few hundred
// labels would overfit it.
type Combiner struct {
	A   float64 `json:"a"`
	B   float64 `json:"b"`
	KNN bool    `json:"knn"`
}

// Identity is the combiner that changes nothing (calibration none).
var Identity = Combiner{A: 1}

// Apply writes the pooled, normalised log-probabilities (knn nil when
// the combiner has no kNN term).
func (c Combiner) Apply(lr, knn, out []float64) {
	for k := range lr {
		out[k] = c.A * lr[k]
		if c.KNN && knn != nil {
			out[k] += c.B * knn[k]
		}
	}
	lse := logSumExp(out, 0, len(out)-1)
	for k := range out {
		out[k] = math.Max(out[k]-lse, minLogP)
	}
}

// combineOptim are the optimiser settings of the 1–2 parameter
// calibration fit: algorithm constants (a smooth, convex-in-practice fit
// of at most two scales converges far inside them), not settings.
var combineOptim = optim.Options{Memory: 5, MaxIterations: 200, Tolerance: 1e-9}

// FitCombiner fits the scales by minimising the NLL of the labels'
// classes over out-of-fold log-probabilities: A (when fitA: temperature
// scaling; else A stays 1, the head's own scale) and B when knn is given
// (the kNN pooling weight). Each fitted scale has a weak log-normal prior
// centred on 1, worth prior labels (the penalty prior·(log A)²/2 added to
// the summed NLL; layer1.calibration.prior_labels): with labels that
// never contradict the head (a one-sided question, perfectly separated
// out of fold) the unpenalised NLL keeps falling as A grows, and an A of
// 77 fitted on 119 such labels made the one dev exception cost 9 nats
// (round 1, e5-nocal); with informative labels the prior barely moves A.
func FitCombiner(ctx context.Context, lr, knn [][]float64, labels []int, fitA bool, prior float64) (Combiner, error) {
	if len(lr) == 0 || len(lr) != len(labels) || (knn != nil && len(knn) != len(lr)) {
		return Combiner{}, errors.New("layer1: the calibration needs one prediction per label")
	}
	withKNN := knn != nil
	if !fitA && !withKNN {
		return Identity, nil
	}
	inv := func(x float64) float64 { return math.Log(math.Expm1(x)) } // softplus⁻¹
	// x holds the fitted scales' softplus⁻¹: [A][, B]
	var x0 []float64
	if fitA {
		x0 = append(x0, inv(1))
	}
	if withKNN {
		x0 = append(x0, inv(0.5))
	}
	scales := func(x []float64) (a, da, b, db float64) {
		a, da = 1, 0
		i := 0
		if fitA {
			a, da = softplus(x[0]), sigmoid(x[0])
			i++
		}
		if withKNN {
			b, db = softplus(x[i]), sigmoid(x[i])
		}
		return a, da, b, db
	}
	buf := make([]float64, len(lr[0]))
	f := func(x, g []float64) float64 {
		a, da, b, db := scales(x)
		var nll, ga, gb float64
		for i, l := range lr {
			var kn []float64
			if withKNN {
				kn = knn[i]
			}
			for k := range l {
				buf[k] = a * l[k]
				if withKNN {
					buf[k] += b * kn[k]
				}
			}
			lse := logSumExp(buf, 0, len(buf)-1)
			y := labels[i]
			nll -= buf[y] - lse
			// ∂(−log p_y)/∂a = Σ_k p_k ℓ_k − ℓ_y (and the same with knn for b)
			var el, ek float64
			for k := range l {
				p := math.Exp(buf[k] - lse)
				el += p * l[k]
				if withKNN {
					ek += p * kn[k]
				}
			}
			ga += el - l[y]
			if withKNN {
				gb += ek - kn[y]
			}
		}
		n := float64(len(lr))
		i := 0
		if fitA {
			la := math.Log(a)
			nll += prior * la * la / 2
			ga += prior * la / a // ∂/∂a of prior·(log a)²/2
			g[0] = ga / n * da
			i++
		}
		if withKNN {
			lb := math.Log(b)
			nll += prior * lb * lb / 2
			gb += prior * lb / b
			g[i] = gb / n * db
		}
		return nll / n
	}
	res, err := optim.Minimize(ctx, f, x0, combineOptim)
	if err != nil {
		return Combiner{}, err
	}
	a, _, b, _ := scales(res.X)
	return Combiner{A: a, B: b, KNN: withKNN}, nil
}
