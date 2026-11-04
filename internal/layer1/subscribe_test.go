package layer1

import (
	"errors"
	"testing"
)

// TestSubscribeAllRollsBackOnFailure pins the one subscription-install policy:
// a failure stops every subscription already installed and runs nothing after
// it, so the trigger and the automatic-label loop cannot leak half a wiring
// when the bus refuses one topic.
func TestSubscribeAllRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("bus refused")
	stopped := 0
	stop, err := subscribeAll(
		func() (func(), error) { return func() { stopped++ }, nil },
		func() (func(), error) { return nil, boom },
		func() (func(), error) {
			t.Fatal("a subscription was installed after a failure")
			return nil, nil
		},
	)
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want the failing subscription's error", err)
	}
	if stop != nil {
		t.Fatal("a failed install returned a stop function")
	}
	if stopped != 1 {
		t.Fatalf("stopped %d installed subscriptions, want 1", stopped)
	}

	// A complete install stops every subscription exactly once.
	stopped = 0
	stop, err = subscribeAll(
		func() (func(), error) { return func() { stopped++ }, nil },
		func() (func(), error) { return func() { stopped++ }, nil },
	)
	if err != nil || stop == nil {
		t.Fatalf("stop nil=%v err %v", stop == nil, err)
	}
	stop()
	if stopped != 2 {
		t.Fatalf("stopped %d subscriptions, want 2", stopped)
	}
}
