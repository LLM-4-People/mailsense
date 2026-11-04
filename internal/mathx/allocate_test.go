package mathx

import (
	"slices"
	"testing"
)

func TestAllocate(t *testing.T) {
	for _, c := range []struct {
		sizes []int
		n     int
		want  []int
	}{
		{[]int{5, 3, 2}, 5, []int{3, 1, 1}}, // 2.5 1.5 1.0: remainders .5 .5 0 → first two
		{[]int{1, 1, 1}, 2, []int{1, 1, 0}},
		{[]int{10, 0}, 3, []int{3, 0}},
		{[]int{2, 2}, 9, []int{2, 2}}, // never more than there is
		{nil, 3, []int{}},
		{[]int{0, 0}, 3, []int{0, 0}},
		{[]int{4, -1}, 2, []int{2, 0}}, // misuse: a negative size counts as empty
		{[]int{3, 3}, -1, []int{0, 0}},
	} {
		if got := Allocate(c.sizes, c.n); !slices.Equal(got, c.want) {
			t.Errorf("Allocate(%v, %d) = %v, want %v", c.sizes, c.n, got, c.want)
		}
	}
}
