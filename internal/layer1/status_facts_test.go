package layer1

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// statusLatch is a learning latch that never persists a scope: the status
// builds of these tests only read.
type statusLatch struct{}

func (statusLatch) LearnedScope(context.Context) (string, error)  { return "", nil }
func (statusLatch) SetLearnedScope(context.Context, string) error { return nil }

// countingReads counts the store reads a status build makes.
type countingReads struct {
	statusReads
	mu         sync.Mutex
	runs       map[int]int
	evidence   int
	sequence   int
	storeLabel int
	decisions  int
}

func (c *countingReads) ListLayer1Runs(ctx context.Context, limit int) ([]store.Layer1Run, error) {
	c.mu.Lock()
	c.runs[limit]++
	c.mu.Unlock()
	return c.statusReads.ListLayer1Runs(ctx, limit)
}

func (c *countingReads) ListEvidenceRuns(ctx context.Context, limit int) ([]store.EvidenceRun, error) {
	c.mu.Lock()
	c.evidence++
	c.mu.Unlock()
	return c.statusReads.ListEvidenceRuns(ctx, limit)
}

func (c *countingReads) EmbeddingSequence(ctx context.Context) (int64, error) {
	c.mu.Lock()
	c.sequence++
	c.mu.Unlock()
	return c.statusReads.EmbeddingSequence(ctx)
}

func (c *countingReads) DecisionSummary(ctx context.Context, k store.TriageKey) (int, string, error) {
	c.mu.Lock()
	c.decisions++
	c.mu.Unlock()
	return c.statusReads.DecisionSummary(ctx, k)
}

func (c *countingReads) ListLabels(ctx context.Context) ([]store.LabelRow, error) {
	c.mu.Lock()
	c.storeLabel++
	c.mu.Unlock()
	return c.statusReads.ListLabels(ctx)
}

// statusCountingInputs counts the labels fact the labels stage reads.
type statusCountingInputs struct {
	Inputs
	mu     sync.Mutex
	labels int
}

func (c *statusCountingInputs) Labels(ctx context.Context) ([]store.LabelRow, error) {
	c.mu.Lock()
	c.labels++
	c.mu.Unlock()
	return c.Inputs.Labels(ctx)
}

// factCounter installs the counting doubles of one status build's facts.
type factCounter struct {
	reads  *countingReads
	inputs *statusCountingInputs
	mu     sync.Mutex
	plans  int
}

func (c *factCounter) install(r *Runner) {
	c.reads = &countingReads{statusReads: r.st, runs: map[int]int{}}
	c.inputs = &statusCountingInputs{Inputs: r.in}
	r.reads, r.in = c.reads, c.inputs
	r.buildPlan = func(cfg *config.Config) (*Plan, error) {
		c.mu.Lock()
		c.plans++
		c.mu.Unlock()
		return NewPlan(cfg)
	}
}

func (c *factCounter) planCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.plans
}

// statusStages is one build's stage fan-out: every stage that reads a
// layer-1 fact.
func statusStages(r *Runner) []learning.Stage {
	return []learning.Stage{r.LabelsStage(), r.Stage(), r.QuestionsStage(), r.RouteStage()}
}

// buildOnce is one computeStatus build: a new machine has no cached
// status, so Status asks every stage.
func buildOnce(t *testing.T, stages []learning.Stage) learning.Status {
	t.Helper()
	m, err := learning.New(func() string { return "store-1" }, statusLatch{}, nil, nil, stages...)
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func stageStatus(t *testing.T, st learning.Status, id string) learning.StageStatus {
	t.Helper()
	for _, s := range st.Stages {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("status has no stage %q: %+v", id, st.Stages)
	return learning.StageStatus{}
}

// certProbe reads two reports' certifications inside one build.
type certProbe struct {
	r       *Runner
	reports []*Report
	got     []map[string]selective.Certification
}

func (p *certProbe) ID() string { return "cert-probe" }

func (p *certProbe) Status(ctx context.Context) (learning.StageStatus, error) {
	for _, rep := range p.reports {
		certified, err := p.r.certifications(ctx, rep)
		if err != nil {
			return learning.StageStatus{}, err
		}
		p.got = append(p.got, certified)
	}
	return learning.StageStatus{State: learning.Done}, nil
}

// Two reports of different runs never share certifications, even inside
// one build: after a failed pass the questions stage reads the newest run
// while the labels stage reads the newest succeeded one.
func TestCertificationsArePerReport(t *testing.T) {
	t.Parallel()
	one := &Report{RunID: 1, Routes: []RouteReport{{Question: "q",
		Certification: selective.Certification{Certified: true}}}}
	two := &Report{RunID: 2, Routes: []RouteReport{{Question: "q",
		Certification: selective.Certification{Certified: false}}}}
	p := &certProbe{r: &Runner{}, reports: []*Report{one, two}}
	m, err := learning.New(func() string { return "store-1" }, statusLatch{}, nil, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(p.got) != 2 {
		t.Fatalf("probe read %d reports, want 2", len(p.got))
	}
	if !p.got[0]["q"].Certified {
		t.Fatal("the first report's certification is missing")
	}
	if p.got[1]["q"].Certified {
		t.Fatal("the second report reused the first report's certification")
	}
}

// A succeeded-run status build reads each layer-1 fact once, and its
// status equals the same stages read outside the build memo.
func TestStatusBuildReadsEachLayer1FactOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	w.run()
	c := &factCounter{}
	c.install(w.r)

	stages := statusStages(w.r)
	st := buildOnce(t, stages)

	if got := c.reads.runs[1]; got != 1 {
		t.Fatalf("lastRun read ListLayer1Runs(1) %d times, want 1", got)
	}
	if got := c.reads.runs[50]; got != 1 {
		t.Fatalf("lastSucceeded read ListLayer1Runs(50) %d times, want 1", got)
	}
	if got := len(c.reads.runs); got != 2 {
		t.Fatalf("the build read layer-1 runs with %d limits, want 2: %+v", got, c.reads.runs)
	}
	if got := c.inputs.labels; got != 1 {
		t.Fatalf("the build read Labels %d times, want 1", got)
	}
	if got := c.reads.evidence; got != 1 {
		t.Fatalf("the fingerprint read ListEvidenceRuns %d times, want 1", got)
	}
	if got := c.reads.sequence; got != 1 {
		t.Fatalf("the fingerprint read EmbeddingSequence %d times, want 1", got)
	}
	if got := c.reads.decisions; got != 1 {
		t.Fatalf("the fingerprint read DecisionSummary %d times, want 1", got)
	}
	if got := c.reads.storeLabel; got != 1 {
		t.Fatalf("the fingerprint read ListLabels %d times, want 1", got)
	}
	if got := c.planCount(); got != 1 {
		t.Fatalf("the build built the plan %d times, want 1", got)
	}

	// The memo changes no value: every stage read outside the build
	// returns exactly the status the build returned.
	for i, s := range stages {
		direct, err := s.Status(t.Context())
		if err != nil {
			t.Fatalf("%s: %v", s.ID(), err)
		}
		direct.ID = s.ID()
		if !reflect.DeepEqual(direct, st.Stages[i]) {
			t.Fatalf("%s differs outside the build memo:\nbuild:  %+v\ndirect: %+v", s.ID(), st.Stages[i], direct)
		}
	}
}

// The memo is per build: a second build reads every fact again, and a
// label written between builds is observed.
func TestStatusBuildMemoIsPerBuild(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	w.run()
	c := &factCounter{}
	c.install(w.r)

	first := buildOnce(t, statusStages(w.r))
	second := buildOnce(t, statusStages(w.r))
	if !reflect.DeepEqual(first.Stages, second.Stages) {
		t.Fatalf("two builds over the same facts differ:\n%+v\n%+v", first.Stages, second.Stages)
	}
	if got := c.reads.runs[1]; got != 2 {
		t.Fatalf("a second build left ListLayer1Runs(1) at %d calls, want 2", got)
	}
	if got := c.reads.runs[50]; got != 2 {
		t.Fatalf("a second build left ListLayer1Runs(50) at %d calls, want 2", got)
	}
	if got := c.inputs.labels; got != 2 {
		t.Fatalf("a second build left Labels at %d calls, want 2", got)
	}
	if got := c.reads.evidence; got != 2 {
		t.Fatalf("a second build left ListEvidenceRuns at %d calls, want 2", got)
	}
	if got := c.planCount(); got != 2 {
		t.Fatalf("a second build left the plan at %d builds, want 2", got)
	}

	// A label write between builds is visible to the next build: the
	// labels stage counts one more evaluation label and no cross-build
	// memo hides it.
	before := stageStatus(t, second, learning.StageLabels)
	q, ok := w.p.Question("urgency")
	if !ok {
		t.Fatal("the profile has no urgency question")
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], q.Name, labelValue(t, q, 0), store.LabelManual); err != nil {
		t.Fatal(err)
	}
	third := buildOnce(t, statusStages(w.r))
	after := stageStatus(t, third, learning.StageLabels)
	if after.Progress.Done != before.Progress.Done+1 {
		t.Fatalf("the next build did not see the new label: done %d, want %d", after.Progress.Done, before.Progress.Done+1)
	}
	if got := c.inputs.labels; got != 3 {
		t.Fatalf("a third build left Labels at %d calls, want 3", got)
	}
}
