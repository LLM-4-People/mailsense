package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrBudget is returned by an adapter whose next call the run's budget
// cannot cover (the call is not made; earlier calls' results are kept).
var ErrBudget = errors.New("engine: the run's budget cannot cover the next call")

// Budget is a run's spending limit: the most calls and the most estimated
// USD it may spend. MaxUSD cannot hold for an unpriced provider, so such a
// run is refused unless AllowUnpriced says the owner accepts it. A zero
// MaxCalls or MaxUSD is a limit of zero (every call is refused), never
// "unlimited": paid runs must state their ceiling.
type Budget struct {
	MaxCalls      int     `json:"max_calls"`
	MaxUSD        float64 `json:"max_usd"`
	AllowUnpriced bool    `json:"allow_unpriced"`
}

// Refuse explains why an estimate exceeds the budget ("" = within it).
func (b Budget) Refuse(e Estimate) string {
	switch {
	case e.Calls > b.MaxCalls:
		return fmt.Sprintf("needs %d calls, the budget allows %d", e.Calls, b.MaxCalls)
	case e.Calls > 0 && !e.PriceKnown && !b.AllowUnpriced:
		return "the provider has no price, so the USD budget cannot hold: set its price in engine.providers.file or allow unpriced providers"
	case e.PriceKnown && e.EstimatedUSD > b.MaxUSD:
		return fmt.Sprintf("estimated $%.4f, the budget allows $%.4f", e.EstimatedUSD, b.MaxUSD)
	}
	return ""
}

// Reserver is a run's spending guard. An adapter asks it before every
// billable call and reports what the call consumed afterwards, so calls in
// flight on parallel workers are always covered: the guard sees spent +
// reserved, never an estimate that a call could exceed.
type Reserver interface {
	// Reserve holds a call's ceiling (its worst-case cost); false means
	// the budget cannot cover it and the call must not be made.
	Reserve(ceiling Estimate) bool
	// Settle replaces the reservation by what the call consumed.
	Settle(ceiling Estimate, used Usage)
}

// BudgetGuard is the one engine.Reserver: it enforces a Budget over the
// calls in flight and accumulates what settled calls consumed. An adapter
// and a run share it through the context (WithReserver), so no consumer
// re-implements the guard's arithmetic.
type BudgetGuard struct {
	mu       sync.Mutex
	budget   Budget
	usage    Usage
	reserved Estimate // ceilings of the calls in flight (their prices only)
	unpriced int      // calls in flight whose price is unknown
	reason   string   // the refusal that stopped the run ("" = none)
}

// NewBudgetGuard tracks a run's spend against b.
func NewBudgetGuard(b Budget) *BudgetGuard { return &BudgetGuard{budget: b} }

// Reserve holds a call's ceiling while spent + in flight + it stays
// within the budget; otherwise the run stops for the budget.
func (g *BudgetGuard) Reserve(ceiling Estimate) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reason != "" {
		return false
	}
	next := Estimate{Calls: g.usage.Calls, EstimatedUSD: g.usage.EstimatedUSD, PriceKnown: true}
	next.Add(g.reserved)
	next.Add(ceiling)
	next.PriceKnown = ceiling.PriceKnown && g.unpriced == 0
	if reason := g.budget.Refuse(next); reason != "" {
		g.reason = reason
		return false
	}
	g.reserved.Add(ceiling)
	g.reserved.PriceKnown = true // priced-ness is counted, not stuck (Estimate.Add never restores it)
	if !ceiling.PriceKnown {
		g.unpriced++
	}
	return true
}

// Settle replaces a reservation by the call's usage; a call that cost more
// than its ceiling is still counted (the provider billed past its reserve).
func (g *BudgetGuard) Settle(ceiling Estimate, used Usage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserved.Sub(ceiling)
	if !ceiling.PriceKnown {
		g.unpriced--
	}
	g.usage.Add(used)
}

// Charge is the one reserve/settle protocol of a paid call: it reserves the
// ceiling, runs call, and settles with the usage call reports even when it
// failed (a failed call may still be billed). A refused reservation returns
// ErrBudget without running call, so a refusal costs nothing.
func Charge[T any](ctx context.Context, ceiling Estimate, call func() (T, Usage, error)) (T, Usage, error) {
	var zero T
	guard := ReserverOf(ctx)
	if guard != nil && !guard.Reserve(ceiling) {
		return zero, Usage{}, ErrBudget
	}
	value, used, err := call()
	if guard != nil {
		guard.Settle(ceiling, used)
	}
	return value, used, err
}

// Usage is what settled calls consumed (a copy).
func (g *BudgetGuard) Usage() Usage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.usage
}

// Stopped reports whether a reservation was refused.
func (g *BudgetGuard) Stopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason != ""
}

// Reason is the refusal that stopped the run ("" = none).
func (g *BudgetGuard) Reason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}

type reserverKey struct{}

// WithReserver returns a context whose adapter calls go through r.
func WithReserver(ctx context.Context, r Reserver) context.Context {
	return context.WithValue(ctx, reserverKey{}, r)
}

// ReserverOf is the context's guard (nil = no budget).
func ReserverOf(ctx context.Context) Reserver {
	r, _ := ctx.Value(reserverKey{}).(Reserver)
	return r
}
