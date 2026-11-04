package logx

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

// TestCloseStopsFollowingConfiguration: the configuration follower is
// events.On's goroutine, and Close's stop unsubscribes it. A change published
// afterwards must not be applied.
func TestCloseStopsFollowingConfiguration(t *testing.T) {
	bus := events.New()
	applied, _ := events.Subscribe(bus, TopicApplied, 8)
	svc, cfg := setup(t, bus, nil)
	<-applied.C // Setup's apply
	if err := cfg.Set("log.level", "error"); err != nil {
		t.Fatal(err)
	}
	<-applied.C
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("log.level", "debug"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-applied.C:
		t.Fatal("a configuration change was applied after Close")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestLogxFollowsThroughEventsOn: the package keeps no second
// subscribe-and-loop, subscription field or WaitGroup; events.On owns them.
func TestLogxFollowsThroughEventsOn(t *testing.T) {
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if got := strings.Count(source, "events.On("); got != 1 {
		t.Fatalf("service.go calls events.On %d times, want once", got)
	}
	for _, banned := range []string{"events.Subscribe", "sync.WaitGroup", "s.sub", "s.wg"} {
		if strings.Contains(source, banned) {
			t.Fatalf("service.go still keeps %q; events.On owns the follower", banned)
		}
	}
}
