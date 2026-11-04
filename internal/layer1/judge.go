package layer1

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// JudgeJobKind is the jobs.Manager kind of judge runs.
const JudgeJobKind = "labels.judge"

// SettingJudgeWorkers is how many judge calls one run keeps in flight.
const SettingJudgeWorkers = "judge.workers"

// Judge bounds: one run never buys more than JudgeLimitMax messages (an
// explicit Limit is required: a paid run is never unbounded), and a run
// stops after this many consecutive messages whose call or answer failed
// (a broken prompt must not burn the budget).
const (
	JudgeLimitMax             = 5000
	JudgeMaxConsecutiveErrors = 5
	JudgeStoppedBudget        = "budget"
	JudgeStoppedUnavailable   = "unavailable"
	JudgeStoppedCancelled     = "cancelled"
	JudgeStoppedErrors        = "errors"
)

// Judge option and run errors.
var (
	// ErrJudgeBusy is returned while another judge run is in progress.
	ErrJudgeBusy = errors.New("layer1: a judge run is already in progress")
	// ErrJudgeBudget is a run refused before its first call (nothing was
	// spent or written); it wraps engine.ErrBudget.
	ErrJudgeBudget = fmt.Errorf("layer1: judge run refused for its budget (%w)", engine.ErrBudget)
	// ErrJudgeNotBuilt is returned when no judge or no model-state source is
	// wired (the run_judge action needs both).
	ErrJudgeNotBuilt = errors.New("layer1: no judge is wired")
)

// JudgeOptions select what a judge run labels.
type JudgeOptions struct {
	// Questions are the profile questions to label; nil/empty means every
	// question in profile order.
	Questions []string `json:"questions,omitempty"`
	// Limit is the most messages one run may buy (1..JudgeLimitMax).
	Limit int `json:"limit"`
	// Provider names a catalog generator; "" is judge.provider.
	Provider string `json:"provider,omitempty"`
	// Source is the label source written: "" or "judge" is the reference
	// label, "judge_answer" is the production answer of the certified
	// fallback route. The two are disjoint so the route can certify the
	// answer against the reference.
	Source string `json:"source,omitempty"`
	// Audit selects messages that already carry a reference label and are
	// in the certification part, so a second pass measures the judge's
	// agreement with itself on the held-out split. It is only meaningful
	// with Source "judge_answer".
	Audit bool `json:"audit,omitempty"`
	// MessageIDs restricts a run to these message rows (nil = the ordinary
	// population). The automatic label loop passes the uncertainty queue
	// here, so the batch buys the messages the model is least sure about
	// instead of the oldest mail; the order is preserved.
	MessageIDs []int64 `json:"message_ids,omitempty"`
	// Budget is the run's spending ceiling (engine.Budget): the whole run is
	// refused before its first call when its plan exceeds it, and every call
	// is reserved before it is sent.
	Budget engine.Budget `json:"budget"`
}

// JudgeReport is one judge run's outcome: aggregates only, never message
// content, ids or label values.
type JudgeReport struct {
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Prompt    string   `json:"prompt"`
	Questions []string `json:"questions"`
	// Requested is the number of messages selected (after Limit); Planned
	// those whose state and context budget could be estimated.
	Requested int `json:"requested"`
	Planned   int `json:"planned"`
	// Called counts Label invocations (one per message); Usage.Calls is the
	// provider's billed calls (a call that failed after being sent counts).
	Called int `json:"called"`
	// Unsendable counts messages skipped without a call (unreadable state,
	// over the context budget).
	Unsendable int `json:"unsendable"`
	Stored     int `json:"stored"`  // labels written
	Refused    int `json:"refused"` // questions the model answered null
	Failed     int `json:"failed"`  // questions whose answer failed the contract (or the call)
	// Estimate is the pre-flight estimate of the planned calls; Usage is
	// what settled calls consumed.
	Estimate engine.Estimate `json:"estimate"`
	Usage    engine.Usage    `json:"usage"`
	// Stopped is why the run ended early ("" = it finished); Refusal is the
	// budget refusal's explanation (pre-flight or in flight).
	Stopped string `json:"stopped,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

// StartJudge starts a judge run as a background job (ErrJudgeBusy while
// one runs). The job owns its context; ctx only refuses an already
// cancelled request and does not cancel the job.
//
// Triggers stay paused until the job ends: the judge writes one label batch
// per message, and a layer-1 run started by those events holds a read
// transaction over the store while the next batch waits for the write lock
// (messages advance at seconds per message instead of batch per second).
// When the job ends — succeeded, failed or cancelled: partial labels are
// still valid — triggers resume and exactly one run is requested, so the
// labels bought by the judge are learned. The request goes through
// jobs.Single, so a label event arriving at the same instant can only
// queue one more run; that run starts after this one has committed and
// sees unchanged inputs, so it exits without a job (no overlapping runs,
// and the final run is never dropped).
func (r *Runner) StartJudge(ctx context.Context, m *jobs.Manager, o JudgeOptions) (*jobs.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !r.judgeSingle.Begin() {
		return nil, ErrJudgeBusy
	}
	r.pauseTriggers()
	j, err := m.Start(JudgeJobKind, "judge", func(ctx context.Context, report func(jobs.Progress)) error {
		// Release the judge slot last: a new judge must not start between
		// the resume and the run request this one makes.
		defer r.judgeSingle.End()
		defer func() {
			r.resumeTriggers()
			r.startAfter(m, "judge")
		}()
		_, err := r.runJudge(ctx, o, report)
		return err
	})
	if err != nil {
		r.judgeSingle.End()
		r.resumeTriggers()
	}
	return j, err
}

// RunJudge labels messages synchronously (ErrJudgeBusy while a run is in
// progress). Triggers are paused for the whole run; a synchronous run has
// no manager to request the run its labels deserve, so it only resumes:
// the next trigger or an explicit run trains. A failed or cancelled run
// keeps every label already written; a budget refusal before the first
// call writes nothing.
func (r *Runner) RunJudge(ctx context.Context, o JudgeOptions, report func(jobs.Progress)) (JudgeReport, error) {
	if !r.judgeSingle.Begin() {
		return JudgeReport{}, ErrJudgeBusy
	}
	defer r.judgeSingle.End()
	r.pauseTriggers()
	defer r.resumeTriggers()
	return r.runJudge(ctx, o, report)
}

// judgeItem is one selected message and the questions it still misses.
type judgeItem struct {
	rowID int64
	qs    []engine.Question
}

// runJudge is the one run path of StartJudge and RunJudge.
func (r *Runner) runJudge(ctx context.Context, o JudgeOptions, report func(jobs.Progress)) (JudgeReport, error) {
	rep := JudgeReport{}
	if r.judge == nil || r.state == nil {
		return rep, ErrJudgeNotBuilt
	}
	if o.Limit < 1 || o.Limit > JudgeLimitMax {
		return rep, fmt.Errorf("layer1: judge limit must be 1..%d, got %d", JudgeLimitMax, o.Limit)
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	plan, err := NewPlan(r.cfg)
	if err != nil {
		return rep, err
	}
	questions, err := judgeQuestions(plan, o.Questions)
	if err != nil {
		return rep, err
	}
	provider := o.Provider
	if provider == "" {
		provider = r.cfg.String(judge.SettingProvider)
	}
	source := o.Source
	if source == "" {
		source = store.LabelJudge
	}
	if source != store.LabelJudge && source != store.LabelJudgeAnswer {
		return rep, fmt.Errorf("layer1: judge source must be %q or %q, got %q", store.LabelJudge, store.LabelJudgeAnswer, source)
	}
	if o.Audit && source != store.LabelJudgeAnswer {
		return rep, fmt.Errorf("layer1: a judge audit writes %q labels", store.LabelJudgeAnswer)
	}
	j, err := r.judge(provider)
	if err != nil {
		return rep, err
	}
	prompt, err := j.PromptDigest(questions)
	if err != nil {
		return rep, err
	}
	rep.Provider, rep.Model, rep.Prompt = j.Name(), j.Model(), prompt
	for _, q := range questions {
		rep.Questions = append(rep.Questions, q.Name)
	}
	items, err := r.judgePopulation(ctx, plan, questions, o.Limit, prompt, source, o.Audit, o.MessageIDs)
	if err != nil {
		return rep, err
	}
	rep.Requested = len(items)
	if len(items) == 0 {
		return rep, nil
	}
	// Pre-flight: estimate every planned call (composing the state once)
	// and refuse the whole run before spending anything when the budget
	// cannot cover it. Nothing is written before this check.
	planned := make([]judgeItem, 0, len(items))
	rep.Estimate.PriceKnown = true
	for i := range items {
		state, err := r.state.ModelState(ctx, items[i].rowID)
		if err != nil {
			rep.Unsendable++
			continue
		}
		est, err := j.Plan(state, items[i].qs)
		if err != nil {
			rep.Unsendable++
			continue
		}
		rep.Estimate.Add(est)
		planned = append(planned, items[i])
	}
	rep.Planned = len(planned)
	if reason := o.Budget.Refuse(rep.Estimate); reason != "" {
		rep.Stopped, rep.Refusal = JudgeStoppedBudget, reason
		return rep, fmt.Errorf("%w: %s", ErrJudgeBudget, reason)
	}
	if len(planned) == 0 {
		return rep, fmt.Errorf("layer1: no message could be planned (%d unsendable)", rep.Unsendable)
	}
	guard := engine.NewBudgetGuard(o.Budget)
	runCtx, cancel := context.WithCancel(engine.WithReserver(ctx, guard))
	defer cancel()
	// The judge calls are I/O-bound (a call takes 2–30 s); a few in flight
	// make a 200-message batch minutes, not an hour. SQLite writes and the
	// report counters stay serialized under mu; the calls run in parallel.
	workers := min(r.cfg.Int(SettingJudgeWorkers), len(planned))
	if workers < 1 {
		workers = 1
	}
	var mu sync.Mutex
	var reportMu sync.Mutex
	var runErr error
	consecutive := 0
	var done int64
	progress := func() {
		n := atomic.AddInt64(&done, 1)
		if report != nil {
			// The jobs manager's report closure is not concurrent.
			reportMu.Lock()
			report(jobs.Progress{Done: n, Total: int64(len(planned)), Unit: learning.UnitMessages})
			reportMu.Unlock()
		}
	}
	stop := func(stopped string, err error) {
		mu.Lock()
		if runErr == nil {
			rep.Stopped, runErr = stopped, err
			cancel()
		}
		mu.Unlock()
	}
	consecutiveFailure := func() bool {
		consecutive++
		if consecutive < JudgeMaxConsecutiveErrors {
			return false
		}
		return true
	}
	work := make(chan judgeItem)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				if runCtx.Err() != nil {
					continue
				}
				state, err := r.state.ModelState(runCtx, it.rowID)
				if err != nil {
					mu.Lock()
					rep.Unsendable++
					tooMany := consecutiveFailure()
					mu.Unlock()
					if tooMany {
						stop(JudgeStoppedErrors, fmt.Errorf("layer1: judge stopped after %d consecutive failures: %w", JudgeMaxConsecutiveErrors, err))
						continue
					}
					progress()
					continue
				}
				mu.Lock()
				rep.Called++
				mu.Unlock()
				res, err := j.Label(runCtx, state, it.qs)
				if err != nil {
					switch {
					case errors.Is(err, engine.ErrBudget):
						stop(JudgeStoppedBudget, engine.ErrBudget)
					case errors.Is(err, engine.ErrUnavailable):
						stop(JudgeStoppedUnavailable, err)
					case runCtx.Err() != nil:
						stop(JudgeStoppedCancelled, ctx.Err())
					default:
						mu.Lock()
						rep.Failed += len(it.qs) - len(res.Labels)
						rep.Refused += res.Refused()
						n, werr := r.writeJudge(ctx, plan.Profile, it.rowID, prompt, source, res.Labels)
						if werr != nil {
							rep.Stopped, runErr = JudgeStoppedErrors, werr
						} else {
							rep.Stored += n
						}
						tooMany := runErr == nil && consecutiveFailure()
						mu.Unlock()
						if werr != nil {
							cancel()
							continue
						}
						if tooMany {
							stop(JudgeStoppedErrors, fmt.Errorf("layer1: judge stopped after %d consecutive malformed or failed answers: %w", JudgeMaxConsecutiveErrors, err))
							continue
						}
					}
					progress()
					continue
				}
				mu.Lock()
				consecutive = 0
				rep.Refused += res.Refused()
				n, werr := r.writeJudge(ctx, plan.Profile, it.rowID, prompt, source, res.Labels)
				rep.Stored += n
				if werr != nil {
					rep.Stopped, runErr = JudgeStoppedErrors, werr
				}
				mu.Unlock()
				if werr != nil {
					cancel()
					continue
				}
				progress()
			}
		}()
	}
	for _, it := range planned {
		if runCtx.Err() != nil {
			break
		}
		work <- it
	}
	close(work)
	wg.Wait()
	rep.Usage = guard.Usage()
	if rep.Stopped == JudgeStoppedBudget {
		if rep.Refusal == "" {
			rep.Refusal = guard.Reason()
		}
		if runErr == nil {
			runErr = engine.ErrBudget
		}
	}
	return rep, runErr
}

// judgeQuestions is the requested questions in profile order; nil/empty
// means every profile question.
func judgeQuestions(plan *Plan, names []string) ([]engine.Question, error) {
	if len(names) == 0 {
		return slices.Clone(plan.Profile.Questions), nil
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		if want[name] {
			return nil, fmt.Errorf("layer1: judge question %q is requested twice", name)
		}
		if _, ok := plan.Profile.Question(name); !ok {
			return nil, fmt.Errorf("layer1: judge question %q is not in the profile", name)
		}
		want[name] = true
	}
	var out []engine.Question
	for i := range plan.Profile.Questions {
		if want[plan.Profile.Questions[i].Name] {
			out = append(out, plan.Profile.Questions[i])
		}
	}
	return out, nil
}

// judgePopulation selects eligible messages for one judge source. A message
// is skipped only when it carries a manual label of the question or a label
// of this source written under the current prompt digest: a label from
// another prompt (or with no prompt) is stale, so it is bought again and
// the store's upsert supersedes it. The reference source (judge) therefore
// re-buys what a prompt or profile change invalidated. The production
// source (judge_answer) selects messages with no manual label and no
// production answer under this prompt; with audit it selects only
// certification-part messages that already carry a reference label, so the
// second pass measures agreement on the held-out split.
func (r *Runner) judgePopulation(ctx context.Context, plan *Plan, questions []engine.Question, limit int, prompt, source string, audit bool, messageIDs []int64) ([]judgeItem, error) {
	wanted := map[int64]int{}
	for i, id := range messageIDs {
		wanted[id] = i
	}
	members, err := r.in.Members(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.in.Labels(ctx)
	if err != nil {
		return nil, err
	}
	labels := labelIndex(rows)
	current := map[judgeTask]bool{}
	manual := map[judgeTask]bool{}
	for _, row := range rows {
		task := judgeTask{row.MessageID, row.Question}
		switch {
		case row.Source == source && row.Prompt == prompt:
			current[task] = true
		case row.Source == store.LabelManual:
			manual[task] = true
		}
	}
	var ids []int64
	missing := map[int64][]engine.Question{}
	for _, m := range members {
		if len(wanted) > 0 {
			if _, ok := wanted[m.ID]; !ok {
				continue
			}
		}
		if audit && !selective.InCertifyPart(m.Group, plan.Config.CertifyFraction) {
			continue
		}
		var qs []engine.Question
		for i := range questions {
			q := &questions[i]
			task := judgeTask{m.ID, q.Name}
			if current[task] || manual[task] {
				continue
			}
			if source == store.LabelJudgeAnswer && audit {
				if _, ok := labels[store.LabelJudge][m.ID][q.Name]; !ok {
					continue
				}
			}
			qs = append(qs, *q)
		}
		if len(qs) > 0 {
			ids = append(ids, m.ID)
			missing[m.ID] = qs
		}
	}
	switch {
	case len(wanted) > 0:
		// The automatic label loop's queue order is the priority: the
		// most uncertain messages first, not the oldest.
		sort.Slice(ids, func(i, j int) bool { return wanted[ids[i]] < wanted[ids[j]] })
	case source == store.LabelJudgeAnswer && !audit:
		// Production answers the newest mail first: the owner sees the
		// certified fallback where it matters, and a bounded run never
		// spends its budget on the oldest backlog.
		instant := make(map[int64]time.Time, len(members))
		for _, m := range members {
			instant[m.ID] = m.Instant
		}
		sort.Slice(ids, func(i, j int) bool {
			if !instant[ids[i]].Equal(instant[ids[j]]) {
				return instant[ids[i]].After(instant[ids[j]])
			}
			return ids[i] > ids[j]
		})
	default:
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]judgeItem, len(ids))
	for i, id := range ids {
		out[i] = judgeItem{rowID: id, qs: missing[id]}
	}
	return out, nil
}

// judgeTask is one message and question a judge produced or skipped.
type judgeTask struct {
	rowID    int64
	question string
}

// writeJudge stores one message's valid labels in one batch (nil labels
// are refusals and are never passed) and publishes one feedback.label
// event. A cancelled run still writes the labels of the call it paid for.
// A judge_answer label is the model's own production answer, kept for
// audit only: feedback.Teaching and feedback.Reference exclude it, so it
// never teaches or scores itself.
func (r *Runner) writeJudge(ctx context.Context, p *taxonomy.Profile, rowID int64, prompt, source string, labels map[string]*taxonomy.Label) (int, error) {
	values := map[string]taxonomy.Label{}
	for name, l := range labels {
		if l != nil {
			values[name] = *l
		}
	}
	if len(values) == 0 {
		return 0, nil
	}
	//nolint:staticcheck // the source is validated by runJudge
	if _, err := r.labels.PutJudgeSource(context.WithoutCancel(ctx), p, rowID, prompt, source, values); err != nil {
		return 0, err
	}
	return len(values), nil
}
