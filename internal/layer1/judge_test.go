package layer1

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/judge"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// fakeJudge labels every asked question with the first option / yes / level
// 0, unless the test refuses or breaks one; it honors the run's budget
// guard like the real generator adapter.
type fakeJudge struct {
	name   string
	model  string
	digest string
	est    engine.Estimate
	refuse map[string]bool // named questions answered null
	broken map[string]bool // named questions omitted (a contract error)
	fail   error           // every call fails with this error

	mu      sync.Mutex
	calls   int
	asked   [][]string
	states  []string
	blockAt int           // when > 0, the call after this many blocks
	entered chan struct{} // signalled when a call blocks
	block   chan struct{} // released by the test

	// baseline is the fixture labels already stored under digest: judgeLabels
	// counts what the run under test wrote, not the fixture.
	baseline int
}

func newFakeJudge() *fakeJudge {
	return &fakeJudge{name: "fake-judge", model: "fake-model-1", digest: "prompt-digest-1",
		est: engine.Estimate{Calls: 1, InputTokens: 100, EstimatedUSD: 0.001, PriceKnown: true}}
}

func (f *fakeJudge) Name() string  { return f.name }
func (f *fakeJudge) Model() string { return f.model }
func (f *fakeJudge) PromptDigest([]engine.Question) (string, error) {
	return f.digest, nil
}
func (f *fakeJudge) Plan(string, []engine.Question) (engine.Estimate, error) {
	return f.est, nil
}

func (f *fakeJudge) Label(ctx context.Context, state string, qs []engine.Question) (judge.Result, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.asked = append(f.asked, namesOf(qs))
	f.states = append(f.states, state)
	block := f.blockAt > 0 && n > f.blockAt
	f.mu.Unlock()
	if block {
		if f.entered != nil {
			// Parallel workers can block on several calls at once; the
			// signal is a hint, never a reason to deadlock the test.
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-f.block:
		case <-ctx.Done():
			return judge.Result{}, ctx.Err()
		}
	}
	usage := engine.Usage{Calls: 1, InputTokens: f.est.InputTokens, EstimatedUSD: f.est.EstimatedUSD}
	if guard := engine.ReserverOf(ctx); guard != nil {
		if !guard.Reserve(f.est) {
			return judge.Result{}, engine.ErrBudget
		}
		defer guard.Settle(f.est, usage)
	}
	if f.fail != nil {
		return judge.Result{Usage: usage}, f.fail
	}
	res := judge.Result{Labels: map[string]*taxonomy.Label{}, Usage: usage}
	for _, q := range qs {
		switch {
		case f.refuse[q.Name]:
			res.Labels[q.Name] = nil
		case f.broken[q.Name]:
		default:
			res.Labels[q.Name] = labelFor(&q)
		}
	}
	if len(f.broken) > 0 {
		return res, fmt.Errorf("%w: a broken answer", judge.ErrContract)
	}
	return res, nil
}

func (f *fakeJudge) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func namesOf(qs []engine.Question) []string {
	out := make([]string, len(qs))
	for i := range qs {
		out[i] = qs[i].Name
	}
	return out
}

func labelFor(q *engine.Question) *taxonomy.Label {
	switch q.Type {
	case engine.Choice:
		l := taxonomy.Label{Choice: q.Options[0].Name}
		return &l
	case engine.Noul:
		l := taxonomy.Label{Number: 1}
		return &l
	}
	l := taxonomy.Label{Number: 0}
	return &l
}

// fakeStates is the model-state source; the state carries the row id only
// (no content) and can fail for chosen rows.
type fakeStates struct {
	mu   sync.Mutex
	fail map[int64]bool
	seen []int64
}

func (f *fakeStates) ModelState(_ context.Context, rowID int64) (string, error) {
	f.mu.Lock()
	f.seen = append(f.seen, rowID)
	f.mu.Unlock()
	if f.fail[rowID] {
		return "", errors.New("cannot compose this state")
	}
	return fmt.Sprintf("Message: state of row %d", rowID), nil
}

func (f *fakeStates) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// judgeWorld wires a fake judge and state source over a layer-1 world. The
// fixture's judge labels are stamped with the fake judge's digest: the
// population treats another prompt's label as stale and buys it again.
func judgeWorld(t *testing.T, n int) (*world, *fakeJudge, *fakeStates) {
	t.Helper()
	w := newWorld(t, n, nil)
	fj, fs := newFakeJudge(), &fakeStates{fail: map[int64]bool{}}
	stampJudgePrompt(t, w.st, fj.digest)
	fj.baseline = judgeLabelRows(t, w.st, fj.digest)
	w.r.judge = func(string) (judge.Judge, error) { return fj, nil }
	w.r.state = fs
	return w, fj, fs
}

// stampJudgePrompt rewrites the fixture's judge labels under prompt.
func stampJudgePrompt(t *testing.T, st *store.Store, prompt string) {
	t.Helper()
	rows, err := st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var writes []store.LabelWrite
	for _, row := range rows {
		if row.Source == store.LabelJudge {
			writes = append(writes, store.LabelWrite{MessageID: row.MessageID, Question: row.Question,
				Value: row.Value, Source: row.Source, Prompt: prompt})
		}
	}
	if len(writes) == 0 {
		return
	}
	if err := st.PutLabels(t.Context(), writes); err != nil {
		t.Fatal(err)
	}
}

func judgeOpts(limit int, questions ...string) JudgeOptions {
	return JudgeOptions{Questions: questions, Limit: limit, Budget: engine.Budget{MaxCalls: 1000, MaxUSD: 1}}
}

func judgeLabelRows(t *testing.T, st *store.Store, prompt string) int {
	t.Helper()
	rows, err := st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Source == store.LabelJudge && r.Prompt == prompt {
			n++
		}
	}
	return n
}

// judgeLabels counts the labels the run under test wrote under the fake
// judge's prompt, excluding the fixture's own labels.
func judgeLabels(t *testing.T, st *store.Store, fj *fakeJudge) int {
	t.Helper()
	return judgeLabelRows(t, st, fj.digest) - fj.baseline
}

func TestJudgeOneCallPerMessageWithEveryMissingQuestion(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requested != 1 || rep.Planned != 1 || rep.Called != 1 || fj.count() != 1 || len(fj.asked) != 1 {
		t.Fatalf("report %+v, calls %d", rep, fj.count())
	}
	// The first message already has a judge category label (teach): the
	// one call asks every OTHER question, in profile order.
	if got := fj.asked[0]; len(got) != len(w.p.Questions)-1 || contains(got, "category") || rep.Stored != len(got) {
		t.Fatalf("asked %v, stored %d", got, rep.Stored)
	}
	if rep.Usage.Calls != 1 || rep.Usage.InputTokens != 100 || rep.Usage.EstimatedUSD != 0.001 {
		t.Fatalf("usage %+v", rep.Usage)
	}
	if judgeLabels(t, w.st, fj) != len(w.p.Questions)-1 {
		t.Fatalf("stored labels %d", judgeLabels(t, w.st, fj))
	}
	if rep.Provider != fj.name || rep.Model != fj.model || rep.Prompt != fj.digest {
		t.Fatalf("provenance %+v", rep)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestJudgeIsIdempotentAndPublishesOneEventPerBatch(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	bus := events.New()
	t.Cleanup(bus.Close)
	w.r.labels = feedback.NewLabeler(w.st, bus)
	changes, err := events.Subscribe(bus, feedback.TopicLabel, 256)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(50, "category"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requested != 20 || rep.Called != 20 || rep.Stored != 20 || fj.count() != 20 {
		t.Fatalf("report %+v, calls %d", rep, fj.count())
	}
	// One event per message, not one per label and not one per run.
	n := 0
	for {
		select {
		case ev := <-changes.C:
			n++
			if ev.Action != "set" || ev.Count != 1 || ev.Question != "category" || ev.MessageID == 0 {
				t.Fatalf("batch event %+v", ev)
			}
		default:
			if n != 20 {
				t.Fatalf("%d label events for 20 messages", n)
			}
			goto drained
		}
	}
drained:
	again, err := w.r.RunJudge(t.Context(), judgeOpts(50, "category"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Requested != 0 || again.Called != 0 || again.Stored != 0 || fj.count() != 20 {
		t.Fatalf("a second run bought labels again: %+v, calls %d", again, fj.count())
	}
}

// A refusal is counted for the run and never stored: null is no label, not
// a default and not a target. It leaves the question missing, so a later
// run may ask it again (the action's count stays honest).
func TestJudgeRefusalsAreCountedAndNeverStoredAsLabels(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	fj.refuse = map[string]bool{"category": true}
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(3, "category"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Called != 3 || rep.Refused != 3 || rep.Stored != 0 || fj.count() != 3 || len(fj.asked) != 3 {
		t.Fatalf("report %+v, calls %d, asked %v", rep, fj.count(), fj.asked)
	}
	if judgeLabels(t, w.st, fj) != 0 {
		t.Fatalf("a refusal became a label: %d", judgeLabels(t, w.st, fj))
	}
}

func TestJudgeContractErrorStoresNoDefault(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	fj.broken = map[string]bool{"category": true}
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(2, "category"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Called != 2 || rep.Failed != 2 || rep.Stored != 0 || judgeLabels(t, w.st, fj) != 0 {
		t.Fatalf("report %+v, labels %d", rep, judgeLabels(t, w.st, fj))
	}
}

func TestJudgeSkipsALabelAlreadyPaidForUnderTheSamePrompt(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 10)
	// A judge row under this prompt whose value is stale (invalid under the
	// current profile) is not bought again: the prompt digest, not the
	// value, says the task was already paid for.
	if err := w.st.PutLabels(t.Context(), []store.LabelWrite{{MessageID: w.ids[1], Question: "category",
		Value: `"not_an_option"`, Source: store.LabelJudge, Prompt: fj.digest}}); err != nil {
		t.Fatal(err)
	}
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(1, "category"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Called != 1 || len(fj.states) != 1 || fj.states[0] != fmt.Sprintf("Message: state of row %d", w.ids[2]) {
		t.Fatalf("the stale same-prompt task was bought again: %+v states %v", rep, fj.states)
	}
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.MessageID == w.ids[1] && row.Question == "category" && row.Source == store.LabelJudge {
			if row.Value != `"not_an_option"` || row.Prompt != fj.digest {
				t.Fatalf("the skipped row changed: %+v", row)
			}
		}
	}
}

func TestJudgeBudgetRefusesBeforeSpendingOrWriting(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	before := judgeLabels(t, w.st, fj)
	opts := judgeOpts(5, "category")
	opts.Budget.MaxUSD = 1e-9
	rep, err := w.r.RunJudge(t.Context(), opts, nil)
	if !errors.Is(err, ErrJudgeBudget) || !errors.Is(err, engine.ErrBudget) {
		t.Fatalf("want ErrJudgeBudget, got %v", err)
	}
	if rep.Stopped != JudgeStoppedBudget || rep.Refusal == "" || rep.Called != 0 || rep.Stored != 0 || rep.Usage.Calls != 0 {
		t.Fatalf("report %+v", rep)
	}
	if fj.count() != 0 || judgeLabels(t, w.st, fj) != before {
		t.Fatalf("a refused run spent or wrote: calls %d labels %d", fj.count(), judgeLabels(t, w.st, fj))
	}
}

func TestJudgeCancellationKeepsPriorLabels(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	fj.blockAt = 1
	fj.entered = make(chan struct{}, 1)
	fj.block = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan JudgeReport, 1)
	errs := make(chan error, 1)
	go func() {
		rep, err := w.r.RunJudge(ctx, judgeOpts(10, "category"), nil)
		done <- rep
		errs <- err
	}()
	select {
	case <-fj.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second call never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop after cancellation")
	}
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("want a cancellation error, got %v", err)
	}
	if judgeLabels(t, w.st, fj) != 1 {
		t.Fatalf("the first call's label was lost: %d", judgeLabels(t, w.st, fj))
	}
}

// A judge call that fails is one message, not a provider-wide outage: the
// run stops only at the consecutive-failure limit instead of aborting on
// the first error.
func TestJudgeStopsAfterConsecutiveFailedCalls(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	fj.fail = judge.ErrTimeout
	rep, err := w.r.RunJudge(t.Context(), judgeOpts(10, "category"), nil)
	if !errors.Is(err, judge.ErrTimeout) || rep.Stopped != JudgeStoppedErrors {
		t.Fatalf("want a stop after the failure limit, got %v, %+v", err, rep)
	}
	// Parallel workers can have a few calls in flight past the counter's
	// limit; the run must stop at the limit, not on the first error.
	if rep.Called < JudgeMaxConsecutiveErrors || rep.Called > 2*JudgeMaxConsecutiveErrors {
		t.Fatalf("%d calls; want about the consecutive-failure limit: %+v", rep.Called, rep)
	}
	if judgeLabels(t, w.st, fj) != 0 {
		t.Fatalf("a failed call stored a label: %d", judgeLabels(t, w.st, fj))
	}
}

func TestJudgeLabelsOnlyCertifyWhereCertifySourcesNameJudge(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 30)
	if _, err := w.r.RunJudge(t.Context(), judgeOpts(50, "category"), nil); err != nil {
		t.Fatal(err)
	}
	labelRequirements(t, w, 5, []string{"manual"})
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); !ok || b.Measure == nil || b.Measure.Have != 0 {
		t.Fatalf("judge labels counted for a manual-only requirement: %+v", b)
	}
	labelRequirements(t, w, 5, []string{"judge"})
	st, err = w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); ok {
		t.Fatalf("judge labels did not count for a judge requirement: %+v", b)
	}
	if judgeLabels(t, w.st, fj) != 20 {
		t.Fatalf("labels %d", judgeLabels(t, w.st, fj))
	}
}

func TestJudgeRefusesUnwiredOrUnsoundRequests(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	if _, err := w.r.RunJudge(t.Context(), judgeOpts(1, "category"), nil); !errors.Is(err, ErrJudgeNotBuilt) {
		t.Fatalf("no judge: %v", err)
	}
	fj := newFakeJudge()
	w.r.judge = func(string) (judge.Judge, error) { return fj, nil }
	if _, err := w.r.RunJudge(t.Context(), judgeOpts(1, "category"), nil); !errors.Is(err, ErrJudgeNotBuilt) {
		t.Fatalf("no state source: %v", err)
	}
	w.r.state = &fakeStates{fail: map[int64]bool{}}
	for name, o := range map[string]JudgeOptions{
		"no limit":      {Questions: []string{"category"}, Budget: engine.Budget{MaxCalls: 1, MaxUSD: 1}},
		"limit too big": {Questions: []string{"category"}, Limit: JudgeLimitMax + 1, Budget: engine.Budget{MaxCalls: 1, MaxUSD: 1}},
		"unknown":       {Questions: []string{"nope"}, Limit: 1, Budget: engine.Budget{MaxCalls: 1, MaxUSD: 1}},
		"duplicate":     {Questions: []string{"category", "category"}, Limit: 1, Budget: engine.Budget{MaxCalls: 1, MaxUSD: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := w.r.RunJudge(t.Context(), o, nil); err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
	if fj.count() != 0 {
		t.Fatalf("%d calls for refused requests", fj.count())
	}
}

func TestStartJudgeRunsAsAJob(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 10)
	jm := jobs.NewManager(w.cfg, nil, nil)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	j, err := w.r.StartJudge(t.Context(), jm, judgeOpts(3, "category"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the judge job did not finish")
	}
	if info := j.Info(); info.Kind != JudgeJobKind || info.Status != "succeeded" {
		t.Fatalf("job %+v", info)
	}
	if fj.count() != 3 {
		t.Fatalf("calls %d", fj.count())
	}
}

// jobKinds is the manager's jobs in start order.
func jobKinds(m *jobs.Manager) []string {
	list := m.List()
	out := make([]string, len(list))
	for i := range list {
		out[i] = list[i].Kind
	}
	return out
}

// A judge run owns the store: its label batches must not start a layer-1
// run (each would hold a read transaction while the next batch waits for
// the write lock). When it ends, exactly one run is requested so the labels
// it bought are learned.
func TestAJudgeRunPausesTriggersAndRequestsOneRunAfter(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 40)
	bus := events.New()
	t.Cleanup(bus.Close)
	w.r.labels = feedback.NewLabeler(w.st, bus)
	m := jobs.NewManager(w.cfg, bus, nil)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	stop, err := w.r.AfterRuns(bus, m)
	if err != nil {
		t.Fatal(err)
	}
	// The fake blocks every call after the first; a roomy signal buffer keeps
	// the calls after the unblock from waiting on the test's single receive.
	fj.blockAt, fj.entered, fj.block = 1, make(chan struct{}, 64), make(chan struct{})
	j, err := w.r.StartJudge(t.Context(), m, judgeOpts(40, "category"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fj.entered: // the first batch was written and published
	case <-time.After(5 * time.Second):
		t.Fatal("the judge never reached its second call")
	}
	if !w.r.triggersPaused() {
		t.Fatal("triggers are not paused during the judge run")
	}
	stop() // drain: the label event of the first batch was handled under the pause
	if got := jobKinds(m); !slices.Equal(got, []string{JudgeJobKind}) {
		t.Fatalf("jobs during the judge: %v", got)
	}
	if w.r.single.Busy() || w.r.single.Pending() {
		t.Fatal("a layer-1 run claimed the slot during the judge")
	}
	close(fj.block)
	select {
	case <-j.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the judge job did not finish")
	}
	if w.r.triggersPaused() {
		t.Fatal("triggers stayed paused after the judge")
	}
	if got := jobKinds(m); !slices.Equal(got, []string{JudgeJobKind, JobKind}) {
		t.Fatalf("run requests after the judge: %v", got)
	}
	list := m.List()
	if list[0].Status != runstate.Succeeded || list[1].Kind != JobKind {
		t.Fatalf("jobs after the judge: %+v", list)
	}
	post, ok := m.Get(list[1].ID)
	if !ok {
		t.Fatal("the post-judge run vanished")
	}
	select {
	case <-post.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the post-judge run did not finish")
	}
	if info := post.Info(); info.Status != runstate.Succeeded {
		t.Fatalf("the post-judge run: %+v", info)
	}
	// A label event from the judge's last batch at the same instant only
	// queues behind that run; once it has committed, the queued start sees
	// unchanged inputs and opens no second job.
	w.r.startAfter(m, "labels")
	if got := jobKinds(m); !slices.Equal(got, []string{JudgeJobKind, JobKind}) {
		t.Fatalf("a label event opened a second run after the judge: %v", got)
	}
}

// A judge job that cannot start still resumes the triggers it paused.
func TestAJudgeStartFailureResumesTriggers(t *testing.T) {
	t.Parallel()
	w, _, _ := judgeWorld(t, 4)
	jm := jobs.NewManager(w.cfg, nil, nil)
	if err := jm.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.r.StartJudge(t.Context(), jm, judgeOpts(1, "category")); !errors.Is(err, jobs.ErrShutdown) {
		t.Fatalf("want ErrShutdown, got %v", err)
	}
	if w.r.triggersPaused() {
		t.Fatal("a judge start that failed left the triggers paused")
	}
}

// The synchronous path pauses the triggers too; it has no manager, so it
// only resumes (the next trigger or an explicit run trains).
func TestRunJudgePausesTriggersWithoutAManager(t *testing.T) {
	t.Parallel()
	w, fj, _ := judgeWorld(t, 10)
	fj.blockAt, fj.entered, fj.block = 1, make(chan struct{}, 8), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := w.r.RunJudge(t.Context(), judgeOpts(5, "category"), nil); err != nil {
			t.Errorf("judge run: %v", err)
		}
	}()
	select {
	case <-fj.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the judge never reached its second call")
	}
	if !w.r.triggersPaused() {
		t.Fatal("RunJudge did not pause the triggers")
	}
	close(fj.block)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the judge run did not finish")
	}
	if w.r.triggersPaused() {
		t.Fatal("RunJudge left the triggers paused")
	}
}

type judgeSecrets map[string]string

func (f judgeSecrets) Get(name string) (string, error) {
	if v, ok := f[name]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (judgeSecrets) Set(string, string) error { return errors.New("read only") }
func (judgeSecrets) Delete(string) error      { return errors.New("read only") }
func (judgeSecrets) List() ([]string, error)  { return nil, nil }
func (judgeSecrets) Kind() string             { return "fake" }
func (judgeSecrets) ReadOnly() bool           { return true }

// The run_judge action names its provider and carries the real estimate of
// the configured judge source (never "unknown" when the provider exists).
func TestJudgeActionCarriesProviderAndRealCost(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	src := judge.NewSource(engine.Deps{Config: w.cfg, Secrets: judgeSecrets{"PROVIDER_JUDGE_API_KEY": "k"}})
	w.r.judgeCost = src.Cost
	a := w.r.judgeAction("category", 10)
	if a.ID != "run_judge" || a.Params["provider"] != "example-provider-gemma" || a.Params["count"] != "10" {
		t.Fatalf("action %+v", a)
	}
	if a.Params["estimated_cost_usd"] == "unknown" || a.Params["cost_note"] != "" {
		t.Fatalf("the judge cost is still unknown: %+v", a)
	}
	cost, err := strconv.ParseFloat(a.Params["estimated_cost_usd"], 64)
	if err != nil || cost <= 0 {
		t.Fatalf("cost %q %v", a.Params["estimated_cost_usd"], err)
	}
	small := w.r.judgeAction("category", 1)
	if small.Params["estimated_cost_usd"] == "0.00" {
		t.Fatalf("a one-message cost shows as zero: %+v", small)
	}
	if cost, err := strconv.ParseFloat(small.Params["estimated_cost_usd"], 64); err != nil || cost <= 0 {
		t.Fatalf("small cost %q %v", small.Params["estimated_cost_usd"], err)
	}
}
