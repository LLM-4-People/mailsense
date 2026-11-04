package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// JobKind is the jobs.Manager kind of index runs.
const JobKind = "index"

// hashDigits is the length of the stored input hash (64 bits: a
// collision would only skip one re-embedding).
const hashDigits = 16

// Start runs an index pass as a background job (ErrBusy while one runs).
func (ix *Indexer) Start(m *jobs.Manager, o Options) (*jobs.Job, error) {
	if !ix.single.Begin() {
		return nil, ErrBusy
	}
	return ix.startClaimed(m, o)
}

// startClaimed starts the job of a run whose slot is claimed (ix.single);
// the slot is released when the job ends or cannot start.
func (ix *Indexer) startClaimed(m *jobs.Manager, o Options) (*jobs.Job, error) {
	return jobs.StartClaimed(&ix.single, m, JobKind, "semantic index",
		func(ctx context.Context, report func(jobs.Progress)) error {
			_, err := ix.run(ctx, o, func(p Progress) {
				report(jobs.Progress{Done: int64(p.Done), Total: int64(p.Total), Unit: learning.UnitMessages,
					Message: fmt.Sprintf("%d embedded, %d unchanged, %d failed", p.Embedded, p.Unchanged, p.Failed)})
			})
			return err
		})
}

// Run embeds what changed, newest first, one batch (embed.batch_size) at
// a time. A failed batch is reported and the run goes on; the run stops
// when the embedder is unavailable for everyone, when cancelled, or when
// the store cannot write (an error). Vectors are only ever replaced by
// newer ones.
func (ix *Indexer) Run(ctx context.Context, o Options, report func(Progress)) (Report, error) {
	if !ix.single.Begin() {
		return Report{State: runstate.Failed, Error: ErrBusy.Error()}, ErrBusy
	}
	defer ix.single.End()
	return ix.run(ctx, o, report)
}

func (ix *Indexer) run(ctx context.Context, o Options, report func(Progress)) (Report, error) {
	start := ix.clock.Now()
	rep := Report{}
	e, err := ix.src.Embedder()
	if err != nil {
		return ix.finish(ctx, &rep, start, retryKey{}, err)
	}
	rep.Space, rep.TextVersion = e.Space(), ix.textVersion()
	key := ix.retryKeyFor(rep.Space, rep.TextVersion)
	work, cov, err := ix.plan(ctx, rep.Space, o)
	if err != nil {
		return ix.finish(ctx, &rep, start, key, err)
	}
	rep.Before, rep.Selected = cov, len(work)
	ix.beginLive(rep.Space, cov.Eligible, rep.Selected, start)
	defer ix.endLive()
	if rep.RunID, err = ix.st.BeginIndexRun(ctx, rep.Space); err != nil {
		return ix.finish(ctx, &rep, start, key, err)
	}
	ix.log.Info("index run started", "run", rep.RunID, "space", rep.Space, "eligible", cov.Eligible, "indexed", cov.Indexed,
		"stale", cov.Stale, "pending", cov.Pending, "selected", len(work))
	batch := ix.cfg.Int(SettingEmbedBatch)
	if sizer, ok := e.(interface{ MaxBatch() int }); ok {
		// One index batch is one provider request: a failed batch then costs
		// at most N+1 calls instead of discarding already-paid sub-batches.
		if max := sizer.MaxBatch(); max > 0 && max < batch {
			batch = max
		}
	}
	for i := 0; i < len(work) && err == nil && rep.StoppedBy == ""; i += batch {
		err = ix.batch(ctx, e, work[i:min(i+batch, len(work))], &rep)
		ix.progress(&rep, report)
	}
	return ix.finish(ctx, &rep, start, key, err)
}

// batch handles one batch: unchanged inputs are confirmed, the rest
// embedded and stored. Only a store failure is returned (it ends the run).
func (ix *Indexer) batch(ctx context.Context, e engine.Embedder, states []store.EmbeddingState, rep *Report) error {
	if ctx.Err() != nil {
		rep.StoppedBy = StoppedCancelled
		return nil
	}
	ids := make([]int64, len(states))
	for i, s := range states {
		ids[i] = s.RowID
	}
	texts, err := ix.st.MessageTexts(ctx, ids)
	if err != nil {
		return err
	}
	confirm := map[int64]int64{}
	var todo []store.EmbeddingState
	var inputs []engine.Text
	var hashes []string
	for _, s := range states {
		t, ok := texts[s.RowID]
		if !ok {
			continue // removed since the plan
		}
		in := engine.Text{Purpose: engine.PurposeDocument, Title: t.Subject, Body: mail.CleanBody(t.Body)}
		h := digest.Hex([]byte(e.Input(in)), hashDigits)
		if s.Space == rep.Space && s.TextHash == h {
			confirm[s.RowID] = s.Revision
			continue
		}
		todo, inputs, hashes = append(todo, s), append(inputs, in), append(hashes, h)
	}
	if err := ix.st.ConfirmEmbeddings(ctx, confirm, rep.TextVersion); err != nil {
		if ret, handled := storeStop(ctx, err, rep); handled {
			return ret
		}
		return err
	}
	rep.Unchanged += len(confirm)
	if len(todo) == 0 {
		return nil
	}
	return ix.embed(ctx, e, todo, inputs, hashes, rep)
}

// embed embeds and stores one batch. A batch that fails for a reason
// other than an unavailable or temporarily failing embedder, or a
// cancellation, is embedded again one message at a time, so one bad input
// never keeps its neighbours out.
func (ix *Indexer) embed(ctx context.Context, e engine.Embedder, todo []store.EmbeddingState, inputs []engine.Text, hashes []string, rep *Report) error {
	res, err := e.Embed(ctx, inputs)
	rep.Usage.Add(res.Usage)
	if err != nil && len(todo) > 1 && ctx.Err() == nil && !errors.Is(err, engine.ErrUnavailable) &&
		!errors.Is(err, engine.ErrTransient) {
		ix.log.Info("index batch failed: embedding its messages one by one", "run", rep.RunID, "messages", len(todo), "err", logx.RedactedError(err))
		for i := range todo {
			if err := ix.embed(ctx, e, todo[i:i+1], inputs[i:i+1], hashes[i:i+1], rep); err != nil || rep.StoppedBy != "" {
				return err
			}
		}
		return nil
	}
	if err != nil {
		ix.fail(ctx, todo, err, rep)
		return nil
	}
	rows := make([]store.EmbeddingRow, len(todo))
	for i, s := range todo {
		rows[i] = store.EmbeddingRow{MessageID: s.RowID, Space: rep.Space, TextHash: hashes[i], TextVersion: rep.TextVersion, Revision: s.Revision,
			Vector: vector.Quantize(res.Vectors[i])}
	}
	if err := ix.st.SaveEmbeddings(ctx, rows); err != nil {
		// The vectors in this batch are paid for but a busy store could not
		// commit them. Stop without counting or quarantining messages: the
		// same mail is embedded again on the next run.
		if ret, handled := storeStop(ctx, err, rep); handled {
			return ret
		}
		return fmt.Errorf("index: cannot store %d vector(s): %w", len(rows), err)
	}
	rep.Embedded += len(rows)
	rep.Truncated += res.Truncated
	return nil
}

// storeStop classifies a store write failure, the one way a mail write can end
// a run: a busy store had every attempt refused (it ends the run without a
// message failure), and an ended request context marks the run cancelled. It
// returns the error the run must end with and whether it classified the
// failure; a caller keeps its own wording for a real store error.
func storeStop(ctx context.Context, err error, rep *Report) (ret error, handled bool) {
	if errors.Is(err, ErrStoreBusy) {
		rep.StoppedBy, rep.Error = StoppedBusy, err.Error()
		return nil, true
	}
	if ctx.Err() != nil {
		rep.StoppedBy = StoppedCancelled
		return context.Cause(ctx), true
	}
	return err, false
}

// fail records a failed batch (or message); an unavailable embedder or a cancelled
// context stops the run.
func (ix *Indexer) fail(ctx context.Context, todo []store.EmbeddingState, err error, rep *Report) {
	switch {
	case ctx.Err() != nil:
		rep.StoppedBy = StoppedCancelled
		return
	case errors.Is(err, engine.ErrUnavailable):
		rep.StoppedBy, rep.Error = StoppedUnavailable, err.Error()
	case errors.Is(err, engine.ErrTransient):
		// A temporary provider-wide stop is not this batch's failure: the
		// same mail is attempted on the next run and nothing is quarantined.
		rep.StoppedBy, rep.Error = StoppedTransient, err.Error()
		return
	}
	rep.Failed += len(todo)
	if len(todo) == 1 && rep.StoppedBy == "" {
		ix.quarantineCount(ctx, todo[0], err, rep)
	}
	if len(rep.Failures) < ix.cfg.Int(SettingMaxLoggedFailures) {
		f := Failure{Error: err.Error()}
		for _, s := range todo {
			f.Messages = append(f.Messages, s.RowID)
		}
		rep.Failures = append(rep.Failures, f)
		ix.log.Warn("index batch failed", "run", rep.RunID, "messages", len(todo), "err", logx.RedactedError(err))
	}
}

// quarantineCount records a message's failed attempt (index.quarantine_after
// of them at one revision keep it out until it changes).
func (ix *Indexer) quarantineCount(ctx context.Context, s store.EmbeddingState, err error, rep *Report) {
	fail := store.IndexFailure{MessageID: s.RowID, Revision: s.Revision, Error: err.Error()}
	if rerr := ix.st.RecordIndexFailures(context.WithoutCancel(ctx), []store.IndexFailure{fail}); rerr != nil {
		ix.log.Warn("index failure not recorded", "message", s.RowID, "err", rerr)
		return
	}
	if s.Failures+1 >= ix.cfg.Int(SettingQuarantineAfter) {
		rep.Quarantined++
		ix.log.Warn("message quarantined: it failed again at the same revision", "run", rep.RunID, "message", s.RowID, "failures", s.Failures+1, "err", logx.RedactedError(err))
	}
}

func (ix *Indexer) progress(rep *Report, report func(Progress)) {
	p := Progress{RunID: rep.RunID, Done: rep.Attempted(), Total: rep.Selected,
		Embedded: rep.Embedded, Unchanged: rep.Unchanged, Failed: rep.Failed, Tokens: rep.Usage.InputTokens}
	ix.updateLive(p)
	events.PublishProgress(ix.bus, TopicProgress, p, report)
}

// finish completes the report (throughput, coverage after, outcome),
// persists it, arms or clears the unavailable wait and publishes it.
func (ix *Indexer) finish(ctx context.Context, rep *Report, start time.Time, key retryKey, err error) (Report, error) {
	rep.Duration = ix.clock.Now().Sub(start)
	if secs := rep.Duration.Seconds(); secs > 0 {
		rep.MailsPerSecond = float64(rep.Embedded) / secs
		rep.TokensPerSecond = float64(rep.Usage.InputTokens) / secs
	}
	rep.NotAttempted = rep.Selected - rep.Attempted()
	// An unavailable run that committed no vector leaves the vector store
	// and coverage exactly as they were before: re-planning would only
	// rescan the whole mailbox and could fold this run's recorded failures
	// into a coverage change the run never wrote. A mixed
	// hard-failure-then-unavailable run may still have recorded
	// index_failures. The report keeps the coverage it started from.
	unavailableStop := rep.StoppedBy == StoppedUnavailable && !rep.Committed()
	err = outcome(ctx, rep, err)
	rep.State = runstate.Of(ctx, err)
	if err != nil {
		rep.Error = err.Error()
	}
	keep := context.WithoutCancel(ctx)
	if unavailableStop {
		rep.After = rep.Before
	} else if rep.Space != "" {
		if _, after, cerr := ix.plan(keep, rep.Space, Options{}); cerr == nil {
			rep.After = after
		}
	}
	if rep.RunID > 0 {
		body, _ := json.Marshal(rep)
		if ferr := ix.st.FinishIndexRun(keep, rep.RunID, rep.State, string(body), rep.Error); ferr != nil {
			if err == nil {
				err = ferr
			}
		} else if runs, perr := ix.st.PruneIndexRuns(keep, ix.cfg.Int(SettingRunsKeep)); perr != nil {
			// Retention is housekeeping: a full disk or a busy writer must not
			// turn a finished run into a failed one. The next finished run
			// prunes again.
			ix.log.Warn("index history not pruned", "err", perr)
		} else if runs > 0 {
			ix.log.Debug("index history pruned", "runs", runs)
		}
	}
	if unavailableStop {
		ix.armUnavailable(key, rep, start)
	} else {
		ix.clearRetry()
	}
	// The stored newest-run report changed (or its write was attempted):
	// the stage's unavailable check must read it again instead of reusing
	// the previous run's answer.
	ix.runSeq.Add(1)
	// The run is over before the finished event: its subscriber must see
	// the full coverage, not the live counters of the run that just ended.
	ix.endLive()
	ix.log.Info("index run finished", "run", rep.RunID, "state", rep.State, "embedded", rep.Embedded, "unchanged", rep.Unchanged,
		"failed", rep.Failed, "not_attempted", rep.NotAttempted, "tokens", rep.Usage.InputTokens, "duration", rep.Duration,
		"mails_per_second", rep.MailsPerSecond, "indexed", rep.After.Indexed, "eligible", rep.After.Eligible, "err", rep.Error)
	if ix.bus != nil {
		events.Publish(ix.bus, TopicFinished, *rep)
	}
	return *rep, err
}

// outcome is the run's error: a store failure, a stop, or every attempted
// batch failing.
func outcome(ctx context.Context, rep *Report, err error) error {
	switch {
	case err != nil:
		return err
	case rep.StoppedBy == StoppedCancelled:
		return context.Cause(ctx)
	case rep.StoppedBy == StoppedUnavailable:
		return fmt.Errorf("index: stopped, the embedder is unavailable: %s", rep.Error)
	case rep.StoppedBy == StoppedTransient:
		return fmt.Errorf("index: stopped, the embedder is temporarily unavailable: %s", rep.Error)
	case rep.StoppedBy == StoppedBusy:
		return fmt.Errorf("index: stopped, the store stayed busy: %w", ErrStoreBusy)
	case rep.Failed > 0 && rep.Embedded+rep.Unchanged == 0:
		return fmt.Errorf("index: every attempted batch failed (%d messages); existing vectors are kept", rep.Failed)
	}
	return nil
}
