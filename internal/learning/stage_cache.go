package learning

import "sync"

// StageCache is the one cross-build cache of a stage's answer. A stage
// owns its key: it fingerprints exactly the inputs its Status reads (the
// store generations, the settings snapshot version, the run sequence) and
// asks the cache before doing the work. The cache holds one entry: a stage
// reports one status, and keeping its history would grow with every write.
//
// A status is reused only while the key is unchanged. A Running status is
// never stored: a job's progress, ETA, stage and detail come from the live
// run, never from a stale frame. A failed read is never stored either, so
// the next status read retries a transient store or provider failure.
//
// The caller reads the key before building the status. A write that lands
// during the build advances its generation, so the result is kept under
// the old key and the next read recomputes; the reverse order could
// publish the new key with the old rows (the rule the index coverage cache
// follows).
type StageCache struct {
	mu     sync.Mutex
	key    string
	status StageStatus
	valid  bool
}

// Status returns the cached answer for key, or runs build and keeps its
// stable answer. The returned status is read-only, like every answer the
// status memo shares within one build: callers must not mutate it. build
// must not be called with the lock held.
func (c *StageCache) Status(key string, build func() (StageStatus, error)) (StageStatus, error) {
	c.mu.Lock()
	if c.valid && c.key == key {
		st := c.status
		c.mu.Unlock()
		return st, nil
	}
	c.mu.Unlock()

	st, err := build()
	if err != nil || st.State == Running {
		return st, err
	}
	c.mu.Lock()
	c.key, c.status, c.valid = key, st, true
	c.mu.Unlock()
	return st, nil
}
