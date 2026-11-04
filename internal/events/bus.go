// Package events is the in-process typed pub/sub used for progress, log lines
// and settings changes. Publishing never blocks: every subscriber owns a
// bounded buffer and an event that does not fit is dropped and counted.
//
// events deliberately does not log (logx publishes through it); its health is
// exposed as counters instead.
package events

import (
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

// Topic names a stream of events of one payload type.
type Topic[T any] struct{ name string }

// NewTopic declares a topic. The owning package declares it once and exports it.
func NewTopic[T any](name string) Topic[T] { return Topic[T]{name: name} }

// Name returns the topic name.
func (t Topic[T]) Name() string { return t.name }

type subscriber interface {
	deliver(v any) bool
	closeChannel()
}

// Bus routes events from publishers to subscribers.
type Bus struct {
	mu      sync.RWMutex
	subs    map[string]map[uint64]subscriber
	types   map[string]reflect.Type
	nextID  uint64
	closed  bool
	dropped atomic.Uint64
}

// New returns an empty bus.
func New() *Bus {
	return &Bus{subs: map[string]map[uint64]subscriber{}, types: map[string]reflect.Type{}}
}

// Dropped is the total number of events dropped across all subscribers.
func (b *Bus) Dropped() uint64 { return b.dropped.Load() }

// subscribers returns how many subscribers a topic currently has.
func (b *Bus) subscribers(topic string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[topic])
}

// Close closes every subscription channel; later publishes are no-ops and
// later subscriptions fail.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, group := range b.subs {
		for _, s := range group {
			s.closeChannel()
		}
	}
	b.subs = map[string]map[uint64]subscriber{}
}

// bindType records the payload type of a topic name; a second declaration of
// the same name with another type is a programming error.
func (b *Bus) bindType(name string, t reflect.Type) error {
	if known, ok := b.types[name]; ok && known != t {
		return fmt.Errorf("events: topic %q carries %v, not %v", name, known, t)
	}
	b.types[name] = t
	return nil
}
