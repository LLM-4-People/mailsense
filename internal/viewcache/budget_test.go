package viewcache

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixturewait"
)

// A cache over its byte budget evicts the oldest entries until the total
// fits, and Bytes reports the retained estimate, not the budget.
func TestBytesBoundEvictsOldestUntilItFits(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	const budget = 100
	cache := New[int, string](Options[int, string]{
		TTL: time.Minute, Size: 8, MaxBytes: budget, Weigh: func(v string) int { return len(v) },
		Now: clk.Now, Component: "viewcache-test",
	})
	build := func(context.Context) (string, error) { return strings.Repeat("x", 40), nil }
	for key := 1; key <= 3; key++ {
		clk.Advance(time.Second) // distinct ages: the oldest is unambiguous
		if _, err := cache.Get(t.Context(), key, build); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := cache.Len(), 2; got != want {
		t.Fatalf("len %d, want %d (three 40-byte values under a %d-byte budget)", got, want, budget)
	}
	if got, want := cache.Bytes(), 80; got != want {
		t.Fatalf("bytes %d, want the two retained values (%d)", got, want)
	}
	cache.mu.Lock()
	_, evicted := cache.entries[1]
	_, kept2 := cache.entries[2]
	_, kept3 := cache.entries[3]
	cache.mu.Unlock()
	if evicted || !kept2 || !kept3 {
		t.Fatalf("evicted=%v kept2=%v kept3=%v, want the oldest evicted only", evicted, kept2, kept3)
	}
	before := cache.Bytes()
	if _, err := cache.Get(t.Context(), 2, build); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(t.Context(), 3, build); err != nil {
		t.Fatal(err)
	}
	if cache.Bytes() != before {
		t.Fatal("serving a retained entry changed the accounted bytes")
	}
}

// One value larger than the whole budget stays alone: the just-installed
// entry is never evicted, and the next install evicts it as the oldest.
func TestBytesBoundKeepsAnOversizedEntryAlone(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	const budget = 100
	cache := New[int, string](Options[int, string]{
		TTL: time.Minute, Size: 4, MaxBytes: budget, Weigh: func(v string) int { return len(v) },
		Now: clk.Now, Component: "viewcache-test",
	})
	build := func(content string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return content, nil }
	}
	if _, err := cache.Get(t.Context(), 1, build(strings.Repeat("a", 30))); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if _, err := cache.Get(t.Context(), 2, build(strings.Repeat("b", 1000))); err != nil {
		t.Fatal(err)
	}
	if got, want := cache.Len(), 1; got != want {
		t.Fatalf("len %d, want %d: the oversized entry must stay alone", got, want)
	}
	if got, want := cache.Bytes(), 1000; got != want {
		t.Fatalf("bytes %d, want the oversized value (%d), not the budget", got, want)
	}
	cache.mu.Lock()
	_, smaller := cache.entries[1]
	_, oversized := cache.entries[2]
	cache.mu.Unlock()
	if smaller || !oversized {
		t.Fatalf("smaller=%v oversized=%v, want the smaller entry evicted", smaller, oversized)
	}
	// A later install evicts the oversized entry (it is now the oldest not
	// being installed) instead of dropping the fresh value.
	clk.Advance(time.Second)
	if _, err := cache.Get(t.Context(), 3, build(strings.Repeat("c", 30))); err != nil {
		t.Fatal(err)
	}
	if got, want := cache.Len(), 1; got != want {
		t.Fatalf("len %d, want %d", got, want)
	}
	if got, want := cache.Bytes(), 30; got != want {
		t.Fatalf("bytes %d, want the fresh value (%d)", got, want)
	}
	cache.mu.Lock()
	_, oversized = cache.entries[2]
	_, fresh := cache.entries[3]
	cache.mu.Unlock()
	if oversized || !fresh {
		t.Fatalf("oversized=%v fresh=%v, want the oversized entry evicted for the fresh one", oversized, fresh)
	}
}

// Replacing a value subtracts the replaced estimate: a refresh of a larger
// value counts the new size only, and a failed refresh keeps the old one.
func TestReplacementSubtractsTheReplacedBytes(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{
		TTL: 10 * time.Second, Size: 2, MaxBytes: 1000, Weigh: func(v string) int { return len(v) },
		Now: clk.Now, Component: "viewcache-test",
	})
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		return strings.Repeat("a", 60), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := cache.Bytes(), 60; got != want {
		t.Fatalf("bytes %d, want %d", got, want)
	}

	clk.Advance(11 * time.Second)
	if got, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		return strings.Repeat("b", 80), nil
	}); err != nil || len(got) != 60 {
		t.Fatalf("an expired entry did not serve its snapshot: %q %v", got, err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the refresh installed its larger value", func() bool {
		return cache.Bytes() == 80
	})

	clk.Advance(11 * time.Second)
	if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
		return "", context.DeadlineExceeded
	}); err != nil {
		t.Fatal(err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the failed refresh cleared its flag", func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		cached := cache.entries[1]
		return cached != nil && !cached.refreshing
	})
	if got, want := cache.Bytes(), 80; got != want {
		t.Fatalf("a failed refresh changed the accounted bytes to %d, want %d", got, want)
	}
}

// A refresh that grows one value over the budget evicts the older entries,
// never the value just refreshed.
func TestRefreshOverTheBudgetEvictsOlderEntries(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{
		TTL: 10 * time.Second, Size: 4, MaxBytes: 100, Weigh: func(v string) int { return len(v) },
		Now: clk.Now, Component: "viewcache-test",
	})
	build := func(size int) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return strings.Repeat("x", size), nil }
	}
	if _, err := cache.Get(t.Context(), 1, build(40)); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if _, err := cache.Get(t.Context(), 2, build(40)); err != nil {
		t.Fatal(err)
	}
	if got, want := cache.Bytes(), 80; got != want {
		t.Fatalf("bytes %d, want %d", got, want)
	}

	clk.Advance(11 * time.Second) // key 1 is expired, key 2 is not
	if _, err := cache.Get(t.Context(), 1, build(90)); err != nil {
		t.Fatal(err)
	}
	fixturewait.WaitUntil(t, 5*time.Second, "the refresh trimmed the older entry", func() bool {
		return cache.Bytes() == 90 && cache.Len() == 1
	})
	cache.mu.Lock()
	_, kept := cache.entries[1]
	_, evicted := cache.entries[2]
	cache.mu.Unlock()
	if !kept || evicted {
		t.Fatalf("kept=%v evicted=%v, want the refreshed entry kept", kept, evicted)
	}
}

// A failed build, a Keep-rejected install and an in-flight placeholder
// account zero bytes: Bytes never counts a value the cache does not hold.
func TestUnmeasuredInstallsAccountZero(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))

	t.Run("failed first build", func(t *testing.T) {
		cache := New[int, string](Options[int, string]{
			TTL: time.Minute, Size: 4, MaxBytes: 1000, Weigh: func(v string) int { return len(v) },
			Now: clk.Now, Component: "viewcache-test",
		})
		if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
			return "", context.DeadlineExceeded
		}); err == nil {
			t.Fatal("the failed build returned no error")
		}
		if cache.Bytes() != 0 || cache.Len() != 0 {
			t.Fatalf("a failed build left bytes=%d len=%d, want 0/0", cache.Bytes(), cache.Len())
		}
	})

	t.Run("rejected keep install", func(t *testing.T) {
		cache := New[int, string](Options[int, string]{
			TTL: time.Minute, Size: 4, MaxBytes: 1000, Weigh: func(v string) int { return len(v) },
			Now: clk.Now, Component: "viewcache-test",
			Keep: func(int, string) bool { return false },
		})
		if _, err := cache.Get(t.Context(), 1, func(context.Context) (string, error) {
			return strings.Repeat("x", 50), nil
		}); err != nil {
			t.Fatal(err)
		}
		if cache.Bytes() != 0 || cache.Len() != 0 {
			t.Fatalf("a rejected install left bytes=%d len=%d, want 0/0", cache.Bytes(), cache.Len())
		}
	})

	t.Run("placeholder in flight", func(t *testing.T) {
		cache := New[int, string](Options[int, string]{
			TTL: time.Minute, Size: 4, MaxBytes: 1000, Weigh: func(v string) int { return len(v) },
			Now: clk.Now, Component: "viewcache-test",
		})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := cache.Get(context.Background(), 1, func(context.Context) (string, error) {
				<-release
				return strings.Repeat("x", 10), nil
			})
			done <- err
		}()
		fixturewait.WaitUntil(t, 5*time.Second, "the placeholder is installed", func() bool {
			return cache.Len() == 1
		})
		if got := cache.Bytes(); got != 0 {
			t.Fatalf("an in-flight placeholder accounted %d bytes, want 0", got)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got, want := cache.Bytes(), 10; got != want {
			t.Fatalf("bytes %d after the build, want %d", got, want)
		}
	})
}

// MaxBytes 0 keeps the cache unbounded by bytes: Size still evicts, and a
// caller-supplied Weigh is still measured (Bytes reports it) without ever
// evicting.
func TestMaxBytesZeroLeavesTheCacheUnbounded(t *testing.T) {
	clk := clocktest.New(time.Unix(0, 0))
	cache := New[int, string](Options[int, string]{
		TTL: time.Minute, Size: 2, Weigh: func(string) int { return 1 << 30 },
		Now: clk.Now, Component: "viewcache-test",
	})
	build := func(context.Context) (string, error) { return "v", nil }
	for key := 1; key <= 2; key++ {
		clk.Advance(time.Second)
		if _, err := cache.Get(t.Context(), key, build); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := cache.Bytes(), 2<<30; got != want {
		t.Fatalf("bytes %d, want the measured total %d", got, want)
	}
	clk.Advance(time.Second)
	if _, err := cache.Get(t.Context(), 3, build); err != nil {
		t.Fatal(err)
	}
	if got, want := cache.Len(), 2; got != want {
		t.Fatalf("len %d, want the Size bound %d", got, want)
	}
	cache.mu.Lock()
	_, present := cache.entries[1]
	cache.mu.Unlock()
	if present {
		t.Fatal("Size no longer evicted after MaxBytes was left at 0")
	}
}

// MaxBytes without Weigh is a construction error: a byte bound that cannot
// measure would silently stay unbounded.
func TestMissingWeighPanicsAtNew(t *testing.T) {
	const wantPanic = "viewcache: Options.MaxBytes requires Options.Weigh"
	cases := []struct {
		name     string
		maxBytes int
		weigh    func(string) int
		want     string
	}{
		{name: "max bytes without weigh", maxBytes: 100, weigh: nil, want: wantPanic},
		{name: "max bytes with weigh", maxBytes: 100, weigh: func(string) int { return 1 }},
		{name: "no max bytes without weigh", maxBytes: 0, weigh: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			func() {
				defer func() {
					if r := recover(); r != nil {
						got, _ = r.(string)
					}
				}()
				New[string, string](Options[string, string]{
					TTL: time.Minute, Size: 1, MaxBytes: tc.maxBytes, Weigh: tc.weigh,
					Now: func() time.Time { return time.Unix(0, 0) },
				})
			}()
			if got != tc.want {
				t.Fatalf("panic %q, want %q", got, tc.want)
			}
		})
	}
}

// Weigh runs once when a value is installed, never on the Get path.
func TestWeighRunsOncePerInstallNotPerGet(t *testing.T) {
	var weighs atomic.Int64
	cache := New[int, string](Options[int, string]{
		TTL: time.Minute, Size: 2, MaxBytes: 8,
		Weigh: func(string) int { weighs.Add(1); return 4 },
		Now:   func() time.Time { return time.Unix(0, 0) },
	})
	build := func(context.Context) (string, error) { return "v", nil }
	for range 5 {
		if _, err := cache.Get(t.Context(), 1, build); err != nil {
			t.Fatal(err)
		}
	}
	if got := weighs.Load(); got != 1 {
		t.Fatalf("Weigh ran %d times for one install and five Gets, want 1", got)
	}
}
