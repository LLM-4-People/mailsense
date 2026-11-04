package logx

import (
	"sort"
	"sync"
)

// Ring keeps the most recent entries in memory for the Logs view.
type Ring struct {
	mu    sync.Mutex
	buf   []Entry
	start int // index of the oldest entry
	n     int
}

// NewRing returns a ring holding up to size entries (minimum 1).
func NewRing(size int) *Ring { return &Ring{buf: make([]Entry, max(size, 1))} }

// add inserts e with its own Seq, for tests only. Production entries go
// through push so the sequence and the insertion share one lock.
func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.insertLocked(e)
}

// push assigns the next process-wide Seq and inserts e atomically. It is
// the one production insert: insertion order is sequence order even when
// several goroutines log at once. When the pipeline has a JSON consumer
// (the log file or the event bus), the entry's wire bytes are encoded once
// here and retained with it; a UI-only pipeline passes encode=false and
// pays no encoding. The encode is under the lock because Seq is part of the
// bytes and insertion order must equal sequence order.
func (r *Ring) push(e Entry, encode bool) Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Seq = seq.Add(1)
	if encode {
		e.cacheWire()
	}
	r.insertLocked(e)
	return e
}

func (r *Ring) insertLocked(e Entry) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
		return
	}
	r.buf[r.start] = e
	r.start = (r.start + 1) % len(r.buf)
}

// Snapshot returns the entries oldest first.
func (r *Ring) Snapshot() []Entry {
	entries, _, _ := r.Read(0)
	return entries
}

// Since returns the entries with Seq greater than seq, oldest first.
func (r *Ring) Since(seq uint64) []Entry {
	entries, _, _ := r.Read(seq)
	return entries
}

// Read returns the entries with Seq greater than seq, oldest first, and
// the retained bounds, all under one lock: a rotation between a bounds
// check and an entries read cannot hide a gap. oldest and last are 0 when
// the ring is empty.
func (r *Ring) Read(seq uint64) (entries []Entry, oldest, last uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readLimitLocked(seq, r.n, false)
}

// ReadLimit returns at most limit entries with Seq greater than seq, oldest
// first, and the retained bounds, all under one lock. It allocates exactly
// the returned window and never more than limit. newest selects the window:
// true is the newest limit entries (the initial Logs view), false the oldest
// limit entries after seq (the forward page a since cursor asks for).
func (r *Ring) ReadLimit(seq uint64, limit int, newest bool) (entries []Entry, oldest, last uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readLimitLocked(seq, limit, newest)
}

func (r *Ring) readLimitLocked(seq uint64, limit int, newest bool) (entries []Entry, oldest, last uint64) {
	if r.n == 0 {
		return nil, 0, 0
	}
	oldest = r.buf[r.start].Seq
	last = r.buf[(r.start+r.n-1)%len(r.buf)].Seq
	if seq >= last || limit < 1 {
		// The caught-up case is the common one for every SSE heartbeat and
		// every event: no entry matches. Returning here avoids allocating a
		// capacity-sized slice while holding the global ring lock.
		return nil, oldest, last
	}
	return r.windowLocked(seq, limit, newest), oldest, last
}

// Bounds is the O(1) live-only cursor source (0, 0 when empty).
func (r *Ring) Bounds() (oldest, last uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == 0 {
		return 0, 0
	}
	return r.buf[r.start].Seq, r.buf[(r.start+r.n-1)%len(r.buf)].Seq
}

// windowLocked cuts the requested window under the ring lock. Sequence
// order is insertion order, so one binary search finds the first matching
// entry without inspecting the rest.
func (r *Ring) windowLocked(seq uint64, limit int, newest bool) []Entry {
	first := sort.Search(r.n, func(i int) bool {
		return r.buf[(r.start+i)%len(r.buf)].Seq > seq
	})
	if first == r.n {
		return nil
	}
	count := r.n - first
	if newest && count > limit {
		first += count - limit
		count = limit
	}
	if count > limit {
		count = limit
	}
	out := make([]Entry, count)
	for i := range count {
		out[i] = r.buf[(r.start+first+i)%len(r.buf)]
	}
	return out
}

// Resize changes the capacity, keeping the newest entries (atomically with
// respect to concurrent writes).
func (r *Ring) Resize(size int) {
	size = max(size, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if size == len(r.buf) {
		return
	}
	all := r.windowLocked(0, r.n, false)
	if len(all) > size {
		all = all[len(all)-size:]
	}
	r.buf = make([]Entry, size)
	copy(r.buf, all)
	r.start, r.n = 0, len(all)
}

// Cap returns the capacity.
func (r *Ring) Cap() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}
