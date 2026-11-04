package agenda

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

type fakeWorld struct {
	events    []calendar.Event
	deadlines []Deadline
	summaries map[string]ThreadSummary
	owner     map[string]bool
	err       error
	mailGen   uint64
	routeGen  uint64
	calls     int
	started   chan struct{}
	release   chan struct{}
}

func (f *fakeWorld) MailGeneration() uint64  { return f.mailGen }
func (f *fakeWorld) RouteGeneration() uint64 { return f.routeGen }

func (f *fakeWorld) CalendarEvents(context.Context, time.Time, time.Time) ([]calendar.Event, error) {
	return f.events, f.err
}

func (f *fakeWorld) DueDeadlines(context.Context, time.Time, time.Time) ([]Deadline, error) {
	return f.deadlines, f.err
}

func (f *fakeWorld) ThreadSummaries(_ context.Context, ids []string) (map[string]ThreadSummary, error) {
	out := map[string]ThreadSummary{}
	for _, id := range ids {
		if s, ok := f.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, f.err
}

func (f *fakeWorld) OwnerSet(context.Context) (map[string]bool, error) {
	f.calls++
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return f.owner, f.err
}

type fakeThreads struct {
	states map[string]threadintel.Assessment
	err    error
}

func (f *fakeThreads) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	return f.states, f.err
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time                             { return c.now }
func (c *fakeClock) Sleep(context.Context, time.Duration) error { return nil }
func (c *fakeClock) Advance(d time.Duration)                    { c.now = c.now.Add(d) }

func serviceFixture(t *testing.T) (*Service, *fakeWorld, *fakeThreads, *fakeClock) {
	t.Helper()
	now := ts("2026-11-03T09:00:00Z")
	w := &fakeWorld{
		owner: map[string]bool{"owner@example.test": true},
		events: []calendar.Event{meeting("e1", "Kickoff", "2026-11-03T10:30:00Z", "2026-11-03T11:00:00Z",
			"client@example.test")},
		deadlines: []Deadline{{MessageID: 9, Subject: "Send invoice", Due: now.Add(2 * time.Hour)}},
		summaries: map[string]ThreadSummary{"C1": {ConversationID: "C1", Subject: "Plan review",
			MessageID: 7, LastAt: "2026-11-01T09:00:00Z", Participants: []string{"client@example.test"}}},
	}
	th := &fakeThreads{states: map[string]threadintel.Assessment{
		"C1": {State: threadintel.AwaitingOwner, ReplyToMessageID: 7, ActorName: "Client", OwnerActorName: "Client"},
		"C2": {State: threadintel.AnsweredByOther, ReplyToMessageID: 8, ActorName: "Colleague", OwnerActorName: "Colleague"},
	}}
	clk := &fakeClock{now: now}
	return New(Deps{Store: w, Threads: th, Clock: clk, DefaultHorizon: 3 * time.Hour}), w, th, clk
}

func TestServiceJoinsObligationsEventsAndDeadlines(t *testing.T) {
	svc, _, _, _ := serviceFixture(t)
	res, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Obligations != 1 || res.Events != 1 || res.Deadlines != 1 {
		t.Fatalf("counts %+v", res)
	}
	reply := res.Items[0]
	if reply.Kind != KindReply || reply.ConversationID != "C1" || reply.LinkedEventID != "e1" ||
		!reply.At.Equal(ts("2026-11-03T10:30:00Z")) {
		t.Fatalf("reply %+v", reply)
	}
	if reply.MessageID != 7 || reply.Detail != "Client" || reply.LinkedEvent != "Kickoff" {
		t.Fatalf("reply detail %+v", reply)
	}
}

func TestServiceCachesHorizon(t *testing.T) {
	svc, world, _, _ := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if world.calls != 1 {
		t.Fatalf("first build calls %d", world.calls)
	}
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if world.calls != 1 {
		t.Fatalf("a fresh horizon rebuilt: %d calls", world.calls)
	}
}

func TestServiceServesStaleWhileRefreshRuns(t *testing.T) {
	svc, world, _, clk := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	world.summaries["C1"] = ThreadSummary{ConversationID: "C1", Subject: "New subject",
		MessageID: 7, LastAt: "2026-11-01T09:00:00Z", Participants: []string{"client@example.test"}}
	clk.Advance(time.Hour) // past the horizon TTL

	stale, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if title := agendaTitle(stale, "C1"); title != "Plan review" {
		t.Fatalf("an expired horizon blocked instead of serving stale: %q", title)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fresh, err := svc.Build(t.Context(), 3*time.Hour)
		if err == nil && agendaTitle(fresh, "C1") == "New subject" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the agenda never refreshed after its TTL")
}

// Eight concurrent cold builds of one horizon share one read of the world
// model.
func TestServiceColdBuildIsSingleFlight(t *testing.T) {
	svc, world, _, _ := serviceFixture(t)
	world.started = make(chan struct{}, 8)
	world.release = make(chan struct{})

	const n = 8
	entered := make(chan struct{}, n)
	results := make([]Result, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered <- struct{}{}
			results[i], errs[i] = svc.Build(context.Background(), 3*time.Hour)
		}()
	}
	for range n {
		<-entered
	}
	<-world.started
	for range 100 {
		runtime.Gosched()
	}
	close(world.release)
	wg.Wait()

	if world.calls != 1 {
		t.Fatalf("a cold horizon read the world %d times, want 1", world.calls)
	}
	for i := range n {
		if errs[i] != nil || results[i].Obligations != 1 {
			t.Fatalf("caller %d: %+v %v", i, results[i], errs[i])
		}
	}
}

func agendaTitle(result Result, conversationID string) string {
	for _, item := range result.Items {
		if item.ConversationID == conversationID {
			return item.Title
		}
	}
	return ""
}

func TestServiceCollapsesAColleagueAnswer(t *testing.T) {
	svc, w, _, _ := serviceFixture(t)
	w.summaries["C2"] = ThreadSummary{ConversationID: "C2", Subject: "Answered", MessageID: 8}
	res, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.ConversationID == "C2" {
			t.Fatalf("an answered thread must not be an obligation: %+v", it)
		}
	}
}

func TestServiceFailsClosedOnThreadReadError(t *testing.T) {
	svc, _, th, _ := serviceFixture(t)
	th.err = errors.New("thread read failed")
	if _, err := svc.Build(t.Context(), 3*time.Hour); err == nil {
		t.Fatal("a thread read failure must fail the agenda, not hide obligations")
	}
}

func TestServiceClampsTheHorizon(t *testing.T) {
	svc, _, _, _ := serviceFixture(t)
	res, err := svc.Build(t.Context(), 1000*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.HorizonHours != MaxHorizon.Hours() {
		t.Fatalf("horizon %v", res.HorizonHours)
	}
	res, err = svc.Build(t.Context(), 0)
	if err != nil || res.HorizonHours != 3 {
		t.Fatalf("default horizon %v %v", res.HorizonHours, err)
	}
}

// The obligation items carry the priority thread truth, which is re-derived
// when route answers commit. The generations are part of the key, so a route
// move builds a new horizon immediately instead of serving the old one.
func TestServiceRefreshesWhenTheRouteGenerationMoves(t *testing.T) {
	svc, world, threads, _ := serviceFixture(t)
	if _, err := svc.Build(t.Context(), 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	threads.states = map[string]threadintel.Assessment{}
	world.routeGen++
	fresh, err := svc.Build(t.Context(), 3*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if title := agendaTitle(fresh, "C1"); title != "" {
		t.Fatalf("a route generation move served the old obligation: %q", title)
	}
	if world.calls != 2 {
		t.Fatalf("a route generation move built %d times, want 2", world.calls)
	}
}
