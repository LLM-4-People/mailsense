package commitments

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// stubbornThreads answers only when the test releases it: it ignores the build
// context on purpose, so it stands for a store read a cancellation cannot
// interrupt. A cooperative source is canceled and joined at once instead.
type stubbornThreads struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *stubbornThreads) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	return nil, nil
}

// closeFixture builds a service whose one set is already cached, so a TTL
// advance starts the detached refresh the test can hold open.
func closeFixture(t *testing.T) (*Service, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
	svc := New(Deps{Store: &fakeStore{}, Clock: clk})
	if _, err := svc.List(t.Context(), Filter{}); err != nil {
		t.Fatal(err)
	}
	return svc, clk
}

// Close joins a refresh reading the store and reports the conjunction; the
// installed set stays readable afterwards and a second Close is idempotent.
func TestCloseJoinsTheRefreshAndIsIdempotent(t *testing.T) {
	svc, clk := closeFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.threads = &stubbornThreads{entered: entered, release: release}
	clk.Advance(CacheTTL + time.Second)

	if _, err := svc.List(t.Context(), Filter{}); err != nil {
		t.Fatalf("the stale set was not served: %v", err)
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
		t.Fatalf("Close returned (%v) with the refresh still reading", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the refresh")
	}

	if _, err := svc.List(t.Context(), Filter{}); err != nil {
		t.Fatalf("post-Close read: %v", err)
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// An expired caller context yields the miss without hanging, and the repeated
// Close observes the eventual join.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	svc, clk := closeFixture(t)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.threads = &stubbornThreads{entered: entered, release: release}
	clk.Advance(CacheTTL + time.Second)
	if _, err := svc.List(t.Context(), Filter{}); err != nil {
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
