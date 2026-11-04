package mathx

import "math"

// NormalCDF is the standard normal cumulative distribution.
func NormalCDF(z float64) float64 {
	return 0.5 * math.Erfc(-z/math.Sqrt2)
}

// NormalQuantile returns the z whose standard normal CDF is p. ok is false
// for p outside (0, 1). The bisection is deterministic and accurate to the
// double precision of NormalCDF; it is called once per requirements parse,
// never per answer.
func NormalQuantile(p float64) (float64, bool) {
	if !(p > 0 && p < 1) {
		return 0, false
	}
	lo, hi := -10.0, 10.0
	for range 100 {
		mid := (lo + hi) / 2
		if NormalCDF(mid) < p {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2, true
}
