package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

func TestMaintainLayer1ReconcilesOnceAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	ensure := func(*jobs.Manager) { calls.Add(1) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainLayer1(ctx, time.Hour, ensure, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("startup reconcile did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("the worker returned while its context was live")
	case <-time.After(50 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatalf("reconcile ran %d times before its interval", calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop on cancellation")
	}
}
