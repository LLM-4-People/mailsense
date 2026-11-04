package layer1

import (
	"runtime"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/vector"
)

// designRetentionBefore is the measured heap retained by the same fixture
// before the packed row layout (100,000 rows × 768 int8 + 40 sparse cells,
// on Go 1.27, 2026-11-03): one []int32 per row and a message → row map.
// The packed layout keeps the same features with one cells buffer and one
// offsets buffer, so the test pins that the bookkeeping does not regress.
const designRetentionBefore = 101242800

// memoryDesign builds a mailbox-sized synthetic design: rows, a 768-column
// embedding and 40 sparse cells per row (the evidence block's order of
// magnitude).
func memoryDesign(rows int) *Design {
	b := NewDesign(768, 584)
	v := vector.Quantized{Values: make([]int8, 768), Scale: 0.5}
	for i := 0; i < 768; i += 3 {
		v.Values[i] = int8(i%127 - 63)
	}
	for i := range rows {
		cells := make([]int32, 40)
		for j := range cells {
			cells[j] = int32(i%7 + j*14)
		}
		_ = b.Add(int64(i+1), &v, cells)
	}
	d, err := b.Build()
	if err != nil {
		panic(err)
	}
	return d
}

// TestDesignRetention pins the packed design's retained bytes and proves
// that every row is still addressable by id and by index. The fixture's
// feature payload is irreducible (the solver reads the full matrix on every
// objective evaluation); what must not come back is per-row slice headers
// and a second mailbox-sized lookup map.
func TestDesignRetention(t *testing.T) {
	// Deliberately serial: it measures the process heap, and a parallel test
	// allocating beside it would change the number under test.
	if raceDetector {
		t.Skip("heap retention is measured on the normal build; the race detector's heap is not comparable")
	}
	const rows = 100000
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d := memoryDesign(rows)
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)

	packed := int64(len(d.IDs))*8 + int64(len(d.Emb)) + int64(len(d.Scale))*4 +
		int64(len(d.Feat))*4 + int64(len(d.off))*4
	t.Logf("rows=%d retained=%d bytes (%.1f MB, %.1f bytes/row), packed payload=%d bytes (%.1f MB)",
		rows, retained, float64(retained)/(1<<20), float64(retained)/rows, packed, float64(packed)/(1<<20))
	if retained >= designRetentionBefore {
		t.Errorf("packed design retains %d bytes, not below the measured per-row layout's %d", retained, designRetentionBefore)
	}
	// The retained heap is the packed payload plus allocator/GC slack; a
	// per-row []int32 or a map would add several megabytes to this bound.
	if retained > packed+(5<<20) {
		t.Errorf("packed design retains %d bytes, more than its payload %d plus 5 MiB of slack", retained, packed)
	}
	if len(d.off) != d.Len()+1 || len(d.Feat) != rows*40 || len(d.Emb) != rows*768 {
		t.Fatalf("shape: ids=%d off=%d feat=%d emb=%d", d.Len(), len(d.off), len(d.Feat), len(d.Emb))
	}
	for i, id := range d.IDs {
		row, ok := d.Row(id)
		if !ok || row != i {
			t.Fatalf("message %d looked up as %d/%v, want row %d", id, row, ok, i)
		}
		if cells := d.cellsOf(i); len(cells) != 40 || cells[0] != int32(i%7) {
			t.Fatalf("row %d cells: %d starting %d", i, len(cells), cells[0])
		}
	}
	runtime.KeepAlive(d)
}
