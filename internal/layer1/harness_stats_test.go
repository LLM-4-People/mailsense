package layer1

// Paired comparison of two harness runs (used by the fixture harness;
// untagged so its test runs with every go test).

import (
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
)

// itemResults is the per-item result of a run (private: kept under the
// cache directory for paired comparisons, never in out/).
type itemResults map[string]map[string]itemResult // question → message row → result

// itemResult is one dev item's layer-1 answer: right or not, and the
// stack's confidence (a layer-0 key above 1; −1: no answer).
type itemResult struct {
	Correct bool    `json:"c"`
	Conf    float64 `json:"p"`
}

// meanAccuracyOf and coverageOf are the per-question statistics compared.
func accuracyOf(rows []itemResult) float64 {
	n := 0
	for _, r := range rows {
		if r.Correct {
			n++
		}
	}
	return float64(n) / float64(max(len(rows), 1))
}

// coverageAt is the dev-curve coverage at a precision target.
func coverageAt(target float64) func([]itemResult) float64 {
	return func(rows []itemResult) float64 {
		var rk []selective.Ranked
		for i, r := range rows {
			if r.Conf >= 0 {
				rk = append(rk, selective.Ranked{ID: int64(i), Correct: r.Correct, Confidence: r.Conf})
			}
		}
		if len(rk) == 0 {
			return 0
		}
		return float64(selective.Targets(selective.Rank(rk), []float64{target}, 1.96)[0].Covered) / float64(len(rows))
	}
}

// pairedBootstrap is the mean over questions of stat(b) − stat(a) on the
// items both runs hold, with a 95 % percentile interval over resampled
// dev items (seeded; the items are the sorted union over every question,
// so the result never depends on map order: bugs review #12).
func pairedBootstrap(a, b itemResults, rounds int, stat func([]itemResult) float64) (diff, lo, hi float64) {
	seen := map[string]bool{}
	for _, r := range []itemResults{a, b} {
		for _, byItem := range r {
			for id := range byItem {
				seen[id] = true
			}
		}
	}
	items := slices.Sorted(maps.Keys(seen))
	var qs []string
	for q := range a {
		if b[q] != nil {
			qs = append(qs, q)
		}
	}
	slices.Sort(qs)
	mean := func(sample []string) float64 {
		var s float64
		for _, q := range qs {
			var ra, rb []itemResult
			for _, id := range sample {
				va, oka := a[q][id]
				vb, okb := b[q][id]
				if oka && okb {
					ra, rb = append(ra, va), append(rb, vb)
				}
			}
			if len(ra) > 0 {
				s += stat(rb) - stat(ra)
			}
		}
		return s / float64(max(len(qs), 1))
	}
	diff = mean(items)
	r := rand.New(rand.NewPCG(20261015, 1))
	var ds []float64
	sample := make([]string, len(items))
	for range rounds {
		for i := range sample {
			sample[i] = items[r.IntN(len(items))]
		}
		ds = append(ds, mean(sample))
	}
	slices.Sort(ds)
	return diff, ds[int(math.Floor(0.025*float64(rounds)))], ds[int(math.Floor(0.975*float64(rounds)))-1]
}

func TestPairedBootstrap(t *testing.T) {
	t.Parallel()
	a, b := itemResults{"q1": {}, "q2": {}}, itemResults{"q1": {}, "q2": {}}
	for i := range 200 {
		id := strconv.Itoa(i)
		conf := 1 - float64(i)/400
		a["q1"][id], b["q1"][id] = itemResult{i%2 == 0, conf}, itemResult{i%2 == 0 || i%5 == 1, conf} // b better on q1 by 10 pt
		a["q2"][id], b["q2"][id] = itemResult{i%3 == 0, conf}, itemResult{i%3 == 0, conf}             // equal
	}
	d, lo, hi := pairedBootstrap(a, b, 1000, accuracyOf)
	if math.Abs(d-0.05) > 1e-9 || !(lo > 0) || !(hi > d) {
		t.Fatalf("diff %v [%v, %v]", d, lo, hi)
	}
	d, lo, hi = pairedBootstrap(a, a, 200, accuracyOf)
	if d != 0 || lo != 0 || hi != 0 {
		t.Fatalf("a run against itself: %v [%v, %v]", d, lo, hi)
	}
	// the most confident item right, the next wrong: coverage at 1.0 is 1/n
	rows := []itemResult{{true, 0.9}, {false, 0.8}, {true, 0.7}, {true, -1}}
	if c := coverageAt(1)(rows); c != 0.25 {
		t.Fatalf("coverage %v", c)
	}
}

// Finding (bugs review #12): the items came from a random question's map
// key, so when questions hold different dev items 30 calls gave 2
// different results.
func TestPairedBootstrapIsDeterministic(t *testing.T) {
	t.Parallel()
	a, b := itemResults{}, itemResults{}
	for q := range 6 {
		name := "q" + strconv.Itoa(q)
		a[name], b[name] = map[string]itemResult{}, map[string]itemResult{}
		for i := range 40 + 10*q { // every question holds a different set
			id := strconv.Itoa(i * (q + 1))
			a[name][id], b[name][id] = itemResult{i%3 == 0, 0.9}, itemResult{i%2 == 0, 0.9}
		}
	}
	_, lo0, hi0 := pairedBootstrap(a, b, 200, accuracyOf)
	for range 30 {
		if _, lo, hi := pairedBootstrap(a, b, 200, accuracyOf); lo != lo0 || hi != hi0 {
			t.Fatalf("[%v, %v] then [%v, %v]", lo0, hi0, lo, hi)
		}
	}
}
