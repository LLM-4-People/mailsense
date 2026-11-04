package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// JobKind is the jobs.Manager kind of evidence runs.
const JobKind = "evidence"

// Start runs an evidence pass as a background job (ErrBusy while one runs).
func (e *Engine) Start(m *jobs.Manager) (*jobs.Job, error) {
	if !e.single.Begin() {
		return nil, ErrBusy
	}
	return e.startClaimed(m)
}

// startClaimed starts the job of a run whose slot is claimed (e.single);
// the slot is released when the job ends or cannot start.
func (e *Engine) startClaimed(m *jobs.Manager) (*jobs.Job, error) {
	return jobs.StartClaimed(&e.single, m, JobKind, "structural evidence",
		func(ctx context.Context, report func(jobs.Progress)) error {
			_, err := e.run(ctx, func(p Progress) {
				report(jobs.Progress{Done: int64(p.Done), Total: int64(p.Total), Unit: learning.UnitMessages})
			})
			return err
		})
}

// Run computes the evidence of every visible message: the shared context
// once, then the messages in batches of evidence.batch_size on
// evidence.workers goroutines. A message-only signal is reused while the
// message and its effective version are unchanged; a context signal is
// computed again; only changed rows are written. A cancelled run keeps
// what it wrote.
func (e *Engine) Run(ctx context.Context, report func(Progress)) (Report, error) {
	if !e.single.Begin() {
		return Report{State: runstate.Failed, Error: ErrBusy.Error()}, ErrBusy
	}
	defer e.single.End()
	return e.run(ctx, report)
}

func (e *Engine) run(ctx context.Context, report func(Progress)) (Report, error) {
	start := e.clock.Now()
	rep := Report{Signals: len(e.signals)}
	err := e.pass(ctx, &rep, report)
	return e.finish(ctx, &rep, start, err)
}

func (e *Engine) pass(ctx context.Context, rep *Report, report func(Progress)) error {
	set, err := settingsFrom(e.cfg)
	if err != nil {
		return err
	}
	sch, err := e.schema()
	if err != nil {
		return err
	}
	rep.Space = e.spaceName()
	inputs, err := e.inputs(ctx, sch, rep.Space)
	if err != nil {
		return err
	}
	if rep.Schema, err = e.st.EvidenceSchemaID(ctx, sch.JSON); err != nil {
		return err
	}
	schemas, err := e.storedSchemas(ctx)
	if err != nil {
		return err
	}
	if rep.RunID, err = e.st.BeginEvidenceRun(ctx, inputs); err != nil {
		return err
	}
	t0 := e.clock.Now()
	env, err := buildEnv(ctx, e.st, set, rep.Space)
	if err != nil {
		return err
	}
	ids, err := e.st.MessageIDs(ctx, store.MessageFilter{})
	if err != nil {
		return err
	}
	rep.ContextTime, rep.Messages = e.clock.Now().Sub(t0), len(ids)
	e.log.Info("evidence run started", "run", rep.RunID, "messages", len(ids), "signals", len(e.signals), "schema", rep.Schema,
		"space", rep.Space, "context_time", rep.ContextTime)
	t1 := e.clock.Now()
	b := &batcher{ctx: ctx, e: e, env: env, sch: sch, schemas: schemas, schemaID: rep.Schema, workers: set.Workers}
	for i := 0; i < len(ids); i += set.Batch {
		if ctx.Err() != nil {
			rep.Cancelled = true
			return context.Cause(ctx)
		}
		if err := b.batch(ctx, ids[i:min(i+set.Batch, len(ids))], rep); err != nil {
			return err
		}
		e.progress(Progress{RunID: rep.RunID, Done: rep.Done, Total: rep.Messages}, report)
	}
	rep.ValuesTime = e.clock.Now().Sub(t1)
	return nil
}

// batcher handles the batches of one run.
type batcher struct {
	ctx      context.Context
	e        *Engine
	env      *Env
	sch      *Schema
	schemas  map[int64]stored
	schemaID int64
	workers  int
}

func (b *batcher) batch(ctx context.Context, ids []int64, rep *Report) error {
	if err := ctx.Err(); err != nil {
		rep.Cancelled = true
		return context.Cause(ctx)
	}
	var rows []store.MessageRow
	if err := b.e.st.EachMessage(ctx, store.MessageFilter{RowIDs: ids}, func(r *store.MessageRow) error {
		rows = append(rows, *r)
		return nil
	}); err != nil {
		if ctx.Err() != nil {
			rep.Cancelled = true
			return context.Cause(ctx)
		}
		return err
	}
	old, err := b.e.st.EvidenceOf(ctx, ids)
	if err != nil {
		if ctx.Err() != nil {
			rep.Cancelled = true
			return context.Cause(ctx)
		}
		return err
	}
	out := make([]store.EvidenceRow, len(rows))
	computed := make([]bool, len(rows))
	type computedRow struct {
		row      store.EvidenceRow
		computed bool
	}
	err = mapReduce(ctx, b.workers, rows, func(r store.MessageRow) string {
		return fmt.Sprintf("message %d", r.RowID)
	}, func(r store.MessageRow) (computedRow, error) {
		row, fresh, err := b.message(&r, old[r.RowID])
		return computedRow{row: row, computed: fresh}, err
	}, func(i int, res computedRow) {
		out[i], computed[i] = res.row, res.computed
	})
	// A cancelled pool abandons the batch; the cause wins over any item error
	// an interrupted message produced. This batch is not written, the earlier
	// ones are kept.
	if ctx.Err() != nil {
		rep.Cancelled = true
		return context.Cause(ctx)
	}
	if err != nil {
		return err
	}
	var write []store.EvidenceRow
	for i, r := range out {
		if computed[i] {
			rep.Computed++
		}
		o, ok := old[r.MessageID]
		o.Current = 0 // read back only
		if !ok || o != r {
			write = append(write, r)
		}
	}
	if err := b.e.st.SaveEvidence(ctx, write); err != nil {
		if ctx.Err() != nil {
			rep.Cancelled = true
			return context.Cause(ctx)
		}
		return fmt.Errorf("evidence: cannot store %d message(s): %w", len(write), err)
	}
	rep.Done += len(rows)
	rep.Written += len(write)
	if len(write) > 0 {
		b.e.writes.Add(1)
	}
	return nil
}

// message computes one message's record, reusing the stored message-only
// values of an unchanged message (same revision, same context: verdict,
// owner, capabilities, organisations) under the same effective version.
func (b *batcher) message(r *store.MessageRow, old store.EvidenceRow) (store.EvidenceRow, bool, error) {
	if err := b.stopped(); err != nil {
		return store.EvidenceRow{}, false, err
	}
	var prev Record
	ctxKey := b.env.ctxs.of(r)
	if old.MessageID != 0 && old.Revision == r.Revision && old.Context == ctxKey {
		if err := b.stopped(); err != nil {
			return store.EvidenceRow{}, false, err
		}
		var err error
		if prev, err = decodeRow(b.sch, b.schemas, old, b.e.reasonText(b.ctx)); err != nil {
			return store.EvidenceRow{}, false, err
		}
	}
	in := newInput(r, b.env)
	rec, computed := Record{}, false
	for _, sig := range b.sch.Signals {
		if err := b.stopped(); err != nil {
			return store.EvidenceRow{}, false, err
		}
		if v, ok := prev[sig.Name]; ok && !sig.Context {
			rec[sig.Name] = v
			continue
		}
		v, err := extract(sig, in)
		if err != nil {
			return store.EvidenceRow{}, false, err
		}
		rec[sig.Name] = v
		computed = computed || !sig.Context
	}
	if err := b.stopped(); err != nil {
		return store.EvidenceRow{}, false, err
	}
	vals, err := encodeRecord(b.sch, rec, b.e.reasonID(b.ctx))
	return store.EvidenceRow{MessageID: r.RowID, Revision: r.Revision, SchemaID: b.schemaID, Context: ctxKey, Values: vals}, computed, err
}

// stopped reports the cancellation cause before a message or one of its
// signals starts its expensive work.
func (b *batcher) stopped() error {
	if err := b.ctx.Err(); err != nil {
		return context.Cause(b.ctx)
	}
	return nil
}

// extract applies a signal: Unavailable when the source lacks a needed
// capability (never guessed; every missing one named), or while the
// message's details are pending for a signal that reads them; else its
// extractor. An enum value outside the signal's vocabulary is a defect of
// the extractor: an error, never stored.
func extract(sig Signal, in *Input) (Value, error) {
	var lack []string
	for _, c := range sig.Needs {
		if ok, _ := in.Caps.Has(c); !ok {
			lack = append(lack, c)
		}
	}
	if len(sig.AnyOf) > 0 && !slices.ContainsFunc(sig.AnyOf, func(c string) bool { ok, _ := in.Caps.Has(c); return ok }) {
		lack = append(lack, strings.Join(sig.AnyOf, "|"))
	}
	switch {
	case len(lack) > 0:
		return Missing(ReasonCapability + strings.Join(lack, ",")), nil
	case in.Msg.Partial && !sig.Summary:
		return Missing(ReasonPending), nil
	}
	v := sig.Extract(in)
	if v.State == OK && sig.Type == TypeEnum && !slices.Contains(sig.Values, v.Str) {
		return Value{}, fmt.Errorf("evidence: signal %s gave %q, outside its vocabulary %v", sig.Name, v.Str, sig.Values)
	}
	return v, nil
}

func (e *Engine) progress(p Progress, report func(Progress)) {
	events.PublishProgress(e.bus, TopicProgress, p, report)
}

// finish completes the report, persists and publishes it.
func (e *Engine) finish(ctx context.Context, rep *Report, start time.Time, err error) (Report, error) {
	rep.Duration = e.clock.Now().Sub(start)
	if s := rep.Duration.Seconds(); s > 0 {
		rep.PerSecond = float64(rep.Done) / s
	}
	rep.State = runstate.Of(ctx, err)
	if err != nil {
		rep.Error = err.Error()
	}
	if rep.RunID > 0 {
		body, _ := json.Marshal(rep)
		keep := context.WithoutCancel(ctx)
		if ferr := e.st.FinishEvidenceRun(keep, rep.RunID, rep.State, string(body), rep.Error); ferr != nil && err == nil {
			err = ferr
		}
		if runs, schemas, perr := e.st.PruneEvidence(keep, e.cfg.Int(settingRunsKeep), rep.Schema); perr != nil {
			e.log.Warn("evidence history not pruned", "err", perr)
		} else if runs+schemas > 0 {
			e.log.Debug("evidence history pruned", "runs", runs, "schemas", schemas)
		}
	}
	e.log.Info("evidence run finished", "run", rep.RunID, "state", rep.State, "messages", rep.Messages, "done", rep.Done,
		"computed", rep.Computed, "written", rep.Written, "duration", rep.Duration, "err", rep.Error)
	if e.bus != nil {
		events.Publish(e.bus, TopicFinished, *rep)
	}
	return *rep, err
}
