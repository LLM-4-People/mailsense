package outlook

import (
	"errors"
	"math"
	"testing"
)

func TestColumnMajorGrid(t *testing.T) {
	// A 3×2 SAFEARRAY as stored: column 0 (rows 0..2), then column 1.
	mem := []any{"r0c0", "r1c0", "r2c0", "r0c1", "r1c1", "r2c1"}
	grid, err := columnMajor(3, 2, func(i int) (any, error) { return mem[i], nil })
	if err != nil || len(grid) != 3 || grid[1][0] != "r1c0" || grid[2][1] != "r2c1" || grid[0][1] != "r0c1" {
		t.Fatalf("%v %v", grid, err)
	}
	if _, err := columnMajor(1, 1, func(int) (any, error) { return nil, errors.New("bad element") }); err == nil {
		t.Fatal("element errors surface")
	}
	if g, _ := columnMajor(0, 5, nil); len(g) != 0 {
		t.Fatal("empty table")
	}
}

func TestReversedAndInt32Args(t *testing.T) {
	if r := reversed([]int{1, 2, 3}); r[0] != 3 || r[2] != 1 || len(reversed([]int{})) != 0 {
		t.Fatalf("%v", r)
	}
	if v, err := int32Arg(500); err != nil || v != 500 {
		t.Fatal(err)
	}
	for _, bad := range []int64{math.MaxInt32 + 1, math.MinInt32 - 1} {
		if _, err := int32Arg(bad); err == nil {
			t.Errorf("%d must not be truncated silently", bad)
		}
	}
}

func TestDispidCachePerClassAndPerObject(t *testing.T) {
	d := newDispidCache()
	fetches := 0
	fetch := func() (int32, error) { fetches++; return 0x37, nil }
	var own1, own2 map[string]int32
	for range 3 {
		if id, _ := d.id(classFolder, &own1, "EntryID", fetch); id != 0x37 {
			t.Fatal(id)
		}
		_, _ = d.id(classFolder, &own2, "EntryID", fetch)
	}
	if fetches != 1 {
		t.Fatalf("a cacheable class fetches once for all its objects: %d", fetches)
	}
	fetches = 0
	_, _ = d.id(classItem, &own1, "Body", fetch)
	_, _ = d.id(classItem, &own1, "Body", fetch)
	_, _ = d.id(classItem, &own2, "Body", fetch)
	if fetches != 2 {
		t.Fatalf("items (MailItem, ReportItem…) cache per object only: %d", fetches)
	}
	if _, err := d.id("", &own1, "X", func() (int32, error) { return 0, errors.New("unknown name") }); err == nil {
		t.Fatal("fetch errors surface and are not cached")
	}
}
