package mathx

import "math"

// Wilson is the Wilson score interval of a proportion: successes out of
// n at the normal quantile z (1.96 for 95 %). ok is false for n ≤ 0,
// successes outside 0..n or z ≤ 0.
func Wilson(successes, n int, z float64) (lo, hi float64, ok bool) {
	if n <= 0 || successes < 0 || successes > n || !(z > 0) {
		return 0, 0, false
	}
	p, fn, z2 := float64(successes)/float64(n), float64(n), z*z
	center := (p + z2/(2*fn)) / (1 + z2/fn)
	half := z / (1 + z2/fn) * math.Sqrt(p*(1-p)/fn+z2/(4*fn*fn))
	return max(0, center-half), min(1, center+half), true
}
