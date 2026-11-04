package events

// PublishProgress publishes a run's progress on the bus (when there is one)
// and hands the same value to the caller's callback. Five run packages wrote
// this pair; the order matters (an event handler must never see a value the
// direct callback has not), so it lives here once.
func PublishProgress[T any](bus *Bus, topic Topic[T], p T, report func(T)) {
	if bus != nil {
		Publish(bus, topic, p)
	}
	if report != nil {
		report(p)
	}
}
