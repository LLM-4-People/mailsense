// Package viewcache is the bounded, single-flight, stale-while-revalidate
// cache shared by the read services.
//
// The generations a caller reads carry correctness: a caller that can put them
// in the key gets another entry after a write, and a caller with one fixed key
// puts them in the value and checks them in Options.Keep, so a value built
// before the write is treated as stale. The TTL bounds only the staleness a
// generation cannot see, the entry bound keeps a paging session's memory
// small, and the byte bound (MaxBytes and Weigh) caps the retained values of
// one cache. An expired entry is served immediately while one detached,
// timeout-bounded rebuild runs. Entries are immutable once installed: Get
// never mutates a value it returns.
//
// A detached rebuild outlives the request that started it, so it is also
// invisible to the API's handler join. Close is that missing join: it stops
// new rebuilds and waits for the in-flight ones, so the store owner can close
// the store without a reader behind it.
package viewcache

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/safe"
)

// defaultRefreshTimeout bounds one background rebuild when Options leaves it
// unset, so a stuck store read cannot keep a stale snapshot installed forever.
const defaultRefreshTimeout = 2 * time.Minute

// ErrClosed reports that a cache has nothing installed for the key and no
// longer builds: Get would have to read the store, which Close just made the
// caller's responsibility to close.
var ErrClosed = errors.New("viewcache: cache is closed")

// Options configures one cache. K should identify every input that changes
// the snapshot; a service with one fixed key puts those inputs in V and checks
// them in Keep instead.
type Options[K comparable, V any] struct {
	// TTL is how long a snapshot is fresh. After it, Get serves the snapshot
	// and starts one background rebuild.
	TTL time.Duration
	// NeverExpires keeps an installed snapshot until Keep rejects it or the
	// entry is evicted. Use it for a value whose key already covers every
	// input that can change it (the search query cache keys the vector space
	// and the folded text), where a TTL would only rebuild an identical
	// value.
	NeverExpires bool
	// Size is the maximum number of entries; 0 means 1.
	Size int
	// MaxBytes bounds the estimated retained bytes of the installed values;
	// 0 means no byte bound. When set, Weigh is required: New panics
	// without it, because a byte bound that cannot measure is the unbounded
	// cache this field exists to remove.
	MaxBytes int
	// Weigh estimates how many bytes one value retains. It runs once per
	// install or refresh, under the cache lock, never on the Get path, and
	// its result is clamped to zero, so it must return a non-negative,
	// cheap estimate. It is required when MaxBytes is set and ignored when
	// nil.
	Weigh func(V) int
	// RefreshTimeout bounds one background rebuild; 0 means 2 minutes.
	RefreshTimeout time.Duration
	// Now reads the clock; nil means time.Now.
	Now func() time.Time
	// Component is the logx component a failed refresh reports under.
	Component string
	// Keep, when set, decides whether a successful build may be installed and
	// whether an installed value may still be served. It runs outside the
	// cache lock, so it may read a store generation.
	//
	// False on a build installs nothing: a first build leaves no entry (its
	// foreground caller still gets the value, and waiters retry), while a
	// refresh keeps the stale snapshot and clears the refresh flag so a later
	// Get retries. False on an installed value treats it as stale: Get serves
	// that value and starts one background rebuild.
	//
	// It exists for a build that raced a write: a snapshot is never installed
	// after the generation it was keyed by moved. A service with one fixed key
	// uses it on the serve path too, so a moved generation refreshes at once
	// instead of waiting for the TTL.
	Keep func(K, V) bool
}

// Cache is one bounded, single-flight, stale-while-revalidate cache.
type Cache[K comparable, V any] struct {
	mu           sync.Mutex
	entries      map[K]*entry[V]
	ttl          time.Duration
	neverExpires bool
	size         int
	maxBytes     int
	weigh        func(V) int
	// bytes is the total estimated retained bytes of the installed values,
	// guarded by mu. A placeholder, a failed build and a rejected Keep
	// install account zero; a replacement swaps the entry's estimate.
	bytes          int
	refreshTimeout time.Duration
	now            func() time.Time
	keep           func(K, V) bool
	log            *slog.Logger
	// base is canceled by Close, so a cooperative rebuild stops at once
	// instead of running out its RefreshTimeout under a closing store.
	base   context.Context
	cancel context.CancelFunc
	// jobs tracks every build the cache owns: the foreground build of a
	// missing key and every detached rebuild. Its closed state is the one
	// "no more store reads" flag, so Get and Close cannot disagree.
	jobs *join.Group
}

// entry is one cached snapshot, or the placeholder of a build in flight (set
// false). ready closes once the first build of the entry finished, so
// concurrent callers of a missing key share that one build.
type entry[V any] struct {
	at   time.Time
	view V
	set  bool
	// bytes is Weigh's estimate of view while set, 0 on a placeholder. It
	// is written only under the cache lock, at install or refresh.
	bytes      int
	done       bool // the first build finished (successfully or not)
	err        error
	ready      chan struct{}
	refreshing bool
	waiters    int // callers waiting on ready
	// stamp counts the installs of this entry. Get captures it before it
	// leaves the lock to run Keep, so a rebuild that installed meanwhile is
	// recognised instead of being started a second time.
	stamp uint64
}

// New returns one cache with the given bounds.
func New[K comparable, V any](opts Options[K, V]) *Cache[K, V] {
	if opts.MaxBytes > 0 && opts.Weigh == nil {
		panic("viewcache: Options.MaxBytes requires Options.Weigh")
	}
	size := opts.Size
	if size <= 0 {
		size = 1
	}
	refresh := opts.RefreshTimeout
	if refresh <= 0 {
		refresh = defaultRefreshTimeout
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	component := opts.Component
	if component == "" {
		component = "viewcache"
	}
	base, cancel := context.WithCancel(context.Background())
	return &Cache[K, V]{
		entries:        map[K]*entry[V]{},
		ttl:            opts.TTL,
		neverExpires:   opts.NeverExpires,
		size:           size,
		maxBytes:       opts.MaxBytes,
		weigh:          opts.Weigh,
		refreshTimeout: refresh,
		now:            now,
		keep:           opts.Keep,
		log:            logx.For(component),
		base:           base,
		cancel:         cancel,
		jobs:           join.New(),
	}
}

// Get returns the snapshot of key. A fresh entry is returned at once. A
// missing entry is built here, once: concurrent callers wait for that build
// instead of reading the store again. An expired entry, or a fresh one that
// Keep rejects, is returned at once while one background rebuild runs. A
// failed first build fails its builder and everyone already waiting on it,
// then leaves nothing installed; a failed refresh keeps the last snapshot and
// logs.
//
// After Close, Get never reads the store again. An installed entry is
// returned as it stands — expired or not, and without the Keep gate, which
// exists only to decide whether a rebuild is needed. A key with nothing
// installed returns the failed first build's error while that build's record
// is still observable, and ErrClosed when no build ran (or one is still in
// flight and will be joined by Close). That is the honest answer during
// shutdown: the data the store would supply is no longer ours to read.
func (c *Cache[K, V]) Get(ctx context.Context, key K, build func(context.Context) (V, error)) (V, error) {
	var zero V
	for {
		c.mu.Lock()
		cached := c.entries[key]
		if cached == nil {
			// Track under the same lock that installs the placeholder:
			// Close either sees this task or refuses it, never neither.
			if !c.jobs.Track() {
				c.mu.Unlock()
				return zero, ErrClosed
			}
			cached = &entry[V]{at: c.now(), ready: make(chan struct{})}
			c.installLocked(key, cached)
			c.mu.Unlock()
			return c.buildFirst(ctx, key, cached, build)
		}
		if !cached.set {
			if c.jobs.Closed() {
				// A build in flight is joined by Close; a finished failed
				// build still answers its own error.
				done, err := cached.done, cached.err
				c.mu.Unlock()
				if done && err != nil {
					return zero, err
				}
				return zero, ErrClosed
			}
			if cached.done && cached.err != nil && cached.waiters == 0 {
				// A failed build no one is left to observe: a fresh caller
				// does not inherit the error.
				c.removeLocked(key)
				c.mu.Unlock()
				continue
			}
			cached.waiters++
			ready := cached.ready
			c.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				c.mu.Lock()
				cached.waiters--
				if c.entries[key] == cached && cached.waiters == 0 && cached.done && cached.err != nil {
					c.removeLocked(key)
				}
				c.mu.Unlock()
				return zero, ctx.Err()
			}
			c.mu.Lock()
			same := c.entries[key] == cached
			view, set, err := cached.view, cached.set, cached.err
			cached.waiters--
			if same && !set && cached.done && cached.err != nil && cached.waiters == 0 {
				c.removeLocked(key)
			}
			c.mu.Unlock()
			switch {
			case same && set:
				return view, nil
			case same && !set && err != nil:
				return zero, err
			}
			continue // the entry was evicted or rejected while building: build again
		}
		view := cached.view
		if c.jobs.Closed() {
			// The last installed value is the whole answer after Close: no
			// store read, no rebuild.
			c.mu.Unlock()
			return view, nil
		}
		if c.neverExpires || c.now().Sub(cached.at) < c.ttl {
			if c.keep == nil {
				c.mu.Unlock()
				return view, nil
			}
			// The serve gate reads a store generation, so it runs without
			// the cache lock. A value whose generation moved is stale: serve
			// it once while the refresh below rebuilds it.
			stamp := cached.stamp
			c.mu.Unlock()
			if c.keep(key, view) {
				return view, nil
			}
			c.mu.Lock()
			// A rebuild that was running (or finished) while Keep read the
			// store may have installed this very generation. Starting
			// another one would scan the store twice for one write, so
			// re-read the entry instead.
			if c.entries[key] != cached || cached.stamp != stamp {
				c.mu.Unlock()
				continue
			}
		}
		if !cached.refreshing {
			if !c.jobs.Track() {
				c.mu.Unlock()
				return view, nil
			}
			cached.refreshing = true
			c.mu.Unlock()
			go c.refresh(key, cached, build)
			return view, nil
		}
		c.mu.Unlock()
		return view, nil
	}
}

// buildFirst runs and finalizes the one build of a missing key. The task is
// tracked before the placeholder is installed; the deferred Done is what
// Close waits on, so it covers the install and the release of the waiters.
// The build honors both the caller's context and Close, so shutdown ends it
// instead of waiting out a store read.
func (c *Cache[K, V]) buildFirst(ctx context.Context, key K, cached *entry[V], build func(context.Context) (V, error)) (V, error) {
	defer c.jobs.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.base, cancel)
	defer stop()
	view, err := safe.Value(func() (V, error) { return build(ctx) })
	// Keep runs outside the lock: it may read a store generation.
	keep := err == nil && (c.keep == nil || c.keep(key, view))
	c.mu.Lock()
	cached.err, cached.done = err, true
	switch {
	case err == nil && keep && c.entries[key] == cached:
		c.installValueLocked(key, cached, view)
		cached.stamp++
	case err == nil && c.entries[key] == cached:
		// A rejected install leaves nothing behind.
		c.removeLocked(key)
	case err != nil && c.entries[key] == cached && cached.waiters == 0:
		// With no waiter to share the failure with, the failed
		// build leaves nothing behind; the next call builds again.
		c.removeLocked(key)
	}
	close(cached.ready)
	c.mu.Unlock()
	return view, err
}

// refresh rebuilds one expired snapshot off the request path. The build runs
// under the cache's base context, so Close cancels it at once instead of
// letting it read a store its owner is closing, and it is still bounded by
// RefreshTimeout. A result is installed only while this exact entry is still
// installed and the cache is open, so an eviction, a newer build or a
// shutdown is never overwritten.
func (c *Cache[K, V]) refresh(key K, cached *entry[V], build func(context.Context) (V, error)) {
	defer c.jobs.Done()
	ctx, cancel := context.WithTimeout(c.base, c.refreshTimeout)
	defer cancel()
	view, err := safe.Value(func() (V, error) { return build(ctx) })
	// Keep runs outside the lock: it may read a store generation.
	keep := err == nil && (c.keep == nil || c.keep(key, view))
	c.mu.Lock()
	if c.entries[key] == cached {
		if err == nil && keep && !c.jobs.Closed() {
			c.installValueLocked(key, cached, view)
			cached.refreshing = false
			cached.stamp++
		} else {
			cached.refreshing = false
		}
	}
	closed := c.jobs.Closed()
	c.mu.Unlock()
	if err != nil && !closed {
		c.log.Warn("view cache refresh failed; serving the cached view", "err", err)
	}
}

// Close refuses new builds, cancels the rebuilds already running and waits
// for every build this cache owns to finish, bounded by ctx. It reports
// whether they joined; false means ctx ended first, so the store owner must
// keep the store open because a build may still be reading it.
//
// It is idempotent and safe for concurrent callers. A cache that was closed
// with an expired ctx can be closed again to observe the join it missed.
// Because every build is tracked under the same lock that admits it, and
// Close never holds that lock across a build, there is no deadlock with a
// finishing install: the install completes, then the join returns.
func (c *Cache[K, V]) Close(ctx context.Context) bool {
	c.cancel()
	return c.jobs.Close(ctx)
}

// Len is how many entries the cache holds: a value plus any build in flight.
// It is the entry count a caller's Size bound promises to keep.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Bytes is the estimated retained bytes of the installed values: the total
// MaxBytes bounds (zero while no Weigh is set), except that one value larger
// than the bound stays alone (see MaxBytes). Placeholders, failed builds and
// rejected Keep installs account zero, so Bytes never counts a value the
// cache does not hold.
func (c *Cache[K, V]) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// installLocked inserts a placeholder for a key the caller has shown to be
// absent and enforces both bounds. A build in flight counts as young (its
// placeholder carries the instant it was installed), so a burst of first
// builds cannot evict one another. The caller holds mu.
func (c *Cache[K, V]) installLocked(key K, cached *entry[V]) {
	c.entries[key] = cached
	c.trimLocked(key)
}

// installValueLocked records one built value on the entry installed under
// key, replacing the entry's previous estimate, and enforces both bounds. The
// caller holds mu and has established that cached is the entry the map holds.
func (c *Cache[K, V]) installValueLocked(key K, cached *entry[V], view V) {
	c.bytes -= cached.bytes
	cached.view, cached.set, cached.at = view, true, c.now()
	// A negative estimate would silently buy budget; clamp it to zero.
	cached.bytes = 0
	if c.weigh != nil {
		if n := c.weigh(view); n > 0 {
			cached.bytes = n
		}
	}
	c.bytes += cached.bytes
	c.trimLocked(key)
}

// trimLocked evicts the oldest entries until the entry bound and the byte
// bound hold, never evicting the entry under key (the one just installed or
// refreshed): a single value larger than MaxBytes stays alone, because
// serving the fresh value beats enforcing the budget. The caller holds mu.
func (c *Cache[K, V]) trimLocked(key K) {
	for len(c.entries) > c.size || (c.maxBytes > 0 && c.bytes > c.maxBytes) {
		var oldestKey K
		var oldest *entry[V]
		for candidate, existing := range c.entries {
			if candidate == key {
				continue
			}
			if oldest == nil || existing.at.Before(oldest.at) {
				oldestKey, oldest = candidate, existing
			}
		}
		if oldest == nil {
			return // only the just-installed entry is left
		}
		c.removeLocked(oldestKey)
	}
}

// removeLocked drops one entry and its estimated bytes. The caller holds mu.
func (c *Cache[K, V]) removeLocked(key K) {
	if existing := c.entries[key]; existing != nil {
		c.bytes -= existing.bytes
		delete(c.entries, key)
	}
}
