package triage

import (
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Report is the outcome of one run (stored as JSON with the run).
type Report struct {
	RunID        int64          `json:"run_id"`
	State        runstate.State `json:"state"`
	Plan         *Plan          `json:"plan"`
	Decided      int            `json:"decided"`
	Partial      int            `json:"partial"` // some answers paid for and kept, the rest asked next run
	Failed       int            `json:"failed"`
	NotAttempted int            `json:"not_attempted"` // budget reached, provider unavailable or transient, cancelled
	StoppedBy    string         `json:"stopped_by,omitempty"`
	Usage        engine.Usage   `json:"usage"`
	// Overruns counts calls that cost more than the ceiling reserved for
	// them (a provider billing past reserve_tokens_per_byte).
	Overruns int       `json:"overruns,omitempty"`
	Failures []Failure `json:"failures,omitempty"` // first triage.max_logged_failures; all are stored
	Error    string    `json:"error,omitempty"`
}

// Drained reports whether this run handled the whole pending backlog it
// planned: Pending is the number of messages that needed work before the
// batch, Decided and Partial are the ones this run stored. A backlog larger
// than the batch leaves it false, so the automatic catch-up keeps going and
// the layer-1 trigger waits for the mailbox to settle.
func (r Report) Drained() bool {
	if r.Plan == nil {
		return true
	}
	return r.Decided+r.Partial >= r.Plan.Pending
}

// Failure is one message the run could not decide.
type Failure struct {
	MessageID int64  `json:"message_id"`
	Error     string `json:"error"`
}

// Progress is published while a run decides.
type Progress struct {
	RunID    int64        `json:"run_id"`
	Done     int          `json:"done"`
	Total    int          `json:"total"`
	Decided  int          `json:"decided"`
	Failed   int          `json:"failed"`
	Usage    engine.Usage `json:"usage"`
	Estimate float64      `json:"estimated_usd"` // of the whole plan
}

// Topics of triage runs.
var (
	TopicProgress = events.NewTopic[Progress]("triage.progress")
	TopicFinished = events.NewTopic[Report]("triage.finished")
)

// Stop reasons.
const (
	StoppedBudget      = "budget"
	StoppedUnavailable = "provider_unavailable"
	StoppedTransient   = "provider_transient" // rate-limited, gateway down: retry the same mail later
	StoppedCancelled   = "cancelled"
)
