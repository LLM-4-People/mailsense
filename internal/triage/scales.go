package triage

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/threads"
)

// scales are the response-time scales states and priors band on: the
// speed buckets (threads.response_buckets) and the urgency-prior bounds,
// merged into one fine scale the causal history indexes once.
type scales struct {
	fine        []float64
	speed       []float64
	speedLabels []string
	urgency     []float64
}

// newScales merges the speed buckets (exclusive bounds, as legacy's
// response buckets) with the urgency-prior bounds, which are inclusive at
// the urgency resolution (legacy rounds the median to 0.1 minute and
// compares with ≤: a bound b holds every median below b + resolution/2).
func newScales(speed []int, urgency []int, resolution float64) scales {
	sp := threads.Floats(speed)
	ur := threads.Floats(urgency)
	for i := range ur {
		ur[i] += resolution / 2
	}
	fine := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(sp), ur...))))
	return scales{fine: fine, speed: sp, speedLabels: threads.BucketLabels(speed), urgency: ur}
}

// coarse maps a fine bucket to its bucket on bounds (a subset of the fine
// bounds): the bucket containing the fine bucket's lower edge.
func (s scales) coarse(fine int, bounds []float64) int {
	if fine <= 0 {
		return 0
	}
	lower := s.fine[fine-1]
	n := 0
	for _, b := range bounds {
		if b <= lower {
			n++
		}
	}
	return n
}
