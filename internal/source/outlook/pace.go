package outlook

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
)

// Pacing: every guarded call runs on Outlook's own user-interface thread,
// so the connector bounds the load it puts there at the one choke point
// every call passes (guard.invoke): a share of wall time inside Outlook
// and a call rate, lower while the user is using the computer, and lower
// again after lost connections (breaker.factor). Unthrottled, a detail
// pass made ≈1,250 calls per second; Outlook crashed twice under that
// load (docs/OUTLOOK_SYNC.md, "Load and crash safety").

// paceLimits bound the load: the largest share of wall time spent inside
// Outlook calls and the most calls per second.
type paceLimits struct {
	Share   float64       `json:"busy_share"`
	Rate    float64       `json:"calls_per_second"`
	MaxWait time.Duration `json:"max_wait"`
}

// scaled is l with both limits multiplied by f (0 < f ≤ 1).
func (l paceLimits) scaled(f float64) paceLimits {
	return paceLimits{Share: l.Share * f, Rate: l.Rate * f, MaxWait: l.MaxWait}
}

// next is the earliest start of the call after one that ran from start
// to end: the rest that keeps the busy share, and the rate's interval
// counted from the call's start, but never more than MaxWait after the
// end. Time not spent in Outlook between calls (the connector's own work)
// counts as rest; rest never carries over. The wait is computed in
// seconds (float): limits scaled toward zero give a huge, infinite or NaN
// wait, which MaxWait bounds, instead of overflowing time.Duration.
func (l paceLimits) next(start, end time.Time) time.Time {
	busy := end.Sub(start).Seconds()
	wait := max(busy*(1-l.Share)/l.Share, 1/l.Rate-busy)
	if limit := l.MaxWait.Seconds(); !(wait < limit) { // NaN and +Inf included
		wait = limit
	}
	if wait <= 0 {
		return end
	}
	return end.Add(time.Duration(wait * float64(time.Second)))
}

// paceSettings are the outlook.pace.* settings.
type paceSettings struct {
	idle, active paceLimits
	idleAfter    time.Duration
}

func paceFrom(snap *config.Snapshot) paceSettings {
	wait := snap.Duration("outlook.pace.max_wait")
	return paceSettings{
		idle:      paceLimits{Share: snap.Float("outlook.pace.busy_share"), Rate: snap.Float("outlook.pace.calls_per_second"), MaxWait: wait},
		active:    paceLimits{Share: snap.Float("outlook.pace.active.busy_share"), Rate: snap.Float("outlook.pace.active.calls_per_second"), MaxWait: wait},
		idleAfter: snap.Duration("outlook.pace.idle_after"),
	}
}

// pacer is the connector's pacing state, shared by every guard of every
// operation (calls happen one at a time on the apartment thread; the
// mutex only makes Stats safe to read meanwhile). Every call into Outlook
// passes it: member calls, their retries, and the final Release of every
// object the connector holds (a cross-process call too).
type pacer struct {
	clock clock.Clock
	idle  func() (time.Duration, error) // the user's idle time (host.UserIdle)
	log   *slog.Logger

	mu       sync.Mutex
	next     time.Time     // earliest start of the next call
	active   bool          // the user counted as active at the last call
	idleErr  bool          // UserIdle failed (logged once; counted as active)
	paused   time.Duration // total time waited
	activeN  int64         // calls made under the active limits
	switches int64         // active ↔ away transitions
}

// pacing is one operation's view of the pacer: its settings and the
// breaker's factor, fixed for the operation.
type pacing struct {
	p      *pacer
	s      paceSettings
	factor float64
}

// limits are the limits in force now: the active ones while the user's
// last input is more recent than idleAfter (or cannot be read), scaled by
// the breaker's factor.
func (g pacing) limits() paceLimits {
	idle, err := g.p.idle()
	p := g.p
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil && !p.idleErr {
		p.log.Warn("cannot read the user's idle time; pacing as if the user were active", "err", err)
	}
	p.idleErr = err != nil
	active := err != nil || idle < g.s.idleAfter
	if active != p.active {
		p.switches++
		p.log.Debug("user activity changed the Outlook pace", "active", active, "idle", idle)
	}
	p.active = active
	l := g.s.idle
	if active {
		p.activeN++
		l = g.s.active
	}
	return l.scaled(g.factor)
}

// wait blocks until the next call may start (ctx ends the wait).
func (g pacing) wait(ctx context.Context) error {
	p := g.p
	p.mu.Lock()
	d := p.next.Sub(p.clock.Now())
	if d > 0 {
		p.paused += d
	}
	p.mu.Unlock()
	if d <= 0 {
		return nil
	}
	return p.clock.Sleep(ctx, d)
}

// done records a call that ran from start to end under l.
func (g pacing) done(l paceLimits, start, end time.Time) {
	g.p.mu.Lock()
	g.p.next = l.next(start, end)
	g.p.mu.Unlock()
}

// PaceStats are the pacing measurements of a connector.
type PaceStats struct {
	Paused      time.Duration `json:"paused"`       // total time calls waited for the pace
	ActiveCalls int64         `json:"active_calls"` // calls made while the user was active
	Switches    int64         `json:"switches"`     // user active ↔ away transitions seen
}

func (p *pacer) stats() PaceStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PaceStats{Paused: p.paused, ActiveCalls: p.activeN, Switches: p.switches}
}
