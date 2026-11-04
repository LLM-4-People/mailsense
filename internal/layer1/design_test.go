package layer1

import "testing"

// The packed rows and the binary-search Row depend on one invariant: the
// builder receives message ids in ascending order. Both violations fail at
// the builder, before a partial design can be used.
func TestDesignRowsMustAscend(t *testing.T) {
	t.Parallel()
	b := NewDesign(0, 1)
	if err := b.Add(2, nil, []int32{0}); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(1, nil, []int32{0}); err == nil {
		t.Fatal("a descending message id was accepted")
	}
	if err := b.Add(2, nil, []int32{0}); err == nil {
		t.Fatal("a duplicate message id was accepted")
	}
	if err := b.Add(3, nil, []int32{0}); err != nil {
		t.Fatalf("a valid row after a rejected one: %v", err)
	}
	d, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{2, 3} {
		if row, ok := d.Row(id); !ok || row != i {
			t.Fatalf("message %d looked up as %d/%v, want %d", id, row, ok, i)
		}
	}
}
