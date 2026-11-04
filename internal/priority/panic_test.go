package priority

import (
	"testing"
	"time"
)

// TestAPanickingColdBuildStillReleasesTheLatch: every ranking read waits on
// the one cold build's done channel. A panic must return an error and clear
// the slot, not leave the screen (and every thread-truth consumer) waiting
// until the process exits.
func TestAPanickingColdBuildStillReleasesTheLatch(t *testing.T) {
	svc, fs := newService(t, nil, false)
	fs.mu.Lock()
	fs.panicEach = true
	fs.mu.Unlock()

	errc := make(chan error, 1)
	go func() {
		_, _, _, err := svc.Rank(t.Context(), FilterAll, 10)
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a panicking ranking build returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ranking read hung after a panic")
	}

	fs.mu.Lock()
	fs.panicEach = false
	fs.mu.Unlock()
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 10); err != nil {
		t.Fatalf("the latch was not released: %v", err)
	}
}

// TestAPanickingRefreshReleasesItsWaiters: a cold read for another snapshot
// waits on refreshDone. A panicking refresh must close it and clear the
// refreshing flag.
func TestAPanickingRefreshReleasesItsWaiters(t *testing.T) {
	svc, fs := newService(t, nil, false)
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 10); err != nil {
		t.Fatal(err)
	}
	routeKey, err := testRouteKey()
	if err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	fs.panicEach = true
	fs.mu.Unlock()

	svc.mu.Lock()
	base := svc.cached
	svc.refreshing = true
	svc.refreshDone = make(chan struct{})
	done := svc.refreshDone
	svc.builds.Add(1)
	svc.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		svc.refreshRanking(t.Context(), false, svc.clock.Now(), base, routeKey)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh hung after a panic")
	}
	select {
	case <-done:
	default:
		t.Fatal("the refresh waiters were never released")
	}
	svc.mu.Lock()
	refreshing := svc.refreshing
	svc.mu.Unlock()
	if refreshing {
		t.Fatal("refreshing stayed set after a panicking refresh")
	}

	fs.mu.Lock()
	fs.panicEach = false
	fs.mu.Unlock()
}
