package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// warmGate scripts the warm's admission at the policy's own seam. paused
// refuses every stage until clear opens it, which is the deterministic form of
// "the host clears mid-warm" with no sampler to race.
type warmGate struct {
	mu     sync.Mutex
	paused bool
	calls  int
	err    error

	// blockOn holds that stage's admission until hold is closed, so a test can
	// clear the guard at an exact point of the warm without racing a sampler.
	blockOn warmView
	hold    chan struct{}
}

func (g *warmGate) Admission(_ context.Context, stage warmView) (*resource.LimitError, error) {
	g.mu.Lock()
	g.calls++
	blocked, hold := g.blockOn == stage && g.blockOn != "", g.hold
	err, paused := g.err, g.paused
	g.mu.Unlock()
	if blocked {
		<-hold
	}
	if err != nil {
		return nil, err
	}
	if !paused {
		return nil, nil
	}
	return &resource.LimitError{Kind: resource.CPU, Percent: 95, Limit: 80, At: time.Now()}, nil
}

// clear opens the guard: every admission from now on is permitted.
func (g *warmGate) clear() {
	g.mu.Lock()
	g.paused = false
	g.mu.Unlock()
}

func (g *warmGate) admissionCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// warmProbe instruments the fake services: it records which stages actually
// built and how many heavy builds were inside their service call at once.
// block holds every heavy build until hold is closed or the warm's context
// ends, which is how the concurrency bound and the cancellation path are
// observed without timing a real scan.
type warmProbe struct {
	mu   sync.Mutex
	ran  map[string]int
	live int
	peak int

	// block holds every heavy build; blockView holds only the named stage.
	// Both honour the warm's context, so a held build never outlives it.
	block     bool
	blockView string
	hold      chan struct{}
}

func newWarmProbe() *warmProbe {
	return &warmProbe{ran: map[string]int{}, hold: make(chan struct{})}
}

func (p *warmProbe) built(view string) {
	p.mu.Lock()
	p.ran[view]++
	p.mu.Unlock()
}

// enter counts one heavy build and optionally holds it. It always honours the
// warm's context, so a held build never outlives the warm.
func (p *warmProbe) enter(ctx context.Context) error { return p.enterView(ctx, "") }

func (p *warmProbe) enterView(ctx context.Context, view string) error {
	p.mu.Lock()
	p.live++
	if p.live > p.peak {
		p.peak = p.live
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.live--
		p.mu.Unlock()
	}()
	if !p.block && (p.blockView == "" || view != p.blockView) {
		return nil
	}
	select {
	case <-p.hold:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *warmProbe) count(view string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ran[view]
}

func (p *warmProbe) peakBuilds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

func (p *warmProbe) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := 0
	for _, n := range p.ran {
		total += n
	}
	return total
}

// Heavy fakes: the store-scanning stages. Every one counts and may hold.

type probeTriage struct {
	warmTriage
	p *warmProbe
}

func (x probeTriage) Summary(ctx context.Context) (*triage.Summary, error) {
	if err := x.p.enterView(ctx, "triage_summary"); err != nil {
		return nil, err
	}
	x.p.built("triage_summary")
	return x.warmTriage.Summary(ctx)
}

func (x probeTriage) Page(ctx context.Context, q triage.PageQuery) (*triage.Page, error) {
	if err := x.p.enterView(ctx, "triage_page"); err != nil {
		return nil, err
	}
	x.p.built("triage_page")
	return x.warmTriage.Page(ctx, q)
}

type probePriority struct {
	warmPriority
	p *warmProbe
}

func (x probePriority) Rank(ctx context.Context, f priority.Filter, limit int) ([]priority.Item, int, bool, error) {
	if err := x.p.enterView(ctx, "priority"); err != nil {
		return nil, 0, false, err
	}
	x.p.built("priority")
	return x.warmPriority.Rank(ctx, f, limit)
}

type probeConversations struct {
	warmConversations
	p *warmProbe
}

func (x probeConversations) Page(ctx context.Context, q conversations.Query) (conversations.Page, error) {
	if err := x.p.enterView(ctx, "conversations"); err != nil {
		return conversations.Page{}, err
	}
	x.p.built("conversations")
	return x.warmConversations.Page(ctx, q)
}

type probeCompanies struct {
	warmCompanies
	p *warmProbe
}

func (x probeCompanies) List(ctx context.Context, q worldsvc.ListQuery) (worldsvc.ListResult, error) {
	if err := x.p.enterView(ctx, "companies"); err != nil {
		return worldsvc.ListResult{}, err
	}
	x.p.built("companies")
	return x.warmCompanies.List(ctx, q)
}

func (x probeCompanies) Profiles(ctx context.Context, keys []string, months, eventDays int) (map[string]worldsvc.ProfileView, error) {
	// The batch runs inside the same stage: it shares the stage's slot, so it
	// only records that the profile batch built.
	x.p.built("companies_profiles")
	return x.warmCompanies.Profiles(ctx, keys, months, eventDays)
}

type probeLearning struct {
	warmLearning
	p *warmProbe
}

func (x probeLearning) Status(ctx context.Context) (learning.Status, learning.GateState, error) {
	if err := x.p.enterView(ctx, "learning"); err != nil {
		return learning.Status{}, learning.GateState{}, err
	}
	x.p.built("learning")
	return x.warmLearning.Status(ctx)
}

type probeHub struct {
	warmHub
	p *warmProbe
}

func (x probeHub) Hub(ctx context.Context) (hub.Hub, error) {
	if err := x.p.enterView(ctx, "home"); err != nil {
		return hub.Hub{}, err
	}
	x.p.built("home")
	return x.warmHub.Hub(ctx)
}

type probeMessages struct {
	warmMessages
	p *warmProbe
}

func (x probeMessages) List(ctx context.Context, q store.MessageListQuery) (api.MessageListView, error) {
	if err := x.p.enterView(ctx, "mail"); err != nil {
		return api.MessageListView{}, err
	}
	x.p.built("mail")
	return x.warmMessages.List(ctx, q)
}

// Cheap fakes: they compose caches the warm already built. They still record
// that they ran, so "a paused guard builds nothing" covers every stage.

type probeAgenda struct {
	warmAgenda
	p *warmProbe
}

func (x probeAgenda) Build(ctx context.Context, h time.Duration) (agenda.Result, error) {
	x.p.built("agenda")
	return x.warmAgenda.Build(ctx, h)
}

type probeCommitments struct {
	warmCommitments
	p *warmProbe
}

func (x probeCommitments) List(ctx context.Context, f commitments.Filter) (commitments.Result, error) {
	x.p.built("commitments")
	return x.warmCommitments.List(ctx, f)
}

type probeBrief struct {
	warmBrief
	p *warmProbe
}

func (x probeBrief) Brief(ctx context.Context, since string) (brief.Result, error) {
	x.p.built("brief")
	return x.warmBrief.Brief(ctx, since)
}

func probeServices(p *warmProbe, r *warmRecord) api.Services {
	return api.Services{
		Triage:        probeTriage{warmTriage: warmTriage{r: r}, p: p},
		Priority:      probePriority{warmPriority: warmPriority{r: r}, p: p},
		Agenda:        probeAgenda{warmAgenda: warmAgenda{r: r}, p: p},
		Conversations: probeConversations{p: p},
		Commitments:   probeCommitments{p: p},
		Companies:     probeCompanies{p: p},
		Learning:      probeLearning{p: p},
		Hub:           probeHub{p: p},
		Brief:         probeBrief{p: p},
		Messages:      probeMessages{warmMessages: warmMessages{r: r}, p: p},
	}
}

// warmStageNames is the warm's own stage set: the ten derived stages plus the
// Brief. The semantic prepare belongs to the same policy but has its own
// function, so the derived-only tests account for it explicitly below.
var warmStageNames = []string{
	"mail", "learning", "triage_summary", "triage_page", "home", "priority",
	"agenda", "conversations", "commitments", "companies", "companies_profiles", "brief",
}

// derivedStages is the number of stages derived() launches; the semantic
// prepare is the additional one warmStartup owns.
var derivedStages = len(warmViewOrder) - 1

// runWarm runs the derived warm with the given admission and returns the boot
// state after ready(), exactly as production composes it. The semantic prepare
// is recorded as a completed stage, matching a warm whose prepare ran.
func runWarm(t *testing.T, services api.Services, guard warmGuard) *bootState {
	t.Helper()
	boot := newBootState()
	warmDerivedTracked(t.Context(), services, boot, guard)
	boot.start(viewSemanticSearch)
	boot.finish(viewSemanticSearch, nil)
	boot.ready()
	return boot
}

// stageStates maps a boot snapshot to name -> state.
func stageStates(boot *bootState) map[string]string {
	snap := boot.snapshot()
	out := make(map[string]string, len(snap.Views))
	for _, view := range snap.Views {
		out[view.Name] = view.State
	}
	return out
}

// A paused guard builds nothing: every warm stage is skipped, the warm still
// returns, and no skipped stage is reported ready or failed.
func TestWarmSkipsEveryStageWhenTheGuardRefuses(t *testing.T) {
	p := newWarmProbe()
	gate := &warmGate{paused: true}
	boot := runWarm(t, probeServices(p, &warmRecord{}), gate)

	if got := p.total(); got != 0 {
		t.Fatalf("%d stage builds ran under a paused guard, want 0: %v", got, p.ran)
	}
	if gate.admissionCalls() != derivedStages {
		t.Fatalf("%d admission calls, want one per derived stage (%d)", gate.admissionCalls(), derivedStages)
	}
	snap := boot.snapshot()
	if snap.State != bootReady {
		t.Fatalf("state %q, want ready", snap.State)
	}
	// The semantic prepare is started by startWarm, not by this warm, so it is
	// the one stage the derived warm can never skip.
	if snap.Skipped != snap.Total-1 {
		t.Fatalf("skipped %d, total %d: every derived stage was refused", snap.Skipped, snap.Total)
	}
	// Done is the semantic prepare alone; no derived stage may be done.
	if snap.Done != 1 || snap.Failed != 0 {
		t.Fatalf("done %d, failed %d, want 1 and 0: a refused stage is neither", snap.Done, snap.Failed)
	}
	for _, view := range snap.Views {
		if view.Name == string(viewSemanticSearch) {
			if view.State != stageDone {
				t.Fatalf("view %q is %q, want done: the semantic prepare is not a derived stage", view.Name, view.State)
			}
			continue
		}
		if view.State != stageSkipped {
			t.Fatalf("view %q is %q, want skipped", view.Name, view.State)
		}
		if view.DurationMS != 0 {
			t.Fatalf("view %q reports %d ms although it never built", view.Name, view.DurationMS)
		}
		if view.Error == "" {
			t.Fatalf("view %q is skipped without naming the ceiling", view.Name)
		}
		if view.Name == string(viewPriority) {
			if want := "resource: cpu at 95% is above the 80% limit"; view.Error != want {
				t.Fatalf("priority reason %q, want %q", view.Error, want)
			}
		}
	}
}

// A live guard builds every stage the old warm built, and the boot snapshot is
// the same ready one as before admission existed.
func TestWarmBuildsEveryStageUnderALiveGuard(t *testing.T) {
	baseline := newWarmTrace()
	boot := runWarm(t, baseline.services(), &warmGate{})
	states := stageStates(boot)
	for _, view := range warmViewOrder {
		if states[string(view)] != stageDone {
			t.Fatalf("view %q is %q, want done: %+v", view, states[string(view)], states)
		}
	}
	for view, want := range map[string]int{
		"priority": 1, "learning": 1, "triage summary": 1, "triage page": 1,
		"agenda": 1, "home": 1, "conversations": 7, "commitments": 1,
		"companies list": 1, "companies profiles": 1, "brief": 1, "mail": 1,
	} {
		if got := baseline.count(view); got != want {
			t.Fatalf("%s warmed %d times, want %d", view, got, want)
		}
	}
}

// A guard that clears mid-warm admits the later stages and leaves the earlier
// refusals skipped: the warm never silently retries a stage it skipped.
func TestWarmDoesNotRetryStagesSkippedBeforeTheGuardCleared(t *testing.T) {
	p := newWarmProbe()
	// The ranking and the five early stages ask the paused guard while the
	// ranking's own admission is held; the host then clears, so every stage
	// asked later builds. The earlier refusals stay skipped.
	gate := &warmGate{paused: true, blockOn: viewPriority, hold: make(chan struct{})}
	result := make(chan *bootState, 1)
	go func() {
		result <- runWarm(t, probeServices(p, &warmRecord{}), gate)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for gate.admissionCalls() != 6 {
		if time.Now().After(deadline) {
			close(gate.hold)
			t.Fatalf("%d admission calls before the clear, want 6", gate.admissionCalls())
		}
		time.Sleep(time.Millisecond)
	}
	gate.clear()
	close(gate.hold)
	var boot *bootState
	select {
	case boot = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return after the guard cleared")
	}

	states := stageStates(boot)
	skipped, built := 0, 0
	for name, state := range states {
		if name == string(viewSemanticSearch) {
			continue
		}
		switch state {
		case stageSkipped:
			skipped++
		case stageDone:
			built++
		default:
			t.Fatalf("view %q is %q, want skipped or done", name, state)
		}
	}
	// Six stages asked while paused (the ranking and the five early screens);
	// the five that asked after the clear built.
	if skipped != 6 || built != len(warmViewOrder)-1-6 {
		t.Fatalf("skipped %d, built %d, want 6 and %d: %+v", skipped, built, len(warmViewOrder)-1-6, states)
	}
	for _, view := range []warmView{viewAgenda, viewConversations, viewCommitments, viewCompanies, viewBrief} {
		if got := states[string(view)]; got != stageDone {
			t.Fatalf("view %q is %q after the guard cleared, want done", view, got)
		}
	}
	snap := boot.snapshot()
	if snap.Skipped != skipped || snap.Done != built+1 || snap.Failed != 0 {
		t.Fatalf("skipped %d done %d failed %d, want %d/%d/0",
			snap.Skipped, snap.Done, snap.Failed, skipped, built+1)
	}
	for view, want := range map[string]int{
		"agenda": 1, "commitments": 1, "companies": 1, "companies_profiles": 1, "brief": 1,
		// The conversations stage warms the default page and every state
		// chip, each one a separate page build.
		"conversations": 1 + len(threadintel.States),
	} {
		if got := p.count(view); got != want {
			t.Fatalf("%s built %d times after the guard cleared, want %d", view, got, want)
		}
	}
	for name, state := range states {
		if state == stageSkipped && p.count(name) != 0 {
			t.Fatalf("%s is skipped but built %d times: a skipped stage must not be retried", name, p.count(name))
		}
	}
	// A skipped stage is asked once, not retried inside the same warm.
	if calls := gate.admissionCalls(); calls != derivedStages {
		t.Fatalf("%d admission calls, want %d: a skipped stage must not be re-asked", calls, derivedStages)
	}
}

// The warm never runs more than warmHeavyBuilds store-scanning stages at once.
func TestWarmBoundsConcurrentHeavyBuilds(t *testing.T) {
	p := newWarmProbe()
	p.block = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmDerivedTracked(t.Context(), probeServices(p, &warmRecord{}), newBootState(), &warmGate{})
	}()

	// Wait until the bound is reached, then release: three heavy builds must
	// be in flight at the peak, and the warm must finish all of them.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p.peakBuilds() > 0 && p.liveBuilds() == warmHeavyBuilds {
			break
		}
		if time.Now().After(deadline) {
			close(p.hold)
			<-done
			t.Fatalf("only %d heavy builds ran at once, want %d", p.peakBuilds(), warmHeavyBuilds)
		}
		time.Sleep(time.Millisecond)
	}
	close(p.hold)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return after the hold was released")
	}
	if got := p.peakBuilds(); got > warmHeavyBuilds {
		t.Fatalf("%d heavy builds ran at once, the bound is %d", got, warmHeavyBuilds)
	}
}

// liveBuilds is the number of heavy builds inside their service call now.
func (p *warmProbe) liveBuilds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live
}

// A stage that blocks respects the warm's context and never outlives it: the
// warm returns on its own deadline with the stage recorded failed and no
// goroutine left behind.
func TestWarmReturnsWhenAStageRespectsItsContext(t *testing.T) {
	p := newWarmProbe()
	p.block = true
	// The warm derives its 90 s bound from the caller, so this shorter parent
	// deadline is the one the blocked stage sees.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	boot := newBootState()
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmDerivedTracked(ctx, probeServices(p, &warmRecord{}), boot, &warmGate{})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return after its context expired")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the warm took %s to honour a 150 ms deadline", elapsed)
	}
	boot.ready()
	states := stageStates(boot)
	for name, state := range states {
		if state == stageDone {
			t.Fatalf("view %q reports done although every heavy build blocked until the deadline", name)
		}
	}
	if p.liveBuilds() != 0 {
		t.Fatalf("%d builds are still inside their service call after the warm returned", p.liveBuilds())
	}
}

// warmSlots grants permits in reservation order, not in goroutine order, and a
// stage that gives up never swallows a permit handed to it afterwards.
func TestWarmSlotsGrantInReservationOrder(t *testing.T) {
	s := newWarmSlots(1)
	first := s.reserve()
	select {
	case <-first:
	default:
		t.Fatal("the first reservation did not receive the free permit")
	}

	var granted []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	tickets := make([]<-chan struct{}, 4)
	for i := range tickets {
		tickets[i] = s.reserve()
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !s.wait(t.Context(), tickets[i]) {
				return
			}
			mu.Lock()
			granted = append(granted, i)
			mu.Unlock()
			s.release()
		}(i)
	}
	s.release()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	want := []int{0, 1, 2, 3}
	if len(granted) != len(want) {
		t.Fatalf("granted %v, want %v", granted, want)
	}
	for i, got := range granted {
		if got != want[i] {
			t.Fatalf("granted order %v, want %v", granted, want)
		}
	}
}

// A cancelled waiter leaves the queue without consuming the permit the queue
// then hands to the next stage.
func TestWarmSlotsCancelledWaiterPassesThePermitOn(t *testing.T) {
	s := newWarmSlots(1)
	held := s.reserve()
	<-held

	abandonedCtx, abandon := context.WithCancel(t.Context())
	abandoned := s.reserve()
	next := s.reserve()
	abandon()

	if s.wait(abandonedCtx, abandoned) {
		t.Fatal("a cancelled waiter was granted a permit")
	}
	// The held permit now goes to the waiter that is still queued, never to
	// the abandoned ticket.
	s.release()
	if !s.wait(t.Context(), next) {
		t.Fatal("the permit was not passed to the next waiter")
	}
	s.release()
}

// The semantic prepare is a DB-heavy startup build: it is admitted like a
// derived stage, counted against the same heavy bound, and reported skipped
// when the guard refuses it.
func TestWarmSemanticPrepareSharesThePolicy(t *testing.T) {
	t.Run("admitted and bounded with the views", func(t *testing.T) {
		p := newWarmProbe()
		p.block = true
		boot := newBootState()
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		var prepareCalls int
		var mu sync.Mutex
		prepare := func(ctx context.Context) error {
			mu.Lock()
			prepareCalls++
			mu.Unlock()
			return p.enterView(ctx, "semantic_search")
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			warmStartup(ctx, probeServices(p, &warmRecord{}), prepare, boot, &warmGate{})
		}()
		t.Cleanup(func() {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("the warm did not return after its deadline")
			}
		})

		deadline := time.Now().Add(5 * time.Second)
		for p.liveBuilds() != warmHeavyBuilds {
			if time.Now().After(deadline) {
				t.Fatalf("%d heavy builds ran at once, want %d", p.liveBuilds(), warmHeavyBuilds)
			}
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		calls := prepareCalls
		mu.Unlock()
		if calls != 1 {
			t.Fatalf("the semantic prepare ran %d times, want 1", calls)
		}
		if got := p.peakBuilds(); got > warmHeavyBuilds {
			t.Fatalf("%d heavy builds ran at once, the bound is %d", got, warmHeavyBuilds)
		}
	})

	t.Run("skipped when the guard refuses", func(t *testing.T) {
		p := newWarmProbe()
		boot := newBootState()
		prepared := false
		prepare := func(context.Context) error { prepared = true; return nil }
		warmStartup(t.Context(), probeServices(p, &warmRecord{}), prepare, boot, &warmGate{paused: true})
		boot.ready()
		if prepared {
			t.Fatal("the semantic prepare ran under a paused guard")
		}
		states := stageStates(boot)
		if got := states[string(viewSemanticSearch)]; got != stageSkipped {
			t.Fatalf("semantic_search is %q, want skipped", got)
		}
	})
}
