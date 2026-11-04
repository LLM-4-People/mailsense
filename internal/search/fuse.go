package search

import (
	"cmp"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/vector"
)

// candidate is a message found by at least one ranking.
type candidate struct {
	id         int64
	score      float64 // reciprocal rank fusion
	textRank   int     // 1-based; 0 = not found by full text
	vectorRank int     // 1-based; 0 = not found by similarity
	similarity float32
}

// fuse merges the rankings with reciprocal rank fusion: a message scores
// Σ 1/(k + rank) over the rankings that found it, so agreement between
// full text and similarity wins and neither score scale matters. Order:
// score, then the better text rank, then id (deterministic).
func fuse(text []int64, similar []vector.Scored, k int) []candidate {
	byID := map[int64]*candidate{}
	get := func(id int64) *candidate {
		c := byID[id]
		if c == nil {
			c = &candidate{id: id}
			byID[id] = c
		}
		return c
	}
	for i, id := range text {
		c := get(id)
		c.textRank = i + 1
		c.score += 1 / float64(k+i+1)
	}
	for i, s := range similar {
		c := get(s.ID)
		c.vectorRank, c.similarity = i+1, s.Sim
		c.score += 1 / float64(k+i+1)
	}
	out := make([]candidate, 0, len(byID))
	for _, c := range byID {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b candidate) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		if c := cmp.Compare(rankOrLast(a.textRank), rankOrLast(b.textRank)); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	return out
}

func rankOrLast(r int) int {
	if r == 0 {
		return int(^uint(0) >> 1)
	}
	return r
}
