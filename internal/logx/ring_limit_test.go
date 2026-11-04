package logx

import "testing"

func ringOf(t testing.TB, n int) *Ring {
	t.Helper()
	r := NewRing(n)
	for i := 1; i <= n; i++ {
		r.add(Entry{Seq: uint64(i), Message: "entry"})
	}
	return r
}

// TestRingReadLimitWindows pins the two page directions: the initial view
// takes the newest N retained entries, a since cursor takes the oldest N
// after it, and neither allocates the backlog.
func TestRingReadLimitWindows(t *testing.T) {
	const retained = 100_000
	r := ringOf(t, retained)

	newest, oldestSeq, last := r.ReadLimit(0, 500, true)
	if len(newest) != 500 || newest[0].Seq != retained-499 || newest[499].Seq != retained {
		t.Fatalf("newest window: %d entries, %d..%d", len(newest), newest[0].Seq, newest[len(newest)-1].Seq)
	}
	if oldestSeq != 1 || last != retained {
		t.Fatalf("bounds %d..%d", oldestSeq, last)
	}

	oldestPage, _, _ := r.ReadLimit(0, 500, false)
	if len(oldestPage) != 500 || oldestPage[0].Seq != 1 || oldestPage[499].Seq != 500 {
		t.Fatalf("oldest window: %d entries, %d..%d", len(oldestPage), oldestPage[0].Seq, oldestPage[len(oldestPage)-1].Seq)
	}

	since, _, _ := r.ReadLimit(retained-500, 500, false)
	if len(since) != 500 || since[0].Seq != retained-499 || since[499].Seq != retained {
		t.Fatalf("since window: %d entries, %d..%d", len(since), since[0].Seq, since[len(since)-1].Seq)
	}

	// A forward page that is cut off keeps the oldest entries after the
	// cursor; the newest direction of the same page keeps the newest.
	first, _, _ := r.ReadLimit(retained-1000, 100, false)
	if first[0].Seq != retained-999 || first[99].Seq != retained-900 {
		t.Fatalf("forward cut: %d..%d", first[0].Seq, first[len(first)-1].Seq)
	}
	latest, _, _ := r.ReadLimit(retained-1000, 100, true)
	if latest[0].Seq != retained-99 || latest[99].Seq != retained {
		t.Fatalf("newest cut: %d..%d", latest[0].Seq, latest[len(latest)-1].Seq)
	}

	if caught, _, _ := r.ReadLimit(retained, 500, true); caught != nil {
		t.Fatalf("caught-up read returned %d entries", len(caught))
	}
	if none, _, _ := r.ReadLimit(0, 0, true); none != nil {
		t.Fatalf("zero limit returned %d entries", len(none))
	}
}

// TestRingReadLimitAllocationIsTheWindow: a 100,000-entry ring and a
// 500-entry page allocate the page, never the retained ring.
func TestRingReadLimitAllocationIsTheWindow(t *testing.T) {
	r := ringOf(t, 100_000)
	allocs := testing.AllocsPerRun(20, func() {
		entries, _, _ := r.ReadLimit(0, 500, true)
		if len(entries) != 500 {
			t.Fatalf("page %d", len(entries))
		}
	})
	if allocs > 2 {
		t.Fatalf("ReadLimit allocates %.1f times for a 500-entry page", allocs)
	}
}

// TestRingReadLimitPreservesRetention: the bounded read changes no ring
// state, so a later full Read still sees every retained entry.
func TestRingReadLimitPreservesRetention(t *testing.T) {
	r := ringOf(t, 1000)
	r.ReadLimit(0, 10, true)
	all, oldest, last := r.Read(0)
	if len(all) != 1000 || oldest != 1 || last != 1000 {
		t.Fatalf("retention changed: %d entries, %d..%d", len(all), oldest, last)
	}
}
