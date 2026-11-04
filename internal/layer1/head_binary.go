package layer1

import "math"

// logistic is the head of a yes/no question: one output, the
// log-odds of yes (class 1).
type logistic struct{}

func (logistic) kind() Kind   { return Binary }
func (logistic) classes() int { return 2 }
func (logistic) outputs() int { return 1 }
func (logistic) extra() int   { return 0 }
func (logistic) init(prior []float64, bias, _ []float64) {
	p := math.Min(math.Max(prior[1], 1e-6), 1-1e-6)
	bias[0] = math.Log(p / (1 - p))
}

func (logistic) loss(z, _ []float64, t Target, dz, _ []float64, _ float64) float64 {
	s := sigmoid(z[0])
	var l, g float64
	for _, p := range t {
		switch {
		case p.W == 0 || (p.Lo == 0 && p.Hi == 1): // both classes: certain, no information
		case p.Lo == 1: // yes
			l -= p.W * logSigmoid(z[0])
			g += p.W * (s - 1)
		default: // no
			l -= p.W * logSigmoid(-z[0])
			g += p.W * s
		}
	}
	dz[0] = g
	return l
}

func (logistic) logProbs(z, _ []float64, out []float64) {
	out[0], out[1] = math.Max(logSigmoid(-z[0]), minLogP), math.Max(logSigmoid(z[0]), minLogP)
}
