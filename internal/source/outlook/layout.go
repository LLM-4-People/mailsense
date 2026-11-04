package outlook

import (
	"fmt"
	"math"
	"sync"
)

// Pure parts of the Windows COM layer, so they are tested on every OS.

// columnMajor builds rows × cols from a SAFEARRAY's element accessor:
// Table.GetArray returns dimension 1 = rows, stored column-major (the
// leftmost index varies fastest), so element (r, c) is at r + c·rows.
func columnMajor(rows, cols int, at func(i int) (any, error)) ([][]any, error) {
	out := make([][]any, rows)
	for r := range out {
		out[r] = make([]any, cols)
		for c := range out[r] {
			v, err := at(r + c*rows)
			if err != nil {
				return nil, err
			}
			out[r][c] = v
		}
	}
	return out, nil
}

// reversed returns s in reverse order (IDispatch::Invoke takes arguments
// last-first).
func reversed[T any](s []T) []T {
	out := make([]T, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// int32Arg is an integer argument as VT_I4 (every integer parameter the
// connector passes is a 32-bit Long); out of range is an error, never a
// silent truncation.
func int32Arg(v int64) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("outlook: integer argument %d does not fit a 32-bit COM Long", v)
	}
	return int32(v), nil
}

// dispidCache remembers member DISPIDs: per class for classes whose
// objects share one interface (model.go cacheable), per object otherwise.
type dispidCache struct {
	mu      sync.Mutex
	byClass map[string]map[string]int32
}

func newDispidCache() *dispidCache { return &dispidCache{byClass: map[string]map[string]int32{}} }

// id returns the DISPID of name, fetching it once per class (or per object
// through own when the class is not cacheable or unknown).
func (d *dispidCache) id(class string, own *map[string]int32, name string, fetch func() (int32, error)) (int32, error) {
	var cache map[string]int32
	if class != "" && cacheable(class) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.byClass[class] == nil {
			d.byClass[class] = map[string]int32{}
		}
		cache = d.byClass[class]
	} else {
		if *own == nil {
			*own = map[string]int32{}
		}
		cache = *own
	}
	if id, ok := cache[name]; ok {
		return id, nil
	}
	id, err := fetch()
	if err != nil {
		return 0, err
	}
	cache[name] = id
	return id, nil
}
