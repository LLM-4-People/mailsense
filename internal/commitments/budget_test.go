package commitments

import "testing"

// The set budget admits a set at every read cap: the reply and decision
// windows can name thousands of items, and the byte bound must not evict a
// set the service would legitimately build.
func TestSnapshotBudgetAdmitsTheReadCaps(t *testing.T) {
	s := snapshot{
		items:  make([]Commitment, replyCandidates),
		counts: map[string]int{},
		loops:  make([]OrganizationOpenLoops, 100),
	}
	for i := range s.items {
		s.items[i].Evidence = []int64{1, 2, 3}
	}
	for i := range s.loops {
		s.loops[i].Items = make([]Commitment, OrganizationItemCap)
		s.loops[i].Kinds = map[string]int{}
	}
	if got := weighSnapshot(s); got > cacheMaxBytes {
		t.Fatalf("a set at the read caps weighs %d bytes, over the %d-byte budget", got, cacheMaxBytes)
	}
}
