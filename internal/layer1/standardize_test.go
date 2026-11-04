package layer1

import "testing"

// The certification rows must not shape the weights that predict them. Two
// designs share every training row and differ only in a held-out row's
// features, so the same examples must produce exactly the same parameters.
func TestFitStandardisationUsesOnlyItsOwnRows(t *testing.T) {
	t.Parallel()
	build := func(held []int32) *Design {
		b := NewDesign(0, 3)
		for _, row := range []struct {
			id    int64
			cells []int32
		}{{1, []int32{0}}, {2, []int32{1}}, {3, held}} {
			if err := b.Add(row.id, nil, row.cells); err != nil {
				t.Fatal(err)
			}
		}
		d, err := b.Build()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	ex := []Example{
		{Row: 0, Target: OneHot(0), Weight: 1},
		{Row: 1, Target: OneHot(1), Weight: 1},
	}
	h, _ := newHead(Binary, 2)
	pa, _, err := Fit(t.Context(), build([]int32{0}), h, ex, nil, testFit)
	if err != nil {
		t.Fatal(err)
	}
	pb, _, err := Fit(t.Context(), build([]int32{2}), h, ex, nil, testFit)
	if err != nil {
		t.Fatal(err)
	}
	if len(pa.W) != len(pb.W) {
		t.Fatalf("weights differ in shape: %d vs %d", len(pa.W), len(pb.W))
	}
	for i := range pa.W {
		if pa.W[i] != pb.W[i] {
			t.Fatalf("a held-out row moved weight %d: %v vs %v", i, pa.W[i], pb.W[i])
		}
	}
	for i := range pa.B {
		if pa.B[i] != pb.B[i] {
			t.Fatalf("a held-out row moved bias %d: %v vs %v", i, pa.B[i], pb.B[i])
		}
	}
}
