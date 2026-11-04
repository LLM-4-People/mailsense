package brief

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/commitments"
)

// stubbornCommitments answers only when the test releases it: it ignores the
// build context on purpose, standing for a store read a cancellation cannot
// interrupt. The production commitments service honors the context.
type stubbornCommitments struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *stubbornCommitments) List(context.Context, commitments.Filter) (commitments.Result, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	<-s.release
	return commitments.Result{}, nil
}

// Close joins a Brief rebuild that is still composing its sections, then keeps
// serving the installed Brief. It is idempotent.
func TestCloseJoinsTheBriefRefreshAndIsIdempotent(t *testing.T) {
	svc, clk := newTestService(t, &fakeCommitments{}, &fakeConversations{}, &fakePriority{})
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.commitments = &stubbornCommitments{entered: entered, release: release}
	clk.Advance(CacheTTL + time.Second)
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatalf("the stale Brief was not served: %v", err)
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
		t.Fatalf("Close returned (%v) with the Brief rebuild still running", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the Brief rebuild")
	}

	after, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatalf("post-Close Brief: %v", err)
	}
	again, err := svc.Brief(t.Context(), "")
	if err != nil || again.GeneratedAt != after.GeneratedAt {
		t.Fatalf("post-Close Brief moved from %q to %q (%v)", after.GeneratedAt, again.GeneratedAt, err)
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// An expired caller context yields the miss without hanging; the repeated
// Close observes the eventual join.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	svc, clk := newTestService(t, &fakeCommitments{}, &fakeConversations{}, &fakePriority{})
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	svc.commitments = &stubbornCommitments{entered: entered, release: release}
	clk.Advance(CacheTTL + time.Second)
	if _, err := svc.Brief(t.Context(), ""); err != nil {
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
