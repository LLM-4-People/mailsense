package events

// On runs fn for every event of topic, one at a time, in a goroutine of
// its own: the one subscribe-and-loop of the codebase (learning refresh,
// install-profile learners, the index run after a sync). stop
// unsubscribes and waits for the event being handled.
func On[T any](b *Bus, topic Topic[T], buffer int, fn func(T)) (stop func(), err error) {
	sub, err := Subscribe(b, topic, buffer)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for v := range sub.C {
			fn(v)
		}
	}()
	return func() { sub.Unsubscribe(); <-done }, nil
}
