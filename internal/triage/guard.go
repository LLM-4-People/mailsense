package triage

import (
	"sync"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// runState is the shared state of one run's workers and its budget guard
// (engine.Reserver): the adapter reserves every call's ceiling before
// sending it and settles its usage after, so calls in flight on parallel
// workers can never take the run past its budget. The guard's arithmetic
// is the engine's (engine.BudgetGuard); runState adds the run's stop state
// and counters.
type runState struct {
	mu     sync.Mutex
	rep    *Report
	guard  *engine.BudgetGuard
	stop   string
	output error
	logged int
}

func newRunState(rep *Report, b Budget) *runState {
	return &runState{rep: rep, guard: engine.NewBudgetGuard(b)}
}

// Reserve holds a call's ceiling while spent + in flight + it stays
// within the budget; otherwise the run stops for the budget.
func (rs *runState) Reserve(ceiling engine.Estimate) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.stop != "" || rs.output != nil {
		return false
	}
	if !rs.guard.Reserve(ceiling) {
		rs.stop = StoppedBudget
		return false
	}
	return true
}

// Settle replaces a reservation by the call's usage; a call that cost more
// than its ceiling is counted (the provider billed past its reserve).
func (rs *runState) Settle(ceiling engine.Estimate, used engine.Usage) {
	rs.guard.Settle(ceiling, used)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.rep.Usage.Add(used)
	if used.EstimatedUSD > ceiling.EstimatedUSD {
		rs.rep.Overruns++
	}
}

func (rs *runState) stopped() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.stop != "" || rs.output != nil
}
