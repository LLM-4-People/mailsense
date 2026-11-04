package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// warmRecord is what the warm asked each read model for.
type warmRecord struct {
	mu          sync.Mutex
	summaries   int
	pages       []triage.PageQuery
	horizons    []time.Duration
	priorities  int
	mailQueries []store.MessageListQuery
	gated       error
}

func (r *warmRecord) record(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

type warmTriage struct{ r *warmRecord }

func (t warmTriage) Summary(context.Context) (*triage.Summary, error) {
	t.r.record(func() { t.r.summaries++ })
	if t.r.gated != nil {
		return nil, t.r.gated
	}
	return &triage.Summary{}, nil
}

func (t warmTriage) Page(_ context.Context, q triage.PageQuery) (*triage.Page, error) {
	t.r.record(func() { t.r.pages = append(t.r.pages, q) })
	if t.r.gated != nil {
		return nil, t.r.gated
	}
	return &triage.Page{}, nil
}

func (warmTriage) Result(context.Context, int64) (triage.Result, error) { return triage.Result{}, nil }
func (warmTriage) Questions(context.Context) (api.QuestionsView, error) {
	return api.QuestionsView{}, nil
}

type warmPriority struct{ r *warmRecord }

func (p warmPriority) Rank(context.Context, priority.Filter, int) ([]priority.Item, int, bool, error) {
	p.r.record(func() { p.r.priorities++ })
	return nil, 0, false, nil
}

func (p warmPriority) RankPage(ctx context.Context, filter priority.Filter, limit, offset int) (priority.Page, error) {
	items, total, preview, err := p.Rank(ctx, filter, limit)
	return priority.Page{Items: items, Total: total, Preview: preview,
		Counts: map[string]int{string(filter): total}}, err
}

type warmAgenda struct{ r *warmRecord }

func (a warmAgenda) Build(_ context.Context, horizon time.Duration) (agenda.Result, error) {
	a.r.record(func() { a.r.horizons = append(a.r.horizons, horizon) })
	return agenda.Result{}, nil
}
func (warmAgenda) DefaultHorizon() time.Duration { return warmTestHorizon }

// warmTestHorizon is the fake agenda default: deliberately not one of the
// Hub's 8 h/24 h chip windows, so a warm that ignores the service default or
// hard-codes a chip window fails the trace.
const warmTestHorizon = 7 * time.Hour

type warmConversations struct{}

func (warmConversations) Page(context.Context, conversations.Query) (conversations.Page, error) {
	return conversations.Page{}, nil
}

type warmCommitments struct{}

func (warmCommitments) List(context.Context, commitments.Filter) (commitments.Result, error) {
	return commitments.Result{}, nil
}

type warmCompanies struct{}

func (warmCompanies) List(context.Context, worldsvc.ListQuery) (worldsvc.ListResult, error) {
	return worldsvc.ListResult{}, nil
}
func (warmCompanies) Profile(context.Context, string, int, int) (worldsvc.ProfileView, error) {
	return worldsvc.ProfileView{}, nil
}
func (warmCompanies) Profiles(context.Context, []string, int, int) (map[string]worldsvc.ProfileView, error) {
	return map[string]worldsvc.ProfileView{}, nil
}

type warmLearning struct{}

func (warmLearning) Allow(context.Context) error { return nil }
func (warmLearning) GateState(context.Context) learning.GateState {
	return learning.GateState{Open: true}
}
func (warmLearning) Status(context.Context) (learning.Status, learning.GateState, error) {
	return learning.Status{}, learning.GateState{Open: true}, nil
}
func (warmLearning) ConfirmZone(context.Context, string) error { return nil }

type warmHub struct{}

func (warmHub) Hub(context.Context) (hub.Hub, error) { return hub.Hub{}, nil }
func (warmHub) Answers(context.Context, hub.AnswerQuery) (hub.AnswerPage, error) {
	return hub.AnswerPage{}, nil
}
func (warmHub) LabelSummary(context.Context) (hub.LabelSummary, error) {
	return hub.LabelSummary{}, nil
}

type warmBrief struct{}

func (warmBrief) Brief(context.Context, string) (brief.Result, error) { return brief.Result{}, nil }

type warmMessages struct{ r *warmRecord }

func (m warmMessages) List(_ context.Context, q store.MessageListQuery) (api.MessageListView, error) {
	m.r.record(func() { m.r.mailQueries = append(m.r.mailQueries, q) })
	return api.MessageListView{}, nil
}
func (warmMessages) Get(context.Context, int64) (api.MessageView, error) {
	return api.MessageView{}, nil
}
func (warmMessages) Attachment(context.Context, int64, int) (api.AttachmentAsset, error) {
	return api.AttachmentAsset{}, store.ErrAssetUnavailable
}
func (warmMessages) StartBackfill(context.Context, string) (jobs.Info, error) {
	return jobs.Info{}, nil
}

// services is a warm fixture: the derived read models warmDerived touches
// are fakes, everything else (Ask, Search, providers, labels) is nil, so a
// warm that reached a paid or sync surface would panic.
func warmServices(r *warmRecord) api.Services {
	return api.Services{
		Triage:        warmTriage{r},
		Priority:      warmPriority{r},
		Agenda:        warmAgenda{r},
		Conversations: warmConversations{},
		Commitments:   warmCommitments{},
		Companies:     warmCompanies{},
		Learning:      warmLearning{},
		Hub:           warmHub{},
		Brief:         warmBrief{},
		Messages:      warmMessages{r},
	}
}

// The warm builds the triage summary and the first Recent page, and only the
// configured default agenda horizon, all without touching a paid surface.
// The Hub's 8 h and 24 h chips build their window on click.
func TestWarmDerivedWarmsTriageAndTheDefaultAgendaHorizon(t *testing.T) {
	r := &warmRecord{}
	warmDerived(context.Background(), warmServices(r))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.priorities != 1 {
		t.Fatalf("priority warmed %d times, want 1", r.priorities)
	}
	if r.summaries != 1 {
		t.Fatalf("triage summary warmed %d times, want 1", r.summaries)
	}
	if len(r.pages) != 1 {
		t.Fatalf("triage page warmed %d times, want 1", len(r.pages))
	}
	if q := r.pages[0]; q.Limit != triage.TriagePageDefault || q.Offset != 0 {
		t.Fatalf("triage page warmed with %+v, want the default first page", q)
	}
	if want := []time.Duration{warmTestHorizon}; !slices.Equal(r.horizons, want) {
		t.Fatalf("agenda warmed horizons %v, want only the default %v", r.horizons, want)
	}
}

// warmTrace records the order and coverage of one warm and can hold the
// priority ranking at its start. It lets the startup-order tests prove which
// views overlap without timing a real build.
type warmTrace struct {
	mu       sync.Mutex
	starts   map[string]int
	ends     map[string]int
	order    []string
	events   []string
	horizons []time.Duration
	pages    []conversations.Query
	triage   []triage.PageQuery
	profiles []string

	// blockPriority holds Rank inside the priority build and blockMail holds
	// List inside the Mail first-page build; releasePriority/releaseMail let
	// them return. The entered channels fire once, so a test can wait without
	// polling.
	blockPriority   bool
	blockMail       bool
	priorityEntered chan struct{}
	learningEntered chan struct{}
	mailEntered     chan struct{}
	release         chan struct{}
	releaseOnce     sync.Once
	mailRelease     chan struct{}
	mailReleaseOnce sync.Once

	mailQueries []store.MessageListQuery
	learningErr error
	profilesErr error
}

func newWarmTrace() *warmTrace {
	return &warmTrace{
		starts:          map[string]int{},
		ends:            map[string]int{},
		priorityEntered: make(chan struct{}, 1),
		learningEntered: make(chan struct{}, 1),
		mailEntered:     make(chan struct{}, 1),
		release:         make(chan struct{}),
		mailRelease:     make(chan struct{}),
	}
}

func (t *warmTrace) start(view string) {
	t.mu.Lock()
	t.starts[view]++
	t.order = append(t.order, view)
	t.events = append(t.events, "start:"+view)
	t.mu.Unlock()
	switch view {
	case "priority":
		select {
		case t.priorityEntered <- struct{}{}:
		default:
		}
	case "learning":
		select {
		case t.learningEntered <- struct{}{}:
		default:
		}
	case "mail":
		select {
		case t.mailEntered <- struct{}{}:
		default:
		}
	}
}

func (t *warmTrace) end(view string) {
	t.mu.Lock()
	t.ends[view]++
	t.events = append(t.events, "end:"+view)
	t.mu.Unlock()
}

func (t *warmTrace) count(view string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts[view]
}

func (t *warmTrace) startOrder() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.order...)
}

func (t *warmTrace) eventsSnapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

// waitStart waits until view has started at least once. It only observes the
// trace; the caller decides whether the start happened before a gate opened.
func (t *warmTrace) waitStart(tb testing.TB, view string) {
	tb.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for t.count(view) == 0 {
		if time.Now().After(deadline) {
			tb.Fatalf("%s did not start within 2 s: %v", view, t.startOrder())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// eventIndex returns the position of one start/end event in the trace.
func eventIndex(events []string, event string) int {
	for i, got := range events {
		if got == event {
			return i
		}
	}
	return -1
}

func (t *warmTrace) releasePriority() {
	t.releaseOnce.Do(func() { close(t.release) })
}

func (t *warmTrace) releaseMail() {
	t.mailReleaseOnce.Do(func() { close(t.mailRelease) })
}

// waitFor runs the warm in the background and guarantees both gates are
// released and the goroutine joined when the test ends, even on failure.
func (t *warmTrace) waitFor(tb testing.TB) <-chan struct{} {
	tb.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmDerived(context.Background(), t.services())
	}()
	tb.Cleanup(func() {
		t.releasePriority()
		t.releaseMail()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			tb.Error("the warm did not return after the priority/mail release")
		}
	})
	return done
}

type traceTriage struct{ t *warmTrace }

func (x traceTriage) Summary(context.Context) (*triage.Summary, error) {
	x.t.start("triage summary")
	defer x.t.end("triage summary")
	return &triage.Summary{}, nil
}

func (x traceTriage) Page(_ context.Context, q triage.PageQuery) (*triage.Page, error) {
	x.t.start("triage page")
	x.t.mu.Lock()
	x.t.triage = append(x.t.triage, q)
	x.t.mu.Unlock()
	x.t.end("triage page")
	return &triage.Page{}, nil
}

func (traceTriage) Result(context.Context, int64) (triage.Result, error) { return triage.Result{}, nil }
func (traceTriage) Questions(context.Context) (api.QuestionsView, error) {
	return api.QuestionsView{}, nil
}

type tracePriority struct{ t *warmTrace }

func (x tracePriority) Rank(context.Context, priority.Filter, int) ([]priority.Item, int, bool, error) {
	x.t.start("priority")
	if x.t.blockPriority {
		<-x.t.release
	}
	x.t.end("priority")
	return nil, 0, false, nil
}

func (x tracePriority) RankPage(ctx context.Context, filter priority.Filter, limit, offset int) (priority.Page, error) {
	items, total, preview, err := x.Rank(ctx, filter, limit)
	return priority.Page{Items: items, Total: total, Preview: preview,
		Counts: map[string]int{string(filter): total}}, err
}

type traceAgenda struct{ t *warmTrace }

func (x traceAgenda) Build(_ context.Context, horizon time.Duration) (agenda.Result, error) {
	x.t.start("agenda")
	x.t.mu.Lock()
	x.t.horizons = append(x.t.horizons, horizon)
	x.t.mu.Unlock()
	x.t.end("agenda")
	return agenda.Result{}, nil
}

func (traceAgenda) DefaultHorizon() time.Duration { return warmTestHorizon }

type traceConversations struct{ t *warmTrace }

func (x traceConversations) Page(_ context.Context, q conversations.Query) (conversations.Page, error) {
	x.t.start("conversations")
	x.t.mu.Lock()
	x.t.pages = append(x.t.pages, q)
	x.t.mu.Unlock()
	x.t.end("conversations")
	return conversations.Page{}, nil
}

type traceCommitments struct{ t *warmTrace }

func (x traceCommitments) List(context.Context, commitments.Filter) (commitments.Result, error) {
	x.t.start("commitments")
	defer x.t.end("commitments")
	return commitments.Result{}, nil
}

type traceCompanies struct{ t *warmTrace }

func (x traceCompanies) List(context.Context, worldsvc.ListQuery) (worldsvc.ListResult, error) {
	x.t.start("companies list")
	defer x.t.end("companies list")
	rows := make([]worldsvc.CompanyRow, 7)
	for i := range rows {
		rows[i].Key = fmt.Sprintf("org-%d", i)
	}
	return worldsvc.ListResult{Companies: rows}, nil
}

func (x traceCompanies) Profile(context.Context, string, int, int) (worldsvc.ProfileView, error) {
	return worldsvc.ProfileView{}, nil
}

func (x traceCompanies) Profiles(_ context.Context, keys []string, _, _ int) (map[string]worldsvc.ProfileView, error) {
	x.t.start("companies profiles")
	defer x.t.end("companies profiles")
	x.t.mu.Lock()
	x.t.profiles = append([]string(nil), keys...)
	err := x.t.profilesErr
	x.t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	views := make(map[string]worldsvc.ProfileView, len(keys))
	for _, key := range keys {
		views[key] = worldsvc.ProfileView{Key: key}
	}
	return views, nil
}

type traceLearning struct{ t *warmTrace }

func (traceLearning) Allow(context.Context) error { return nil }
func (traceLearning) GateState(context.Context) learning.GateState {
	return learning.GateState{Open: true}
}
func (x traceLearning) Status(context.Context) (learning.Status, learning.GateState, error) {
	x.t.start("learning")
	defer x.t.end("learning")
	if x.t.learningErr != nil {
		return learning.Status{}, learning.GateState{}, x.t.learningErr
	}
	return learning.Status{}, learning.GateState{Open: true}, nil
}
func (traceLearning) ConfirmZone(context.Context, string) error { return nil }

type traceHub struct{ t *warmTrace }

func (x traceHub) Hub(context.Context) (hub.Hub, error) {
	x.t.start("home")
	defer x.t.end("home")
	return hub.Hub{}, nil
}
func (traceHub) Answers(context.Context, hub.AnswerQuery) (hub.AnswerPage, error) {
	return hub.AnswerPage{}, nil
}
func (traceHub) LabelSummary(context.Context) (hub.LabelSummary, error) {
	return hub.LabelSummary{}, nil
}

type traceBrief struct{ t *warmTrace }

func (x traceBrief) Brief(context.Context, string) (brief.Result, error) {
	x.t.start("brief")
	defer x.t.end("brief")
	return brief.Result{}, nil
}

type traceMessages struct{ t *warmTrace }

func (x traceMessages) List(_ context.Context, q store.MessageListQuery) (api.MessageListView, error) {
	x.t.start("mail")
	x.t.mu.Lock()
	x.t.mailQueries = append(x.t.mailQueries, q)
	x.t.mu.Unlock()
	if x.t.blockMail {
		<-x.t.mailRelease
	}
	x.t.end("mail")
	return api.MessageListView{}, nil
}

func (traceMessages) Get(context.Context, int64) (api.MessageView, error) {
	return api.MessageView{}, nil
}

func (traceMessages) Attachment(context.Context, int64, int) (api.AttachmentAsset, error) {
	return api.AttachmentAsset{}, store.ErrAssetUnavailable
}

func (traceMessages) StartBackfill(context.Context, string) (jobs.Info, error) {
	return jobs.Info{}, nil
}

func (t *warmTrace) services() api.Services {
	return api.Services{
		Triage:        traceTriage{t},
		Priority:      tracePriority{t},
		Agenda:        traceAgenda{t},
		Conversations: traceConversations{t},
		Commitments:   traceCommitments{t},
		Companies:     traceCompanies{t},
		Learning:      traceLearning{t},
		Hub:           traceHub{t},
		Brief:         traceBrief{t},
		Messages:      traceMessages{t},
	}
}

// The first learning status build is independent of the priority ranking,
// so it starts beside the priority warm instead of waiting for it.
func TestWarmDerivedStartsLearningWithPriority(t *testing.T) {
	tr := newWarmTrace()
	tr.blockPriority = true
	tr.waitFor(t)

	select {
	case <-tr.priorityEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("priority never started")
	}
	select {
	case <-tr.learningEntered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("learning did not start within 500 ms while priority was still building")
	}
	if got := tr.count("priority"); got != 1 {
		t.Fatalf("priority started %d times, want 1", got)
	}
	tr.mu.Lock()
	ended := tr.ends["priority"]
	tr.mu.Unlock()
	if ended != 0 {
		t.Fatal("the priority block did not hold")
	}
}

// Agenda, conversations, commitments and companies join the priority
// service's in-flight ranking build for its cached thread truth, and do their
// own store work around it. With the priority build held, each must still
// start; the Brief must not start until the whole group has finished.
func TestWarmDerivedRunsTheFourAfterPriority(t *testing.T) {
	tr := newWarmTrace()
	tr.blockPriority = true
	done := tr.waitFor(t)

	select {
	case <-tr.priorityEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("priority never started")
	}
	// The live host measured the concurrent group as slower (15.7 s vs
	// 13.6 s): priority owns the store first, and the four dependent views
	// must not start until its build returns. While the rank is held, none
	// of them may have started.
	for _, view := range []string{"agenda", "conversations", "commitments", "companies list"} {
		if got := tr.count(view); got != 0 {
			t.Fatalf("%s started while the priority block was held", view)
		}
	}
	if got := tr.count("brief"); got != 0 {
		t.Fatal("Brief started while priority was blocked")
	}

	tr.releasePriority()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return")
	}

	tr.mu.Lock()
	horizons := append([]time.Duration(nil), tr.horizons...)
	pages := append([]conversations.Query(nil), tr.pages...)
	tr.mu.Unlock()
	if want := []time.Duration{warmTestHorizon}; !slices.Equal(horizons, want) {
		t.Fatalf("agenda warmed horizons %v, want only the default %v", horizons, want)
	}
	if len(pages) != 1+len(threadintel.States) {
		t.Fatalf("conversations warmed %d pages, want %d", len(pages), 1+len(threadintel.States))
	}
	for _, view := range []string{"priority", "agenda", "commitments", "companies list", "brief"} {
		if got := tr.count(view); got != 1 {
			t.Fatalf("%s started %d times, want 1", view, got)
		}
	}

	// Priority ends before the four start; Brief starts after all five and
	// every input had finished.
	events := tr.eventsSnapshot()
	priorityEnd := eventIndex(events, "end:priority")
	if priorityEnd < 0 {
		t.Fatalf("priority never ended: %v", events)
	}
	for _, view := range []string{"agenda", "conversations", "commitments", "companies list"} {
		start := eventIndex(events, "start:"+view)
		if start < 0 {
			t.Fatalf("%s never started: %v", view, events)
		}
		if start <= priorityEnd {
			t.Fatalf("%s started before priority ended: %v", view, events)
		}
	}
	brief := eventIndex(events, "start:brief")
	if brief < 0 {
		t.Fatalf("Brief never started: %v", events)
	}
	for _, view := range []string{"priority", "agenda", "conversations", "commitments", "companies list", "companies profiles"} {
		starts, ends, last := 0, 0, -1
		for i, event := range events {
			switch event {
			case "start:" + view:
				starts++
				last = i
			case "end:" + view:
				ends++
				last = i
			}
		}
		if starts == 0 || starts != ends {
			t.Fatalf("%s started %d times and ended %d times, want at least one of each: %v", view, starts, ends, events)
		}
		if last >= brief {
			t.Fatalf("%s was still running when Brief started: %v", view, events)
		}
	}
}

// Every view the UI can open is warmed before the API port: the rank, both
// triage pages, the default agenda horizon, home, every conversation chip,
// the commitments, the company list plus the three prefetched profiles, and
// the Brief.
func TestWarmDerivedCoversEveryView(t *testing.T) {
	tr := newWarmTrace()
	done := tr.waitFor(t)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return")
	}

	for view, want := range map[string]int{
		"priority": 1, "learning": 1, "triage summary": 1, "triage page": 1,
		"agenda": 1, "home": 1, "conversations": 7, "commitments": 1,
		"companies list": 1, "companies profiles": 1, "brief": 1, "mail": 1,
	} {
		if got := tr.count(view); got != want {
			t.Fatalf("%s warmed %d times, want %d", view, got, want)
		}
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if want := []time.Duration{warmTestHorizon}; !slices.Equal(tr.horizons, want) {
		t.Fatalf("agenda warmed horizons %v, want only the default %v", tr.horizons, want)
	}
	if len(tr.pages) != 1+len(threadintel.States) {
		t.Fatalf("conversations warmed %d pages, want %d", len(tr.pages), 1+len(threadintel.States))
	}
	if q := tr.pages[0]; q.State != "" || q.Limit != conversations.DefaultLimit {
		t.Fatalf("conversations warmed %+v first, want the default page", q)
	}
	if len(tr.triage) != 1 || tr.triage[0].Limit != triage.TriagePageDefault {
		t.Fatalf("triage warmed %+v, want one default page", tr.triage)
	}
	if len(tr.profiles) != warmProfiles {
		t.Fatalf("companies warmed %d profiles, want %d", len(tr.profiles), warmProfiles)
	}
	for i, key := range tr.profiles {
		if want := fmt.Sprintf("org-%d", i); key != want {
			t.Fatalf("profile %d warmed %q, want %q", i, key, want)
		}
	}
}

// TestWarmBudgetsMatchTheUI pins the two startup budgets the screens assume:
// exactly the three prefetched company profiles, and only the configured
// agenda default, because the Hub's 8 h/24 h chips build on click.
func TestWarmBudgetsMatchTheUI(t *testing.T) {
	if warmProfiles != 3 {
		t.Fatalf("warmProfiles = %d, want 3: the Companies screen prefetches three visible profiles", warmProfiles)
	}
	src, err := os.ReadFile("apiwire.go")
	if err != nil {
		t.Fatalf("read apiwire.go: %v", err)
	}
	if strings.Contains(string(src), "warmAgendaHorizons") {
		t.Fatal("apiwire.go still declares or appends warmAgendaHorizons; the agenda horizon chips build on click")
	}
}

// A failed first learning status is a warm warning: the gate reports it on
// /api/learning, and every other view still warms and the warm returns.
func TestWarmDerivedContinuesWhenLearningStatusFails(t *testing.T) {
	tr := newWarmTrace()
	tr.learningErr = errors.New("learning: no latch")
	done := tr.waitFor(t)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return after the learning status failed")
	}

	for view, want := range map[string]int{
		"priority": 1, "learning": 1, "triage summary": 1, "triage page": 1,
		"agenda": 1, "home": 1, "conversations": 7, "commitments": 1,
		"companies list": 1, "companies profiles": 1, "brief": 1, "mail": 1,
	} {
		if got := tr.count(view); got != want {
			t.Fatalf("%s warmed %d times after the learning failure, want %d", view, got, want)
		}
	}
}

// A closed learning gate leaves the triage warm incomplete and never stops
// startup: the warm returns after trying both views.
func TestWarmDerivedContinuesBehindAClosedGate(t *testing.T) {
	r := &warmRecord{gated: errors.New("learning: the gate is closed")}
	done := make(chan struct{})
	go func() {
		defer close(done)
		warmDerived(context.Background(), warmServices(r))
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the warm did not return behind a closed gate")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summaries != 1 || len(r.pages) != 1 {
		t.Fatalf("a gated warm tried summary %d and page %d times, want one each", r.summaries, len(r.pages))
	}
}

// The Mail first page is warmed once, with the exact query the UI's first
// click asks for: the default limit, whole-mailbox counts and no filter.
func TestWarmDerivedWarmsTheMailFirstPage(t *testing.T) {
	tr := newWarmTrace()
	done := tr.waitFor(t)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warm did not return")
	}
	tr.mu.Lock()
	queries := append([]store.MessageListQuery(nil), tr.mailQueries...)
	tr.mu.Unlock()
	if len(queries) != 1 {
		t.Fatalf("mail warmed %d times, want 1: %+v", len(queries), queries)
	}
	q := queries[0]
	if q.Limit != store.MessageListDefaultLimit || !q.Counts || q.Role != "" || q.Store != "" || !q.Before.IsZero() {
		t.Fatalf("mail warmed with %+v, want limit=%d counts=true unfiltered",
			q, store.MessageListDefaultLimit)
	}
}

// Mail and priority are independent stages on the startup critical path:
// neither may wait for the other. A blocked Mail warm must not keep
// priority from entering, and a blocked priority must not keep Mail from
// entering. The first click joins the same single-flight Mail build.
func TestWarmDerivedStartsMailBesidePriority(t *testing.T) {
	t.Run("priority starts while mail is blocked", func(t *testing.T) {
		tr := newWarmTrace()
		tr.blockMail = true
		tr.waitFor(t)

		select {
		case <-tr.mailEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("mail never started")
		}
		select {
		case <-tr.priorityEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("priority never started while mail was blocked")
		}
		tr.mu.Lock()
		ended := tr.ends["mail"]
		tr.mu.Unlock()
		if ended != 0 {
			t.Fatal("the mail block did not hold")
		}
		if got := tr.count("priority"); got != 1 {
			t.Fatalf("priority started %d times, want 1", got)
		}
	})

	t.Run("mail starts while priority is blocked", func(t *testing.T) {
		tr := newWarmTrace()
		tr.blockPriority = true
		tr.waitFor(t)

		select {
		case <-tr.priorityEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("priority never started")
		}
		select {
		case <-tr.mailEntered:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("mail did not start within 500 ms while priority was still building")
		}
		tr.mu.Lock()
		ended := tr.ends["priority"]
		tr.mu.Unlock()
		if ended != 0 {
			t.Fatal("the priority block did not hold")
		}
		if got := tr.count("mail"); got != 1 {
			t.Fatalf("mail started %d times, want 1", got)
		}
	})
}

// The startup warm and the first Mail request share one wire-level service:
// the click joins the warmed first page instead of starting a second build.
func TestWireSharesTheOneMailFirstPageService(t *testing.T) {
	st, cfg, _ := firstRunWorld(t, nil)
	w := &wire{app: &app{st: st, cfg: cfg}}
	first := w.messagesService()
	if again := w.messagesService(); again != first {
		t.Fatal("messagesService built a second first-page cache")
	}
	if got := (messagesAPI{w: w}).w.messagesService(); got != first {
		t.Fatal("the API and the warm do not share the first-page cache")
	}
}
