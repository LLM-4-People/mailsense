package main

import (
	"context"
	"testing"
	"time"
)

// TestStopWorkerReportsAnUnjoinedWorker: a worker that ignores cancellation
// must not hang shutdown, and the run must remember that the store cannot be
// closed underneath it.
func TestStopWorkerReportsAnUnjoinedWorker(t *testing.T) {
	shutdown := newShutdownRequest()
	done := make(chan struct{})
	if stopWorker(shutdown, done, 20*time.Millisecond) {
		t.Fatal("a worker that never finishes was reported joined")
	}
	if !shutdown.Unjoined() {
		t.Fatal("the run was not marked unjoined")
	}
	close(done)
	other := newShutdownRequest()
	if !stopWorker(other, done, 20*time.Millisecond) {
		t.Fatal("a finished worker was reported unjoined")
	}
	if other.Unjoined() {
		t.Fatal("a joined worker marked the run unjoined")
	}
}

// TestStartWhenReadyJoinsACooperativeWorker: the shared worker helper cancels
// and joins a worker that honors its context, without marking the run.
func TestStartWhenReadyJoinsACooperativeWorker(t *testing.T) {
	shutdown := newShutdownRequest()
	ready := make(chan struct{})
	started := make(chan struct{})
	stop := startWhenReady(shutdown, ready, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	})
	close(ready)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never started")
	}
	stop()
	if shutdown.Unjoined() {
		t.Fatal("a cooperative worker did not join")
	}
}
