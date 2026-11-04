package viewcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixturewait"
)

// A fresh entry is served without building, and concurrent callers of a
// missing key share its one build.
func TestColdBuildIsSingleFlight(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: time.Minute, Size: 4, Now: clk.Now, Component: "viewcache-test"})

	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var builds atomic.Int64
	build := func(context.Context) (string, error) {
		builds.Add(1)
		started <- struct{}{}
		<-release
		return "v", nil
	}

	const n = 8
	entered := make(chan struct{}, n)
	out := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered <- struct{}{}
			out[i], errs[i] = cache.Get(context.Background(), 1, build)
		}()
	}
	for range n {
		<-entered
	}
	<-started
	close(release)
	wg.Wait()

	if got := builds.Load(); got != 1 {
		t.Fatalf("%d builds for one missing key, want 1", got)
	}
	for i := range n {
		if errs[i] != nil || out[i] != "v" {
			t.Fatalf("caller %d: %q %v", i, out[i], errs[i])
		}
	}

	// The installed entry answers without another build.
	again, err := cache.Get(t.Context(), 1, build)
	if err != nil || again != "v" || builds.Load() != 1 {
		t.Fatalf("fresh entry rebuilt: %q %v builds %d", again, err, builds.Load())
	}
}

// A failed first build installs nothing, so the next call builds again.
func TestFailedFirstBuildInstallsNothing(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: time.Minute, Size: 2, Now: clk.Now, Component: "viewcache-test"})
	boom := errors.New("boom")
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		return "", boom
	}); !errors.Is(err, boom) {
		t.Fatalf("first build error %v", err)
	}
	cache.mu.Lock()
	n := len(cache.entries)
	cache.mu.Unlock()
	if n != 0 {
		t.Fatalf("a failed build installed %d entries", n)
	}
	got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) { return "v", nil })
	if err != nil || got != "v" {
		t.Fatalf("retry after a failed build: %q %v", got, err)
	}
}

// A failed first build is shared with the callers already waiting on it and
// leaves nothing installed; the next call builds again.
func TestFailedFirstBuildIsSharedThenRetried(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: time.Minute, Size: 2, Now: clk.Now, Component: "viewcache-test"})
	boom := errors.New("boom")
	started := make(chan struct{}, 1)
	release := make(chan struct{})

	builder := make(chan error, 1)
	go func() {
		_, err := cache.Get(context.Background(), 1, func(context.Context) (string, error) {
			started <- struct{}{}
			<-release
			return "", boom
		})
		builder <- err
	}()
	<-started

	waiter := make(chan error, 1)
	go func() {
		_, err := cache.Get(context.Background(), 1, func(context.Context) (string, error) {
			t.Error("the waiter ran another build instead of sharing the failure")
			return "built", nil
		})
		waiter <- err
	}()
	fixturewait.WaitUntil(t, 5*time.Second, "the waiter joined the failed build", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.waiters == 1
	})
	close(release)

	for _, err := range []error{<-builder, <-waiter} {
		if !errors.Is(err, boom) {
			t.Fatalf("failed build returned %v, want boom", err)
		}
	}
	cache.mu.Lock()
	n := len(cache.entries)
	cache.mu.Unlock()
	if n != 0 {
		t.Fatalf("a shared failed build installed %d entries", n)
	}
	got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) { return "v", nil })
	if err != nil || got != "v" {
		t.Fatalf("retry after a shared failure: %q %v", got, err)
	}
}

// An expired entry is served at once while exactly one background rebuild
// runs; the rebuild installs its result, and a failed refresh keeps the old
// value.
func TestExpiredServesStaleAndRefreshesOnce(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: 10 * time.Second, Size: 2, Now: clk.Now, Component: "viewcache-test"})
	var builds atomic.Int64
	warm, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v1", nil
	})
	if err != nil || warm != "v1" {
		t.Fatalf("warm %q %v", warm, err)
	}

	clk.Advance(11 * time.Second)
	release := make(chan struct{})
	refreshing := make(chan struct{}, 1)
	stale, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		refreshing <- struct{}{}
		<-release
		return "v2", nil
	})
	if err != nil || stale != "v1" {
		t.Fatalf("an expired entry did not serve its snapshot: %q %v", stale, err)
	}
	<-refreshing
	if got := builds.Load(); got != 2 {
		t.Fatalf("%d builds, want the initial one and one refresh", got)
	}
	// While the refresh runs, callers keep getting the snapshot, never a
	// second rebuild.
	second, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v3", nil
	})
	if err != nil || second != "v1" || builds.Load() != 2 {
		t.Fatalf("a second refresh started: %q %v builds %d", second, err, builds.Load())
	}
	close(release)
	fixturewait.WaitUntil(t, 5*time.Second, "the refresh installed its result", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.set && cached.view == "v2" && !cached.refreshing
	})

	// A failed refresh keeps the last snapshot and clears the refresh flag.
	clk.Advance(11 * time.Second)
	boom := errors.New("boom")
	if got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "", boom
	}); err != nil || got != "v2" {
		t.Fatalf("a failed refresh dropped the snapshot: %q %v", got, err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the failed refresh cleared its flag", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.view == "v2" && !cached.refreshing
	})
}

// Keep is evaluated after a successful build and before installing. A
// rejected first build installs nothing but still answers its caller; the
// next call builds again. A rejected refresh keeps the stale snapshot.
func TestKeepRejectsAnInstall(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	var allow atomic.Bool
	cache := New[int, string](Options[int, string]{
		TTL: 10 * time.Second, Size: 2, Now: clk.Now, Component: "viewcache-test",
		Keep: func(int, string) bool { return allow.Load() },
	})
	var builds atomic.Int64
	first, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v1", nil
	})
	if err != nil || first != "v1" {
		t.Fatalf("the foreground caller lost its value: %q %v", first, err)
	}
	cache.mu.Lock()
	_, installed := cache.entries[1]
	cache.mu.Unlock()
	if installed {
		t.Fatal("a rejected first build installed an entry")
	}

	allow.Store(true)
	second, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v2", nil
	})
	if err != nil || second != "v2" || builds.Load() != 2 {
		t.Fatalf("the next call did not rebuild: %q %v builds %d", second, err, builds.Load())
	}

	// A rejected refresh keeps the old snapshot and lets a later call retry.
	clk.Advance(11 * time.Second)
	allow.Store(false)
	stale, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v3", nil
	})
	if err != nil || stale != "v2" {
		t.Fatalf("a rejected refresh did not serve the snapshot: %q %v", stale, err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the rejected refresh cleared its flag", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.view == "v2" && !cached.refreshing
	})
	allow.Store(true)
	if got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v4", nil
	}); err != nil || got != "v2" {
		t.Fatalf("a later call did not retry the refresh: %q %v", got, err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the retried refresh installed its result", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.view == "v4"
	})
}

// Keep may read the built value: a value that carries a generation older than
// the store's current one is not installed, even though its key is still the
// requested one.
func TestKeepCanInspectTheValue(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	var current atomic.Uint64
	current.Store(2)
	cache := New[int, uint64](Options[int, uint64]{
		TTL: time.Minute, Size: 2, Now: clk.Now, Component: "viewcache-test",
		Keep: func(_ int, gen uint64) bool { return gen == current.Load() },
	})
	var builds atomic.Int64
	got, err := cache.Get(t.Context(), 1, func(context.Context) (uint64, error) {
		builds.Add(1)
		return 1, nil
	})
	if err != nil || got != 1 {
		t.Fatalf("the foreground caller lost its value: %d %v", got, err)
	}
	cache.mu.Lock()
	_, installed := cache.entries[1]
	cache.mu.Unlock()
	if installed {
		t.Fatal("a value carrying an older generation was installed")
	}

	got, err = cache.Get(t.Context(), 1, func(context.Context) (uint64, error) {
		builds.Add(1)
		return current.Load(), nil
	})
	if err != nil || got != 2 || builds.Load() != 2 {
		t.Fatalf("the current-generation build: %d %v builds %d", got, err, builds.Load())
	}
	cache.mu.Lock()
	_, installed = cache.entries[1]
	cache.mu.Unlock()
	if !installed {
		t.Fatal("a current-generation value was not installed")
	}
}

// A fresh value that Keep rejects is served once while exactly one background
// rebuild runs; the rebuild installs when its value passes Keep.
func TestKeepRejectsAFreshServeAndRefreshes(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	var current atomic.Uint64
	current.Store(1)
	cache := New[int, uint64](Options[int, uint64]{
		TTL: 10 * time.Second, Size: 2, Now: clk.Now, Component: "viewcache-test",
		Keep: func(_ int, value uint64) bool { return value == current.Load() },
	})
	var builds atomic.Int64
	first, err := cache.Get(t.Context(), 1, func(context.Context) (uint64, error) {
		builds.Add(1)
		return 1, nil
	})
	if err != nil || first != 1 {
		t.Fatalf("warm: %d %v", first, err)
	}

	current.Store(2)
	stale, err := cache.Get(t.Context(), 1, func(context.Context) (uint64, error) {
		builds.Add(1)
		return 2, nil
	})
	if err != nil || stale != 1 {
		t.Fatalf("a moved generation was not served stale: %d %v", stale, err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the rejected fresh serve refreshed", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && cached.view == 2 && !cached.refreshing
	})
	if got := builds.Load(); got != 2 {
		t.Fatalf("%d builds, want the warm one and one refresh", got)
	}
	fresh, err := cache.Get(t.Context(), 1, func(context.Context) (uint64, error) {
		builds.Add(1)
		return 3, nil
	})
	if err != nil || fresh != 2 || builds.Load() != 2 {
		t.Fatalf("fresh after refresh: %d %v builds %d", fresh, err, builds.Load())
	}
}

// A build that panics must finalize the single-flight entry: the next caller
// retries instead of waiting forever on readiness that never closes.
func TestPanicReleasesWaiters(t *testing.T) {
	cache := New[string, int](Options[string, int]{TTL: time.Minute, Size: 2, Now: func() time.Time { return time.Unix(0, 0) }})
	if _, err := cache.Get(t.Context(), "k", func(context.Context) (int, error) {
		panic("boom")
	}); err == nil {
		t.Fatal("a panicking build returned no error")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	got, err := cache.Get(ctx, "k", func(context.Context) (int, error) { return 1, nil })
	if err != nil || got != 1 {
		t.Fatalf("retry after a panic: %d %v", got, err)
	}
}

// The bound holds: the oldest entry is evicted and rebuilds on demand.
func TestIsBounded(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	const size = 4
	cache := New[int, int](Options[int, int]{TTL: time.Minute, Size: size, Now: clk.Now, Component: "viewcache-test"})
	var builds atomic.Int64
	build := func(context.Context) (int, error) { return int(builds.Add(1)), nil }
	if _, err := cache.Get(t.Context(), 1, build); err != nil {
		t.Fatal(err)
	}
	for key := 2; key <= size+1; key++ {
		clk.Advance(time.Second) // distinct ages: the oldest is unambiguous
		if _, err := cache.Get(t.Context(), key, build); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	n := len(cache.entries)
	cache.mu.Unlock()
	if n != size {
		t.Fatalf("%d entries, want the bound %d", n, size)
	}
	before := builds.Load()
	if _, err := cache.Get(t.Context(), 1, build); err != nil {
		t.Fatal(err)
	}
	if builds.Load() != before+1 {
		t.Fatal("the evicted entry was still served")
	}
}

// A never-expiring cache keeps an installed value however old it is, and an
// eviction still rebuilds. Use it for a value whose key covers every input
// that can change it: a TTL would re-derive an identical value.
func TestNeverExpiresServesAnOldEntryUntilEvicted(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: time.Second, NeverExpires: true, Size: 1, Now: clk.Now, Component: "viewcache-test"})
	var builds atomic.Int64
	build := func(context.Context) (string, error) {
		builds.Add(1)
		return "v", nil
	}
	if got, err := cache.Get(t.Context(), 1, build); err != nil || got != "v" {
		t.Fatalf("%q %v", got, err)
	}
	// Well past the TTL, the entry is still fresh: no rebuild, no wait.
	clk.Advance(24 * time.Hour)
	if got, err := cache.Get(t.Context(), 1, build); err != nil || got != "v" {
		t.Fatalf("%q %v", got, err)
	}
	// A rebuild that wrongly started would run at once; give it a moment to
	// show before asserting that none did.
	for deadline := time.Now().Add(200 * time.Millisecond); builds.Load() == 1 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("%d builds for a never-expiring entry, want 1", got)
	}
	// The bound still applies: a second key evicts the first, which rebuilds.
	if _, err := cache.Get(t.Context(), 2, build); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), 1, build); err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 3 {
		t.Fatalf("%d builds, want 3 (one per key plus the evicted rebuild)", got)
	}
}
