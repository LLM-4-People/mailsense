package jobs

import (
	"context"
	"fmt"
	"time"
)

// A job's deadline is its wall-clock backstop. Resource pacing runs only from
// a progress report, so a phase that never reports progress is never paced;
// the deadline bounds such a job without asking its producer to cooperate
// with its own reporting. When it expires the manager cancels the job
// context, and the ordinary finalize path (runstate, Paused cleared, one
// terminal publish) reports the stop, so a deadline expiry looks like any
// other terminal stop to a consumer.
//
// The table is owned here because jobs is the one place every background job
// starts. Its keys are the producers' JobKind constants (internal/syncer,
// internal/triage, internal/index, internal/evidence, internal/layer1 and
// internal/sidecar's literal); a producer that renames a kind lands on
// fallbackDeadline instead of silently becoming unbounded.

// deadlinePolicy returns the wall-clock bound of a job kind; 0 means the
// kind's legitimate runtime has no upper bound.
type deadlinePolicy func(kind string) time.Duration

// noDeadline marks a kind whose legitimate runtime is unbounded by design.
const noDeadline time.Duration = 0

// fallbackDeadline bounds every kind the table does not name. It is never 0:
// a new or renamed producer is bounded by default rather than left to a
// silent hang.
const fallbackDeadline = 30 * time.Minute

// kindDeadlines is the per-kind policy. Each duration is set from measured
// work with headroom, and each entry names the evidence that bounds it:
//
//   - evidence: a full 33,842-message pass measured 7.108920023 s
//     (an internal unread-repair measurement: "evidence then ran
//     **7.108920023 s** and wrote **33,842 rows**"), so 30m is a >250x
//     backstop for a pass that normally takes seconds.
//   - layer1: a 17-question training pass took ~60 s, and its worst
//     measured commit held the SQLite writer for ~13 minutes at 34,763
//     messages (docs/DEVELOPMENT.md, "the commit held the SQLite writer for
//     ~13 minutes"), so 30m covers that commit plus training and calibration.
//   - index: the first full index of 35k messages takes about an hour at the
//     live box's measured ~10 messages/s (docs/EMBEDDINGS.md: "at the live
//     box's measured ≈10 messages/s, 35k messages take about an hour"), so
//     90m covers it with 50% headroom; incremental runs take seconds.
//   - triage: one automatic run decides at most triage.auto.batch messages
//     (default 500) within triage.budget.max_calls (default 1000) on
//     triage.workers (default 4) parallel calls; 60m covers a paid batch on a
//     local decider with headroom, and the decisions it already wrote stay
//     valid across the stop.
//   - labels.judge: 500 messages were labelled in 20.5 minutes, about
//     2.5 s per message; the automatic batch is
//     labels.auto.batch (default 200, ≈8m) and an explicit request asks for a
//     count the UI computes, so 30m covers the observed 500-message run with
//     50% headroom and stops a larger request with every label it already
//     wrote kept.
//   - sidecar.install: the observed install fetches a 342,039,075 B catalog
//     (the same measurement) through a download owner whose own
//     ceiling is sidecar.download.timeout (default 2h per artifact), so the
//     job bound matches that owner instead of contradicting it.
//   - messages.html: the display backfill is paced by the source's own policy
//     ("50 messages per several minutes in this sample",
//     internal measurement) over a production-sized repair population, so a
//     whole pass legitimately takes many hours. 6h bounds one run without
//     losing work: every applied batch is committed and the next run plans
//     from the messages that still need a body (store.BackfillNewest's
//     candidate predicate), so a long repair completes across a handful of
//     bounded runs instead of one run that could hang for days.
//   - sync: new mail may take as long as the mailbox needs. It is the one
//     intrinsically unbounded kind: it is started through StartEssential,
//     which sets no deadline at all (see Manager.StartEssential), and a
//     mid-folder stop would drop that folder's cursor advance.
var kindDeadlines = map[string]time.Duration{
	"evidence":        30 * time.Minute,
	"layer1":          30 * time.Minute,
	"index":           90 * time.Minute,
	"triage":          60 * time.Minute,
	"labels.judge":    30 * time.Minute,
	"sidecar.install": 2 * time.Hour,
	"messages.html":   6 * time.Hour,
	"sync":            noDeadline,
}

// defaultDeadlinePolicy is the shipped table, plus fallbackDeadline for a
// kind the table does not name.
func defaultDeadlinePolicy(kind string) time.Duration {
	if bound, ok := kindDeadlines[kind]; ok {
		return bound
	}
	return fallbackDeadline
}

// DeadlineError is the terminal error of a job the manager stopped because it
// passed its per-kind deadline. It carries the kind and the bound only: the
// fields are the producer's own stable key and two durations, so no mail
// content can enter the job record from here.
type DeadlineError struct {
	Kind     string
	Deadline time.Duration // the kind's bound
	Elapsed  time.Duration // how long the job actually ran
}

// Error names the kind, the bound and the elapsed time.
func (e *DeadlineError) Error() string {
	return fmt.Sprintf("jobs: %s job exceeded its %s deadline after %s", e.Kind, e.Deadline, e.Elapsed)
}

// Unwrap makes the deadline the cause, so errors.Is(err,
// context.DeadlineExceeded) holds for a stopped job.
func (e *DeadlineError) Unwrap() error { return context.DeadlineExceeded }
