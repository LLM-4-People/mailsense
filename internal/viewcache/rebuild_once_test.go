package viewcache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOneStaleGenerationStartsOneRebuild pins the single-flight promise of the
// stale-while-revalidate path. Keep runs outside the cache lock because it
// reads a store generation, so a rebuild can finish and install while a Get is
// inside it. That Get used to re-lock, find `refreshing` already cleared by the
// rebuild it had just missed, and start a SECOND whole rebuild for the same
// generation move: two mailbox scans for one write, which is what made
// TestPageRepeatedCallScansNothing see a third digest read under load.
func TestOneStaleGenerationStartsOneRebuild(t *testing.T) {
	const key = "k"
	var gen atomic.Int64
	gen.Store(1)
	var builds atomic.Int64
	buildGate := make(chan struct{})

	c := New[string, int64](Options[string, int64]{
		// Fresh by TTL, stale by generation: that is the only shape where
		// Keep runs on the serve path.
		TTL: time.Minute,
		// A value is current when it carries the generation it was built at.
		Keep:      func(_ string, v int64) bool { return v == gen.Load() },
		Component: "test",
	})
	t.Cleanup(func() { c.Close(context.Background()) })

	build := func(context.Context) (int64, error) {
		n := builds.Add(1)
		if n == 2 {
			<-buildGate // hold the first rebuild open
		}
		return gen.Load(), nil
	}

	// Warm: build 1 installs generation 1.
	if v, err := c.Get(t.Context(), key, build); err != nil || v != 1 {
		t.Fatalf("warm Get = %d, %v", v, err)
	}

	// A write moves the generation: the installed value is now stale.
	gen.Store(2)

	// First stale read starts exactly one rebuild and is held inside its build.
	if _, err := c.Get(t.Context(), key, build); err != nil {
		t.Fatal(err)
	}

	// Second stale read enters Keep (outside the lock) and waits there, which
	// is the window the rebuild finishes in.
	inKeep := make(chan struct{})
	releaseKeep := make(chan struct{})
	var once sync.Once
	original := c.keep
	c.keep = func(k string, v int64) bool {
		current := original(k, v)
		if !current {
			once.Do(func() {
				close(inKeep)
				<-releaseKeep
			})
		}
		return current
	}
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_, _ = c.Get(context.Background(), key, build)
	}()
	select {
	case <-inKeep:
	case <-time.After(2 * time.Second):
		t.Fatal("the second read never reached Keep")
	}

	// Let the rebuild finish and install while the second read is parked.
	close(buildGate)
	c.mu.Lock()
	installed := c.entries[key].stamp
	c.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for installed < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the rebuild never installed (stamp %d)", installed)
		}
		time.Sleep(time.Millisecond)
		c.mu.Lock()
		installed = c.entries[key].stamp
		c.mu.Unlock()
	}

	// The parked read must see the install and serve it, not rebuild again.
	close(releaseKeep)
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the second read never returned")
	}
	// No third rebuild was started. The cache's own flag is the deterministic
	// witness: the buggy path sets refreshing and spawns the goroutine before
	// Get returns, while the fixed path re-reads the installed entry.
	c.mu.Lock()
	refreshing, stamp := c.entries[key].refreshing, c.entries[key].stamp
	c.mu.Unlock()
	if refreshing {
		t.Fatalf("the parked read started a second rebuild for the same generation move (stamp %d)", stamp)
	}
	if n := builds.Load(); n != 2 {
		t.Fatalf("%d builds for one generation move, want exactly 2 (the warm build and one rebuild)", n)
	}
}

// TestKeepRaceDoesNotServeAReplacedEntry is the same window with an eviction:
// the entry the caller snapshotted is gone by the time it re-locks, so it must
// start over from a fresh entry rather than reason about a stale pointer.
func TestKeepRaceDoesNotServeAReplacedEntry(t *testing.T) {
	var builds atomic.Int64
	c := New[string, int](Options[string, int]{
		TTL:       time.Nanosecond,
		Size:      1,
		Keep:      func(string, int) bool { return true },
		Now:       func() time.Time { return time.Unix(0, 0) },
		Component: "test",
	})
	t.Cleanup(func() { c.Close(context.Background()) })
	build := func(context.Context) (int, error) { return int(builds.Add(1)), nil }

	first, err := c.Get(t.Context(), "a", build)
	if err != nil || first != 1 {
		t.Fatalf("warm Get = %d, %v", first, err)
	}
	// A second key evicts "a" (Size 1) while "a"'s caller is in Keep.
	if _, err := c.Get(t.Context(), "b", build); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(t.Context(), "a", build)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("a replaced entry was served as %d, want a fresh build (3)", got)
	}
}
