package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

type guardSampler struct {
	mu  sync.Mutex
	seq []resource.Usage
	err error
}

func (s *guardSampler) Sample(context.Context) (resource.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return resource.Usage{}, s.err
	}
	if len(s.seq) == 0 {
		return resource.Usage{}, nil
	}
	u := s.seq[0]
	s.seq = s.seq[1:]
	return u, nil
}

func hostUsage(cpu float64) resource.Usage {
	known := func(p float64) resource.Reading { return resource.Reading{Percent: p, Known: true} }
	return resource.Usage{CPU: known(cpu), RAM: known(10), Disk: known(10)}
}

func guardedManager(t *testing.T, bus *events.Bus, sampler resource.Sampler,
	clk *clocktest.Fake) (*Manager, *resource.Guard) {
	t.Helper()
	cfg := configtest.New(t, map[string]any{
		"resource.cpu.max_percent":  80,
		"resource.ram.max_percent":  100,
		"resource.disk.max_percent": 100,
	})
	g := resource.NewGuardWithSampler(cfg, t.TempDir(), clk, sampler)
	return NewManager(cfg, bus, clk, WithGuard(g)), g
}

// TestStartRefusedAboveACeiling: admission is decided before a job exists,
// so a refused action reports the ceiling instead of creating a job that
// immediately looks like a hang.
func TestStartRefusedAboveACeiling(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	m, _ := guardedManager(t, nil, &guardSampler{seq: []resource.Usage{hostUsage(95)}}, clk)
	_, err := m.Start("sync", "sync", func(context.Context, func(Progress)) error { return nil })
	if !errors.Is(err, resource.ErrLimited) {
		t.Fatalf("err = %v, want ErrLimited", err)
	}
	var limited *resource.LimitError
	if !errors.As(err, &limited) || limited.Kind != resource.CPU || limited.Limit != 80 {
		t.Fatalf("err = %+v, want the CPU ceiling", err)
	}
	if got := m.List(); len(got) != 0 {
		t.Fatalf("a refused start created %d jobs", len(got))
	}
}

// TestReportPausesUntilHostRecovers: a running job stops between units when
// the host crosses the ceiling, says why on the job record, and resumes by
// itself on the guard's own cadence.
func TestReportPausesUntilHostRecovers(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	bus := events.New()
	sub, err := events.Subscribe(bus, TopicJob, 32)
	if err != nil {
		t.Fatal(err)
	}
	sampler := &guardSampler{seq: []resource.Usage{hostUsage(10), hostUsage(95), hostUsage(50)}}
	m, g := guardedManager(t, bus, sampler, clk)

	release := make(chan struct{})
	j, err := m.Start("index", "index", func(_ context.Context, report func(Progress)) error {
		<-release
		report(Progress{Done: 1, Total: 10, Unit: "items"})
		report(Progress{Done: 2, Total: 10, Unit: "items"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The admission reading is still cached at the same instant; move past
	// the interval so the first report reads the overloaded host.
	clk.Advance(g.Interval())
	close(release)
	info := wait(t, j)
	if info.Status != runstate.Succeeded || info.Paused != nil {
		t.Fatalf("job = %+v", info)
	}
	sleeps := clk.Sleeps()
	if len(sleeps) != 1 || sleeps[0] != g.Interval() {
		t.Fatalf("sleeps = %v, want one guard interval", sleeps)
	}

	var pauses, resumes int
	for {
		select {
		case event := <-sub.C:
			switch {
			case event.Paused != nil:
				pauses++
				if event.Paused.Kind != resource.CPU || event.Paused.Limit != 80 || event.Paused.Percent != 95 {
					t.Fatalf("pause event = %+v", event.Paused)
				}
			case pauses > resumes:
				resumes++
			}
		default:
			if pauses != 1 || resumes != 1 {
				t.Fatalf("pause events = %d, resume events = %d, want one each", pauses, resumes)
			}
			return
		}
	}
}

func usage3(cpu, ram, disk float64) resource.Usage {
	known := func(p float64) resource.Reading { return resource.Reading{Percent: p, Known: true} }
	return resource.Usage{CPU: known(cpu), RAM: known(ram), Disk: known(disk)}
}

func admissionManager(t *testing.T, seq ...resource.Usage) (*Manager, *resource.Guard, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))
	cfg := configtest.New(t, map[string]any{
		"resource.cpu.max_percent":  80,
		"resource.ram.max_percent":  80,
		"resource.disk.max_percent": 80,
	})
	sampler := &guardSampler{seq: seq}
	g := resource.NewGuardWithSampler(cfg, t.TempDir(), clk, sampler)
	return NewManager(cfg, nil, clk, WithGuard(g)), g, clk
}

// TestEssentialStartIgnoresCPUAndMemory: automatic mail fetching must keep
// running while the host is busy. The same host refuses a heavy analysis
// job, which respects every ceiling.
func TestEssentialStartIgnoresCPUAndMemory(t *testing.T) {
	m, _, _ := admissionManager(t, usage3(95, 95, 10))
	noop := func(context.Context, func(Progress)) error { return nil }
	if _, err := m.Start("index", "index", noop); !errors.Is(err, resource.ErrLimited) {
		t.Fatalf("analysis start = %v, want a ceiling refusal", err)
	}
	j, err := m.StartEssential("sync", "sync", noop)
	if err != nil {
		t.Fatalf("essential sync was refused: %v", err)
	}
	if info := wait(t, j); info.Status != runstate.Succeeded {
		t.Fatalf("essential sync = %+v", info)
	}
}

// TestEssentialJobPausesOnDiskOnly: the disk ceiling still stops an
// essential writer, and it resumes by itself when the volume recovers.
func TestEssentialJobPausesOnDiskOnly(t *testing.T) {
	m, g, clk := admissionManager(t,
		usage3(95, 95, 10), // admission: CPU/RAM pressure, disk fine
		usage3(95, 95, 95), // first report: disk above the ceiling
		usage3(10, 10, 10), // after the pause: recovered
	)
	release := make(chan struct{})
	j, err := m.StartEssential("sync", "sync", func(_ context.Context, report func(Progress)) error {
		<-release
		report(Progress{Done: 1, Total: 2, Unit: "items"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(g.Interval())
	close(release)
	if info := wait(t, j); info.Status != runstate.Succeeded || info.Paused != nil {
		t.Fatalf("essential job = %+v", info)
	}
	sleeps := clk.Sleeps()
	if len(sleeps) != 1 || sleeps[0] != g.Interval() {
		t.Fatalf("sleeps = %v, want one disk pause", sleeps)
	}
}
