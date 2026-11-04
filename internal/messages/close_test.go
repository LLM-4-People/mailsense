package messages

import (
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestCloseJoinsTheDetachedFirstPageBuild: the first-page build outlives the
// click that started it, so the store may only close once it finished. Close
// must wait for it, and a service closed before a build must not start one.
func TestCloseJoinsTheDetachedFirstPageBuild(t *testing.T) {
	f := &fakeStore{page: store.MessageListPage{Total: 1}, block: make(chan struct{}), started: make(chan struct{}, 1)}
	s := New(f)

	go func() { _, _ = s.List(t.Context(), unfilteredPage()) }()
	select {
	case <-f.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first-page build never started")
	}

	// Close waits for the in-flight build.
	closed := make(chan bool, 1)
	go func() { closed <- s.Close(t.Context()) }()
	select {
	case <-closed:
		t.Fatal("Close returned while the detached build was still reading the store")
	case <-time.After(100 * time.Millisecond):
	}

	// Let the in-flight build finish: its store read was already holding the
	// channel the fake captured, so that exact channel is what unblocks.
	f.mu.Lock()
	blocked := f.block
	f.block = nil
	f.mu.Unlock()
	close(blocked)

	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("Close reported a miss after the build returned")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the build finished")
	}

	// The page installed before Close is still served: a closed service must
	// never start a store read, but it may hand out what it already holds.
	if page, err := s.List(t.Context(), unfilteredPage()); err != nil || page.Total != 1 {
		t.Fatalf("the installed page was not served after Close: %v %+v", err, page)
	}
	// A generation that moved has no entry, so it would have to read the
	// store: a closed service refuses instead.
	f.gen++
	if _, err := s.List(t.Context(), unfilteredPage()); !errors.Is(err, ErrClosed) {
		t.Fatalf("a closed service started a store build: %v", err)
	}
	if !s.Close(t.Context()) {
		t.Fatal("a second Close was not idempotent")
	}
}
