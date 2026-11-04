package layer1

import "math"

// multinomial is the softmax head of a choice question: one output per
// option.
type multinomial struct{ k int }

func (m multinomial) kind() Kind   { return Multinomial }
func (m multinomial) classes() int { return m.k }
func (m multinomial) outputs() int { return m.k }
func (m multinomial) extra() int   { return 0 }
func (multinomial) init(prior []float64, bias, _ []float64) {
	for c, p := range prior {
		bias[c] = math.Log(math.Max(p, 1e-6))
	}
}

func (m multinomial) loss(z, _ []float64, t Target, dz, _ []float64, _ float64) float64 {
	lse := logSumExp(z, 0, m.k-1)
	for c := range m.k {
		dz[c] = math.Exp(z[c] - lse)
	}
	var l float64
	for _, p := range t {
		if p.W == 0 {
			continue
		}
		part := logSumExp(z, p.Lo, p.Hi)
		l -= p.W * math.Max(part-lse, minLogP)
		for c := p.Lo; c <= p.Hi; c++ {
			dz[c] -= p.W * math.Exp(z[c]-part)
		}
	}
	return l
}

func (m multinomial) logProbs(z, _ []float64, out []float64) {
	lse := logSumExp(z, 0, m.k-1)
	for c := range m.k {
		out[c] = math.Max(z[c]-lse, minLogP)
	}
}
