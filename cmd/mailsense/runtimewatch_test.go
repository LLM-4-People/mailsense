package main

import (
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

type recordingResetter struct {
	mu sync.Mutex
	n  int
}

func (r *recordingResetter) ResetUnavailable() {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func (r *recordingResetter) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Round 27: a managed runtime that becomes ready can serve the next
// reconcile, so the index's unavailable-embedder wait ends. Other states
// (starting, installing, failed) change nothing, and stop ends the watch.
func TestRuntimeReadyClearsTheIndexBackoff(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	r := &recordingResetter{}
	stop, err := watchRuntimeReady(r, bus, 8)
	if err != nil {
		t.Fatal(err)
	}
	events.Publish(bus, sidecar.TopicState, sidecar.Status{State: sidecar.StateStarting})
	events.Publish(bus, sidecar.TopicState, sidecar.Status{State: sidecar.StateReady})
	// stop waits for the handler to finish the events already delivered:
	// starting is ignored, ready resets exactly once.
	stop()
	if got := r.calls(); got != 1 {
		t.Fatalf("starting+ready reset %d times, want exactly 1 (ready)", got)
	}
	if got := events.Publish(bus, sidecar.TopicState, sidecar.Status{State: sidecar.StateReady}); got != 0 {
		t.Fatalf("a stopped watch still received the event (%d subscribers)", got)
	}
	if got := r.calls(); got != 1 {
		t.Fatalf("a stopped watch reset the index backoff (%d calls)", got)
	}
}
