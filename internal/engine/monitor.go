package engine

import (
	"context"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/secrets"
)

// ProviderStatus is what the UI shows about a provider.
type ProviderStatus struct {
	Provider   string    `json:"provider"`
	KeyPresent bool      `json:"key_present"`
	Healthy    bool      `json:"healthy"` // the last call succeeded (false before any call)
	Calls      int64     `json:"calls"`
	Failures   int64     `json:"failures"`
	LastCall   time.Time `json:"last_call,omitzero"`
	LastStatus int       `json:"last_status,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// TopicProviderHealth is published when a provider's health changes
// (first call, success after failure, failure after success).
var TopicProviderHealth = events.NewTopic[ProviderStatus]("engine.provider")

// Monitor tracks provider health across every engine kind (adapters are
// built per run) and forwards every usage record to the persisted
// counters. It is the UsageSink adapters report to.
type Monitor struct {
	mu     sync.Mutex
	status map[string]*ProviderStatus
	bus    *events.Bus
	next   UsageSink
	clock  clock.Clock
}

// NewMonitor returns a monitor; bus, next and clk may be nil.
func NewMonitor(bus *events.Bus, next UsageSink, clk clock.Clock) *Monitor {
	return &Monitor{status: map[string]*ProviderStatus{}, bus: bus, next: next, clock: clock.Or(clk)}
}

// WithMonitor returns deps with the one app-wide health monitor installed:
// deps.Monitor when the caller supplied one, otherwise a new monitor over the
// bus, sink and clock. Usage is routed through the monitor, so every adapter
// shares one health registry and one usage sink; a factory that wired this
// itself is how two monitors (and two usage streams) appear.
func WithMonitor(deps Deps) Deps {
	mon := deps.Monitor
	if mon == nil {
		mon = NewMonitor(deps.Bus, deps.Sink, deps.Clock)
	}
	deps.Sink = mon
	deps.Monitor = mon
	return deps
}

// AddUsage records one call and forwards it.
func (m *Monitor) AddUsage(ctx context.Context, rec UsageRecord) error {
	m.mu.Lock()
	st := m.status[rec.Provider]
	if st == nil {
		st = &ProviderStatus{Provider: rec.Provider}
		m.status[rec.Provider] = st
	}
	changed := st.Calls == 0 || st.Healthy == rec.Failed
	// A provider error body can echo a prompt, an address or a credential.
	// Keep the same redacted form the log pipeline would have kept.
	rec.Error = logx.Redact(rec.Error)
	st.Calls++
	st.Healthy, st.LastCall, st.LastStatus, st.LastError = !rec.Failed, m.clock.Now().UTC(), rec.Status, rec.Error
	if rec.Failed {
		st.Failures++
	}
	snap := *st
	m.mu.Unlock()
	if changed && m.bus != nil {
		events.Publish(m.bus, TopicProviderHealth, snap)
	}
	if m.next != nil {
		return m.next.AddUsage(ctx, rec)
	}
	return nil
}

// Status returns a provider's status, including whether its key is set. A
// record without a secret needs none (its runtime supplies the key).
func (m *Monitor) Status(p Provider, sec secrets.Store) ProviderStatus {
	m.mu.Lock()
	st := ProviderStatus{Provider: p.ID}
	if s := m.status[p.ID]; s != nil {
		st = *s
	}
	m.mu.Unlock()
	if p.APIKeySecret == "" {
		st.KeyPresent = true
		return st
	}
	if sec != nil {
		v, err := sec.Get(p.APIKeySecret)
		st.KeyPresent = err == nil && v != ""
	}
	return st
}
