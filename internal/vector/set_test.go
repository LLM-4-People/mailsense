package vector

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
)

func randomSet(n, dims int) *Set {
	r := rand.New(rand.NewPCG(7, 9))
	v := &Set{}
	for i := range n {
		x := make([]float32, dims)
		for j := range x {
			x[j] = float32(r.NormFloat64())
		}
		u, _ := Truncate(x, dims)
		q := Quantize(u)
		v.Put(int64(i), q.Values, q.Scale)
	}
	return v
}

func TestTopMatchesAFullSort(t *testing.T) {
	v := randomSet(10000, 64)
	q := make([]float32, 64)
	q[3] = 1
	got := v.Top(q, nil, 25)
	var all []Scored
	for i := range v.ids {
		all = append(all, Scored{v.ids[i], DotInt8(v.values[i*64:(i+1)*64], v.scales[i], q)})
	}
	ByScore(all)
	if fmt.Sprint(got) != fmt.Sprint(all[:25]) {
		t.Fatal("the parallel top-k equals a full sort")
	}
	if got := v.Top(q, []int64{5, 7, 999999}, 25); len(got) != 2 {
		t.Fatalf("only allowed ids (an unknown one ignored): %v", got)
	}
	if got := v.Top(q, []int64{}, 25); len(got) != 0 {
		t.Fatalf("an empty allowed set finds nothing: %v", got)
	}
	if v.Top(make([]float32, 3), nil, 5) != nil {
		t.Fatal("a query of another length finds nothing")
	}
	even := v.TopFunc(q, 25, func(id int64) bool { return id%2 == 0 })
	for _, s := range even {
		if s.ID%2 != 0 {
			t.Fatalf("TopFunc kept %d", s.ID)
		}
	}
	if len(even) != 25 {
		t.Fatalf("TopFunc found %d", len(even))
	}
}

func TestSetRowsAndMisuse(t *testing.T) {
	v := &Set{}
	v.Put(1, []int8{1, 2, 3}, 0.5)
	v.Put(2, []int8{1, 2}, 0.5) // another length: ignored
	v.Put(1, []int8{3, 2, 1}, 0.25)
	if v.Len() != 1 || !v.Has(1) || v.Has(2) || v.Dims() != 3 {
		t.Fatalf("len %d dims %d", v.Len(), v.Dims())
	}
	r := v.Row(1)
	if r == nil || r.Values[0] != 3 || r.Scale != 0.25 || v.Row(9) != nil {
		t.Fatalf("row %+v", r)
	}
	r.Values[0] = 99
	if v.Row(1).Values[0] != 3 {
		t.Fatal("Row is not a copy")
	}
	if v.Count(map[int64]bool{1: true, 5: true}) != 1 {
		t.Fatal("count")
	}
}

type fakeSource struct {
	rows []struct {
		id, seq int64
		v       []byte
	}
}

func (f *fakeSource) EachVector(_ context.Context, _ string, after int64, fn func(int64, int64, []byte, float32) error) error {
	for _, r := range f.rows {
		if r.seq > after {
			if err := fn(r.id, r.seq, r.v, 1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *fakeSource) CountVectors(context.Context, string) (int, error) {
	ids := map[int64]bool{}
	for _, r := range f.rows {
		ids[r.id] = true
	}
	return len(ids), nil
}

func TestRefreshMergesThenReloadsAfterRemovals(t *testing.T) {
	src := &fakeSource{}
	add := func(id, seq int64) {
		src.rows = append(src.rows, struct {
			id, seq int64
			v       []byte
		}{id, seq, []byte{1, 2}})
	}
	add(1, 1)
	add(2, 2)
	v := &Set{}
	if err := v.Refresh(t.Context(), src, "s", 2); err != nil || v.Len() != 2 || v.Seq() != 2 {
		t.Fatalf("%v %d", err, v.Len())
	}
	add(3, 3)
	if err := v.Refresh(t.Context(), src, "s", 2); err != nil || v.Len() != 3 {
		t.Fatalf("incremental: %v %d", err, v.Len())
	}
	src.rows = src.rows[1:] // message 1 removed
	if err := v.Refresh(t.Context(), src, "s", 2); err != nil || v.Len() != 2 || v.Has(1) {
		t.Fatalf("reload: %v %d", err, v.Len())
	}
	if err := v.Refresh(t.Context(), src, "other", 2); err != nil || v.Space() != "other" {
		t.Fatalf("space change: %v", err)
	}
}

// BenchmarkScan100k is the brute-force latency of a 100k × 768 mailbox
// (docs/EMBEDDINGS.md).
func BenchmarkScan100k(b *testing.B) {
	v := randomSet(100_000, 768)
	q := make([]float32, 768)
	for i := range q {
		q[i] = 1 / 27.7
	}
	b.ResetTimer()
	for range b.N {
		v.Top(q, nil, 200)
	}
}

// Finding (bugs review, plausible): the scan skipped a tie with the k-th
// best, so with rows out of id order the neighbours depended on the
// chunking (the CPU count). Ties now keep the lower id in every chunking.
func TestTiesDoNotDependOnTheChunking(t *testing.T) {
	s := &Set{}
	q := Quantize([]float32{1, 0})
	for _, id := range []int64{9, 7, 5, 3, 8, 1, 6, 2, 4} { // out of id order, all the same vector
		s.Put(id, q.Values, q.Scale)
	}
	query := []float32{1, 0}
	whole := s.scan(query, nil, 3, 0, 9)
	ByScore(whole)
	for _, cut := range []int{1, 2, 4, 5, 8} {
		parts := append(s.scan(query, nil, 3, 0, cut), s.scan(query, nil, 3, cut, 9)...)
		ByScore(parts)
		parts = parts[:3]
		for i := range parts {
			if parts[i].ID != whole[i].ID {
				t.Fatalf("chunk at %d: %v vs %v", cut, parts, whole)
			}
		}
	}
	if whole[0].ID != 1 || whole[1].ID != 2 || whole[2].ID != 3 {
		t.Fatalf("ties keep the lowest ids: %v", whole)
	}
}
