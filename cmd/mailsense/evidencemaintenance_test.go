package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/jobs"
)

func TestMaintainEvidenceReconcilesOnceAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	ensure := func(context.Context, *jobs.Manager) (*jobs.Job, error) {
		calls.Add(1)
		return nil, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainEvidence(ctx, time.Hour, ensure, nil)
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

// A busy engine is not a failure: a run is already reading the same inputs,
// and the worker must keep its cadence instead of logging a warning.
func TestMaintainEvidenceTreatsBusyAsNoWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	ensure := func(context.Context, *jobs.Manager) (*jobs.Job, error) {
		calls.Add(1)
		return nil, evidence.ErrBusy
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		maintainEvidence(ctx, 20*time.Millisecond, ensure, nil)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("a busy engine stopped the worker after %d calls", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop on cancellation")
	}
}
