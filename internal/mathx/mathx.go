// Package mathx holds the one rounding rule (half-up, PORTING §9–10) and
// the small statistics every analysis shares: the one median (the
// average of the two middle values for an even count), the one rank rule
// of percentiles, mean, population standard deviation, Pearson r, and the
// one exponential backoff. Pure functions.
package mathx

import (
	"math"
	"slices"
	"time"
)

// IsFinite reports a value that is neither NaN nor infinite. It is the one
// finiteness predicate: JSON decoding, vector validation and the optimizer all
// read it instead of spelling the two math calls.
func IsFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Round rounds v half-up (away from zero on .5) to places decimals: the
// one rounding rule of the codebase (never banker's rounding, never int()
// truncation).
func Round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	// The epsilon absorbs binary representation error (0.285 is 0.28499…).
	return math.Round(v*p+math.Copysign(1e-9, v)) / p
}

// Clamp is the one bounded-limit rule of the request surfaces: a value below
// its valid floor (1) is unset or invalid and falls back to the default, and a
// value above the maximum is capped at it. Callers keep their own default and
// maximum; the shape is what they share.
func Clamp(v, fallback, max int) int {
	if v < 1 {
		return fallback
	}
	return min(v, max)
}

// RoundInt rounds v half-up to an integer.
func RoundInt(v float64) int { return int(Round(v, 0)) }

// Median of values (nil/empty: 0, false).
func Median(values []float64) (float64, bool) {
	n := len(values)
	if n == 0 {
		return 0, false
	}
	s := slices.Sorted(slices.Values(values))
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}

// Mean of values.
func Mean(values []float64) (float64, bool) {
	if len(values) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values)), true
}

// Rank is the index of quantile q among n sorted values: q·(n−1)
// rounded half to even — exactly the legacy p90 rule (Python's
// round(q·(n−1)), history.py _percentile_minutes). It is a rank
// selection, not a value: the half-up value rule (Round) does not apply,
// and half-up would pick the next value whenever q·(n−1) ends in .5
// (for p90: n ≡ 6 mod 10).
func Rank(n int, q float64) int {
	return min(n-1, max(0, int(math.RoundToEven(q*float64(n-1)))))
}

// Percentile is the value at Rank(n, q) of the sorted values (for the
// middle, use Median: it averages the two middle values).
func Percentile(values []float64, q float64) (float64, bool) {
	n := len(values)
	if n == 0 {
		return 0, false
	}
	return slices.Sorted(slices.Values(values))[Rank(n, q)], true
}

// Backoff is min(limit, base·2^attempt) (attempt 0 = base), without
// overflow: the one exponential backoff (HTTP retries, runtime restarts).
func Backoff(base, limit time.Duration, attempt int) time.Duration {
	if attempt < 62 {
		if exp := base << attempt; exp > 0 && exp>>attempt == base && exp < limit {
			return exp
		}
	}
	return limit
}

// PStdev is the population standard deviation.
func PStdev(values []float64) float64 {
	m, ok := Mean(values)
	if !ok {
		return 0
	}
	ss := 0.0
	for _, v := range values {
		ss += (v - m) * (v - m)
	}
	return math.Sqrt(ss / float64(len(values)))
}

// Pearson returns the correlation of x and y; ok is false when the lengths
// differ, fewer than two pairs exist or either side has no variance.
func Pearson(x, y []float64) (float64, bool) {
	if len(x) != len(y) || len(x) < 2 {
		return 0, false
	}
	mx, _ := Mean(x)
	my, _ := Mean(y)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// Ratio is num/den, 0 when den is 0.
func Ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}
