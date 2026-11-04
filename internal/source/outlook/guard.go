package outlook

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

// retryPolicy is the backoff for calls Outlook declines while busy.
type retryPolicy struct {
	attempts int
	base     time.Duration
	max      time.Duration
}

func (p retryPolicy) delay(attempt int) time.Duration {
	d := p.base << min(attempt-1, 30)
	if d <= 0 || d > p.max {
		return p.max
	}
	return d
}

// counters are the call statistics of a guard and its children: totals,
// and count and time per Class.Member.
type counters struct {
	calls   atomic.Int64
	retries atomic.Int64
	mu      sync.Mutex
	members map[string]*MemberCost
}

// MemberCost is the measured cost of one Class.Member.
type MemberCost struct {
	Calls int64         `json:"calls"`
	Time  time.Duration `json:"time"`
	Max   time.Duration `json:"max"` // the slowest single call (a stalled server shows here)
}

func (c *counters) record(key string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.members == nil {
		c.members = map[string]*MemberCost{}
	}
	m := c.members[key]
	if m == nil {
		m = &MemberCost{}
		c.members[key] = m
	}
	m.Calls++
	m.Time += d
	m.Max = max(m.Max, d)
}

func (c *counters) snapshot() map[string]MemberCost {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]MemberCost, len(c.members))
	for k, v := range c.members {
		out[k] = *v
	}
	return out
}

// guard wraps an Object: only members of model are allowed, every call
// (each retry and the final Release too) waits for the pace (pace.go),
// busy calls are retried (still busy after outlook.com.retry.attempts:
// ErrOutlookBusy), calls are counted, and object results are wrapped too. ctx bounds the
// pacing and retry waits of one connector operation.
type guard struct {
	inner  Object
	class  string
	ctx    context.Context
	policy retryPolicy
	clock  clock.Clock
	count  *counters
	pace   pacing
}

var _ Object = (*guard)(nil)

// wrap guards obj as an object of class.
func (g *guard) wrap(obj Object, class string) *guard {
	if c, ok := obj.(classed); ok {
		c.SetClass(class)
	}
	return &guard{inner: obj, class: class, ctx: g.ctx, policy: g.policy, clock: g.clock, count: g.count, pace: g.pace}
}

func (g *guard) Get(name string, args ...any) (any, error)  { return g.invoke(name, false, args) }
func (g *guard) Call(name string, args ...any) (any, error) { return g.invoke(name, true, args) }

// Put sets a writable view flag (see model.writable). Any other member is
// refused exactly like an unlisted call: the connector stays read-only
// against stored data.
func (g *guard) Put(name string, value any) error {
	if !writable[g.class][name] {
		return fmt.Errorf("%w: %s.%s", ErrNotAllowed, g.class, name)
	}
	for attempt := 1; ; attempt++ {
		var err error
		if werr := g.paced(name, false, func() { err = g.inner.Put(name, value) }); werr != nil {
			return werr
		}
		if hr, ok := HResult(err); ok && retryable(hr) && attempt < g.policy.attempts {
			g.count.retries.Add(1)
			if serr := g.clock.Sleep(g.ctx, g.policy.delay(attempt)); serr != nil {
				return serr
			}
			continue
		}
		return classify(err)
	}
}

// Release releases the object: the final release of a proxy is a call
// into Outlook's thread (IRemUnknown::RemRelease), paced and counted as
// Class.Release like any call. It always happens: once the operation's
// context ended it runs without waiting (an object is never leaked).
func (g *guard) Release() { _ = g.paced("Release", true, g.inner.Release) }

// paced runs one call into Outlook after the pace's wait and counts it as
// Class.member. A wait the context ended returns its error without the
// call, unless always.
func (g *guard) paced(member string, always bool, call func()) error {
	if err := g.pace.wait(g.ctx); err != nil && !always {
		return err
	}
	limits := g.pace.limits()
	g.count.calls.Add(1)
	start := g.clock.Now()
	call()
	end := g.clock.Now()
	g.pace.done(limits, start, end)
	g.count.record(g.class+"."+member, end.Sub(start))
	return nil
}

func (g *guard) invoke(name string, call bool, args []any) (any, error) {
	m, ok := lookup(g.class, name, call)
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s", ErrNotAllowed, g.class, name)
	}
	for attempt := 1; ; attempt++ {
		var v any
		var err error
		if werr := g.paced(name, false, func() {
			if call {
				v, err = g.inner.Call(name, args...)
			} else {
				v, err = g.inner.Get(name, args...)
			}
		}); werr != nil {
			return nil, werr
		}
		if hr, ok := HResult(err); ok && retryable(hr) && attempt < g.policy.attempts {
			g.count.retries.Add(1)
			if serr := g.clock.Sleep(g.ctx, g.policy.delay(attempt)); serr != nil {
				return nil, serr
			}
			continue
		}
		if err != nil {
			return nil, classify(err)
		}
		if v == nil && m.result != "" && !m.nullable {
			return nil, fmt.Errorf("%w: %s.%s", ErrNoObject, g.class, name)
		}
		if obj, ok := v.(Object); ok {
			if m.result == "" {
				obj.Release()
				return nil, fmt.Errorf("outlook: %s.%s returned an object where a value was expected", g.class, name)
			}
			return g.wrap(obj, m.result), nil
		}
		return v, nil
	}
}
