package index

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Report is the outcome of one index run (persisted in index_runs).
type Report struct {
	RunID int64          `json:"run_id"`
	State runstate.State `json:"state"`
	Space string         `json:"space"`
	// TextVersion is the version of the cleaning rules the run's texts
	// were made with (vectors carry it: a new one rechecks them).
	TextVersion string   `json:"text_version"`
	Before      Coverage `json:"before"`
	After       Coverage `json:"after"`
	Selected    int      `json:"selected"` // messages looked at
	Embedded    int      `json:"embedded"`
	// Unchanged: changed messages whose embedded text did not change (a
	// read flag, a move): their vectors were kept.
	Unchanged    int           `json:"unchanged"`
	Failed       int           `json:"failed"`
	Quarantined  int           `json:"quarantined"` // messages this run left out until they change
	NotAttempted int           `json:"not_attempted"`
	Truncated    int           `json:"truncated"` // texts cut to embed.max_input_tokens
	Usage        engine.Usage  `json:"usage"`
	Duration     time.Duration `json:"duration"`
	// Throughput of the embedded messages over the run's wall time.
	MailsPerSecond  float64   `json:"mails_per_second"`
	TokensPerSecond float64   `json:"tokens_per_second"`
	StoppedBy       string    `json:"stopped_by,omitempty"`
	Failures        []Failure `json:"failures,omitempty"`
	Error           string    `json:"error,omitempty"`
}

// Attempted is how many selected messages the run looked at: stored or
// confirmed, plus failed. NotAttempted is the rest, so the two always add up to
// Selected.
func (r Report) Attempted() int { return r.Embedded + r.Unchanged + r.Failed }

// Committed reports whether the run stored or confirmed at least one vector.
// Embedded and Unchanged are incremented only after SaveEmbeddings or
// ConfirmEmbeddings returned: a run stopped before any write (unavailable,
// transient, store busy, cancelled, every batch failed) committed nothing,
// and a succeeded run that selected nothing (0/0) committed nothing either.
// Evidence is a consistent snapshot of the mailbox: a partial run that
// committed vectors is worth a run (the missing vectors are represented),
// a run that committed nothing is not.
func (r Report) Committed() bool { return r.Embedded > 0 || r.Unchanged > 0 }

// Failure is one failed batch.
type Failure struct {
	Messages []int64 `json:"messages"`
	Error    string  `json:"error"`
}

// Why a run stopped early.
const (
	StoppedUnavailable = "unavailable" // the embedder cannot serve (not installed, rejected, unreachable)
	StoppedTransient   = "transient"   // the provider is temporarily overloaded or rate-limited
	StoppedBusy        = "store_busy"  // another writer held the database past every attempt
	StoppedCancelled   = "cancelled"
)

// Progress is published after every batch.
type Progress struct {
	RunID     int64 `json:"run_id"`
	Done      int   `json:"done"`
	Total     int   `json:"total"`
	Embedded  int   `json:"embedded"`
	Unchanged int   `json:"unchanged"`
	Failed    int   `json:"failed"`
	Tokens    int64 `json:"tokens"`
}

// Topics.
var (
	TopicProgress = events.NewTopic[Progress]("index.progress")
	TopicFinished = events.NewTopic[Report]("index.finished")
)
