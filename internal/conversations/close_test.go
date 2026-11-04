package conversations

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// stubbornPriority answers only when the test releases it: it ignores the
// build context on purpose, standing for a store read a cancellation cannot
// interrupt. The production priority service honors the context and is joined
// at once instead.
type stubbornPriority struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *stubbornPriority) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	return nil, nil
}

// Close joins a page rebuild that is still reading the store, then serves the
// installed page without another read. It is idempotent.
func TestCloseJoinsThePageRefreshAndIsIdempotent(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, clk := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Cached page", "2026-11-05T09:00:00Z", nil)
	if _, err := svc.Page(t.Context(), Query{}); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.priority = &stubbornPriority{entered: entered, release: release}
	clk.Advance(PageCacheTTL + time.Second)
	stale, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatalf("the stale page was not served: %v", err)
	}
	<-entered

	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- svc.Close(ctx)
	}()
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) with the page rebuild still running", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the page rebuild")
	}

	// The rebuild that raced Close either installed before the join or was
	// refused; either way the page a later read gets is stable, because Close
	// left no rebuild to change it.
	after, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatalf("post-Close page: %v", err)
	}
	again, err := svc.Page(t.Context(), Query{})
	if err != nil || again.GeneratedAt != after.GeneratedAt {
		t.Fatalf("post-Close page moved from %q to %q (%v)", after.GeneratedAt, again.GeneratedAt, err)
	}
	if after.GeneratedAt < stale.GeneratedAt {
		t.Fatalf("post-Close page regressed to %q from %q", after.GeneratedAt, stale.GeneratedAt)
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// An expired caller context yields the miss without hanging; the repeated
// Close observes the eventual join.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	svc, st, _, clk := newTestService(t, &fakePriority{})
	seedConversation(t, st, "c1", "C1", "Cached page", "2026-11-05T09:00:00Z", nil)
	if _, err := svc.Page(t.Context(), Query{}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.priority = &stubbornPriority{entered: entered, release: release}
	clk.Advance(PageCacheTTL + time.Second)
	if _, err := svc.Page(t.Context(), Query{}); err != nil {
		t.Fatal(err)
	}
	<-entered

	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if svc.Close(expired) {
		t.Fatal("Close joined with an expired context")
	}
	close(release)
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if !svc.Close(ctx) {
		t.Fatal("the repeated Close did not observe the join")
	}
}
