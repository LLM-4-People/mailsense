package vector

import (
	"cmp"
	"context"
	"runtime"
	"slices"
	"sync"
)

// Source streams the stored vectors of a space (the store implements it):
// rows written after afterSeq in write order, and how many it holds.
type Source interface {
	EachVector(ctx context.Context, space string, afterSeq int64, fn func(messageID, seq int64, values []byte, scale float32) error) error
	CountVectors(ctx context.Context, space string) (int, error)
}

// Set is the in-memory int8 copy of one vector space, shared by semantic
// search and layer 1's nearest neighbours: loaded once, then only the
// rows written since (the store's write sequence). A brute-force scan
// over int8 values is exact and fast enough for a mailbox (measured in
// docs/EMBEDDINGS.md); there is no approximate index to keep consistent.
// Safe for concurrent use.
type Set struct {
	mu     sync.RWMutex
	space  string
	seq    int64
	dims   int
	ids    []int64
	pos    map[int64]int
	values []int8 // len(ids) × dims
	scales []float32
}

// Scored is one scan result.
type Scored struct {
	ID  int64
	Sim float32
}

// Refresh brings the copy up to date with src: the rows written since the
// last load are merged; when src then holds another number of vectors
// than the copy (messages removed, also when new ones replaced them) or
// the space or length changed, everything is loaded again.
func (v *Set) Refresh(ctx context.Context, src Source, space string, dims int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if space != v.space || dims != v.dims || v.pos == nil {
		v.reset(space, dims)
	}
	if err := v.merge(ctx, src); err != nil {
		return err
	}
	n, err := src.CountVectors(ctx, space)
	if err != nil || n == len(v.ids) {
		return err
	}
	v.reset(space, dims)
	return v.merge(ctx, src)
}

func (v *Set) reset(space string, dims int) {
	v.space, v.dims, v.seq, v.pos, v.ids, v.values, v.scales = space, dims, 0, map[int64]int{}, nil, nil, nil
}

// merge adds or replaces the rows written after the last one loaded,
// streamed straight into the copy (no second copy of the space in memory).
func (v *Set) merge(ctx context.Context, src Source) error {
	return src.EachVector(ctx, v.space, v.seq, func(id, seq int64, values []byte, scale float32) error {
		v.seq = max(v.seq, seq)
		v.put(id, values, scale)
		return nil
	})
}

// Put adds or replaces one vector (int8 values as bytes) outside any
// source: a copy built from another store of vectors (tests, harnesses).
// A vector of another length is ignored.
func (v *Set) Put(id int64, values []int8, scale float32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pos == nil {
		v.reset(v.space, len(values))
	}
	b := make([]byte, len(values))
	for i, x := range values {
		b[i] = byte(x)
	}
	v.put(id, b, scale)
}

func (v *Set) put(id int64, values []byte, scale float32) {
	dims := v.dims
	if len(values) != dims {
		return // written under another length (cannot happen within a space)
	}
	i, ok := v.pos[id]
	if !ok {
		i = len(v.ids)
		v.pos[id] = i
		v.ids = append(v.ids, id)
		v.values = append(v.values, make([]int8, dims)...)
		v.scales = append(v.scales, 0)
	}
	row := v.values[i*dims : (i+1)*dims]
	for j, b := range values {
		row[j] = int8(b)
	}
	v.scales[i] = scale
}

// Space, Dims and Seq describe the copy (Seq: the last write loaded).
func (v *Set) Space() string { v.mu.RLock(); defer v.mu.RUnlock(); return v.space }
func (v *Set) Dims() int     { v.mu.RLock(); defer v.mu.RUnlock(); return v.dims }
func (v *Set) Seq() int64    { v.mu.RLock(); defer v.mu.RUnlock(); return v.seq }

// Len is the number of vectors held.
func (v *Set) Len() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.ids)
}

// Has reports whether id has a vector.
func (v *Set) Has(id int64) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	_, ok := v.pos[id]
	return ok
}

// Count is how many of ids have a vector in the copy.
func (v *Set) Count(ids map[int64]bool) int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	n := 0
	for _, id := range v.ids {
		if ids[id] {
			n++
		}
	}
	return n
}

// Row returns a copy of id's stored vector (nil when absent).
func (v *Set) Row(id int64) *Quantized {
	v.mu.RLock()
	defer v.mu.RUnlock()
	i, ok := v.pos[id]
	if !ok {
		return nil
	}
	return &Quantized{Values: slices.Clone(v.values[i*v.dims : (i+1)*v.dims]), Scale: v.scales[i]}
}

// Top returns the k most similar vectors to q among allowed (message
// ids; nil = all), most similar first. The allowed set becomes a bitset
// over the copy's rows (one bit per vector, no per-query map of every
// message).
func (v *Set) Top(q []float32, allowed []int64, k int) []Scored {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if allowed == nil {
		return v.top(q, k, nil)
	}
	mask := make(bitset, (len(v.ids)+63)/64)
	for _, id := range allowed {
		if i, ok := v.pos[id]; ok {
			mask.set(i)
		}
	}
	return v.top(q, k, mask.has)
}

// TopFunc is Top among the vectors whose id keep accepts (called from
// several goroutines at once: it must only read).
func (v *Set) TopFunc(q []float32, k int, keep func(id int64) bool) []Scored {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.top(q, k, func(i int) bool { return keep(v.ids[i]) })
}

// top scans in parallel chunks (rows [from, to) per worker) and merges.
func (v *Set) top(q []float32, k int, keep func(row int) bool) []Scored {
	n := len(v.ids)
	if n == 0 || k < 1 || len(q) != v.dims {
		return nil
	}
	workers := min(runtime.GOMAXPROCS(0), max(1, n/minChunk))
	parts := make([][]Scored, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parts[w] = v.scan(q, keep, k, w*n/workers, (w+1)*n/workers)
		}()
	}
	wg.Wait()
	all := slices.Concat(parts...)
	ByScore(all)
	return all[:min(k, len(all))]
}

// minChunk is the smallest share of a scan worker (below it a goroutine
// costs more than it saves): a performance constant of the scan, never a
// setting — the result does not depend on it (ties are broken by id).
const minChunk = 4096

// bitset marks rows of the copy.
type bitset []uint64

func (b bitset) set(i int)      { b[i/64] |= 1 << (i % 64) }
func (b bitset) has(i int) bool { return b[i/64]&(1<<(i%64)) != 0 }

// scan scores rows [from, to) (only the kept ones when keep is set) and
// keeps the best k by ByScore's order: a tie with the k-th is kept when
// its id is lower, so the result never depends on how rows are chunked
// or ordered.
func (v *Set) scan(q []float32, keep func(int) bool, k, from, to int) []Scored {
	best := make([]Scored, 0, k+1)
	floor := Scored{Sim: -2}
	for i := from; i < to; i++ {
		if keep != nil && !keep(i) {
			continue
		}
		sim := DotInt8(v.values[i*v.dims:(i+1)*v.dims], v.scales[i], q)
		if len(best) == k && (sim < floor.Sim || (sim == floor.Sim && v.ids[i] > floor.ID)) {
			continue
		}
		best = append(best, Scored{v.ids[i], sim})
		if len(best) > k {
			ByScore(best)
			best = best[:k]
			floor = best[k-1]
		}
	}
	return best
}

// ByScore sorts most similar first (ties: lower id first, deterministic).
func ByScore(s []Scored) {
	slices.SortFunc(s, func(a, b Scored) int {
		if c := cmp.Compare(b.Sim, a.Sim); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}
