package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
)

func TestMaintainSemanticIndexEnsuresWarmsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ensured := make(chan struct{}, 4)
	warmed := make(chan struct{}, 1)
	ensure := func(context.Context, *jobs.Manager) (index.Coverage, error) {
		ensured <- struct{}{}
		return index.Coverage{Counts: index.Counts{Eligible: 2, Indexed: 2}}, nil
	}
	warm := func(context.Context) error {
		warmed <- struct{}{}
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainSemanticIndex(ctx, time.Hour, ensure, warm, nil)
	}()
	select {
	case <-ensured:
	case <-time.After(5 * time.Second):
		t.Fatal("startup ensure did not run")
	}
	select {
	case <-warmed:
	case <-time.After(5 * time.Second):
		t.Fatal("startup warm-up did not run")
	}
	select {
	case <-ensured:
		t.Fatal("reconcile ran before its interval")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not stop on cancellation")
	}
}

func TestMaintainSemanticIndexReconcilesOnTheInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	ensure := func(context.Context, *jobs.Manager) (index.Coverage, error) {
		calls.Add(1)
		return index.Coverage{}, nil
	}
	warm := func(context.Context) error { return nil }
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainSemanticIndex(ctx, 20*time.Millisecond, ensure, warm, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("reconcile ran %d times, want at least 2", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not stop on cancellation")
	}
}
