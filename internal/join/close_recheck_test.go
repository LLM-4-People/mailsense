package join

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestJoinedSeesADrainTheSelectMissed: Close wakes on the context or the drain,
// whichever fired first. When both are ready the select may pick the context, so
// the second look must report the drain as joined.
func TestJoinedSeesADrainTheSelectMissed(t *testing.T) {
	drained := make(chan struct{})
	close(drained)
	if !joined(drained) {
		t.Fatal("a closed drain was reported unjoined")
	}
	open := make(chan struct{})
	if joined(open) {
		t.Fatal("an open drain was reported joined")
	}

	// The context branch of Close must use the second look.
	data, err := os.ReadFile("join.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "case <-ctx.Done():\n\t\treturn joined(done)") {
		t.Fatal("Close's context branch does not re-check the drain")
	}
}

// TestCloseReportsAnUnjoinedTaskWhenContextEnds: an active task still running
// when ctx ends is a real unjoined report; once it retires, the group joins.
func TestCloseReportsAnUnjoinedTaskWhenContextEnds(t *testing.T) {
	g := New()
	if !g.Track() {
		t.Fatal("Track refused an open group")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if g.Close(ctx) {
		t.Fatal("an active task was reported joined")
	}
	g.Done()
	if !g.Close(context.Background()) {
		t.Fatal("the group did not join after its last task retired")
	}
}
