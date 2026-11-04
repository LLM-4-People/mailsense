package events

import (
	"sync"
	"testing"
)

var (
	topicInt = NewTopic[int]("test.int")
	topicStr = NewTopic[string]("test.str")
)

func TestPublishDeliversToEverySubscriber(t *testing.T) {
	b := New()
	s1, _ := Subscribe(b, topicInt, 4)
	s2, _ := Subscribe(b, topicInt, 4)
	other, _ := Subscribe(b, topicStr, 4)
	if n := Publish(b, topicInt, 7); n != 2 {
		t.Fatalf("delivered %d, want 2", n)
	}
	if <-s1.C != 7 || <-s2.C != 7 {
		t.Fatal("wrong payload")
	}
	select {
	case v := <-other.C:
		t.Fatalf("other topic received %q", v)
	default:
	}
}

func TestFullBufferDropsAndCounts(t *testing.T) {
	b := New()
	s, _ := Subscribe(b, topicInt, 2)
	for i := range 5 {
		Publish(b, topicInt, i)
	}
	if s.Dropped() != 3 || b.Dropped() != 3 {
		t.Fatalf("dropped sub=%d bus=%d, want 3/3", s.Dropped(), b.Dropped())
	}
	if <-s.C != 0 || <-s.C != 1 {
		t.Fatal("oldest events must be kept, newest dropped")
	}
}

func TestUnsubscribeClosesChannelAndIsIdempotent(t *testing.T) {
	b := New()
	s, _ := Subscribe(b, topicInt, 1)
	s.Unsubscribe()
	s.Unsubscribe()
	if _, ok := <-s.C; ok {
		t.Fatal("channel must be closed")
	}
	if Publish(b, topicInt, 1) != 0 || b.subscribers(topicInt.Name()) != 0 {
		t.Fatal("unsubscribed subscriber still registered")
	}
}

func TestMisuse(t *testing.T) {
	b := New()
	if _, err := Subscribe(b, topicInt, 0); err == nil {
		t.Fatal("zero buffer must be rejected")
	}
	if _, err := Subscribe(b, topicInt, 1); err != nil {
		t.Fatal(err)
	}
	clash := NewTopic[string](topicInt.Name())
	if _, err := Subscribe(b, clash, 1); err == nil {
		t.Fatal("same topic name with another type must be rejected")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("publishing the wrong type must panic")
			}
		}()
		Publish(b, clash, "x")
	}()
	b.Close()
	b.Close()
	if _, err := Subscribe(b, topicInt, 1); err != ErrClosed {
		t.Fatalf("subscribe after close: %v", err)
	}
	if Publish(b, topicInt, 1) != 0 {
		t.Fatal("publish after close must be a no-op")
	}
}

func TestCloseClosesSubscribers(t *testing.T) {
	b := New()
	s, _ := Subscribe(b, topicInt, 1)
	b.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("close must close subscriber channels")
	}
	s.Unsubscribe() // after close: no panic
}

func TestConcurrentPublishSubscribeUnsubscribe(t *testing.T) {
	b := New()
	var readers, writers sync.WaitGroup
	received := make([]int, 4)
	subs := make([]*Subscription[int], len(received))
	for k := range received {
		s, err := Subscribe(b, topicInt, 16)
		if err != nil {
			t.Fatal(err)
		}
		subs[k] = s
		readers.Go(func() {
			for range s.C {
				received[k]++
			}
		})
	}
	const publishers, per = 8, 500
	for range publishers {
		writers.Go(func() {
			for i := range per {
				Publish(b, topicInt, i)
			}
		})
	}
	for range 4 {
		writers.Go(func() {
			for range 100 {
				if s, err := Subscribe(b, topicInt, 1); err == nil {
					s.Unsubscribe()
				}
			}
		})
	}
	writers.Wait()
	b.Close()
	readers.Wait()
	for k, s := range subs {
		if got := uint64(received[k]) + s.Dropped(); got != publishers*per {
			t.Fatalf("subscriber %d: received+dropped=%d, want %d", k, got, publishers*per)
		}
	}
}
