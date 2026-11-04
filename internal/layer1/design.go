package layer1

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/LLM-4-People/mailsense/internal/vector"
)

// Design is the feature rows of the messages layer 1 sees: per row the
// message's int8 embedding (cut to Dims, Matryoshka) and its sparse
// layer-0 cells (0/1, indices into Cells). Standardisation is not a
// property of the design: one fit computes it from the examples it trains
// on and folds it into its weights (w′ = w/σ, b′ = b − Σ w·μ/σ), so a
// held-out row can never shape the weights that predict it.
//
// The rows are one packed buffer, not one slice per row: a mailbox-sized
// design at 100k rows kept 2.4 MB of slice headers alone and one allocation
// per row, while the solver reads rows one at a time and never needs a row's
// cells as an independent object. Emb is already one packed int8 buffer;
// Feat/off give the sparse cells the same layout (row i is
// Feat[off[i]:off[i+1]]). Rows must be added in ascending id order (the
// builder enforces it), so Row is a binary search rather than a second
// mailbox-sized map.
//
// The full matrix is retained on purpose: every objective evaluation reads
// every example (the optimizer calls it hundreds of times per fit, and a λ
// path, its folds and the final fit each fit again), while certification and
// serving need random access by row. Streaming or re-reading the rows per
// evaluation would multiply I/O by the iteration count, and the embedding is
// already int8 (the quantized model's own precision); the safe reduction is
// therefore the representation, not the rows.
type Design struct {
	IDs   []int64 // message row ids, ascending
	Dims  int     // embedding columns (0: no embedding)
	Cells int     // evidence columns (0: no evidence)
	Emb   []int8  // len(IDs) × Dims, packed
	Scale []float32
	Feat  []int32 // packed sparse cells, row i at Feat[off[i]:off[i+1]]
	off   []int32 // len(IDs) + 1 offsets into Feat
}

// maxDesignCells bounds off's int32 indices (a design larger than this is
// beyond the process's addressable data anyway).
const maxDesignCells = int64(1)<<31 - 1

// Cols is the number of columns.
func (d *Design) Cols() int { return d.Dims + d.Cells }

// Len is the number of rows.
func (d *Design) Len() int { return len(d.IDs) }

// Row is the row of a message (false when it has none).
func (d *Design) Row(id int64) (int, bool) {
	i := sort.Search(len(d.IDs), func(i int) bool { return d.IDs[i] >= id })
	if i < len(d.IDs) && d.IDs[i] == id {
		return i, true
	}
	return 0, false
}

// DesignBuilder collects rows.
type DesignBuilder struct {
	d *Design
}

// NewDesign starts a design with dims embedding columns and cells
// evidence columns.
func NewDesign(dims, cells int) *DesignBuilder {
	return &DesignBuilder{d: &Design{Dims: dims, Cells: cells, off: []int32{0}}}
}

// Add adds a message's row: its stored vector (cut to the design's
// length and re-quantised when longer; nil when the design has no
// embedding) and its evidence cells (sorted, each < cells). Ids must be
// added in ascending order; the one design of a pass is built that way
// (buildDesign sorts the members once), and the order is what makes Row a
// binary search and the packed rows deterministic.
func (b *DesignBuilder) Add(id int64, v *vector.Quantized, cells []int32) error {
	d := b.d
	if n := len(d.IDs); n > 0 {
		switch last := d.IDs[n-1]; {
		case id == last:
			return fmt.Errorf("layer1: message %d added twice", id)
		case id < last:
			return fmt.Errorf("layer1: message %d added after %d: rows must ascend", id, last)
		}
	}
	for i, c := range cells {
		if c < 0 || int(c) >= d.Cells || (i > 0 && c <= cells[i-1]) {
			return fmt.Errorf("layer1: message %d: cells must be ascending indices below %d", id, d.Cells)
		}
	}
	if int64(len(d.Feat))+int64(len(cells)) > maxDesignCells {
		return fmt.Errorf("layer1: design holds more than %d cells", maxDesignCells)
	}
	if d.Dims > 0 {
		if v == nil || len(v.Values) < d.Dims {
			return fmt.Errorf("layer1: message %d has no vector of %d dimensions", id, d.Dims)
		}
		q := *v
		if len(v.Values) > d.Dims {
			cut, err := vector.Truncate(v.Floats(), d.Dims)
			if err != nil {
				return err
			}
			q = vector.Quantize(cut)
		}
		d.Emb = append(d.Emb, q.Values...)
		d.Scale = append(d.Scale, q.Scale)
	}
	d.Feat = append(d.Feat, cells...)
	d.off = append(d.off, int32(len(d.Feat)))
	d.IDs = append(d.IDs, id)
	return nil
}

// Build freezes the collected rows. Standardisation belongs to one fit,
// not to the design: computing it here would include the certification
// rows and make C no longer out of sample.
func (b *DesignBuilder) Build() (*Design, error) {
	d := b.d
	if len(d.IDs) == 0 {
		return nil, errors.New("layer1: no rows")
	}
	scaleLen := 0
	if d.Dims > 0 {
		scaleLen = len(d.IDs)
	}
	if len(d.off) != len(d.IDs)+1 || len(d.Emb) != len(d.IDs)*d.Dims || len(d.Scale) != scaleLen {
		return nil, errors.New("layer1: the design's packed rows disagree with its shape")
	}
	return d, nil
}

// ExtendSparse appends extra sparse columns to a built design. cellAt appends
// a row's new cell indices (already offset by the current Cells) to dst and
// returns the extended slice; ExtendSparse reuses one scratch buffer, so the
// callback must not retain dst. The columns are raw 0/1 cells; a fit
// standardises them from its own examples. A feature block added after the
// design is built (the decider's answers, read by gather) uses this. The rows
// are rebuilt in one packed pass, so the caller never keeps a row-indexed map
// of slices just to extend the design, and the callback allocates nothing per
// row.
func (d *Design) ExtendSparse(extra int, cellAt func(row int, id int64, dst []int32) []int32) error {
	if extra <= 0 {
		return nil
	}
	if cellAt == nil {
		return errors.New("layer1: ExtendSparse without a cell source")
	}
	base, limit := d.Cells, d.Cells+extra
	feat := make([]int32, 0, len(d.Feat)+len(d.IDs))
	off := make([]int32, len(d.IDs)+1)
	scratch := make([]int32, 0, 64)
	for i, id := range d.IDs {
		scratch = cellAt(i, id, scratch[:0])
		slices.Sort(scratch)
		for _, c := range scratch {
			if int64(c) < int64(base) || int64(c) >= int64(limit) {
				return fmt.Errorf("layer1: message %d: extended cell %d outside [%d, %d)", id, c, base, limit)
			}
		}
		old := d.Feat[d.off[i]:d.off[i+1]]
		rowStart := len(feat)
		j, k := 0, 0
		for j < len(old) || k < len(scratch) {
			var c int32
			if k == len(scratch) || (j < len(old) && old[j] <= scratch[k]) {
				c, j = old[j], j+1
			} else {
				c, k = scratch[k], k+1
			}
			if int64(c) < 0 || int64(c) >= int64(limit) {
				return fmt.Errorf("layer1: message %d: cell %d outside [0, %d)", id, c, limit)
			}
			if len(feat) > rowStart && feat[len(feat)-1] == c {
				continue
			}
			if len(feat) >= int(maxDesignCells) {
				return fmt.Errorf("layer1: design holds more than %d cells", maxDesignCells)
			}
			feat = append(feat, c)
		}
		off[i+1] = int32(len(feat))
	}
	d.Feat, d.off, d.Cells = feat, off, limit
	return nil
}

// emb is row i's embedding values and scale.
func (d *Design) emb(i int) ([]int8, float64) {
	if d.Dims == 0 {
		return nil, 0
	}
	return d.Emb[i*d.Dims : (i+1)*d.Dims], float64(d.Scale[i])
}

// cellsOf is row i's sparse cells (the packed view; never retained by the
// caller).
func (d *Design) cellsOf(i int) []int32 {
	return d.Feat[d.off[i]:d.off[i+1]]
}
