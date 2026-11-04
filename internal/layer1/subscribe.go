package layer1

// subscribeAll installs every subscription and returns its stop. A failing
// subscription stops the ones already installed, so a caller never leaks half
// a wiring: the trigger and the automatic-label loop both wire several topics
// at start-up and both must roll back the same way.
func subscribeAll(subs ...func() (func(), error)) (func(), error) {
	stops := make([]func(), 0, len(subs))
	for _, sub := range subs {
		stop, err := sub()
		if err != nil {
			for _, installed := range stops {
				installed()
			}
			return nil, err
		}
		stops = append(stops, stop)
	}
	return func() {
		for _, stop := range stops {
			stop()
		}
	}, nil
}
