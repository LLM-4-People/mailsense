// Package jobs runs cancellable background work with progress, status and
// errors, published on the event bus for the UI.
package jobs

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Progress is the latest progress report of a job.
type Progress struct {
	Done    int64  `json:"done"`
	Total   int64  `json:"total"` // 0 when unknown
	Unit    string `json:"unit"`
	Message string `json:"message"`
	// Stage names the phase of the work as a stable key the UI translates
	// (jobs.stage.<stage>); "" when the job has no stages.
	Stage string `json:"stage,omitempty"`
	// Detail is the current item within the stage (a question, a file):
	// data, never a sentence, so the UI shows it untranslated.
	Detail string `json:"detail,omitempty"`
	// StepDone and StepTotal are the position within Stage (0/0: unknown).
	StepDone  int64 `json:"step_done,omitempty"`
	StepTotal int64 `json:"step_total,omitempty"`
	// ETA is the estimated time left. The manager fills it from the job's
	// own rate; a producer that measures a better one sets it itself.
	ETA *time.Duration `json:"eta,omitempty"`
}

// Info is an immutable snapshot of a job.
type Info struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Name       string         `json:"name"`
	Status     runstate.State `json:"status"`
	Progress   Progress       `json:"progress"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at,omitzero"`
	// Paused is the resource ceiling currently stopping this job. It is nil
	// while the job runs. The UI names the ceiling and its limit, so a job
	// that waits for the host is never mistaken for a hang.
	Paused *resource.LimitError `json:"paused,omitempty"`
}

// TopicJob carries every status and progress change.
var TopicJob = events.NewTopic[Info]("jobs.update")

// Func is the work of a job; it must stop promptly when ctx is cancelled.
type Func func(ctx context.Context, report func(Progress)) error

// ErrShutdown is returned by Start after Shutdown.
var ErrShutdown = errors.New("jobs: manager is shut down")

// ErrUnjoined reports that Shutdown could not join every job before its
// context ended. A job that ignores cancellation keeps running.
var ErrUnjoined = errors.New("jobs: shutdown did not join every job")

// Job is one running or finished job.
type Job struct {
	mu     sync.Mutex
	info   Info
	err    error // the terminal error, kept off the wire (Info.Error is redacted)
	cancel context.CancelFunc
	done   chan struct{}
	// ctx is the job's own context. runstate classifies it and producers
	// receive it, so a deadline (which cancels it) and a Cancel are the same
	// event to every consumer.
	ctx context.Context
	// deadline is the bound this job was started with (0: unbounded).
	deadline time.Duration
	// release is the producer's slot hook (jobs.StartClaimed); finalize runs
	// it once, after the terminal record is published.
	release func()
	// finished is true once finalize wrote the terminal record; a report
	// that arrives after it is dropped.
	finished bool
	// bodyReturned is true once fn returned; expire consults it so a job
	// that finished inside its bound keeps its own outcome.
	bodyReturned bool
	// expiring is true once the deadline claimed the record; a body that
	// returns afterwards yields to the expiry instead of overwriting it.
	expiring bool
	// finalizeOnce makes the terminal record single-writer: the body
	// returning and the deadline expiring both call finalize.
	finalizeOnce sync.Once
	// admission is the set of ceilings this job respects. It is fixed when
	// the job starts: an essential writer keeps running under CPU or memory
	// pressure, while every heavy analysis job keeps all three ceilings.
	admission resource.Admission
}

// Info returns a snapshot.
func (j *Job) Info() Info {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.info
}

// Err returns the job's terminal error, or nil while it runs. It is the
// unredacted cause (a DeadlineError satisfies errors.Is(err,
// context.DeadlineExceeded)); Info.Error keeps only the redacted wire text.
func (j *Job) Err() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// Cancel asks the job to stop.
func (j *Job) Cancel() { j.cancel() }

// Done is closed when the job has finished.
func (j *Job) Done() <-chan struct{} { return j.done }
