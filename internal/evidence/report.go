package evidence

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Report is the outcome of one evidence run.
type Report struct {
	RunID     int64          `json:"run_id"`
	State     runstate.State `json:"state"`
	Error     string         `json:"error,omitempty"`
	Schema    int64          `json:"schema"`
	Signals   int            `json:"signals"`
	Space     string         `json:"space,omitempty"` // the vector space template_matches read ("" none)
	Messages  int            `json:"messages"`        // visible messages
	Done      int            `json:"done"`            // messages handled
	Computed  int            `json:"computed"`        // of them, with a message-only signal computed again
	Written   int            `json:"written"`         // of them, with stored values that changed
	Cancelled bool           `json:"cancelled,omitempty"`
	// Timings: the shared context (pass 1) and the values (pass 2).
	ContextTime time.Duration `json:"context_time"`
	ValuesTime  time.Duration `json:"values_time"`
	Duration    time.Duration `json:"duration"`
	PerSecond   float64       `json:"messages_per_second"`
}

// Progress is a running evidence run's position.
type Progress struct {
	RunID int64 `json:"run_id"`
	Done  int   `json:"done"`
	Total int   `json:"total"`
}

// Topics of evidence runs.
var (
	TopicProgress = events.NewTopic[Progress]("evidence.progress")
	TopicFinished = events.NewTopic[Report]("evidence.finished")
)
