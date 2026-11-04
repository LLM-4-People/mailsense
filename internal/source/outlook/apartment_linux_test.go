//go:build linux

package outlook

import (
	"context"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// Rules review 1b §6.4: the apartment runs every function on ONE OS thread
// (an STA is bound to its thread, not just to a goroutine).
func TestApartmentUsesOneOSThread(t *testing.T) {
	var initTID int
	a, err := startApartment(func() error { initTID = unix.Gettid(); return nil }, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	var mu sync.Mutex
	tids := map[int]bool{}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			_ = a.do(context.Background(), func() error {
				mu.Lock()
				tids[unix.Gettid()] = true
				mu.Unlock()
				return nil
			})
		})
	}
	wg.Wait()
	if len(tids) != 1 || !tids[initTID] {
		t.Fatalf("functions ran on threads %v, init on %d", tids, initTID)
	}
}
