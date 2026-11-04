package learning

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// ProgressRefreshInterval bounds how often a running job's progress may
// invalidate the learning status: at most one rebuild per interval, on the
// trailing edge of a burst. Index runs publish progress per batch; without
// this the status would be rebuilt for every batch on top of the API reads.
const ProgressRefreshInterval = time.Second

// RefreshOn recomputes the status (and publishes a change) after every
// event of topic — a sync, a discovery, an index run, a settings change —
// so the cached status the gate answers from stays current. stop ends it.
func RefreshOn[T any](m *Machine, bus *events.Bus, topic events.Topic[T], buffer int) (stop func(), err error) {
	return events.On(bus, topic, buffer, func(T) {
		m.Invalidate()
		ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
		defer cancel()
		if _, err := m.Status(ctx); err != nil {
			m.log.Warn("learning status not refreshed", "after", topic.Name(), "err", err)
		}
	})
}

// RefreshOnProgress is the one owner of progress-driven invalidation. It
// coalesces a burst of topic events into at most one status rebuild per
// interval, and it never invalidates while a rebuild is already in flight:
// that would keep moving the revision and starve the published status.
// stop ends it, dropping a pending refresh; a final change still arrives
// through its own RefreshOn topic (index.finished), so the last status uses
// the full coverage.
func RefreshOnProgress[T any](m *Machine, bus *events.Bus, topic events.Topic[T], buffer int, interval time.Duration) (stop func(), err error) {
	if interval <= 0 {
		return nil, fmt.Errorf("learning: progress refresh interval must be positive, got %s", interval)
	}
	sub, err := events.Subscribe(bus, topic, buffer)
	if err != nil {
		return nil, err
	}
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		dirty := false
		for {
			select {
			case _, ok := <-sub.C:
				if !ok {
					return
				}
				dirty = true
			case <-ticker.C:
				if !dirty {
					continue
				}
				m.mu.Lock()
				rebuilding := m.refreshing
				m.mu.Unlock()
				if rebuilding {
					continue // leave dirty: the next tick retries
				}
				dirty = false
				m.Invalidate()
				ctx, cancel := context.WithTimeout(context.Background(), statusRefreshTimeout)
				if _, err := m.Status(ctx); err != nil {
					m.log.Warn("learning status not refreshed", "after", topic.Name(), "err", err)
				}
				cancel()
			case <-quit:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			sub.Unsubscribe()
			close(quit)
			<-done
		})
	}, nil
}
