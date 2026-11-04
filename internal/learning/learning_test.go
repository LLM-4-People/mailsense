package learning

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
)

// fakeStage reports what the test sets. blocker/entered let a test hold one
// status rebuild in flight and observe when it started.
type fakeStage struct {
	id      string
	mu      sync.Mutex
	st      StageStatus
	err     error
	calls   int
	blocker <-chan struct{}
	entered chan<- struct{}
}

func (f *fakeStage) ID() string { return f.id }
func (f *fakeStage) set(s StageStatus) {
	f.mu.Lock()
	f.st = s
	f.mu.Unlock()
}
func (f *fakeStage) setBlocker(blocker <-chan struct{}, entered chan<- struct{}) {
	f.mu.Lock()
	f.blocker, f.entered = blocker, entered
	f.mu.Unlock()
}
func (f *fakeStage) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
func (f *fakeStage) Status(context.Context) (StageStatus, error) {
	f.mu.Lock()
	blocker, entered := f.blocker, f.entered
	f.mu.Unlock()
	if blocker != nil {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		<-blocker
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.st, f.err
}

type memLatch struct{ scope string }

func (l *memLatch) LearnedScope(context.Context) (string, error)      { return l.scope, nil }
func (l *memLatch) SetLearnedScope(_ context.Context, s string) error { l.scope = s; return nil }

// settleStatus forces the synchronous build after a test changes a stage by
// hand. Production calls Invalidate from the event that changed the stage;
// the next read is allowed to serve stale, so the test must not race it.
func settleStatus(t *testing.T, m *Machine) {
	t.Helper()
	m.Invalidate()
	if _, err := m.computeStatus(t.Context(), m.scope()); err != nil {
		t.Fatal(err)
	}
}

func TestTheGateOpensOnlyWhenEveryStageIsDoneAndStaysOpenForThatScope(t *testing.T) {
	clk := clocktest.New(time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC))
	bus := events.New()
	defer bus.Close()
	sub, _ := events.Subscribe(bus, TopicStatus, 16)
	a := &fakeStage{id: "a", st: StageStatus{State: Running, Progress: Progress{Done: 10, Total: 110}}}
	b := &fakeStage{id: "b", st: StageStatus{State: Done}}
	scope := "store-1"
	latch := &memLatch{}
	m, err := New(func() string { return scope }, latch, bus, clk, a, b, NotBuilt(StageLayer1, "heads"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	err = m.Allow(ctx)
	var locked *LockedError
	if !errors.As(err, &locked) || !errors.Is(err, ErrLocked) || locked.Status.State != Learning {
		t.Fatalf("closed while learning: %v", err)
	}
	if st := locked.Status.Stages[2]; st.State != Pending || !strings.Contains(st.Note, "not built") {
		t.Fatalf("a stage that is not built is pending: %+v", st)
	}
	// ETA from the measured rate: 10 → 60 of 110 in 50 s ⇒ 50 s left.
	clk.Advance(50 * time.Second)
	a.set(StageStatus{State: Running, Progress: Progress{Done: 60, Total: 110}})
	settleStatus(t, m)
	st, _ := m.Status(ctx)
	if eta := st.Stages[0].ETA; eta == nil || *eta != 50*time.Second {
		t.Fatalf("eta %v", eta)
	}
	a.set(StageStatus{State: Blocked, Blockers: []Blocker{{Code: "x", Actions: []Action{Do("do")}}}})
	settleStatus(t, m)
	if st, _ := m.Status(ctx); st.State != Stuck || st.Stages[0].ETA != nil {
		t.Fatalf("a blocker blocks: %+v", st)
	}
	// Every stage done except the not-built one: still closed (never relaxes).
	a.set(StageStatus{State: Done})
	settleStatus(t, m)
	if err := m.Allow(ctx); err == nil {
		t.Fatal("a pending stage keeps the gate closed")
	}
	if latch.scope != "" {
		t.Fatal("nothing latched before learning finished")
	}
	events := 0
	for len(sub.C) > 0 {
		<-sub.C
		events++
	}
	if events == 0 {
		t.Fatal("status changes are published")
	}
}

func TestLatchedScopeAndANewPick(t *testing.T) {
	a := &fakeStage{id: "a", st: StageStatus{State: Done}}
	scope := "k1"
	latch := &memLatch{}
	m, _ := New(func() string { return scope }, latch, nil, nil, a)
	if err := m.Allow(t.Context()); err != nil || latch.scope != "k1" {
		t.Fatalf("learned: %v %q", err, latch.scope)
	}
	// Learning continues on new mail: the gate stays open for that scope.
	a.set(StageStatus{State: Running})
	if err := m.Allow(t.Context()); err != nil {
		t.Fatalf("a learned scope stays open: %v", err)
	}
	// A new pick is a new scope: closed until it is learned too.
	scope = "k1,k2"
	if err := m.Allow(t.Context()); !errors.Is(err, ErrLocked) {
		t.Fatalf("a new pick closes the gate: %v", err)
	}
	// Review L0 rules §1.10 (failed first: unpicking k2 closed the gate):
	// learned {k1,k2}; a strict subset stays open, an added store closes.
	scope = "k1,k2"
	a.set(StageStatus{State: Done})
	settleStatus(t, m)
	if err := m.Allow(t.Context()); err != nil || latch.scope != "k1,k2" {
		t.Fatalf("learned k1,k2: %v %q", err, latch.scope)
	}
	a.set(StageStatus{State: Running})
	m.Invalidate()
	scope = "k2"
	if err := m.Allow(t.Context()); err != nil {
		t.Fatalf("a subset of the learned selection stays open: %v", err)
	}
	scope = "k2,k3"
	if err := m.Allow(t.Context()); !errors.Is(err, ErrLocked) {
		t.Fatalf("an added store closes the gate: %v", err)
	}
	// Nothing picked: never open, even with every stage done.
	scope = ""
	a.set(StageStatus{State: Done})
	if err := m.Allow(t.Context()); !errors.Is(err, ErrLocked) {
		t.Fatalf("an empty scope never opens: %v", err)
	}
}

func TestPreviewUnlocksAllowButNeverTheRealGate(t *testing.T) {
	a := &fakeStage{id: "a", st: StageStatus{State: Blocked, Blockers: []Blocker{{Code: "wait"}}}}
	scope := "k1"
	latch := &memLatch{}
	m, err := New(func() string { return scope }, latch, nil, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	var preview atomic.Bool
	m.SetPreview(preview.Load)
	ctx := t.Context()

	// Preview off: the real locked error, and the real gate answer is open
	// only for a learned selection.
	if err := m.Allow(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("preview off must keep the real gate: %v", err)
	}
	if m.Previewing() {
		t.Fatal("preview off reports previewing")
	}
	if open, err := m.Open(ctx); err != nil || open {
		t.Fatalf("preview off: open=%v err=%v", open, err)
	}
	before, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != Stuck || latch.scope != "" {
		t.Fatalf("preview off changed the gate: %+v latch=%q", before, latch.scope)
	}

	// Preview on: Allow is nil, but the real gate answer and status are
	// untouched and nothing is latched.
	preview.Store(true)
	if err := m.Allow(ctx); err != nil {
		t.Fatalf("preview must let the answer screens through: %v", err)
	}
	if !m.Previewing() {
		t.Fatal("preview on does not report previewing")
	}
	if open, err := m.Open(ctx); err != nil || open {
		t.Fatalf("preview must not open the real gate: open=%v err=%v", open, err)
	}
	after, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed(before, after) || after.State != Stuck || latch.scope != "" {
		t.Fatalf("preview changed the real status: %+v -> %+v latch=%q", before, after, latch.scope)
	}

	// Preview off again: reversible, the same closed gate.
	preview.Store(false)
	if err := m.Allow(ctx); !errors.Is(err, ErrLocked) {
		t.Fatalf("preview off must restore the locked gate: %v", err)
	}
	if m.Previewing() {
		t.Fatal("preview off again reports previewing")
	}

	// A truly learned selection opens Open whether preview is on or off.
	a.set(StageStatus{State: Done})
	settleStatus(t, m)
	if open, err := m.Open(ctx); err != nil || !open {
		t.Fatalf("learned gate: open=%v err=%v", open, err)
	}
	preview.Store(true)
	if err := m.Allow(ctx); err != nil {
		t.Fatalf("learned gate with preview: %v", err)
	}
	if open, err := m.Open(ctx); err != nil || !open {
		t.Fatalf("learned gate must open regardless of preview: open=%v err=%v", open, err)
	}
}

// PreviewAnswers is the one displayed preview rule: the explicit setting is
// on and the real gate is closed. A learned selection stays certified even
// while the setting is on.
func TestPreviewAnswersIsTheOneDisplayedPreviewRule(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting bool
		stage   StageStatus
		want    bool
	}{
		{"setting off, gate closed", false, StageStatus{State: Blocked}, false},
		{"setting on, gate closed", true, StageStatus{State: Blocked}, true},
		{"setting on, gate open", true, StageStatus{State: Done}, false},
		{"setting off, gate open", false, StageStatus{State: Done}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(func() string { return "k1" }, &memLatch{}, nil, nil, &fakeStage{id: "a", st: tc.stage})
			if err != nil {
				t.Fatal(err)
			}
			m.SetPreview(func() bool { return tc.setting })
			if got := m.PreviewAnswers(t.Context()); got != tc.want {
				t.Fatalf("PreviewAnswers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewRefusesMisuse(t *testing.T) {
	s := &fakeStage{id: "a"}
	for name, f := range map[string]func() (*Machine, error){
		"no scope":     func() (*Machine, error) { return New(nil, &memLatch{}, nil, nil, s) },
		"no latch":     func() (*Machine, error) { return New(func() string { return "" }, nil, nil, nil, s) },
		"no stage":     func() (*Machine, error) { return New(func() string { return "" }, &memLatch{}, nil, nil) },
		"duplicate id": func() (*Machine, error) { return New(func() string { return "" }, &memLatch{}, nil, nil, s, s) },
		"nil stage":    func() (*Machine, error) { return New(func() string { return "" }, &memLatch{}, nil, nil, nil) },
	} {
		if _, err := f(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// #12: the time zone is confirmed by the user, never assumed.
func TestMachineStageWantsAConfirmedZone(t *testing.T) {
	cfg := configtest.New(t, nil)
	st, _ := MachineStage(cfg, nil).Status(t.Context())
	if st.State != Blocked || st.Blockers[0].Code != BlockZone || st.Blockers[0].Actions[0].ID != ActionConfirmTimeZone ||
		st.Blockers[0].Actions[0].Params["offset"] == "" {
		t.Fatalf("unconfirmed: %+v", st)
	}
	// Misuse: the machine's zone and nonsense are refused, nothing saved.
	for _, bad := range []string{"", "Local", "Mars/Base"} {
		if err := ConfirmZone(cfg, bad); err == nil || cfg.String("time.zone") != "" {
			t.Fatalf("%q must be refused", bad)
		}
	}
	if err := ConfirmZone(cfg, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	if st, _ := MachineStage(cfg, nil).Status(t.Context()); st.State != Done {
		t.Fatalf("confirmed: %+v", st)
	}
}

func TestRefreshOnPublishesAfterEvents(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	trigger := events.NewTopic[int]("test.trigger")
	a := &fakeStage{id: "a", st: StageStatus{State: Running}}
	m, _ := New(func() string { return "k" }, &memLatch{}, bus, nil, a)
	sub, _ := events.Subscribe(bus, TopicStatus, 4)
	stop, err := RefreshOn(m, bus, trigger, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	a.set(StageStatus{State: Done})
	events.Publish(bus, trigger, 1)
	if st := <-sub.C; st.State != Learned {
		t.Fatalf("%+v", st)
	}
}

// A running job's progress events invalidate the cached status, but a
// burst coalesces into one (and at most a trailing second) rebuild: the
// index publishes one event per batch.
func TestProgressRefreshInvalidatesAndCoalescesABurst(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	trigger := events.NewTopic[int]("test.progress")
	a := &fakeStage{id: "a", st: StageStatus{State: Running, Progress: Progress{Done: 1, Total: 100}}}
	m, _ := New(func() string { return "k" }, &memLatch{}, bus, nil, a)
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	sub, _ := events.Subscribe(bus, TopicStatus, 8)
	stop, err := RefreshOnProgress(m, bus, trigger, 16, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	a.set(StageStatus{State: Running, Progress: Progress{Done: 50, Total: 100}})
	for i := range 200 {
		events.Publish(bus, trigger, i)
	}
	select {
	case st := <-sub.C:
		if st.Stages[0].Progress.Done != 50 {
			t.Fatalf("progress event did not invalidate the status: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status published after the progress burst")
	}
	// Let any trailing rebuild finish, then bound the work the burst caused
	// (the initial build plus at most two coalesced rebuilds).
	time.Sleep(80 * time.Millisecond)
	if calls := a.callCount(); calls > 3 {
		t.Fatalf("200 progress events caused %d status rebuilds, want at most 3", calls)
	}
}

// The final change must not be lost when it lands while a progress rebuild
// is already running: the machine runs one more rebuild and publishes the
// final status instead of leaving the pre-run one cached.
func TestFinalInvalidationDuringARebuildIsNotLost(t *testing.T) {
	bus := events.New()
	defer bus.Close()
	a := &fakeStage{id: "a", st: StageStatus{State: Running, Progress: Progress{Done: 0, Total: 100}}}
	m, _ := New(func() string { return "k" }, &memLatch{}, bus, nil, a)
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	sub, _ := events.Subscribe(bus, TopicStatus, 8)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	a.set(StageStatus{State: Running, Progress: Progress{Done: 10, Total: 100}})
	a.setBlocker(release, entered)
	m.Invalidate()
	if _, err := m.Status(t.Context()); err != nil { // starts the background rebuild
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the background rebuild did not start")
	}
	// The final event lands while that rebuild is blocked in the stage.
	a.set(StageStatus{State: Done, Progress: Progress{Done: 100, Total: 100}})
	m.Invalidate()
	if _, err := m.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	a.setBlocker(nil, nil)
	close(release)
	select {
	case st := <-sub.C:
		if st.Stages[0].State != Done || st.Stages[0].Progress.Done != 100 {
			t.Fatalf("the final status was lost behind the progress rebuild: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no final status published after the invalidation during a rebuild")
	}
}

// countingLatch records how many times the gate rule read the learned scope.
type countingLatch struct {
	memLatch
	reads atomic.Int64
}

func (l *countingLatch) LearnedScope(ctx context.Context) (string, error) {
	l.reads.Add(1)
	return l.memLatch.LearnedScope(ctx)
}

// failingLatch is a broken gate dependency: every read fails.
type failingLatch struct{ err error }

func (l failingLatch) LearnedScope(context.Context) (string, error)  { return "", l.err }
func (l failingLatch) SetLearnedScope(context.Context, string) error { return l.err }

// TestGateStateIsOneConsistentReading pins the one reading the gate route
// asks for: the preview rule, the real gate and the allowance all derive from
// a single evaluation of the rule (one learned-scope read). Asking the three
// questions separately reads it three times, so a learning event landing
// between them can produce a view that never existed.
func TestGateStateIsOneConsistentReading(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		setting                bool
		stage                  StageStatus
		preview, open, allowed bool
	}{
		{"setting off, gate closed", false, StageStatus{State: Blocked}, false, false, false},
		{"setting on, gate closed", true, StageStatus{State: Blocked}, true, false, true},
		{"setting on, gate open", true, StageStatus{State: Done}, false, true, true},
		{"setting off, gate open", false, StageStatus{State: Done}, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latch := &countingLatch{}
			m, err := New(func() string { return "k1" }, latch, nil, nil, &fakeStage{id: "a", st: tc.stage})
			if err != nil {
				t.Fatal(err)
			}
			m.SetPreview(func() bool { return tc.setting })
			gate := m.GateState(t.Context())
			if gate.Preview != tc.preview || gate.Open != tc.open || (gate.Allow == nil) != tc.allowed {
				t.Fatalf("GateState = %+v, want preview=%v open=%v allowed=%v",
					gate, tc.preview, tc.open, tc.allowed)
			}
			if gate.Err != nil {
				t.Fatalf("a healthy rule reported Err = %v", gate.Err)
			}
			if got := latch.reads.Load(); got != 1 {
				t.Fatalf("the gate rule read the learned scope %d times, want 1", got)
			}

			// The three question readers are the same reading: each must
			// answer exactly what GateState answered a moment before.
			answered, err := m.Open(t.Context())
			if answered != gate.Open || err != gate.Err {
				t.Fatalf("Open = (%v, %v), GateState says (%v, %v)", answered, err, gate.Open, gate.Err)
			}
			if got := m.PreviewAnswers(t.Context()); got != gate.Preview {
				t.Fatalf("PreviewAnswers = %v, GateState says %v", got, gate.Preview)
			}
			before := latch.reads.Load()
			if err := m.Allow(t.Context()); (err == nil) != tc.allowed {
				t.Fatalf("Allow = %v, GateState says allowed=%v", err, tc.allowed)
			}
			// The documented Allow shortcut: the preview answers without
			// reading the rule at all; otherwise it reads it once.
			wantReads := int64(1)
			if tc.setting {
				wantReads = 0
			}
			if got := latch.reads.Load() - before; got != wantReads {
				t.Fatalf("Allow read the rule %d times, want %d (preview=%v)", got, wantReads, tc.setting)
			}
		})
	}
}

// A broken rule is still an error even while the test preview lets answers
// through: the preview hides the lock, never a failed latch read.
func TestGateStateKeepsTheRawFailureUnderThePreview(t *testing.T) {
	boom := errors.New("latch unavailable")
	for _, setting := range []bool{false, true} {
		m, err := New(func() string { return "k1" }, failingLatch{err: boom}, nil, nil,
			&fakeStage{id: "a", st: StageStatus{State: Done}})
		if err != nil {
			t.Fatal(err)
		}
		m.SetPreview(func() bool { return setting })
		gate := m.GateState(t.Context())
		if !errors.Is(gate.Err, boom) {
			t.Fatalf("setting=%v: Err = %v, want the latch failure", setting, gate.Err)
		}
		if setting {
			if gate.Allow != nil {
				t.Fatalf("the preview must still let answers through, got %v", gate.Allow)
			}
		} else if !errors.Is(gate.Allow, boom) {
			t.Fatalf("setting off: Allow = %v, want the latch failure", gate.Allow)
		}
		if gate.Open {
			t.Fatal("a failed rule reported the gate open")
		}
		// A broken rule is neither open nor known-closed, so the preview must
		// not claim the gate is closed: the readers agree with GateState.
		if gate.Preview {
			t.Fatal("a failed rule claimed the test preview")
		}
		if got := m.PreviewAnswers(t.Context()); got != gate.Preview {
			t.Fatalf("PreviewAnswers = %v, GateState says %v", got, gate.Preview)
		}
		if open, err := m.Open(t.Context()); open != gate.Open || !errors.Is(err, boom) {
			t.Fatalf("Open = (%v, %v), want (false, the latch failure)", open, err)
		}
		if err := m.Allow(t.Context()); setting && err != nil {
			t.Fatalf("the preview must let answers through, got %v", err)
		} else if !setting && !errors.Is(err, boom) {
			t.Fatalf("setting off: Allow = %v, want the latch failure", err)
		}
	}
}
