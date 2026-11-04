package layer1

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// Folds assigns each item to one of k folds, keeping every group (a
// conversation) in one fold: groups in the order of a digest of their
// key, each to the fold holding the fewest items so far (ties: the lower
// fold). Deterministic: the same groups give the same folds anywhere.
func Folds(groups []string, k int) []int {
	size := map[string]int{}
	for _, g := range groups {
		size[g]++
	}
	keys := make([]string, 0, len(size))
	for g := range size {
		keys = append(keys, g)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := strings.Compare(digest.Hex([]byte(a), 16), digest.Hex([]byte(b), 16)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	load := make([]int, max(k, 1))
	fold := map[string]int{}
	for _, g := range keys {
		best := 0
		for f := range load {
			if load[f] < load[best] {
				best = f
			}
		}
		fold[g] = best
		load[best] += size[g]
	}
	out := make([]int, len(groups))
	for i, g := range groups {
		out[i] = fold[g]
	}
	return out
}
