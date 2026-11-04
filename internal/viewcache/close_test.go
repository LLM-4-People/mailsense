package viewcache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

// warmCache installs one fresh value under key 1 and returns the cache.
func warmCache(t *testing.T) (*Cache[int, string], *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: 10 * time.Second, Size: 4, Now: clk.Now,
		Component: "viewcache-test"})
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) { return "v1", nil }); err != nil {
		t.Fatal(err)
	}
	return cache, clk
}

// Close joins a refresh that is still inside its build, then refuses to start
// another one: the expired snapshot is served as it stands.
func TestCloseJoinsASlowRefreshAndStartsNoMore(t *testing.T) {
	cache, clk := warmCache(t)
	clk.Advance(11 * time.Second)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var builds atomic.Int64
	build := func(context.Context) (string, error) {
		builds.Add(1)
		started <- struct{}{}
		<-release
		return "v2", nil
	}
	// The refresh itself runs under the cache's base context, so Close has to
	// cancel it: this build waits only on release, and Close must still join.
	if got, err := cache.Get(t.Context(), 1, build); err != nil || got != "v1" {
		t.Fatalf("an expired entry did not serve its snapshot: %q %v", got, err)
	}
	<-started

	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- cache.Close(ctx)
	}()
	// Close must not return while the build is in flight.
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) before the refresh finished", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join a refresh that finished")
	}

	// No refresh starts after Close: the expired snapshot is the answer. The
	// refresh that was in flight when Close began is canceled, and its result
	// is refused, so the last value Close saw stays installed.
	clk.Advance(time.Hour)
	got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v3", nil
	})
	if err != nil || got != "v1" {
		t.Fatalf("post-Close Get = %q %v, want the last installed v1", got, err)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("%d post-warm builds, want exactly the one joined refresh", n)
	}
}

// A Get that arrives after Close with nothing installed returns ErrClosed and
// never reads the store, while a key whose only build failed still reports
// that failure.
func TestGetAfterCloseServesOrReportsButNeverBuilds(t *testing.T) {
	cache, _ := warmCache(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !cache.Close(ctx) {
		t.Fatal("Close did not join an idle cache")
	}

	// An installed value is served without the Keep gate, even once stale.
	cache.keep = func(int, string) bool { return false }
	if got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		t.Error("a closed cache ran a build for an installed key")
		return "", nil
	}); err != nil || got != "v1" {
		t.Fatalf("post-Close installed key = %q %v", got, err)
	}

	// A key with nothing installed cannot be built: ErrClosed, no store read.
	var builds atomic.Int64
	_, err := cache.Get(t.Context(), 2, func(context.Context) (string, error) {
		builds.Add(1)
		return "v", nil
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("post-Close missing key error = %v, want ErrClosed", err)
	}
	if builds.Load() != 0 {
		t.Fatal("a closed cache read the store")
	}
}

// A failed first build's error stays readable after Close: the caller that
// owned it is not handed ErrClosed as well.
func TestGetAfterCloseReportsAFailedBuildError(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{TTL: time.Minute, Size: 2, Now: clk.Now})
	boom := errors.New("boom")
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("first build error %v", err)
	}
	// Reinstall the failure with a waiter so it survives the build's exit,
	// which is exactly the state a shutdown can land in.
	cache.mu.Lock()
	cached := &entry[string]{at: clk.Now(), done: true, err: boom, ready: make(chan struct{}), waiters: 1}
	close(cached.ready)
	cache.entries[2] = cached
	cache.mu.Unlock()

	if !cache.Close(t.Context()) {
		t.Fatal("Close did not join an idle cache")
	}
	if _, err := cache.Get(t.Context(), 2, func(context.Context) (string, error) { return "v", nil }); !errors.Is(err, boom) {
		t.Fatalf("post-Close failed-build key error = %v, want boom", err)
	}
}

// Close is bounded by the caller's context and reports a miss without
// hanging; a later Close observes the join it missed.
func TestCloseIsBoundedByIdempotentAndRepeatable(t *testing.T) {
	cache, clk := warmCache(t)
	clk.Advance(11 * time.Second)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		started <- struct{}{}
		<-release
		return "v2", nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if cache.Close(expired) {
		t.Fatal("Close reported a join with an already expired context")
	}

	// Idempotent and concurrent: every later caller waits on the same drain,
	// none starts a build.
	var wg sync.WaitGroup
	out := make([]bool, 3)
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out[i] = cache.Close(ctx)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, joined := range out {
		if !joined {
			t.Fatalf("concurrent Close %d did not join after the build finished", i)
		}
	}
	// Closing an already-joined cache is still true.
	if !cache.Close(t.Context()) {
		t.Fatal("a second Close of a joined cache reported a miss")
	}
}

// A refresh racing Close either installs completely before Close returns or
// does not install at all: the entry is never left half-written or flagged as
// refreshing after the join.
func TestRefreshRacingCloseInstallsFullyOrNotAtAll(t *testing.T) {
	for round := range 40 {
		cache, clk := warmCache(t)
		clk.Advance(11 * time.Second)
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var once sync.Once
		build := func(context.Context) (string, error) {
			once.Do(func() { entered <- struct{}{} })
			<-release
			return "v2", nil
		}
		if _, err := cache.Get(t.Context(), 1, build); err != nil {
			t.Fatal(err)
		}
		<-entered

		closed := make(chan bool, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			closed <- cache.Close(ctx)
		}()
		// Both orders race for real: the install and the join touch the same
		// lock, so one always completes before the other observes it.
		close(release)
		if !<-closed {
			t.Fatalf("round %d: Close did not join the racing refresh", round)
		}
		cache.mu.Lock()
		cached, installed := cache.entries[1]
		cache.mu.Unlock()
		if !installed || cached == nil {
			t.Fatalf("round %d: the entry vanished across Close", round)
		}
		if cached.refreshing {
			t.Fatalf("round %d: Close returned with a refresh still flagged", round)
		}
		if cached.view != "v1" && cached.view != "v2" {
			t.Fatalf("round %d: torn install: %q", round, cached.view)
		}
	}
}

// A canceled Close leaves the cache closed and every later Get on an
// installed key serving the snapshot, so shutdown cannot resurrect a reader.
func TestCloseKeepsTheCacheClosedAfterAMiss(t *testing.T) {
	cache, clk := warmCache(t)
	clk.Advance(11 * time.Second)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		started <- struct{}{}
		<-release
		return "v2", nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if cache.Close(expired) {
		t.Fatal("Close joined on an expired context")
	}
	var builds atomic.Int64
	if got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		builds.Add(1)
		return "v3", nil
	}); err != nil || got != "v1" {
		t.Fatalf("a Get after a missed join = %q %v, want the snapshot", got, err)
	}
	if builds.Load() != 0 {
		t.Fatal("a Get after a missed join started a build")
	}
	close(release)
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !cache.Close(ctx) {
		t.Fatal("the repeated Close did not observe the join")
	}
}
