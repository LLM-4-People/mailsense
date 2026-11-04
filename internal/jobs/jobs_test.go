package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

func wait(t *testing.T, j *Job) Info {
	t.Helper()
	<-j.Done()
	return j.Info()
}

func TestLifecycleAndEvents(t *testing.T) {
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicJob, 16)
	clk := clocktest.New(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC))
	m := NewManager(configtest.New(t, nil), bus, clk)
	j, err := m.Start("sync", "legacy", func(ctx context.Context, report func(Progress)) error {
		report(Progress{Done: 1, Total: 2, Unit: "items"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info := wait(t, j)
	if info.Status != runstate.Succeeded || info.Progress.Done != 1 || !info.StartedAt.Equal(clk.Now()) || !info.FinishedAt.Equal(clk.Now()) {
		t.Fatalf("info %+v", info)
	}
	var statuses []runstate.State
	for len(statuses) < 3 {
		statuses = append(statuses, (<-sub.C).Status)
	}
	if statuses[0] != runstate.Running || statuses[2] != runstate.Succeeded {
		t.Fatalf("event sequence %v", statuses)
	}
}

func TestFailureCancelAndPanic(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	failed, _ := m.Start("k", "fail", func(context.Context, func(Progress)) error { return errors.New("boom") })
	if info := wait(t, failed); info.Status != runstate.Failed || info.Error != "boom" {
		t.Fatalf("failed: %+v", info)
	}
	started := make(chan struct{})
	slow, _ := m.Start("k", "slow", func(ctx context.Context, _ func(Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	slow.Cancel()
	if info := wait(t, slow); info.Status != runstate.Cancelled {
		t.Fatalf("cancelled: %+v", info)
	}
	panicky, _ := m.Start("k", "panic", func(context.Context, func(Progress)) error { panic("oops") })
	if info := wait(t, panicky); info.Status != runstate.Failed || info.Error != "jobs: panic: oops" {
		t.Fatalf("panic: %+v", info)
	}
	if _, err := m.Start("", "x", func(context.Context, func(Progress)) error { return nil }); err == nil {
		t.Fatal("empty kind must be rejected")
	}
	if _, err := m.Start("k", "x", nil); err == nil {
		t.Fatal("nil func must be rejected")
	}
	if _, ok := m.Get(failed.Info().ID); !ok {
		t.Fatal("get")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	m := NewManager(configtest.New(t, map[string]any{"jobs.history_size": 2}), nil, nil)
	for range 5 {
		j, _ := m.Start("k", "n", func(context.Context, func(Progress)) error { return nil })
		wait(t, j)
	}
	if n := len(m.List()); n != 2 {
		t.Fatalf("history holds %d, want 2", n)
	}
}

func TestShutdownCancelsAndRefuses(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		_, _ = m.Start("k", "n", func(ctx context.Context, report func(Progress)) error {
			report(Progress{Done: 1})
			wg.Done()
			<-ctx.Done()
			return ctx.Err()
		})
	}
	wg.Wait()
	if err := m.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, info := range m.List() {
		if info.Status != runstate.Cancelled {
			t.Fatalf("%s: %s", info.ID, info.Status)
		}
	}
	if _, err := m.Start("k", "n", func(context.Context, func(Progress)) error { return nil }); !errors.Is(err, ErrShutdown) {
		t.Fatalf("start after shutdown: %v", err)
	}
	stuck := NewManager(configtest.New(t, nil), nil, nil)
	release := make(chan struct{})
	defer close(release)
	_, _ = stuck.Start("k", "stuck", func(context.Context, func(Progress)) error { <-release; return nil })
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the caller gives up waiting
	if err := stuck.Shutdown(ctx); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnjoined) {
		t.Fatalf("a job ignoring cancel must report the unjoined shutdown: %v", err)
	}
}

// Review L0 (bugs #12): Safe turns a worker's panic into that item's error.
func TestSafeRecoversAPanic(t *testing.T) {
	err := Safe(func() error { panic("bad item") })
	var p *PanicError
	if !errors.As(err, &p) || p.Value != "bad item" || !strings.Contains(p.Stack, "TestSafeRecoversAPanic") {
		t.Fatalf("%v %+v", err, p)
	}
	if err := Safe(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// Job errors are published on /api/jobs and the SSE stream. A failed provider
// call must not carry its key or an address into those surfaces.
func TestFailureErrorIsRedacted(t *testing.T) {
	m := NewManager(configtest.New(t, nil), nil, nil)
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	const address = "bob@example.test"
	j, err := m.Start("k", "fail", func(context.Context, func(Progress)) error {
		return errors.New("provider said " + secret + " for " + address)
	})
	if err != nil {
		t.Fatal(err)
	}
	info := wait(t, j)
	if strings.Contains(info.Error, secret) || strings.Contains(info.Error, address) {
		t.Fatalf("job error leaked the provider body: %q", info.Error)
	}
	if !strings.Contains(info.Error, config.SecretMask) {
		t.Fatalf("job error did not mark the value: %q", info.Error)
	}
}
