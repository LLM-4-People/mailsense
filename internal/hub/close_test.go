package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// stubbornCounts is a store reader whose label counts answer only on release:
// it ignores the build context on purpose, standing for a store read a
// cancellation cannot interrupt.
type stubbornCounts struct {
	storeReader
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *stubbornCounts) LabelCounts(context.Context) ([]store.LabelCount, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	return nil, nil
}

// closeFixture builds a hub service over a real store with a clock the test
// moves, so the TTL can expire on demand.
func closeFixture(t *testing.T) (*Service, *world, *closeClock) {
	t.Helper()
	w := newWorld(t)
	clk := &closeClock{now: w.now}
	svc := New(Deps{Store: w.store, Learning: w.learn, Sync: w.sync, Now: clk.Now})
	return svc, w, clk
}

// closeClock is the one mutable clock of the close tests.
type closeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *closeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *closeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Close joins the count rebuilds of the three caches: the hub's view is served
// from the installed entries afterwards and a second Close is idempotent.
func TestCloseJoinsTheCountRefreshAndIsIdempotent(t *testing.T) {
	svc, w, clk := closeFixture(t)
	if _, err := svc.Hub(t.Context()); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}, 4), make(chan struct{})
	svc.store = &stubbornCounts{storeReader: w.store, entered: entered, release: release}
	clk.advance(11 * time.Second)
	if _, err := svc.Hub(t.Context()); err != nil {
		t.Fatalf("the stale hub was not served: %v", err)
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
		t.Fatalf("Close returned (%v) with a count rebuild still running", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the count rebuilds")
	}

	first, err := svc.Hub(t.Context())
	if err != nil {
		t.Fatalf("post-Close hub: %v", err)
	}
	second, err := svc.Hub(t.Context())
	if err != nil || second.GeneratedAt != first.GeneratedAt {
		t.Fatalf("post-Close hub moved from %v to %v (%v)", first.GeneratedAt, second.GeneratedAt, err)
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// An expired caller context yields the miss without hanging; the repeated
// Close observes the eventual join.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	svc, w, clk := closeFixture(t)
	if _, err := svc.Hub(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 4), make(chan struct{})
	svc.store = &stubbornCounts{storeReader: w.store, entered: entered, release: release}
	clk.advance(11 * time.Second)
	if _, err := svc.Hub(t.Context()); err != nil {
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
