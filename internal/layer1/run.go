package layer1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// JobKind is the jobs.Manager kind of layer-1 runs.
const JobKind = "layer1"

// Decider is the decider key layer 1's answers are stored under
// (triage_decisions): local and free, recomputed whenever the active
// models change.
const Decider = "layer1"

// ErrBusy is returned while another layer-1 run is in progress.
var ErrBusy = errors.New("layer1: a run is already in progress")

// Deps are what a Runner needs; Embedder, Bus, Clock, Judge and State may
// be nil (a judge run is then refused; JudgeCost then says unknown).
type Deps struct {
	Config   *config.Config
	Store    *store.Store
	Evidence *evidence.Engine
	Embedder EmbedderSource
	Bus      *events.Bus
	Clock    clock.Clock
	// Judge builds the judge of a catalog generator provider ("" = the
	// configured judge.provider). nil: no judge run can start.
	Judge JudgeSource
	// State is the label-free model view of a message (triage.Triager's
	// ModelState); judging reads no gate and no prediction. nil: refused.
	State StateSource
	// JudgeCost estimates what the judge would bill for labels (USD,
	// known): the run_judge action carries it. nil: the judge is not
	// built, the estimate is "unknown".
	JudgeCost func(labels int) (usd float64, known bool)
}

// JudgeSource builds the judge of a provider name.
type JudgeSource func(provider string) (judge.Judge, error)

// StateSource supplies the label-free model view of one message.
type StateSource interface {
	ModelState(ctx context.Context, rowID int64) (string, error)
}

// Runner trains, promotes and applies layer 1 over one store, and is the
// learning stages "layer1" and "questions".
type Runner struct {
	cfg         *config.Config
	st          *store.Store
	in          Inputs
	bus         *events.Bus
	clock       clock.Clock
	log         *slog.Logger
	single      jobs.Single
	judgeSingle jobs.Single
	autoSingle  jobs.Single
	labels      *feedback.Labeler
	judge       JudgeSource
	state       StateSource
	judgeCost   func(int) (float64, bool)
	// queues is the bounded label-queue read-model cache (labels_cache.go),
	// built on first use so a Runner literal still works.
	queuesOnce sync.Once
	queues     *viewcache.Cache[queueKey, LabelQueue]
	// orders is the label-independent candidate order (labels_queue.go):
	// a label write changes which tasks remain, never their ranking. It is
	// the same bounded single-flight cache as the queue, built on first use
	// so a Runner literal still works.
	ordersOnce sync.Once
	orders     *viewcache.Cache[orderKey, []LabelTask]
	// reads and buildPlan let a test count the facts one status build
	// reads. They are nil in production: the facts read r.st and NewPlan.
	reads     statusReads
	buildPlan func(*config.Config) (*Plan, error)
	// paused refuses trigger-started runs while a judge run owns the store
	// (pauseTriggers/resumeTriggers); triggerGate serializes a pause with
	// the trigger's check-and-start, so no run starts after the pause
	// returns, and the pause waits for a trigger already deciding.
	paused      atomic.Int32
	triggerGate sync.Mutex
	// live is the running run's latest progress: the learning stage reads
	// it while the run row is still open, so the timeline advances with the
	// job instead of waiting for the report.
	liveMu sync.Mutex
	live   *jobs.Progress
	// lastFinished is when the last succeeded run ended: the retrain
	// interval keeps a stream of small teacher batches from restarting a
	// five-minute training pass forever. Zero after a restart, which is a
	// good moment to train.
	lastFinished atomic.Int64
	// retrainTimer is the one deferred automatic run requested while the
	// interval is cooling down (retrainMu guards it).
	retrainMu    sync.Mutex
	retrainTimer *time.Timer
}

// New builds a runner.
func New(d Deps) (*Runner, error) {
	if d.Config == nil || d.Store == nil {
		return nil, errors.New("layer1: config and store are required")
	}
	return &Runner{cfg: d.Config, st: d.Store, bus: d.Bus, clock: clock.Or(d.Clock), log: log, judgeCost: d.JudgeCost,
		labels: feedback.NewLabeler(d.Store, d.Bus), judge: d.Judge, state: d.State,
		in: &StoreInputs{Config: d.Config, Store: d.Store, Evidence: d.Evidence, Embedder: d.Embedder}}, nil
}

// UseModelState wires the message state source a judge run reads (the
// Triager's ModelState is built after the layer-1 runner: it needs the
// learning gate). Call before the first judge run.
func (r *Runner) UseModelState(s StateSource) { r.state = s }

// Topics of layer-1 runs.
var (
	TopicProgress = events.NewTopic[jobs.Progress]("layer1.progress")
	TopicFinished = events.NewTopic[Report]("layer1.finished")
)

// The stages a run reports (jobs.Progress.Stage keys, translated by the UI).
const (
	StageDesign  = "design"  // reading the design's feature rows
	StageGather  = "gather"  // building each question's targets
	StageFit     = "fit"     // choosing a question's λ
	StageFold    = "fold"    // cross-fitting a question's folds
	StageMeasure = "measure" // measuring a question out of fold
	StageCertify = "certify" // certifying a question's threshold
	StageSettle  = "settle"  // promoting or keeping each question's model
	StageAnswer  = "answer"  // answering the mailbox with the settled models
)

// Step is one fine-grained progress report from inside a run: the stage of
// the work, the question it belongs to, the overall question position and
// the position within the stage. It is what makes a long question visible
// (its λ path and its folds) instead of one silent block.
type Step struct {
	Stage     string
	Question  string
	Done      int // questions finished
	Total     int // questions in the run
	StepDone  int // position within the stage
	StepTotal int // size of the stage
}

// unitForStage names what Done/Total count in a stage. The answer phase
// counts messages; every other stage counts questions. It is the one owner
// of the unit, so the job chip and the learning timeline cannot disagree.
func unitForStage(stage string) string {
	if stage == StageAnswer {
		return "messages"
	}
	return "questions"
}

// Report is a run's outcome (aggregates only).
type Report struct {
	RunID    int64          `json:"run_id"`
	State    runstate.State `json:"state"`
	Error    string         `json:"error,omitempty"`
	Code     string         `json:"code,omitempty"`   // a blocker of the whole run (BlockConfig)
	Broken   string         `json:"broken,omitempty"` // with BlockConfig: the file setting whose document has problems
	Rescored bool           `json:"rescored,omitempty"`
	Inputs   string         `json:"inputs"`
	// RouteKey is the configuration the route rows were written under. It is
	// also part of Inputs; the field lets the route stage block a report of
	// another configuration before reading any route row.
	RouteKey string `json:"route_key,omitempty"`
	// Labels are digests of the label rows read, each prefixed with its
	// source ("judge:…"): the next run counts what changed. Stored with
	// the run, never published (TopicFinished carries none).
	Labels    []string          `json:"labels,omitempty"`
	Space     string            `json:"space"`
	Layout    string            `json:"layout"`
	Questions []QuestionReport  `json:"questions"`
	Verdicts  []Verdict         `json:"verdicts"`
	Routes    []RouteReport     `json:"routes,omitempty"`
	Promoted  []string          `json:"promoted"`
	Retired   []string          `json:"retired,omitempty"` // questions whose model retired without a successor (untrainable now)
	Kept      map[string]string `json:"kept,omitempty"`    // question → why its active model stayed
	Answered  int               `json:"answered"`          // messages answered by the active models
	Routed    int               `json:"routed"`            // messages with a route answer row (the same population)
	Given     int               `json:"given"`             // route answers given (not abstained)
	Pending   int               `json:"pending"`           // members without a vector of the space (never imputed)
	Wall      time.Duration     `json:"wall_ns"`
}

// stored is the report as the run row keeps it.
func (r *Report) stored() Report { return *r }

// published is the report as TopicFinished carries it (no label digests).
func (r *Report) published() Report {
	out := *r
	out.Labels = nil
	return out
}

// Start runs a pass as a background job (ErrBusy while one runs).
func (r *Runner) Start(m *jobs.Manager) (*jobs.Job, error) {
	if !r.single.Begin() {
		return nil, ErrBusy
	}
	j, err := r.startClaimed(m, false)
	if err != nil {
		r.single.End()
	}
	return j, err
}

// startClaimed starts the job of a claimed slot: a training run, or a
// re-score of the verdicts (only evaluation labels changed). The caller
// owns the slot: it must release it with single.End when this returns an
// error (the job releases it when it ends). Releasing is the caller's
// because startAfter holds its trigger gate here, and single.End runs a
// queued start synchronously, which would wait for that gate.
func (r *Runner) startClaimed(m *jobs.Manager, rescore bool) (*jobs.Job, error) {
	return m.Start(JobKind, "local classifier", func(ctx context.Context, report func(jobs.Progress)) error {
		defer r.single.End()
		if rescore {
			// A re-score walks every stored answer and route row without a
			// message batch to report on, so it reports the stages it can:
			// the job then has a live progress record and the manager can
			// pace it between stages instead of only at start.
			_, err := r.rescore(ctx, func(stage string, done, total int64) {
				report(jobs.Progress{Done: done, Total: total, Unit: learning.UnitMessages, Message: stage})
			})
			return err
		}
		_, err := r.run(ctx, report)
		return err
	})
}

// Run trains every question, settles each question's model (the
// candidate unless the active one is kept; an untrainable question's
// model retires), and answers every member with them (decider "layer1"),
// all committed at once. A failed or cancelled run keeps the active
// models and their answers.
func (r *Runner) Run(ctx context.Context, report func(jobs.Progress)) (Report, error) {
	if !r.single.Begin() {
		return Report{State: runstate.Failed, Error: ErrBusy.Error()}, ErrBusy
	}
	defer r.single.End()
	return r.run(ctx, report)
}

// statusReads is the store surface a learning-status build reads. The
// runner reads it through statusStore, so a test can count one build's
// reads without a second store path.
type statusReads interface {
	ListLayer1Runs(ctx context.Context, limit int) ([]store.Layer1Run, error)
	ListEvidenceRuns(ctx context.Context, limit int) ([]store.EvidenceRun, error)
	EmbeddingSequence(ctx context.Context) (int64, error)
	DecisionSummary(ctx context.Context, k store.TriageKey) (count int, last string, err error)
	ListLabels(ctx context.Context) ([]store.LabelRow, error)
}

// statusStore is the store one status build reads (production: r.st).
func (r *Runner) statusStore() statusReads {
	if r.reads != nil {
		return r.reads
	}
	return r.st
}

// plan is the build's plan, read once. Callers treat the returned plan as
// read-only: every stage of the build shares it rather than cloning it.
func (r *Runner) plan(ctx context.Context) (*Plan, error) {
	return learning.Memo(ctx, "layer1.plan", func(context.Context) (*Plan, error) {
		if r.buildPlan != nil {
			return r.buildPlan(r.cfg)
		}
		return NewPlan(r.cfg)
	})
}

// begin records a run before anything can fail (a broken configuration
// is a recorded, failed run with its blocker: bugs review #7) and reads
// the plan and the inputs' fingerprint.
func (r *Runner) begin(ctx context.Context, rep *Report) (*Plan, error) {
	var err error
	if rep.RunID, err = r.st.BeginLayer1Run(ctx, ""); err != nil {
		return nil, err
	}
	plan, err := r.plan(ctx)
	if err != nil {
		rep.Code, rep.Broken = BlockConfig, brokenFile(err)
		return nil, err
	}
	rep.RouteKey = plan.RouteKey()
	if rep.Inputs, rep.Labels, err = r.fingerprint(ctx, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// brokenFile is the setting naming the data file a plan error comes from
// ("" when it is another input: the profile, the provider catalog).
func brokenFile(err error) string {
	switch {
	case errors.Is(err, ErrTeachers):
		return SettingTeachersFile
	case errors.Is(err, ErrRequirements):
		return SettingRequirementsFile
	}
	return ""
}

func (r *Runner) run(ctx context.Context, report func(jobs.Progress)) (Report, error) {
	start := r.clock.Now()
	defer r.clearLive()
	rep := Report{Kept: map[string]string{}}
	plan, err := r.begin(ctx, &rep)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	r.log.Info("layer1 run started", "run", rep.RunID)
	active, err := r.active(ctx)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	warm := map[string]*Model{}
	for q, a := range active {
		warm[q] = a.model
	}
	plan.Progress = func(done, total int, q QuestionReport) {
		r.reportProgress(jobs.Progress{Done: int64(done), Total: int64(total), Unit: "questions",
			Message: q.Question, Stage: StageFit, Detail: q.Question}, report)
	}
	plan.Step = func(s Step) {
		r.reportProgress(jobs.Progress{Done: int64(s.Done), Total: int64(s.Total), Unit: unitForStage(s.Stage),
			Message: s.Question, Stage: s.Stage, Detail: s.Question,
			StepDone: int64(s.StepDone), StepTotal: int64(s.StepTotal)}, report)
	}
	tr, err := Train(ctx, plan, r.in, warm)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	rep.Questions, rep.Space, rep.Layout, rep.Pending = tr.Reports, tr.Space, tr.Layout.ID, tr.NoVector
	r.reportProgress(jobs.Progress{Done: int64(len(tr.Reports)), Total: int64(len(plan.Profile.Questions)),
		Unit: "questions", Stage: StageSettle}, report)
	o, err := r.settleTrained(ctx, plan, tr, active, &rep, plan.Step)
	if err != nil {
		return r.finish(ctx, &rep, start, err, false)
	}
	rep.State, rep.Wall = runstate.Succeeded, r.clock.Now().Sub(start)
	answer := func(s *settled, id int64) ([]float64, bool) { return tr.predictWith(s.model, id) }
	p := trainedParticipants(plan, tr)
	err = r.commit(ctx, plan, &rep, o, tr.Design.IDs, func(id int64) int64 { return tr.Members[id].Revision }, answer,
		routeAnswerer(o, func(s *settled, id int64, stored engine.Answer, has bool) []route.Offer {
			return p.offersWith(plan, s, id, stored, has, layer1Final, nil)
		}, &rep), plan.Step)
	return r.finish(ctx, &rep, start, err, err == nil)
}

// reportProgress publishes one progress report and keeps the live copy the
// learning stage reads while the run row is still open.
func (r *Runner) reportProgress(p jobs.Progress, report func(jobs.Progress)) {
	r.liveMu.Lock()
	live := p
	r.live = &live
	r.liveMu.Unlock()
	if r.bus != nil {
		events.Publish(r.bus, TopicProgress, p)
	}
	if report != nil {
		report(p)
	}
}

// liveProgress is the running run's latest progress (false: none).
func (r *Runner) liveProgress() (jobs.Progress, bool) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	if r.live == nil {
		return jobs.Progress{}, false
	}
	return *r.live, true
}

// clearLive forgets the live progress when the run ends.
func (r *Runner) clearLive() {
	r.liveMu.Lock()
	r.live = nil
	r.liveMu.Unlock()
}

// finish records the outcome (unless the commit did) and publishes it.
func (r *Runner) finish(ctx context.Context, rep *Report, start time.Time, err error, committed bool) (Report, error) {
	rep.Wall = r.clock.Now().Sub(start)
	rep.State = runstate.Of(ctx, err)
	if err != nil {
		rep.Error = err.Error()
	}
	keep := context.WithoutCancel(ctx)
	if rep.RunID > 0 && !committed {
		body, _ := json.Marshal(rep.stored())
		if ferr := r.st.FinishLayer1Run(keep, rep.RunID, rep.State, string(body), rep.Error); ferr != nil && err == nil {
			err = ferr
		}
	}
	if rep.RunID > 0 {
		if _, perr := r.st.PruneLayer1(keep, r.cfg.Int(SettingKeepRetired)); perr != nil {
			r.log.Warn("layer1 models not pruned", "err", perr)
		}
	}
	if rep.RunID > 0 && rep.State == runstate.Succeeded {
		r.lastFinished.Store(r.clock.Now().UnixNano())
	}
	r.log.Info("layer1 run finished", "run", rep.RunID, "state", rep.State, "rescored", rep.Rescored, "promoted", len(rep.Promoted),
		"retired", len(rep.Retired), "answered", rep.Answered, "pending", rep.Pending, "wall", rep.Wall, "err", rep.Error)
	if r.bus != nil {
		events.Publish(r.bus, TopicFinished, rep.published())
	}
	return *rep, err
}

// fingerprintFacts is one build's input digest and label digests. The
// label slice is read-only for every caller that shares it.
type fingerprintFacts struct {
	inputs string
	labels []string
}

// fingerprint digests everything a run reads but the labels (whose rows
// are returned as digests: the stage counts how many changed): the
// settings, the merged teachers and requirements documents (every
// per-question override, the report targets), the whole profile, the
// last evidence run, the vector sequence and the decision counts. One
// status build reads it once, even when the layer-1 stage and its
// questions stage both ask.
func (r *Runner) fingerprint(ctx context.Context, plan *Plan) (string, []string, error) {
	facts, err := learning.Memo(ctx, "layer1.fingerprint", func(ctx context.Context) (fingerprintFacts, error) {
		return r.fingerprintInputs(ctx, plan)
	})
	return facts.inputs, facts.labels, err
}

// fingerprintInputs reads the fingerprint's facts. Callers share one
// plan per build, so the digest names the build's configuration.
func (r *Runner) fingerprintInputs(ctx context.Context, plan *Plan) (fingerprintFacts, error) {
	s := r.cfg.Snapshot()
	var parts []any
	for _, k := range settingKeys {
		v, _ := s.Value(k)
		parts = append(parts, k, v)
	}
	profile, err := plan.Profile.Digest()
	if err != nil {
		return fingerprintFacts{}, err
	}
	parts = append(parts, "profile", profile, "teachers", plan.Teachers.Digest, "requirements", plan.Requirements.Digest,
		"decider", plan.DeciderKey, triage.SettingFolderRoles, s.Strings(triage.SettingFolderRoles), "algorithm", algorithmVersion,
		"features", featureVersion, "route", plan.RouteKey())
	runs, err := r.statusStore().ListEvidenceRuns(ctx, 1)
	if err != nil {
		return fingerprintFacts{}, err
	}
	if len(runs) > 0 {
		parts = append(parts, "evidence", runs[0].Inputs, runs[0].State)
	}
	seq, err := r.statusStore().EmbeddingSequence(ctx)
	if err != nil {
		return fingerprintFacts{}, err
	}
	parts = append(parts, "vectors", seq)
	for _, src := range plan.Teachers.Sources {
		if src.From != FromDecisions {
			continue
		}
		n, last, err := r.statusStore().DecisionSummary(ctx, plan.keyOf(src))
		if err != nil {
			return fingerprintFacts{}, err
		}
		parts = append(parts, src.Name, n, last)
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return fingerprintFacts{}, err
	}
	labels, err := r.statusStore().ListLabels(ctx)
	if err != nil {
		return fingerprintFacts{}, err
	}
	digests := make([]string, len(labels))
	for i, l := range labels {
		digests[i] = l.Source + ":" + digest.Hex([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", l.MessageID, l.Question, l.Source, l.Value)), 16)
	}
	return fingerprintFacts{inputs: digest.Hex(raw, 32), labels: digests}, nil
}

// algorithmVersion versions the training code: a change that changes what
// a pass learns from the same inputs bumps it (2: the split
// certification and the leak-free warm starts of the L1 review round; 3:
// the certified route and its stored answers).
const algorithmVersion = 3
