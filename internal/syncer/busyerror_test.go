package syncer

import (
	"context"
	"errors"
	"testing"
)

// TestEveryBusyRefusalIsTheSameError: a second concurrent run of one source is
// refused by four entry points (the background job start, Run, RunFolders and
// Discover). They used to build the ErrSourceBusy wrap themselves, so the
// message could drift per path even though errors.Is kept working. The text and
// the sentinel are now pinned from every entry point.
func TestEveryBusyRefusalIsTheSameError(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake"}
	if !s.claim(c.ID()) {
		t.Fatal("the test could not claim the source")
	}
	defer s.release(c.ID())

	want := "syncer: this source is already syncing: fake"
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"start a background job", func() error { _, err := s.StartMail(nil, c); return err }},
		{"run", func() error { _, err := s.Run(context.Background(), c, nil); return err }},
		{"run the picked folders", func() error { _, err := s.RunFolders(context.Background(), c, nil, nil); return err }},
		{"discover the mailboxes", func() error { _, err := s.Discover(context.Background(), c); return err }},
	} {
		err := tc.call()
		if !errors.Is(err, ErrSourceBusy) {
			t.Fatalf("%s: %v is not ErrSourceBusy", tc.name, err)
		}
		if err.Error() != want {
			t.Fatalf("%s: %q, want %q", tc.name, err.Error(), want)
		}
	}

	// A different source is not busy, and its report keeps the same sentinel
	// contract when it does fail for another reason.
	other := &fakeConn{id: "fake2"}
	if s.claim(other.ID()) {
		s.release(other.ID())
		if _, err := s.Run(context.Background(), other, nil); errors.Is(err, ErrSourceBusy) {
			t.Fatal("a free source reported busy")
		}
	}
}
