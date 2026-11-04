package mathx

import (
	"cmp"
	"math"
	"slices"
)

// Allocate shares n among groups in proportion to their sizes by largest
// remainder (Hamilton: floors first, then one more to the largest
// fractional parts, ties to the earlier group). The result sums to
// min(n, sum of sizes); an empty or all-zero sizes list gives zeros.
func Allocate(sizes []int, n int) []int {
	out := make([]int, len(sizes))
	total := 0
	for _, s := range sizes {
		total += max(s, 0)
	}
	n = min(max(n, 0), total)
	if n == 0 {
		return out
	}
	type rem struct {
		i int
		r float64
	}
	rems := make([]rem, len(sizes))
	left := n
	for i, s := range sizes {
		exact := float64(n) * float64(max(s, 0)) / float64(total)
		out[i] = int(math.Floor(exact))
		left -= out[i]
		rems[i] = rem{i, exact - float64(out[i])}
	}
	slices.SortStableFunc(rems, func(a, b rem) int { return cmp.Compare(b.r, a.r) })
	for _, r := range rems[:left] {
		out[r.i]++
	}
	return out
}
