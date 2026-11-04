package events

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned when subscribing to a closed bus.
var ErrClosed = errors.New("events: bus is closed")

// Subscription receives the events of one topic on C until Unsubscribe or
// Bus.Close closes the channel.
type Subscription[T any] struct {
	C       <-chan T
	ch      chan T
	bus     *Bus
	topic   string
	id      uint64
	dropped atomic.Uint64
	once    sync.Once
}

// Subscribe registers a subscriber with a buffer of the given size (≥1).
func Subscribe[T any](b *Bus, topic Topic[T], buffer int) (*Subscription[T], error) {
	if buffer < 1 {
		return nil, fmt.Errorf("events: buffer for %q must be ≥1, got %d", topic.name, buffer)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	if err := b.bindType(topic.name, reflect.TypeFor[T]()); err != nil {
		return nil, err
	}
	b.nextID++
	ch := make(chan T, buffer)
	s := &Subscription[T]{C: ch, ch: ch, bus: b, topic: topic.name, id: b.nextID}
	if b.subs[topic.name] == nil {
		b.subs[topic.name] = map[uint64]subscriber{}
	}
	b.subs[topic.name][s.id] = s
	return s, nil
}

// Publish offers v to every subscriber of the topic without blocking and
// returns how many accepted it. A full subscriber drops the event.
func Publish[T any](b *Bus, topic Topic[T], v T) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}
	if known, ok := b.types[topic.name]; ok && known != reflect.TypeFor[T]() {
		panic(fmt.Sprintf("events: topic %q carries %v, published %T", topic.name, known, v))
	}
	delivered := 0
	for _, s := range b.subs[topic.name] {
		if s.deliver(v) {
			delivered++
		} else {
			b.dropped.Add(1)
		}
	}
	return delivered
}

// Unsubscribe removes the subscription and closes C. Safe to call twice.
func (s *Subscription[T]) Unsubscribe() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if group, ok := s.bus.subs[s.topic]; ok {
		if _, mine := group[s.id]; mine {
			delete(group, s.id)
			s.closeChannel()
		}
	}
}

// Dropped is the number of events this subscriber missed because its buffer was full.
func (s *Subscription[T]) Dropped() uint64 { return s.dropped.Load() }

func (s *Subscription[T]) deliver(v any) bool {
	select {
	case s.ch <- v.(T):
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

func (s *Subscription[T]) closeChannel() { s.once.Do(func() { close(s.ch) }) }
