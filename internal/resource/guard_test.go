package resource

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

type fakeSampler struct {
	mu    sync.Mutex
	usage Usage
	seq   []Usage
	err   error
	calls int
}

func (f *fakeSampler) Sample(context.Context) (Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return Usage{}, f.err
	}
	if len(f.seq) > 0 {
		f.usage, f.seq = f.seq[0], f.seq[1:]
	}
	return f.usage, nil
}

func (f *fakeSampler) set(u Usage) {
	f.mu.Lock()
	f.usage = u
	f.mu.Unlock()
}

func reading(p float64) Reading { return Reading{Percent: p, Known: true} }

func healthy() Usage {
	return Usage{CPU: reading(10), RAM: reading(10), Disk: reading(10)}
}

func newTestGuard(t *testing.T, overrides map[string]any) (*Guard, *fakeSampler, *clocktest.Fake) {
	t.Helper()
	cfg := configtest.New(t, overrides)
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	sampler := &fakeSampler{usage: healthy()}
	return NewGuardWithSampler(cfg, t.TempDir(), clk, sampler), sampler, clk
}

func cpuOnly(t *testing.T, limit int) (*Guard, *fakeSampler, *clocktest.Fake) {
	t.Helper()
	return newTestGuard(t, map[string]any{
		"resource.cpu.max_percent":  limit,
		"resource.ram.max_percent":  100,
		"resource.disk.max_percent": 100,
	})
}

// TestCeilingBlocksAndResumesWithHysteresis reproduces the one behaviour the
// guard exists for: work stops above the ceiling and resumes only after the
// host has fallen a margin below it, so it cannot flap on every reading.
func TestCeilingBlocksAndResumesWithHysteresis(t *testing.T) {
	g, sampler, clk := cpuOnly(t, 80)
	ctx := t.Context()

	sampler.set(Usage{CPU: reading(85), RAM: reading(10), Disk: reading(10)})
	paused, err := g.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if paused == nil || paused.Kind != CPU || paused.Limit != 80 || paused.Percent != 85 {
		t.Fatalf("pause = %+v, want CPU at 85 against 80", paused)
	}

	sampler.set(Usage{CPU: reading(78), RAM: reading(10), Disk: reading(10)})
	clk.Advance(g.Interval())
	if paused, _ := g.Check(ctx); paused == nil {
		t.Fatal("78% resumed below an 80% ceiling without the 3-point margin")
	}

	sampler.set(Usage{CPU: reading(77), RAM: reading(10), Disk: reading(10)})
	clk.Advance(g.Interval())
	if paused, _ := g.Check(ctx); paused != nil {
		t.Fatalf("77%% still paused: %+v", paused)
	}
}

// TestDiskIsReportedFirst: when several ceilings are crossed, the message
// names the one that cannot recover while the job writes.
func TestDiskIsReportedFirst(t *testing.T) {
	g, sampler, clk := newTestGuard(t, map[string]any{
		"resource.cpu.max_percent":  90,
		"resource.ram.max_percent":  90,
		"resource.disk.max_percent": 90,
	})
	ctx := t.Context()
	sampler.set(Usage{CPU: reading(95), RAM: reading(95), Disk: reading(95)})
	paused, err := g.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if paused == nil || paused.Kind != Disk {
		t.Fatalf("pause = %+v, want the disk ceiling first", paused)
	}

	sampler.set(Usage{CPU: reading(95), RAM: reading(10), Disk: reading(10)})
	clk.Advance(g.Interval())
	paused, err = g.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if paused == nil || paused.Kind != CPU {
		t.Fatalf("pause = %+v, want the CPU ceiling", paused)
	}
}

// TestUnknownReadingsNeverBlock: a metric a platform cannot read must never
// stop the app. Unknown is not a violation.
func TestUnknownReadingsNeverBlock(t *testing.T) {
	g, sampler, _ := cpuOnly(t, 10)
	sampler.set(Usage{RAM: reading(10), Disk: reading(10)})
	if paused, err := g.Check(t.Context()); err != nil || paused != nil {
		t.Fatalf("unknown CPU = %+v, %v", paused, err)
	}
}

// TestHundredPercentDisablesTheCeiling: 100 is the explicit off switch for
// one resource, even while the machine reports full use.
func TestHundredPercentDisablesTheCeiling(t *testing.T) {
	g, sampler, _ := cpuOnly(t, 100)
	sampler.set(Usage{CPU: reading(100), RAM: reading(100), Disk: reading(100)})
	if paused, err := g.Check(t.Context()); err != nil || paused != nil {
		t.Fatalf("100%% ceiling = %+v, %v", paused, err)
	}
}

// TestDisabledGuardNeverBlocks: resource.guard is the one switch that turns
// the whole policy off.
func TestDisabledGuardNeverBlocks(t *testing.T) {
	g, sampler, _ := newTestGuard(t, map[string]any{
		"resource.guard":            false,
		"resource.cpu.max_percent":  10,
		"resource.ram.max_percent":  10,
		"resource.disk.max_percent": 50,
		"resource.sample_interval":  "250ms",
	})
	sampler.set(Usage{CPU: reading(100), RAM: reading(100), Disk: reading(100)})
	if paused, err := g.Check(t.Context()); err != nil || paused != nil {
		t.Fatalf("disabled guard = %+v, %v", paused, err)
	}
}

// TestWaitSleepsUntilTheHostRecovers proves pacing is a bounded poll on the
// guard's own interval, not a busy loop.
func TestWaitSleepsUntilTheHostRecovers(t *testing.T) {
	g, sampler, clk := cpuOnly(t, 80)
	sampler.seq = []Usage{
		{CPU: reading(90), RAM: reading(10), Disk: reading(10)},
		{CPU: reading(90), RAM: reading(10), Disk: reading(10)},
		{CPU: reading(50), RAM: reading(10), Disk: reading(10)},
	}
	if paused := g.Wait(t.Context()); paused != nil {
		t.Fatalf("wait returned %+v, want a recovery", paused)
	}
	sleeps := clk.Sleeps()
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %v, want two intervals", sleeps)
	}
	for _, d := range sleeps {
		if d != g.Interval() {
			t.Fatalf("slept %s, want %s", d, g.Interval())
		}
	}
}

// TestStateKeepsThePolicyWhenReadingsFail: the status endpoint still tells
// the owner which ceilings are configured when the platform cannot measure.
func TestStateKeepsThePolicyWhenReadingsFail(t *testing.T) {
	g, sampler, _ := cpuOnly(t, 80)
	sampler.err = ErrUnsupported
	state, err := g.State(t.Context())
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if state.Limits.CPU != 80 || !state.Limits.Enabled || state.Usage.CPU.Known {
		t.Fatalf("state = %+v", state)
	}
}

func TestLimitErrorIsTheResourceClass(t *testing.T) {
	err := &LimitError{Kind: Disk, Percent: 95, Limit: 90}
	if !errors.Is(err, ErrLimited) {
		t.Fatal("a ceiling error must match ErrLimited")
	}
}

// TestAdmissionSelectsCeilings: a writes-only admission (mail sync) keeps
// running under CPU and memory pressure but still respects the disk ceiling.
// A ceiling it ignores keeps its blocked state for the jobs that do respect
// it, so a later full check still reports it.
func TestAdmissionSelectsCeilings(t *testing.T) {
	g, sampler, clk := newTestGuard(t, map[string]any{
		"resource.cpu.max_percent":  80,
		"resource.ram.max_percent":  80,
		"resource.disk.max_percent": 80,
	})
	ctx := t.Context()
	sampler.set(Usage{CPU: reading(95), RAM: reading(95), Disk: reading(10)})
	if paused, err := g.CheckAdmission(ctx, AdmitWrites); err != nil || paused != nil {
		t.Fatalf("writes-only admission stopped on CPU/RAM: %+v, %v", paused, err)
	}
	paused, err := g.CheckAdmission(ctx, AdmitAll)
	if err != nil || paused == nil || paused.Kind != RAM {
		t.Fatalf("full admission = %+v, %v, want the memory ceiling", paused, err)
	}

	// The writes-only check did not clear the CPU/RAM block it does not own.
	clk.Advance(g.Interval())
	sampler.set(Usage{CPU: reading(10), RAM: reading(10), Disk: reading(10)})
	if paused, err := g.CheckAdmission(ctx, AdmitWrites); err != nil || paused != nil {
		t.Fatalf("writes-only recovery = %+v, %v", paused, err)
	}
	if paused, err := g.CheckAdmission(ctx, AdmitAll); err != nil || paused != nil {
		t.Fatalf("the full check did not clear the recovered ceilings: %+v, %v", paused, err)
	}

	sampler.set(Usage{CPU: reading(10), RAM: reading(10), Disk: reading(95)})
	clk.Advance(g.Interval())
	paused, err = g.CheckAdmission(ctx, AdmitWrites)
	if err != nil || paused == nil || paused.Kind != Disk {
		t.Fatalf("writes-only admission = %+v, %v, want the disk ceiling", paused, err)
	}
}
