package goldexport

import (
	"maps"
	"math/rand/v2"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// unknownCategory is the category of a message the legacy file does not
// classify; it is never topped up.
const unknownCategory = "unknown"

// Draw samples the report's strata: the rare floor first (each legacy
// category's own rows, up to the floor), then the rest of the size by
// largest remainder over the strata's remaining rows; every draw seeded.
// It returns the sampled rows, sorted, and fills the report's counts.
func Draw(rep *Report, o Options) []int64 {
	d := drawer{rep: rep, taken: map[int64]bool{}}
	rng := rand.New(rand.NewPCG(o.Seed, o.Seed))
	byCategory := map[string][]int{} // stratum indexes
	for i := range rep.Strata {
		rows := slices.Sorted(slices.Values(rep.Strata[i].rows))
		rng.Shuffle(len(rows), func(a, b int) { rows[a], rows[b] = rows[b], rows[a] })
		rep.Strata[i].rows = rows
		byCategory[rep.Strata[i].Category] = append(byCategory[rep.Strata[i].Category], i)
	}
	for _, cat := range slices.Sorted(maps.Keys(byCategory)) {
		if cat != unknownCategory {
			d.floor(byCategory[cat], o)
		}
	}
	d.proportional(o.Size - len(d.taken))
	rep.Sampled = len(d.taken)
	rep.Categories = map[string][2]int{}
	for _, s := range rep.Strata {
		c := rep.Categories[s.Category]
		rep.Categories[s.Category] = [2]int{c[0] + s.Population, c[1] + s.Sampled}
	}
	return slices.Sorted(maps.Keys(d.taken))
}

type drawer struct {
	rep   *Report
	taken map[int64]bool
}

// next takes the next untaken row of stratum i.
func (d *drawer) next(i int) bool {
	for _, r := range d.rep.Strata[i].rows {
		if !d.taken[r] {
			d.taken[r] = true
			d.rep.Strata[i].Sampled++
			return true
		}
	}
	return false
}

// floor tops a category whose proportional share is below the floor up
// to it, round-robin over its strata.
func (d *drawer) floor(strata []int, o Options) {
	pop := 0
	for _, i := range strata {
		pop += d.rep.Strata[i].Population
	}
	if float64(o.Size)*float64(pop)/float64(d.rep.Eligible) >= float64(o.RareFloor) {
		return
	}
	want := min(o.RareFloor, pop)
	for n := 0; n < want; {
		progressed := false
		for _, i := range strata {
			if n < want && d.next(i) {
				n++
				progressed = true
			}
		}
		if !progressed {
			return
		}
	}
}

// proportional allocates left over the strata's remaining rows.
func (d *drawer) proportional(left int) {
	if left <= 0 {
		return
	}
	remaining := make([]int, len(d.rep.Strata))
	for i, s := range d.rep.Strata {
		remaining[i] = s.Population - s.Sampled
	}
	for i, n := range mathx.Allocate(remaining, left) {
		for range n {
			d.next(i)
		}
	}
}
