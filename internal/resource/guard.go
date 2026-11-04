// Package resource is the one owner of the owner-facing CPU, memory and disk
// ceilings. Settings own the limits, the host sampler owns the readings, and
// jobs.Manager is the single admission and pacing choke point: every
// background job passes through it, so no producer can bypass the guard.
//
// The readings are host-wide, not per process. A ceiling protects the whole
// machine: when another application has already pushed the host above it,
// starting one more heavy job would make the owner's computer unresponsive.
// Disk is the used share of the volume that holds the data directory, so the
// app can never fill it.
package resource

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Kind names one guarded resource.
type Kind string

// Guarded resources.
const (
	CPU  Kind = "cpu"
	RAM  Kind = "ram"
	Disk Kind = "disk"
)

// margin is the hysteresis below a ceiling, in percentage points. A job that
// paused at 85% resumes at 82%, never at 84.9%: without the gap a job would
// flap between running and paused on every reading.
const margin = 3.0

// Reading is one host measurement. Percent is meaningless when Known is
// false: the first CPU sample has no previous counters to compare against,
// and a platform without a metric keeps it unknown.
type Reading struct {
	Percent float64 `json:"percent"`
	Known   bool    `json:"known"`
}

// Usage is the host state at one instant.
type Usage struct {
	CPU       Reading   `json:"cpu"`
	RAM       Reading   `json:"ram"`
	Disk      Reading   `json:"disk"`
	SampledAt time.Time `json:"sampled_at"`
}

// Limits is the effective policy, read from settings on every decision so a
// saved change applies without a restart.
type Limits struct {
	Enabled bool `json:"enabled"`
	CPU     int  `json:"cpu"`
	RAM     int  `json:"ram"`
	Disk    int  `json:"disk"`
}

// LimitsOf reads the guard policy from the one settings owner.
func LimitsOf(cfg *config.Config) Limits {
	return Limits{
		Enabled: cfg.Bool("resource.guard"),
		CPU:     cfg.Int("resource.cpu.max_percent"),
		RAM:     cfg.Int("resource.ram.max_percent"),
		Disk:    cfg.Int("resource.disk.max_percent"),
	}
}

// Admission says which ceilings one caller respects. Not every job is equal:
// mail fetching must keep running while the host is busy with CPU or memory
// work, because stopping it means new mail never arrives. The disk ceiling
// still governs it, because that is the one a write can fill.
type Admission int

const (
	// AdmitAll respects every ceiling. It is the zero value and the policy of
	// every heavy analysis job.
	AdmitAll Admission = iota
	// AdmitWrites respects only the disk ceiling: an essential writer that
	// must not be starved by CPU or memory pressure.
	AdmitWrites
)

// Respects reports whether a ceiling governs this admission.
func (a Admission) Respects(k Kind) bool { return a != AdmitWrites || k == Disk }

// ErrLimited is the class of every refusal or pause caused by a ceiling.
// Callers use errors.Is, and the API maps it to one localized message.
var ErrLimited = errors.New("resource: host limit reached")

// ErrUnsupported reports a platform without host readings. It never blocks
// work: an unreadable metric is unknown, never a violation.
var ErrUnsupported = errors.New("resource: host metrics are not supported on this platform")

// LimitError names the ceiling that stopped work. It is both the job pause
// record and the API error detail, so the UI composes one message from the
// percent and the limit.
type LimitError struct {
	Kind    Kind      `json:"resource"`
	Percent float64   `json:"percent"`
	Limit   int       `json:"limit"`
	At      time.Time `json:"at"`
}

// Error implements error.
func (e *LimitError) Error() string {
	return fmt.Sprintf("resource: %s at %.0f%% is above the %d%% limit", e.Kind, e.Percent, e.Limit)
}

// Unwrap makes errors.Is(err, ErrLimited) true for every ceiling.
func (e *LimitError) Unwrap() error { return ErrLimited }

// Sampler is the platform seam. One reading reflects the host since the
// previous call, and a metric that cannot be read is left unknown rather
// than reported as zero.
type Sampler interface {
	Sample(ctx context.Context) (Usage, error)
}

// Status is the projection the API serves: current readings, the effective
// limits and the ceiling currently stopping work, if any.
type Status struct {
	Usage  Usage       `json:"usage"`
	Limits Limits      `json:"limits"`
	Paused *LimitError `json:"paused,omitempty"`
}

// Guard evaluates the ceilings over a cached host reading. It is safe for
// concurrent use: background jobs, the scheduler and the status route all
// read through the same instance.
type Guard struct {
	cfg     *config.Config
	clk     clock.Clock
	sampler Sampler
	log     *slog.Logger

	mu        sync.Mutex
	usage     Usage
	have      bool
	blocked   map[Kind]bool
	warned    bool
	lastPause Kind
}

// NewGuard builds the production guard over the platform sampler for the
// volume that holds diskPath.
func NewGuard(cfg *config.Config, diskPath string, clk clock.Clock) *Guard {
	return NewGuardWithSampler(cfg, diskPath, clk, NewHostSampler(diskPath))
}

// NewGuardWithSampler injects a sampler for tests.
func NewGuardWithSampler(cfg *config.Config, diskPath string, clk clock.Clock, sampler Sampler) *Guard {
	if cfg == nil || sampler == nil {
		return nil
	}
	return &Guard{
		cfg:     cfg,
		clk:     clock.Or(clk),
		sampler: sampler,
		log:     logx.For("resource"),
		blocked: map[Kind]bool{},
	}
}

// Interval is the reading age the owner configured. A paused job re-checks
// on this cadence and the cached usage never outlives it.
func (g *Guard) Interval() time.Duration {
	if g == nil {
		return 0
	}
	d := g.cfg.Duration("resource.sample_interval")
	if d <= 0 {
		d = 2 * time.Second
	}
	return d
}

// Limits returns the current policy.
func (g *Guard) Limits() Limits {
	if g == nil {
		return Limits{}
	}
	return LimitsOf(g.cfg)
}

// Check returns the ceiling currently stopping work under every policy.
func (g *Guard) Check(ctx context.Context) (*LimitError, error) {
	return g.CheckAdmission(ctx, AdmitAll)
}

// CheckAdmission returns the ceiling currently stopping work for one
// admission class. A ceiling the class does not respect is left untouched:
// its blocked state belongs to the jobs that do respect it. An unreadable
// host leaves work allowed: unknown is never a violation.
func (g *Guard) CheckAdmission(ctx context.Context, a Admission) (*LimitError, error) {
	if g == nil {
		return nil, nil
	}
	limits := g.Limits()
	if !limits.Enabled {
		g.forget()
		return nil, nil
	}
	usage, err := g.read(ctx)
	if err != nil {
		return nil, err
	}

	type transition struct {
		kind Kind
		on   bool
		lim  int
	}
	var events []transition
	g.mu.Lock()
	for _, b := range bounds(limits, usage) {
		if !a.Respects(b.kind) {
			continue
		}
		if !b.reading.Known || b.limit >= 100 {
			if g.blocked[b.kind] {
				delete(g.blocked, b.kind)
				events = append(events, transition{b.kind, false, b.limit})
			}
			continue
		}
		on := g.blocked[b.kind]
		if on {
			if b.reading.Percent <= float64(b.limit)-margin {
				delete(g.blocked, b.kind)
				events = append(events, transition{b.kind, false, b.limit})
			}
		} else if b.reading.Percent > float64(b.limit) {
			g.blocked[b.kind] = true
			events = append(events, transition{b.kind, true, b.limit})
		}
	}
	var paused *LimitError
	for _, b := range bounds(limits, usage) {
		if a.Respects(b.kind) && g.blocked[b.kind] {
			paused = &LimitError{Kind: b.kind, Percent: b.reading.Percent, Limit: b.limit, At: usage.SampledAt}
			break
		}
	}
	g.mu.Unlock()

	for _, e := range events {
		if e.on {
			g.log.Info("background work paused by the resource guard",
				"resource", string(e.kind), "limit_percent", e.lim)
			continue
		}
		g.log.Info("background work resumed: the host is under the resource limit",
			"resource", string(e.kind), "limit_percent", e.lim)
	}
	return paused, nil
}

// Wait blocks until the host is under every ceiling or ctx ends. It returns
// the last ceiling that was stopping work, so the caller can report why the
// job is paused; nil means work may run.
func (g *Guard) Wait(ctx context.Context) *LimitError {
	return g.WaitAdmission(ctx, AdmitAll, nil)
}

// WaitAdmission blocks until the host is under the ceilings one admission class
// respects or ctx ends. onChange, when set, hears the current ceiling on entry
// and nil once the host recovers, so a job can publish its pause transition.
// The poll sleeps on the guard's own interval: the hysteresis, the admission
// class and the reading live here, and no caller re-spells the loop.
func (g *Guard) WaitAdmission(ctx context.Context, admission Admission, onChange func(*LimitError)) *LimitError {
	if g == nil {
		return nil
	}
	for {
		paused, err := g.CheckAdmission(ctx, admission)
		if err != nil || paused == nil {
			if onChange != nil {
				onChange(nil)
			}
			return nil
		}
		if onChange != nil {
			onChange(paused)
		}
		if err := g.clk.Sleep(ctx, g.Interval()); err != nil {
			return paused
		}
	}
}

// State reads the current reading and policy for the status projection. A
// sampler failure still reports the limits, so the owner sees the policy
// even when the platform cannot measure it.
func (g *Guard) State(ctx context.Context) (Status, error) {
	if g == nil {
		return Status{}, nil
	}
	limits := g.Limits()
	usage, err := g.read(ctx)
	if err != nil {
		return Status{Limits: limits}, err
	}
	paused, _ := g.Check(ctx)
	return Status{Usage: usage, Limits: limits, Paused: paused}, nil
}

type bound struct {
	kind    Kind
	reading Reading
	limit   int
}

// bounds fixes the evaluation order: disk first (it cannot recover while a
// job writes), then memory, then CPU. The first blocked resource is the one
// reported, so one message names the most consequential ceiling.
func bounds(limits Limits, usage Usage) []bound {
	return []bound{
		{Disk, usage.Disk, limits.Disk},
		{RAM, usage.RAM, limits.RAM},
		{CPU, usage.CPU, limits.CPU},
	}
}

func (g *Guard) forget() {
	g.mu.Lock()
	g.blocked = map[Kind]bool{}
	g.mu.Unlock()
}

// read returns a cached reading while it is younger than the configured
// interval, so a job reporting per message does not sample the host per
// message.
func (g *Guard) read(ctx context.Context) (Usage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.have && g.clk.Now().Sub(g.usage.SampledAt) < g.Interval() {
		return g.usage, nil
	}
	usage, err := g.sampler.Sample(ctx)
	if err != nil {
		if !g.warned {
			g.warned = true
			g.log.Warn("host resource readings unavailable; ceilings are not enforced", "err", err)
		}
		return Usage{}, err
	}
	// The guard's clock owns the reading age, not the sampler: a test clock
	// and a host sampler must never disagree about when the reading was
	// taken.
	usage.SampledAt = g.clk.Now()
	g.usage, g.have = usage, true
	return usage, nil
}
