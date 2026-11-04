package threads

import "slices"

// growth is the union-find of group() run in time order: each message
// joins its evidence nodes at its own instant, and the members its
// conversation held at that moment are kept for it. A later message that
// bridges two conversations merges them only from its own instant on:
// the members an earlier message saw never change.
type growth struct {
	*union
	rank    []int         // message index → its position in time order
	members map[int][]int // component root → its members so far, in time order (owned by the root)
	earlier [][]int       // message index → its conversation's members before it (never written again)
	waiting map[int][]int // component root → its inbox messages waiting for the owner's answer, in time order
}

func newGrowth(n int) *growth {
	return &growth{union: newUnion(n), rank: make([]int, n), members: map[int][]int{}, earlier: make([][]int, n), waiting: map[int][]int{}}
}

// add inserts message i (the next one in time order) with its evidence
// keys and records what its conversation held before it.
func (g *growth) add(i, rank int, keys []string) {
	g.rank[i] = rank
	for _, k := range keys {
		g.link(i, g.node(k))
	}
	r := g.find(i)
	list := g.members[r]
	g.earlier[i] = list[:len(list):len(list)] // capped: a later append never shows through this view
	g.members[r] = append(list, i)
}

// link merges the components of a and b: their member lists into a
// fresh list (the old lists stay as the earlier members saw them), their
// waiting lists into one.
func (g *growth) link(a, b int) {
	ra, rb := g.find(a), g.find(b)
	if ra == rb {
		return
	}
	root, gone := min(ra, rb), max(ra, rb)
	g.parent[gone] = root
	for _, set := range []map[int][]int{g.members, g.waiting} {
		if lb := set[gone]; len(lb) > 0 {
			set[root] = g.inOrder(set[root], lb)
		}
		delete(set, gone)
	}
}

// inOrder merges two lists of messages into a fresh one in time order.
func (g *growth) inOrder(a, b []int) []int {
	out := make([]int, 0, len(a)+len(b))
	out = append(append(out, a...), b...)
	slices.SortFunc(out, func(x, y int) int { return g.rank[x] - g.rank[y] })
	return out
}
