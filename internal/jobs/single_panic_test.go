package jobs

import "testing"

// A start closure that panics must release the slot: a panic cannot leave
// busy set with no run, or every later Begin/Want is refused forever.
func TestSingleStartPanicReleasesTheSlot(t *testing.T) {
	var s Single
	s.Want(func() { panic("start broke") })
	if s.Busy() {
		t.Fatal("a panicking start left the slot busy")
	}
	ran := false
	s.Want(func() {
		ran = true
		s.End()
	})
	if !ran {
		t.Fatal("a later Want did not start")
	}
	if s.Busy() {
		t.Fatal("the slot stayed busy after the second run ended")
	}
}

// The pending closure runs from End outside the lock; its panic must release
// the slot it took over just the same.
func TestSinglePendingPanicReleasesTheSlot(t *testing.T) {
	var s Single
	s.Want(func() {}) // holds the slot until End
	s.Want(func() { panic("pending broke") })
	s.End()
	if s.Busy() {
		t.Fatal("a panicking pending start left the slot busy")
	}
	ran := false
	s.Want(func() {
		ran = true
		s.End()
	})
	if !ran {
		t.Fatal("a later Want did not start")
	}
	if s.Busy() {
		t.Fatal("the slot stayed busy after the second run ended")
	}
}
