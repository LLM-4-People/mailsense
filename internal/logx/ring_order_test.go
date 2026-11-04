package logx

import (
	"sync"
	"testing"
)

// TestRingReadAssignsSequenceAndBounds checks the owner fix behind log
// replay: push assigns Seq inside the ring lock, so concurrent logging
// cannot insert Seq 6 before Seq 5, and Read returns entries and bounds
// from one lock.
func TestRingReadAssignsSequenceAndBounds(t *testing.T) {
	const writers, each, capacity = 8, 500, 4096
	svc, _ := setup(t, nil, map[string]any{"log.ring.size": capacity})

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range each {
				For("ring.test").Info("entry", "writer", i)
			}
		}(i)
	}
	wg.Wait()

	entries, oldest, last := svc.Ring().Read(0)
	if len(entries) != writers*each {
		t.Fatalf("Read(0) returned %d entries, want %d", len(entries), writers*each)
	}
	if oldest != entries[0].Seq || last != entries[len(entries)-1].Seq {
		t.Fatalf("bounds = %d..%d, entries %d..%d", oldest, last, entries[0].Seq, entries[len(entries)-1].Seq)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq <= entries[i-1].Seq {
			t.Fatalf("entries are out of sequence order at %d: %d then %d", i, entries[i-1].Seq, entries[i].Seq)
		}
	}
	if oldest, last := svc.Ring().Bounds(); oldest != entries[0].Seq || last != entries[len(entries)-1].Seq {
		t.Fatalf("Bounds = %d..%d", oldest, last)
	}
}

// TestRingReadIsAtomicUnderWrites exercises Read while entries rotate in:
// every returned entry is newer than the cursor and inside the returned
// bounds, which is the arithmetic the SSE tailer relies on.
func TestRingReadIsAtomicUnderWrites(t *testing.T) {
	const capacity = 64
	r := NewRing(capacity)
	var cursor uint64
	for i := 1; i <= capacity/2; i++ {
		r.add(Entry{Seq: uint64(i)})
		cursor = uint64(i)
	}
	stop := make(chan struct{})
	go func() {
		for i := capacity/2 + 1; i <= capacity/2+2000; i++ {
			r.add(Entry{Seq: uint64(i)})
		}
		close(stop)
	}()
	for {
		entries, oldest, last := r.Read(cursor)
		for _, e := range entries {
			if e.Seq <= cursor {
				t.Fatalf("entry %d at or below cursor %d", e.Seq, cursor)
			}
			if oldest != 0 && (e.Seq < oldest || e.Seq > last) {
				t.Fatalf("entry %d outside bounds %d..%d", e.Seq, oldest, last)
			}
		}
		if oldest != 0 && cursor > 0 && cursor < oldest-1 {
			// Rotation past the cursor is a real loss; the next read starts
			// at the retained oldest. The bounds still bracket the entries.
			cursor = oldest - 1
		}
		select {
		case <-stop:
			return
		default:
		}
	}
}
