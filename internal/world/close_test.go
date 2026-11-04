package world

import (
	"context"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

// Close joins the custom list refresh: the stale list stays served afterwards
// without another store read, a second Close is idempotent and both cache
// owners report through the one conjunction.
func TestCloseJoinsTheListRefreshAndIsIdempotent(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	now := worldTime(t, "2026-10-26T12:00:00Z")
	clock := clocktest.New(now)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(t.Context(), ListQuery{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	entry := readListEntry(svc)
	if entry == nil {
		t.Fatal("the first list was not cached")
	}

	clock.Advance(cacheTTL + time.Second)
	// The service now refuses every new refresh, so no goroutine can make the
	// join race the test; the in-flight refresh is simulated by tracking one
	// task and installing it as the list path would.
	if err := st.UpsertMessages(t.Context(), 0, nil); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	if !svc.refreshes.Track() {
		t.Fatal("a closed service admitted a refresh")
	}
	go func() {
		entered <- struct{}{}
		<-release
		svc.refreshes.Done()
	}()
	<-entered

	closed := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- svc.Close(ctx)
	}()
	select {
	case joined := <-closed:
		t.Fatalf("Close returned (%v) with a list refresh still running", joined)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if joined := <-closed; !joined {
		t.Fatal("Close did not join the list refresh")
	}

	// The installed list is served and no refresh starts behind Close.
	before := readListCache(svc)
	if !before.present {
		t.Fatal("the list entry vanished across Close")
	}
	after, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("post-Close list: %v", err)
	}
	again, err := svc.List(t.Context(), ListQuery{Limit: 10})
	if err != nil || again.Total != after.Total {
		t.Fatalf("post-Close list moved from %d to %d (%v)", after.Total, again.Total, err)
	}
	held := readListCache(svc)
	if !held.present || held.refreshing || !held.at.Equal(before.at) {
		t.Fatalf("a post-Close read refreshed the list: %+v", held)
	}
	if !svc.Close(t.Context()) {
		t.Fatal("a second Close reported a miss")
	}
}

// An expired caller context yields the miss without hanging, and a later
// Close observes the join.
func TestCloseIsBoundedAndRepeatable(t *testing.T) {
	st, cfg := openWorldStore(t)
	seedWorldMail(t, st)
	svc, err := New(Deps{Store: st, Config: cfg, Clock: clocktest.New(worldTime(t, "2026-10-26T12:00:00Z"))})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	if !svc.refreshes.Track() {
		t.Fatal("a fresh service refused a refresh")
	}
	go func() {
		entered <- struct{}{}
		<-release
		svc.refreshes.Done()
	}()
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
		t.Fatal("the later Close did not observe the join")
	}
}
