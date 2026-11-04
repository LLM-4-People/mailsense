package layer1

import (
	"strings"
	"testing"
)

func TestExtendSparseAddsColumns(t *testing.T) {
	t.Parallel()
	b := NewDesign(0, 1)
	if err := b.Add(1, nil, []int32{0}); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(2, nil, nil); err != nil {
		t.Fatal(err)
	}
	d, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ExtendSparse(2, func(_ int, id int64, dst []int32) []int32 {
		if id == 1 {
			return append(dst, 1, 2)
		}
		return dst
	}); err != nil {
		t.Fatal(err)
	}
	if d.Cols() != 3 {
		t.Fatalf("cols=%d", d.Cols())
	}
	if got := d.cellsOf(0); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("row 0 cells %v", got)
	}
	// The fit standardises its own rows: both appended columns are present
	// in one of two examples, so neither is a constant column.
	std := standardizerOf(d, []Example{
		{Row: 0, Target: OneHot(0), Weight: 1},
		{Row: 1, Target: OneHot(1), Weight: 1},
	})
	if std.inv[1] == 0 || std.inv[2] == 0 {
		t.Fatalf("a present column has no scale: %v", std.inv)
	}
}

func TestDeciderFeaturesExtendTheDesign(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Train(t.Context(), plan, w.r.in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.Layout.ID, "decider-") {
		t.Fatalf("layout id %q does not name the decider block", tr.Layout.ID)
	}
	if tr.Design.Cells <= featureCellsCount() {
		t.Fatalf("design cells %d did not grow past the lexical block %d", tr.Design.Cells, featureCellsCount())
	}
}
