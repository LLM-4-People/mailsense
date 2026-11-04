package evidence

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// templates counts, for every message with a vector of the current space,
// the earlier messages of the same sender (at most the last window ones
// with a vector) whose vectors are near-duplicates of its own (cosine ≥
// min): the sender's template mail. Causal: only earlier messages count.
type templates struct {
	matches map[int64]int // row → near-duplicate earlier messages (rows with a vector only)
}

// templateItem is one message as the template count sees it.
type templateItem struct {
	row    int64
	sender string
	at     time.Time
}

// unitVector is a stored vector with its norm (vector.NormInt8, once).
type unitVector struct {
	values []int8
	norm   float64
}

func loadVectors(ctx context.Context, st *store.Store, space string) (map[int64]unitVector, error) {
	out := map[int64]unitVector{}
	err := st.EachVector(ctx, space, 0, func(id, _ int64, values []byte, scale float32) error {
		q := vector.FromBytes(values, scale)
		out[id] = unitVector{values: q.Values, norm: vector.NormInt8(q.Values)}
		return nil
	})
	return out, err
}

func cosine(a, b unitVector) float64 { return vector.CosineInt8(a.values, b.values, a.norm, b.norm) }

// countLabel names one template group in a worker's panic log.
func countLabel(group []templateItem) string {
	return fmt.Sprintf("templates of a sender (%d message(s))", len(group))
}

// buildTemplates counts the near-duplicates sender by sender, on the engine's
// one worker pool. A cancelled context abandons the rest of the mailbox instead
// of counting every sender first: the template pass covers every message with
// a vector, so it is a shutdown path of its own.
func buildTemplates(ctx context.Context, items []templateItem, vecs map[int64]unitVector, minCos float64, window, workers int) (*templates, error) {
	bySender := map[string][]templateItem{}
	for _, it := range items {
		if _, ok := vecs[it.row]; ok && it.sender != "" && !it.at.IsZero() {
			bySender[it.sender] = append(bySender[it.sender], it)
		}
	}
	groups := make([][]templateItem, 0, len(bySender))
	for _, group := range bySender {
		groups = append(groups, group)
	}
	t := &templates{matches: map[int64]int{}}
	err := mapReduce(ctx, workers, groups, countLabel,
		func(group []templateItem) (map[int64]int, error) {
			return countGroup(group, vecs, minCos, window), nil
		}, func(_ int, matches map[int64]int) {
			for row, n := range matches {
				t.matches[row] = n
			}
		})
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return t, err
}

func countGroup(group []templateItem, vecs map[int64]unitVector, minCos float64, window int) map[int64]int {
	slices.SortFunc(group, func(a, b templateItem) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return cmp.Compare(a.row, b.row)
	})
	out := make(map[int64]int, len(group))
	for i, it := range group {
		last := i - 1 // the latest message strictly before it (the same instant is not earlier)
		for last >= 0 && !group[last].at.Before(it.at) {
			last--
		}
		n := 0
		for j := last; j >= max(0, last-window+1); j-- {
			if cosine(vecs[it.row], vecs[group[j].row]) >= minCos {
				n++
			}
		}
		out[it.row] = n
	}
	return out
}
