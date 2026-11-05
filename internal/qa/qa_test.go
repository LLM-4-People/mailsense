package qa

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/generate"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

type fakeGate struct {
	mu      sync.Mutex
	err     error
	preview bool
	calls   int
}

func (g *fakeGate) Allow(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.err
}

func (g *fakeGate) PreviewAnswers(context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.preview
}

// allowOnlyGate implements the required Allow but not learning.Previewer,
// the optional preview dependency.
type allowOnlyGate struct{}

func (allowOnlyGate) Allow(context.Context) error { return nil }

type fakePriority struct {
	byFilter     map[priority.Filter][]priority.Item
	totals       map[priority.Filter]int
	top          []priority.Item
	topics       priority.TopicCensus
	topicsErr    error
	threadStates map[string]threadintel.Assessment
	threadErr    error
	preview      bool
	err          error
	calls        int
	topCalls     int
	topicsCalls  int
	filters      []priority.Filter
}

func (p *fakePriority) Topics(context.Context) (priority.TopicCensus, error) {
	p.topicsCalls++
	if p.topicsErr != nil {
		return priority.TopicCensus{}, p.topicsErr
	}
	return p.topics, nil
}

func (p *fakePriority) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	if p.threadErr != nil {
		return nil, p.threadErr
	}
	return p.threadStates, nil
}

func (p *fakePriority) Rank(_ context.Context, filter priority.Filter, limit int) ([]priority.Item, int, bool, error) {
	p.calls++
	p.filters = append(p.filters, filter)
	if p.err != nil {
		return nil, 0, false, p.err
	}
	items := p.byFilter[filter]
	if items == nil {
		items = []priority.Item{}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	total, ok := p.totals[filter]
	if !ok {
		total = len(p.byFilter[filter])
	}
	return items, total, p.preview, nil
}

func (p *fakePriority) Top(_ context.Context, n int) ([]priority.Item, error) {
	p.topCalls++
	if p.err != nil {
		return nil, p.err
	}
	if len(p.top) > n {
		return p.top[:n], nil
	}
	return p.top, nil
}

type fakeSearch struct {
	result      search.Result
	results     map[string]search.Result
	seq         []search.Result
	err         error
	respectCtx  bool
	calls       int
	query       search.Query
	deadline    time.Time
	hasDeadline bool
}

func (s *fakeSearch) Search(ctx context.Context, q search.Query) (search.Result, error) {
	s.calls++
	s.query = q
	s.deadline, s.hasDeadline = ctx.Deadline()
	if s.respectCtx {
		if err := ctx.Err(); err != nil {
			return search.Result{}, err
		}
	}
	if s.err != nil {
		return search.Result{}, s.err
	}
	if len(s.seq) > 0 {
		result := s.seq[0]
		s.seq = s.seq[1:]
		return result, nil
	}
	if result, ok := s.results[q.Text]; ok {
		return result, nil
	}
	return s.result, nil
}

type fakeGenerator struct {
	name, model string
	content     string
	err         error
	errOnce     error
	calls       int
	system      string
	user        string
	prose       bool
}

func (g *fakeGenerator) Name() string  { return g.name }
func (g *fakeGenerator) Model() string { return g.model }
func (g *fakeGenerator) Generate(_ context.Context, system, user string, opts ...engine.GenerateOptions) (engine.Generation, error) {
	g.calls++
	g.system, g.user = system, user
	for _, opt := range opts {
		g.prose = g.prose || opt.Prose
	}
	if g.errOnce != nil {
		err := g.errOnce
		g.errOnce = nil
		return engine.Generation{}, err
	}
	if g.err != nil {
		return engine.Generation{}, g.err
	}
	return engine.Generation{Model: g.model, Content: g.content}, nil
}

type fakeSource struct {
	gen   Generator
	err   error
	calls int
}

func (s *fakeSource) Generator() (Generator, error) {
	s.calls++
	return s.gen, s.err
}

type fakeStore struct {
	mu             sync.Mutex
	saved          []store.QAHistory
	summaries      map[int64]store.Summary
	recipients     map[int64][]mail.Recipient
	details        map[int64]store.ContextDetail
	plans          map[string]mailquery.Plan
	threads        map[string]store.ThreadMessagesResult
	states         map[string]threadintel.Assessment
	queryResult    mailquery.Result
	queryResults   map[string]mailquery.Result
	people         []store.PersonCandidate
	peopleErr      error
	personTerms    []string
	latestThread   string
	latestID       int64
	latestErr      error
	latestAddrs    []string
	nextID         int64
	gen            uint64
	routeGen       uint64
	owner          store.OwnerIdentity
	summariesCalls int
	omitDetails    bool
	saveErr        error
	listErr        error
	clearErr       error
	summariesErr   error
	planErr        error
	planSaveErr    error
	queryErr       error
	clearCalls     int
	queryCalls     int
	turnCalls      int
	turnLimit      int
	lastTurnID     string
	lastPlan       mailquery.Plan
	lastLoc        *time.Location
	planSaves      []string
}

// Generation is the fake store's view generation. Tests bump gen to model a
// label or verdict write that invalidates derived views.
func (s *fakeStore) Generation() uint64 { return s.gen }

// RouteGeneration models a layer-1 re-score that replaces the certified
// answers without touching mail or labels.
func (s *fakeStore) RouteGeneration() uint64 { return s.routeGen }

func (s *fakeStore) SaveQA(_ context.Context, rec store.QAHistory) (store.QAHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return store.QAHistory{}, s.saveErr
	}
	s.nextID++
	rec.ID = s.nextID
	rec.CreatedAt = "2026-10-26T12:00:00Z"
	s.saved = append(s.saved, rec)
	if len(s.saved) > store.QAHistoryMax {
		s.saved = s.saved[len(s.saved)-store.QAHistoryMax:]
	}
	return rec, nil
}

func (s *fakeStore) ListQA(_ context.Context, limit int) ([]store.QAHistory, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	ordered := append([]store.QAHistory(nil), s.saved...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID > ordered[j].ID })
	n := min(limit, len(ordered))
	return ordered[:n], nil
}

func (s *fakeStore) ListQASummaries(_ context.Context, limit int) ([]store.QAHistorySummary, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	ordered := append([]store.QAHistory(nil), s.saved...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID > ordered[j].ID })
	n := min(limit, len(ordered))
	out := make([]store.QAHistorySummary, n)
	for i := range ordered[:n] {
		out[i] = store.QAHistorySummary{ID: ordered[i].ID, Question: ordered[i].Question,
			Mode: ordered[i].Mode, CreatedAt: ordered[i].CreatedAt}
	}
	return out, nil
}

func (s *fakeStore) GetQA(_ context.Context, id int64) (store.QAHistory, bool, error) {
	if s.listErr != nil {
		return store.QAHistory{}, false, s.listErr
	}
	for _, rec := range s.saved {
		if rec.ID == id {
			return rec, true, nil
		}
	}
	return store.QAHistory{}, false, nil
}

func (s *fakeStore) ListQATurns(_ context.Context, conversationID string, limit int) ([]store.QAHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCalls++
	s.lastTurnID, s.turnLimit = conversationID, limit
	if s.listErr != nil {
		return nil, s.listErr
	}
	if limit < 1 || limit > store.QATurnsMax {
		return nil, fmt.Errorf("turn limit %d", limit)
	}
	ordered := make([]store.QAHistory, 0, len(s.saved))
	for _, rec := range s.saved {
		if rec.ConversationID == conversationID {
			ordered = append(ordered, rec)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	if len(ordered) > limit {
		ordered = ordered[len(ordered)-limit:]
	}
	return ordered, nil
}

func (s *fakeStore) ClearQA(context.Context) (int64, error) {
	s.clearCalls++
	if s.clearErr != nil {
		return 0, s.clearErr
	}
	n := int64(len(s.saved))
	s.saved = nil
	s.plans = map[string]mailquery.Plan{}
	return n, nil
}

func (s *fakeStore) GetQAPlan(_ context.Context, key string) (mailquery.Plan, bool, error) {
	if s.planErr != nil {
		return mailquery.Plan{}, false, s.planErr
	}
	plan, ok := s.plans[key]
	return plan, ok, nil
}

func (s *fakeStore) SaveQAPlan(_ context.Context, key, _ string, plan mailquery.Plan) error {
	if s.planSaveErr != nil {
		return s.planSaveErr
	}
	if s.plans == nil {
		s.plans = map[string]mailquery.Plan{}
	}
	s.plans[key] = plan
	s.planSaves = append(s.planSaves, key)
	return nil
}

func (s *fakeStore) ExecuteMailQuery(_ context.Context, plan mailquery.Plan, loc *time.Location) (mailquery.Result, error) {
	s.queryCalls++
	s.lastPlan, s.lastLoc = plan, loc
	if s.queryErr != nil {
		return mailquery.Result{}, s.queryErr
	}
	if result, ok := s.queryResults[plan.Contact]; ok {
		return result, nil
	}
	return s.queryResult, nil
}

func (s *fakeStore) OwnerIdentity(context.Context) (store.OwnerIdentity, error) {
	if len(s.owner.Addresses) == 0 && len(s.owner.Names) == 0 {
		return store.OwnerIdentity{Names: []string{"Owner"}, Addresses: []string{"owner@example.com"}}, nil
	}
	return s.owner, nil
}

func (s *fakeStore) SummariesOwner(_ context.Context, ids []int64) (map[int64]store.Summary, error) {
	s.summariesCalls++
	if s.summariesErr != nil {
		return nil, s.summariesErr
	}
	out := map[int64]store.Summary{}
	for _, id := range ids {
		if sum, ok := s.summaries[id]; ok {
			out[id] = sum
		}
	}
	return out, nil
}

func (s *fakeStore) ContextDetails(_ context.Context, ids []int64) (map[int64]store.ContextDetail, error) {
	out := map[int64]store.ContextDetail{}
	if s.omitDetails {
		return out, nil
	}
	for _, id := range ids {
		if detail, ok := s.details[id]; ok {
			out[id] = detail
			continue
		}
		if recipients, ok := s.recipients[id]; ok {
			out[id] = store.ContextDetail{Recipients: recipients}
			continue
		}
		if _, ok := s.summaries[id]; ok {
			// A stored owner message defaults to read and normal importance,
			// unless a test declares the stored values.
			out[id] = store.ContextDetail{IsRead: true, Importance: 1}
		}
	}
	return out, nil
}

func (s *fakeStore) ThreadMessages(_ context.Context, conversationID string, _ int, _ int64) (store.ThreadMessagesResult, error) {
	if page, ok := s.threads[conversationID]; ok {
		return page, nil
	}
	return store.ThreadMessagesResult{}, fmt.Errorf("no thread %q", conversationID)
}

// ThreadStates derives one assessment per requested thread from the fake
// page, unless a test declared an explicit state. It mirrors the store's
// contract: the last page message decides direction, and the actor is that
// message's sender.
func (s *fakeStore) ThreadStates(_ context.Context, ids []string, _ map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	out := map[string]threadintel.Assessment{}
	for _, id := range ids {
		if assessment, ok := s.states[id]; ok {
			out[id] = assessment
			continue
		}
		page, ok := s.threads[id]
		if !ok || len(page.Messages) == 0 {
			continue
		}
		last := page.Messages[len(page.Messages)-1]
		state, reason := threadintel.AnsweredByOther, threadintel.ReasonOtherReplied
		if last.Sent {
			state, reason = threadintel.AnsweredByOwner, threadintel.ReasonOwnerReplied
		}
		out[id] = threadintel.Assessment{ConversationID: id, State: state, Reason: reason,
			ActorName: last.SenderName, ActorAddress: last.SenderAddress, LastAt: last.ReceivedAt,
			LastMessageID: last.ID, Visibility: threadintel.Complete,
			OwnerActorName: last.SenderName, OwnerActorAddress: last.SenderAddress,
			OwnerLastAt: last.ReceivedAt, OwnerLastMessageID: last.ID}
	}
	return out, nil
}

func (s *fakeStore) PersonCandidates(_ context.Context, terms []string, limit int) ([]store.PersonCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.personTerms = append([]string{}, terms...)
	if s.peopleErr != nil {
		return nil, s.peopleErr
	}
	out := append([]store.PersonCandidate{}, s.people...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) LatestThreadForAddresses(_ context.Context, addresses []string) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latestAddrs = append([]string{}, addresses...)
	if s.latestErr != nil {
		return "", 0, s.latestErr
	}
	return s.latestThread, s.latestID, nil
}

type fakeCompanies struct {
	mu            sync.Mutex
	list          worldsvc.ListResult
	profiles      map[string]worldsvc.ProfileView
	listErr       error
	profileErr    error
	listCalls     int
	profileCalls  int
	lastKey       string
	lastMonths    int
	lastEventDays int
}

func (c *fakeCompanies) List(context.Context, worldsvc.ListQuery) (worldsvc.ListResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCalls++
	return c.list, c.listErr
}

func (c *fakeCompanies) Profile(_ context.Context, key string, months, eventDays int) (worldsvc.ProfileView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.profileCalls++
	c.lastKey, c.lastMonths, c.lastEventDays = key, months, eventDays
	if c.profileErr != nil {
		return worldsvc.ProfileView{}, c.profileErr
	}
	profile, ok := c.profiles[key]
	if !ok {
		return worldsvc.ProfileView{}, fmt.Errorf("unknown company %q", key)
	}
	return profile, nil
}

func (c *fakeCompanies) calls() (list, profile int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listCalls, c.profileCalls
}

type testWorld struct {
	cfg    *config.Config
	store  *fakeStore
	prio   *fakePriority
	search *fakeSearch
	gate   *fakeGate
	source *fakeSource
	gen    *fakeGenerator
	svc    *Service
}

func newWorld(t *testing.T, preview bool) *testWorld {
	t.Helper()
	cfg := configtest.New(t, map[string]any{"learning.test_mode": preview})
	w := &testWorld{
		cfg:    cfg,
		store:  &fakeStore{summaries: map[int64]store.Summary{}, recipients: map[int64][]mail.Recipient{}, details: map[int64]store.ContextDetail{}, plans: map[string]mailquery.Plan{}},
		prio:   &fakePriority{byFilter: map[priority.Filter][]priority.Item{}, totals: map[priority.Filter]int{}},
		search: &fakeSearch{},
		gate:   &fakeGate{},
		gen:    &fakeGenerator{name: "fake", model: "fake-model"},
	}
	w.source = &fakeSource{gen: w.gen}
	w.gate.preview = preview
	svc, err := New(cfg, Deps{Store: w.store, Priority: w.prio, Search: w.search, Gate: w.gate,
		Generator: w.source, Clock: clocktest.New(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	return w
}

// The planner cache identity is the selected record's fingerprint: a
// rename keeps cached plans and answers, a model or endpoint change does
// not.
func TestPlannerIdentityFollowsTheRecordFingerprint(t *testing.T) {
	w := newWorld(t, false)
	write := func(id, model, baseURL string) {
		t.Helper()
		zero := 0.0
		p := engine.Provider{Protocol: engine.ProtocolOpenAIChat, BaseURL: baseURL, Path: "/chat/completions",
			Model: model, APIKeySecret: "PROVIDER_QA_API_KEY", ContextTokens: 10000, CharsPerToken: 3,
			ReserveTokensPerByte: 1, ReserveOutputTokens: 128, ResponseFormat: engine.ResponseFormatJSON,
			PriceInput: &zero, PriceOutput: &zero}
		raw, err := json.Marshal(map[string]engine.Provider{id: p})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.cfg.Paths().DataDir, "providers.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := w.cfg.Set("qa.provider", id); err != nil {
			t.Fatal(err)
		}
	}
	write("gen", "m-1", "http://127.0.0.1:9/v1")
	first := w.svc.plannerIdentity()
	if first == "" || first == "unavailable" {
		t.Fatalf("configured identity %q", first)
	}
	write("renamed", "m-1", "http://127.0.0.1:9/v1")
	if got := w.svc.plannerIdentity(); got != first {
		t.Fatalf("a rename or label change must keep the planner identity: %q -> %q", first, got)
	}
	write("renamed", "m-2", "http://127.0.0.1:9/v1")
	if got := w.svc.plannerIdentity(); got == first {
		t.Fatal("a model change must invalidate cached plans and answers")
	}
	second := w.svc.plannerIdentity()
	if questionKey(first, "fr", "question") == questionKey(second, "fr", "question") {
		t.Fatal("the plan/answer cache key must change with the model")
	}
	write("renamed", "m-2", "http://127.0.0.1:10/v1")
	if got := w.svc.plannerIdentity(); got == second {
		t.Fatal("an endpoint change must invalidate cached plans and answers")
	}
}

func item(id int64, reasons ...string) priority.Item {
	return priority.Item{MessageID: id, Subject: fmt.Sprintf("subject %d", id), SenderName: "sender",
		ReceivedAt: "2026-10-25T10:00:00Z", Reasons: reasons}
}

func hit(id int64) search.Hit {
	return search.Hit{Summary: store.Summary{RowID: id, Subject: fmt.Sprintf("subject %d", id), SenderName: "sender",
		SenderAddress: "sender@example.com", Instant: "2026-10-25T10:00:00Z", Body: "body " + fmt.Sprint(id)}}
}

// companyBook is the visible organization book and profile the company tests
// read. Its counts and open states are deliberately distinct so an answer
// that copies the wrong field is caught.
func companyBook() *fakeCompanies {
	profile := worldsvc.ProfileView{
		Key: "c:atelier nord", Label: "Atelier Nord", Declared: true, Domains: []string{"atelier-nord.test"},
		Messages: 12, Sent: 5, Received: 7, Threads: 4, PeopleContacted: 3,
		LastAt: "2026-10-20T09:00:00Z",
		ThreadStates: map[string]int{
			string(threadintel.AwaitingOwner):   2,
			string(threadintel.AwaitingOther):   1,
			string(threadintel.AnsweredByOwner): 1,
		},
		Recent: []worldsvc.CompanyMessage{
			{MessageID: 11, Subject: "Atelier Nord renewal", SenderName: "Bob Smith", Instant: "2026-10-20T09:00:00Z"},
			{MessageID: 12, Subject: "Kickoff", SenderName: "Alice Martin", Instant: "2026-10-18T09:00:00Z"},
		},
		OpenThreads: []worldsvc.CompanyThread{
			{ConversationID: "C-1", Subject: "Follow-up", LastAt: "2026-10-19T09:00:00Z",
				State: string(threadintel.AwaitingOwner), MessageID: 13},
		},
		Deadlines: []worldsvc.Deadline{{MessageID: 14, Subject: "Contract",
			Due: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}},
		Events: []worldsvc.CalendarEvent{{Subject: "Review", Start: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}},
	}
	return &fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{
			{Key: "c:atelier nord", Label: "Atelier Nord", Declared: true, Domains: []string{"atelier-nord.test"},
				People: 3, Messages: 12, Threads: 4, LastAt: "2026-10-20T09:00:00Z"},
		}},
		profiles: map[string]worldsvc.ProfileView{"c:atelier nord": profile},
	}
}

// planFallback prepares a generated freeform answer so a test can prove a
// question reached the planner instead of the company shortcut.
func planFallback(w *testWorld) {
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Answer [1].","used":[1]}`
}

func TestAskValidationGateAndLanguage(t *testing.T) {
	w := newWorld(t, false)
	if _, err := w.svc.Ask(t.Context(), "ab", "fr"); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatalf("short question: %v", err)
	}
	if _, err := w.svc.Ask(t.Context(), strings.Repeat("a", MaxQuestionRunes+1), "fr"); !errors.Is(err, ErrInvalidQuestion) {
		t.Fatalf("long question: %v", err)
	}
	if _, err := w.svc.Ask(t.Context(), "hello there", "de"); !errors.Is(err, ErrInvalidLanguage) {
		t.Fatalf("language: %v", err)
	}
	if w.gate.calls != 0 || w.store.nextID != 0 {
		t.Fatal("invalid input reached the gate or the history")
	}
	if err := w.cfg.Set("qa.enabled", false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Ask(t.Context(), "hello there", "fr"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if w.gate.calls != 0 {
		t.Fatal("disabled Q&A reached the gate")
	}
	if err := w.cfg.Set("qa.enabled", true); err != nil {
		t.Fatal(err)
	}
	w.gate.err = errors.New("locked")
	if _, err := w.svc.Ask(t.Context(), "hello there", "fr"); err == nil {
		t.Fatal("the gate was bypassed")
	}
	w.gate.err = nil
	w.prio.byFilter[priority.FilterAll] = []priority.Item{item(1, "reply")}
	view, err := w.svc.Ask(t.Context(), "hello there", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.store.saved[len(w.store.saved)-1].Language; got != DefaultLanguage {
		t.Fatalf("empty language became %q", got)
	}
	if view.ID == 0 || view.CreatedAt == "" {
		t.Fatalf("not persisted: %+v", view)
	}
}

func TestStructuredIntents(t *testing.T) {
	w := newWorld(t, false)
	items := map[priority.Filter]priority.Item{
		priority.FilterReply:    item(11, "reply"),
		priority.FilterUrgent:   item(12, "urgent"),
		priority.FilterSecurity: item(13, "security"),
		priority.FilterDecision: item(14, "decision"),
		priority.FilterDeadline: item(15, "deadline"),
		priority.FilterWorkload: item(16, "workload"),
		priority.FilterAll:      item(17, "reply"),
	}
	for f, it := range items {
		w.prio.byFilter[f] = []priority.Item{it}
		w.prio.totals[f] = int(it.MessageID)
	}
	cases := []struct {
		question string
		intent   string
		id       int64
	}{
		{"Quelles réponses attendent une réponse ?", IntentReply, 11},
		{"What is urgent?", IntentUrgent, 12},
		{"Y a-t-il des problèmes de sécurité ?", IntentSecurity, 13},
		{"Do I need to decide?", IntentDecision, 14},
		{"Quelles sont mes échéances ?", IntentDeadline, 15},
		{"Combien de travail ?", IntentWorkload, 16},
		{"Quels sont les plus importants ?", IntentPriority, 17},
		// summary_today is not a priority lookup: it reads the owner-day
		// received window. TestAskAutoSummaryToday owns it.
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			view, err := w.svc.Ask(t.Context(), tc.question, "fr")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent || view.Answer != "" || view.Provider != "" {
				t.Fatalf("view %+v", view)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != tc.id {
				t.Fatalf("citations %+v", view.Citations)
			}
			if view.Counts["reply"] != 11 || view.Counts["total"] != 17 {
				t.Fatalf("counts %+v", view.Counts)
			}
			if w.gen.calls != 0 {
				t.Fatal("a structured intent called the generator")
			}
			if view.ID == 0 {
				t.Fatal("structured answer not persisted")
			}
		})
	}
}

func TestContentQuestionsBypassStructuredIntents(t *testing.T) {
	for _, question := range []string{
		"did Nadia reply?",
		"est-ce que Nadia a répondu ?",
		"la dernière réponse de Nadia",
		"quel est le dernier message de Nadia Tremblay ?",
		"latest urgent message from Nadia",
		"who replied to Nadia?",
	} {
		if intent, _ := parseIntent(question); intent != IntentFreeform {
			t.Errorf("%q parsed as %q, want freeform", question, intent)
		}
	}
	for _, question := range []string{
		"Quelles réponses attendent une réponse ?",
		"Quelles sont mes échéances ?",
	} {
		if intent, _ := parseIntent(question); intent == IntentFreeform {
			t.Errorf("%q must stay a structured intent", question)
		}
	}
}

func TestSenderStructuredAndOwnerOnly(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{Address: "alice@example.test", Name: "Alice Martin",
		Messages: 4, LastAt: "2026-10-25T10:00:00Z"}}
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Rows: []mailquery.Row{{MessageID: 21, Subject: "subject 21", SenderName: "Alice Martin",
			Instant: "2026-10-25T10:00:00Z"}}}
	view, err := w.svc.Ask(t.Context(), "Messages de Alice Martin", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured || view.Intent != IntentSender || view.Answer != "" || len(view.Citations) != 1 ||
		view.Citations[0].MessageID != 21 {
		t.Fatalf("view %+v", view)
	}
	// The plan names the resolved address and the received direction: a body
	// or recipient mention of the name cannot be cited as a message from her.
	if w.store.queryCalls != 1 || w.store.lastPlan.Kind != mailquery.KindMessages ||
		w.store.lastPlan.Direction != mailquery.DirectionReceived ||
		len(w.store.lastPlan.ContactAddresses) != 1 ||
		w.store.lastPlan.ContactAddresses[0] != "alice@example.test" {
		t.Fatalf("sender plan %+v calls=%d", w.store.lastPlan, w.store.queryCalls)
	}
	if w.search.calls != 0 {
		t.Fatalf("a sender question used free-text search %d times", w.search.calls)
	}
	if w.gen.calls != 0 {
		t.Fatal("sender intent called the generator")
	}
}

// TestContentActionQuestionReachesThePlanner proves a question about the act
// recorded in a message is not answered by a mailbox-wide count or list. It
// retrieves the matching message and asks the planner to answer from the
// numbered evidence, with no priority read.
func TestContentActionQuestionReachesThePlanner(t *testing.T) {
	cases := []struct {
		name     string
		question string
		language string
	}{
		{"F1 approved deadlines", "Quelles échéances ai-je approuvées ?", "fr"},
		{"F2 signed deadlines", "Quelles échéances ai-je signées ?", "fr"},
		{"F3 paid deadlines", "Quelles échéances ai-je payées ?", "fr"},
		{"F4 approved decisions", "Quelles décisions ai-je approuvées ?", "fr"},
		{"F5 approved deadlines in English", "Which deadlines did I approve?", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
			w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Échéance", SenderName: "Alice",
				SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z",
				Body: "The deadline was approved on Friday."}
			w.gen.content = `{"route":"answer","answer":"The deadline was approved [1].","used":[1]}`

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeGenerated || view.Intent != IntentFreeform || view.Answer == "" {
				t.Fatalf("content action did not reach the planner: %+v", view)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
				t.Fatalf("citations = %+v", view.Citations)
			}
			if w.gen.calls != 1 || w.search.calls != 1 || w.prio.calls != 0 {
				t.Fatalf("generator=%d search=%d priority=%d", w.gen.calls, w.search.calls, w.prio.calls)
			}
			if len(w.store.saved) != 1 || w.store.saved[0].Mode != ModeGenerated ||
				w.store.saved[0].Question != tc.question {
				t.Fatalf("saved rows = %+v", w.store.saved)
			}
		})
	}
}

// TestContentActionWhQuestionReachesThePlanner proves a wh-framed content
// question separated from its action verb by a modal still reaches the
// planner and answers from numbered evidence.
func TestContentActionWhQuestionReachesThePlanner(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Contract decision", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z",
		Body: "The contract decision is recorded in this message."}
	w.gen.content = `{"route":"answer","answer":"The contract decision is recorded here [1].","used":[1]}`

	view, err := w.svc.Ask(t.Context(), "What do I need to decide about the contract?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Answer == "" || view.Notice != "" {
		t.Fatalf("wh content question did not reach the planner: %+v", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("citations = %+v", view.Citations)
	}
	if w.gen.calls != 1 || w.search.calls != 1 {
		t.Fatalf("generator=%d search=%d", w.gen.calls, w.search.calls)
	}
}

// TestContentActionPlanIsRejected proves the same question cannot be answered
// by a computed message list when the planner returns one: the question asks
// what the messages say, so the retrieved sources are offered instead.
func TestContentActionPlanIsRejected(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Échéance", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z",
		Body: "The deadline was approved on Friday."}
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","limit":5,"order":"desc"}}`

	view, err := w.svc.Ask(t.Context(), "Quelles échéances ai-je approuvées ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoGroundedAnswer {
		t.Fatalf("content plan fell through to %s/%q: %+v", view.Mode, view.Notice, view)
	}
	if view.Answer != "" || view.Plan != nil || view.Result != nil {
		t.Fatalf("a content plan fabricated an answer: %+v", view)
	}
	if w.gen.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", w.gen.calls)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("retrieved evidence was not offered: %+v", view.Citations)
	}
}

// TestAggregateQuestionKeepsItsComputedAnswer pins the boundary between a
// mailbox count/list and a content question. The aggregate list controls
// without a count cue keep their deterministic priority answer, and the plain
// count accepted by the scope gate answers from the priority totals; a count
// the scope gate rejects (an act recorded in a message, French included)
// keeps the freeform path and executes the planner's computed count plan
// instead of a mailbox-wide category total.
func TestAggregateQuestionKeepsItsComputedAnswer(t *testing.T) {
	structured := []struct {
		name     string
		question string
		intent   string
		id       int64
	}{
		{"S1 approved email list", "Which emails did I approve?", IntentDecision, 14},
		{"S2 messages to approve list", "List messages I need to approve", IntentDecision, 14},
		{"S3 plain deadlines", "Quelles sont mes échéances ?", IntentDeadline, 15},
		{"S4 plain deadline count", "How many deadlines do I have?", IntentDeadline, 15},
	}
	w := newWorld(t, false)
	w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(15, "deadline")}
	w.prio.byFilter[priority.FilterDecision] = []priority.Item{item(14, "decision")}
	for _, tc := range structured {
		t.Run(tc.name, func(t *testing.T) {
			before := w.prio.calls
			view, err := w.svc.Ask(t.Context(), tc.question, "fr")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent || view.Notice != "" {
				t.Fatalf("aggregate control did not stay structured: %+v", view)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != tc.id {
				t.Fatalf("citations = %+v", view.Citations)
			}
			if w.gen.calls != 0 {
				t.Fatalf("structured control called the planner %d times", w.gen.calls)
			}
			if w.prio.calls <= before {
				t.Fatal("structured control did not read the priority ranking")
			}
			if view.ID == 0 {
				t.Fatal("structured control not persisted")
			}
		})
	}

	// A count the scope gate rejects stays freeform on every generator
	// configuration, so it reaches the planner and executes its count plan
	// (the action counts here name an act a category total cannot state).
	computed := []struct {
		name     string
		question string
	}{
		{"S5 how many approved deadlines", "Combien d'échéances ai-je approuvées ?"},
		{"S6 how many messages approved", "Combien de messages ai-je approuvés ?"},
	}
	for _, tc := range computed {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"count","direction":"both","time_range":"all","limit":10,"order":"desc"}}`
			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeComputed || view.Plan == nil || view.Plan.Action != mailquery.ActionCount {
				t.Fatalf("aggregate question did not execute its count plan: %+v", view)
			}
			if view.Notice != "" {
				t.Fatalf("computed answer carries notice %q", view.Notice)
			}
			if w.gen.calls != 1 {
				t.Fatalf("planner calls = %d, want 1", w.gen.calls)
			}
			if w.prio.calls != 0 {
				t.Fatalf("computed count read the priority ranking %d times", w.prio.calls)
			}
			if view.ID == 0 {
				t.Fatal("computed answer not persisted")
			}
		})
	}
}

// TestProviderlessAggregateCountUsesThePriorityAnswer pins the provider-less
// availability path: a plain count of one structured category is answered from
// the priority ranking, never from the freeform planner that cannot run
// without a generator. Before the fallback this view was a retrieval/no-match
// answer with no deadline count.
func TestProviderlessAggregateCountUsesThePriorityAnswer(t *testing.T) {
	w := newWorld(t, false)
	src := &fakeSource{err: ErrUnavailable}
	w.svc.source = src
	w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
	w.prio.totals[priority.FilterDeadline] = 1
	w.prio.totals[priority.FilterAll] = 7

	view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured {
		t.Fatalf("mode = %q, notice = %q; the provider-less count did not use the priority answer: %+v",
			view.Mode, view.Notice, view)
	}
	if view.Intent != IntentDeadline {
		t.Fatalf("intent = %q, want %q", view.Intent, IntentDeadline)
	}
	if got := view.Counts["deadline"]; got != 1 {
		t.Fatalf("counts[deadline] = %d, want 1 (counts %v)", got, view.Counts)
	}
	if w.gen.calls != 0 {
		t.Fatalf("generator calls = %d, want 0", w.gen.calls)
	}
	if src.calls != 0 {
		t.Fatalf("generator availability lookups = %d, want 0: the count scope decision must not depend on availability", src.calls)
	}
	if w.search.calls != 0 {
		t.Fatalf("search calls = %d, want 0", w.search.calls)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 41 {
		t.Fatalf("citations = %+v, want the seeded deadline message 41", view.Citations)
	}
	if view.ID == 0 {
		t.Fatal("the provider-less count was not persisted")
	}
	if len(w.store.saved) != 1 {
		t.Fatalf("saved history rows = %d, want 1", len(w.store.saved))
	}
	saved := w.store.saved[0]
	if saved.Mode != ModeStructured || saved.Intent != IntentDeadline || saved.Counts["deadline"] != 1 {
		t.Fatalf("saved history = %+v", saved)
	}
}

// TestProviderlessAggregateCountZeroIsReal proves the fallback returns the
// priority total even when it is zero: an empty category is a real zero with
// no citation, never a substituted priority item.
func TestProviderlessAggregateCountZeroIsReal(t *testing.T) {
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}

	view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured || view.Intent != IntentDeadline {
		t.Fatalf("mode/intent = %q/%q, want %q/%q", view.Mode, view.Intent, ModeStructured, IntentDeadline)
	}
	if got := view.Counts["deadline"]; got != 0 {
		t.Fatalf("counts[deadline] = %d, want the real zero", got)
	}
	if len(view.Citations) != 0 {
		t.Fatalf("a zero count fabricated citations: %+v", view.Citations)
	}
	if w.gen.calls != 0 || w.search.calls != 0 {
		t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
	}
	if view.ID == 0 {
		t.Fatal("the real zero was not persisted")
	}
}

// TestProviderlessAggregateCountControls pins the fallback's boundary: plain
// counts of one structured category answer from priority totals, while a
// scoped, dated, action or content question keeps the freeform path and never
// substitutes a mailbox-wide count. The French deadline question is already
// parseIntent-structured before the fallback; it stays a regression case.
func TestProviderlessAggregateCountControls(t *testing.T) {
	fallback := []struct {
		name     string
		question string
		language string
		intent   string
		id       int64
	}{
		{"urgent", "How many urgent messages?", "en", IntentUrgent, 42},
		{"reply", "How many messages need a reply?", "en", IntentReply, 43},
		{"French deadline", "Combien d'échéances ai-je ?", "fr", IntentDeadline, 41},
		{"French urgent", "Combien de messages urgents ?", "fr", IntentUrgent, 42},
	}
	controls := []struct {
		name     string
		question string
	}{
		{"approved action", "How many deadlines did I approve?"},
		{"person content", "How many emails did Alice write?"},
		{"this week", "How many deadlines this week?"},
		{"company scope", "How many deadlines from Atelier Nord?"},
		{"two categories", "How many of the deadlines are security-related?"},
	}

	for _, tc := range fallback {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.byFilter[priority.FilterReply] = []priority.Item{item(43, "reply")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterReply] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent {
				t.Fatalf("mode/intent = %q/%q, notice = %q; want %q/%q",
					view.Mode, view.Intent, view.Notice, ModeStructured, tc.intent)
			}
			if got := view.Counts[tc.intent]; got != 1 {
				t.Fatalf("counts[%s] = %d, want 1 (counts %v)", tc.intent, got, view.Counts)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != tc.id {
				t.Fatalf("citations = %+v, want message %d", view.Citations, tc.id)
			}
			if w.gen.calls != 0 || w.search.calls != 0 {
				t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
			}
			if view.ID == 0 {
				t.Fatal("the provider-less count was not persisted")
			}
		})
	}

	for _, tc := range controls {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || len(view.Counts) != 0 || len(view.Citations) != 0 {
				t.Fatalf("control substituted a structured count: %+v", view)
			}
			if view.Intent != IntentFreeform {
				t.Fatalf("control intent = %q, want %q", view.Intent, IntentFreeform)
			}
			if w.gen.calls != 0 {
				t.Fatalf("generator calls = %d, want 0", w.gen.calls)
			}
		})
	}
}

// TestProviderlessAggregateCountRejectsCountSynonymScopes pins the same
// fail-closed gate for the count synonyms: a person, organization, date or
// pronoun scope must keep amount/quantity/bare-number counts on the freeform
// path with no priority read, never a mailbox-wide total.
func TestProviderlessAggregateCountRejectsCountSynonymScopes(t *testing.T) {
	cases := []struct {
		name     string
		question string
		language string
	}{
		{"amount person", "Amount of deadlines does Bob have?", "en"},
		{"quantity company", "Quantity of deadlines from Atelier Nord?", "en"},
		{"bare number short person", "Number deadlines does An have?", "en"},
		{"amount date", "Amount of deadlines this week?", "en"},
		{"quantity pronoun", "Quantity of urgent messages does she have?", "en"},
		{"French week", "Combien de messages urgents cette semaine ?", "fr"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("a scoped count synonym answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("a scoped count synonym produced mailbox counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a scoped count synonym read the priority ranking %d times", w.prio.calls)
			}
			if w.gen.calls != 0 {
				t.Fatalf("a scoped count synonym called the generator %d times", w.gen.calls)
			}
		})
	}
}

// TestProviderlessAggregateCountAcceptsPlainCountSynonyms pins the accept
// side: a plain count with exactly one category and no scope is structured
// from the priority totals for the new cues, while a priority count stays
// freeform because there is no priority filter.
func TestProviderlessAggregateCountAcceptsPlainCountSynonyms(t *testing.T) {
	structured := []struct {
		name     string
		question string
		language string
		intent   string
		id       int64
	}{
		{"amount", "Amount of deadlines", "en", IntentDeadline, 41},
		{"quantity", "Quantity of urgent messages", "en", IntentUrgent, 42},
		{"bare number", "Number deadlines", "en", IntentDeadline, 41},
		{"category synonym not repeated", "How many deadlines are due?", "en", IntentDeadline, 41},
		{"reply plural control", "How many reply messages?", "en", IntentReply, 43},
		{"French quantité", "Quantité d'échéances", "fr", IntentDeadline, 41},
	}
	for _, tc := range structured {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.byFilter[priority.FilterReply] = []priority.Item{item(43, "reply")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterReply] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent || view.Counts[tc.intent] != 1 {
				t.Fatalf("plain count synonym = %+v, want structured/%s count 1", view, tc.intent)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != tc.id {
				t.Fatalf("citations = %+v, want message %d", view.Citations, tc.id)
			}
			if w.gen.calls != 0 || w.search.calls != 0 {
				t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
			}
			if view.ID == 0 {
				t.Fatal("the plain count synonym was not persisted")
			}
		})
	}

	t.Run("count cue is not a person term", func(t *testing.T) {
		for _, tc := range []struct {
			question string
			person   store.PersonCandidate
		}{
			{"Amount of deadlines", store.PersonCandidate{Address: "noreply@mont-amount.test",
				Name: "Mont Amount Conseil", Messages: 4}},
			{"Count of deadlines", store.PersonCandidate{Address: "billing@example.com",
				Name: "Accounting Team", Messages: 4}},
		} {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.store.people = []store.PersonCandidate{tc.person}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != IntentDeadline || view.Counts["deadline"] != 1 {
				t.Fatalf("%q with person %q = %+v, want structured/deadline count 1",
					tc.question, tc.person.Name, view)
			}
		}
	})

	for _, question := range []string{
		"Amount of priority messages",
		"Number priority messages",
		"Quantity important messages",
	} {
		t.Run("priority stays freeform "+question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterAll] = []priority.Item{item(50, "important")}
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform || len(view.Counts) != 0 || w.prio.calls != 0 {
				t.Fatalf("a priority count synonym answered from the unfiltered total: %+v (priority reads %d)", view, w.prio.calls)
			}
		})
	}
}

// TestProviderlessAggregateCountLeavesScopedNamesToTheResolver proves the
// fallback's two scoped-name wording guards: an organization name and a
// person name are outside the residual allowlist, so neither answers a
// mailbox-wide count even when the resolver book knows them. These cases are
// rejected by wording before the strict gate; the gate's own peopleErr and
// listErr paths are exercised in TestProviderlessCountScopeDefersOnResolverErrors.
func TestProviderlessAggregateCountLeavesScopedNamesToTheResolver(t *testing.T) {
	t.Run("company name rejected by wording", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = &fakeSource{err: ErrUnavailable}
		w.svc.SetCompanies(companyBook())
		w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
		w.prio.totals[priority.FilterDeadline] = 1

		view, err := w.svc.Ask(t.Context(), "How many deadlines atelier-nord has?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Intent == IntentDeadline || view.Counts["deadline"] != 0 || w.prio.calls != 0 {
			t.Fatalf("a named organization took the mailbox-wide count: %+v (priority reads %d)", view, w.prio.calls)
		}
	})

	t.Run("person name rejected by wording", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = &fakeSource{err: ErrUnavailable}
		w.store.people = []store.PersonCandidate{{
			Address: "alice@example.com", Name: "Alice Martin", Messages: 3,
			LastAt: "2026-10-20T09:00:00Z",
		}}
		w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
		w.prio.totals[priority.FilterDeadline] = 1

		view, err := w.svc.Ask(t.Context(), "How many deadlines does alice have?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Mode == ModeStructured || view.Intent == IntentDeadline || view.Counts["deadline"] != 0 || w.prio.calls != 0 {
			t.Fatalf("a named person took the mailbox-wide count: %+v (priority reads %d)", view, w.prio.calls)
		}
	})
}

// TestProviderlessAggregateCountRejectsUnlistedScopes pins the fail-closed
// residual gate: after the count cue and the one matched category word are
// removed, any scope, date, status or topic token outside the small filler
// allowlist keeps the question freeform. None of these may answer a
// mailbox-wide category total.
func TestProviderlessAggregateCountRejectsUnlistedScopes(t *testing.T) {
	cases := []struct {
		name     string
		question string
	}{
		{"upcoming", "How many upcoming deadlines?"},
		{"year", "How many 2026 deadlines?"},
		{"client", "How many client deadlines?"},
		{"budget", "How many budget deadlines?"},
		{"left", "How many deadlines are left?"},
		{"active security", "How many active security alerts?"},
		{"next quarter", "How many next quarter deadlines?"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterSecurity] = []priority.Item{item(44, "security")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterSecurity] = 2
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("unlisted scope answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("unlisted scope produced mailbox counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("unlisted scope read the priority ranking %d times", w.prio.calls)
			}
			if w.gen.calls != 0 {
				t.Fatalf("unlisted scope called the generator %d times", w.gen.calls)
			}
		})
	}
}

// TestProviderlessAggregateCountDoesNotAnswerPriority pins the removal of the
// priority intent from the provider-less fallback, and the parse-level guard
// for a count whose language reaches parseIntent directly. There is no
// priority filter: the old mapping returned the unfiltered total, which the
// UI renders as "N important messages". A count must stay freeform and must
// not read the ranking; a non-count priority list keeps its structured path.
func TestProviderlessAggregateCountDoesNotAnswerPriority(t *testing.T) {
	cases := []struct {
		name     string
		question string
		language string
		freeform bool
	}{
		{"English count", "How many priority messages?", "en", true},
		{"French count", "Combien de messages importants ?", "fr", true},
		{"number-of count", "Number of important messages?", "en", true},
		{"read-first count", "Number of messages to read first?", "en", true},
		{"priority list control", "Quels sont les plus importants ?", "fr", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterAll] = []priority.Item{item(50, "important")}
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if tc.freeform {
				if view.Mode == ModeStructured || view.Intent != IntentFreeform {
					t.Fatalf("a priority count answered from the unfiltered total: %+v", view)
				}
				if len(view.Counts) != 0 {
					t.Fatalf("a priority count produced mailbox counts %v", view.Counts)
				}
				if w.prio.calls != 0 {
					t.Fatalf("a priority count read the ranking %d times", w.prio.calls)
				}
				return
			}
			if view.Mode != ModeStructured || view.Intent != IntentPriority || view.Counts["total"] != 7 {
				t.Fatalf("the priority list control = %+v, want structured/priority total 7", view)
			}
			if w.prio.calls == 0 {
				t.Fatal("the priority list control did not read the ranking")
			}
		})
	}
}

// TestProviderlessAggregateCountRejectsPersonFrames pins the third-person
// subject that the residual allowlist alone cannot see: a short name folds to
// an ordinary filler ("An" -> "an") and the entity resolver ignores names
// shorter than three runes. First/second-person counts must stay structured.
func TestProviderlessAggregateCountRejectsPersonFrames(t *testing.T) {
	rejected := []struct {
		name     string
		question string
	}{
		{"she", "How many urgent emails does she have?"},
		{"he", "How many deadlines does he have?"},
		{"short name", "How many deadlines does An have?"},
		{"they", "How many deadlines do they have?"},
		{"name", "How many deadlines does Bob have?"},
		{"did she", "How many deadlines did she have?"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("a person-scoped count answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("a person-scoped count produced mailbox counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a person-scoped count read the priority ranking %d times", w.prio.calls)
			}
		})
	}

	kept := []string{
		"How many deadlines do I have?",
		"How many deadlines do we have?",
		"How many deadlines do you have?",
	}
	for _, question := range kept {
		t.Run("kept "+question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != IntentDeadline || view.Counts["deadline"] != 1 {
				t.Fatalf("a first/second-person count was not structured: %+v", view)
			}
			if w.prio.calls == 0 {
				t.Fatal("a first/second-person count did not read the priority ranking")
			}
		})
	}
}

// TestProviderlessAggregateCountRejectsCategoryNamedPerson pins the
// category-name subject: "does Deadline have", "does Urgent have" and "does
// Reply have" are person frames, not plain counts. The category word is also
// considered as a person term on the freeform path, so a real contact named
// Deadline, Urgent or Reply can still scope the follow-up.
func TestProviderlessAggregateCountRejectsCategoryNamedPerson(t *testing.T) {
	cases := []struct {
		name     string
		question string
		subject  string
		address  string
	}{
		{"deadline", "How many deadlines does Deadline have?", "deadline", "deadline@example.com"},
		{"urgent", "How many urgent messages does Urgent have?", "urgent", "urgent@example.com"},
		{"reply", "How many replies does Reply have?", "reply", "reply@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.store.people = []store.PersonCandidate{{
				Address: tc.address, Name: strings.ToUpper(tc.subject[:1]) + tc.subject[1:],
				Messages: 3, LastAt: "2026-10-20T09:00:00Z",
			}}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.byFilter[priority.FilterReply] = []priority.Item{item(43, "reply")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterReply] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("a category-named person answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("a category-named person produced mailbox counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a category-named person read the priority ranking %d times", w.prio.calls)
			}
			considered := false
			for _, term := range w.store.personTerms {
				if term == tc.subject {
					considered = true
				}
			}
			if !considered {
				t.Fatalf("the category-name subject %q was not considered by the freeform resolver (terms %v)", tc.subject, w.store.personTerms)
			}
		})
	}
}

// TestProviderlessAggregateCountRejectsBareCategoryNamedPerson pins the
// repeated-category noun without an auxiliary: "How many deadlines
// Deadline?" is a person name or a list, not the mailbox-wide deadline total.
// The -ies normalization covers "replies Reply"; "securities Security" and
// "workloads Workload" are freeform through the residual allowlist, not the
// stem rule, and TestAggregateRepeatedCategoryWordNormalizesPlurals pins
// those pairs directly.
func TestProviderlessAggregateCountRejectsBareCategoryNamedPerson(t *testing.T) {
	for _, question := range []string{
		"How many deadlines Deadline?",
		"How many urgent messages Urgent?",
		"How many replies Reply?",
		"How many decisions Decision?",
		"How many urgents Urgent?",
		"How many security Security?",
		"How many securities Security?",
		"How many workloads Workload?",
	} {
		t.Run(question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.byFilter[priority.FilterReply] = []priority.Item{item(43, "reply")}
			w.prio.byFilter[priority.FilterDecision] = []priority.Item{item(44, "decision")}
			w.prio.byFilter[priority.FilterWorkload] = []priority.Item{item(45, "workload")}
			w.prio.byFilter[priority.FilterSecurity] = []priority.Item{item(46, "security")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterReply] = 1
			w.prio.totals[priority.FilterDecision] = 1
			w.prio.totals[priority.FilterWorkload] = 1
			w.prio.totals[priority.FilterSecurity] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform || len(view.Counts) != 0 {
				t.Fatalf("a bare category-named person answered as %+v", view)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a bare category-named person read the priority ranking %d times", w.prio.calls)
			}
		})
	}
}

// TestProviderlessAggregateCountRejectsRepeatedCueName pins the has/have
// subject guard: "Amount of deadlines Amount has?" repeats the cue as a
// person subject and must not answer the mailbox total.
func TestProviderlessAggregateCountRejectsRepeatedCueName(t *testing.T) {
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}
	w.store.people = []store.PersonCandidate{{
		Address: "amount@example.com", Name: "Amount", Messages: 2,
		LastAt: "2026-10-20T09:00:00Z",
	}}
	w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
	w.prio.totals[priority.FilterDeadline] = 1
	w.prio.totals[priority.FilterAll] = 7

	view, err := w.svc.Ask(t.Context(), "Amount of deadlines Amount has?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeStructured || view.Intent != IntentFreeform {
		t.Fatalf("a repeated cue name answered as %q/%q: %+v", view.Mode, view.Intent, view)
	}
	if len(view.Counts) != 0 {
		t.Fatalf("a repeated cue name produced mailbox counts %v", view.Counts)
	}
	if w.prio.calls != 0 {
		t.Fatalf("a repeated cue name read the priority ranking %d times", w.prio.calls)
	}
}

// TestProviderlessAggregateCountKeepsPlainAmountWithMontAmountSender pins the
// plain-count side of the entity collision: "amount" is a cue, not a person
// term, so a sender named Mont Amount cannot turn "Amount of deadlines" into a
// person-scoped freeform question.
func TestProviderlessAggregateCountKeepsPlainAmountWithMontAmountSender(t *testing.T) {
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}
	w.store.people = []store.PersonCandidate{{
		Address: "noreply@mont-amount.test", Name: "Mont Amount Conseil",
		Messages: 4, LastAt: "2026-10-20T09:00:00Z",
	}}
	w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
	w.prio.totals[priority.FilterDeadline] = 1
	w.prio.totals[priority.FilterAll] = 7

	view, err := w.svc.Ask(t.Context(), "Amount of deadlines", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured || view.Intent != IntentDeadline || view.Counts["deadline"] != 1 {
		t.Fatalf("plain amount with a MontAmount sender = %+v, want structured/deadline count 1", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 41 {
		t.Fatalf("citations = %+v, want message 41", view.Citations)
	}
	for _, term := range w.store.personTerms {
		if term == "amount" {
			t.Fatalf("the count cue was used as a person term: %v", w.store.personTerms)
		}
	}
}

// TestProviderlessAggregateCountScopesFreeformToBobNotMontAmount pins the D2
// freeform resolver scope: a count forced freeform by the person frame must
// consider Bob, never the sender whose name merely contains the cue "amount".
func TestProviderlessAggregateCountScopesFreeformToBobNotMontAmount(t *testing.T) {
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}
	w.store.people = []store.PersonCandidate{
		{Address: "bob@example.com", Name: "Bob Martin", Messages: 3, LastAt: "2026-10-20T09:00:00Z"},
		{Address: "noreply@mont-amount.test", Name: "Mont Amount Conseil", Messages: 4, LastAt: "2026-10-21T09:00:00Z"},
	}
	w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
	w.prio.totals[priority.FilterDeadline] = 1
	w.prio.totals[priority.FilterAll] = 7

	view, err := w.svc.Ask(t.Context(), "Amount of deadlines does Bob have?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeStructured || view.Intent != IntentFreeform || len(view.Counts) != 0 {
		t.Fatalf("the Bob-scoped count answered as %+v", view)
	}
	if view.Clarify != nil {
		t.Fatalf("the freeform resolver offered a MontAmount/Bob clarification: %+v", view.Clarify)
	}
	var bob, amount, montamount bool
	for _, term := range w.store.personTerms {
		switch term {
		case "bob":
			bob = true
		case "amount":
			amount = true
		case "montamount":
			montamount = true
		}
	}
	if !bob {
		t.Fatalf("Bob was not considered by the freeform resolver (terms %v)", w.store.personTerms)
	}
	if amount || montamount {
		t.Fatalf("MontAmount/cue terms leaked into the freeform person query: %v", w.store.personTerms)
	}
}

// TestProviderlessAggregateCountPreSwitchGate pins the owner before the
// route dispatch: non-"how" count cues can parse directly to a category, so
// the scope gate must run before any route can answer them. Scoped and
// person-framed counts stay free-form; plain counts keep their structured
// answer.
func TestProviderlessAggregateCountPreSwitchGate(t *testing.T) {
	rejected := []string{
		"Number of deadlines does he have?",
		"Count of deadlines does Bob have?",
		"Total deadlines does An have?",
		"Combien d'échéances a-t-elle ?",
		"Combien de messages urgents cette semaine ?",
	}
	for _, question := range rejected {
		t.Run("rejected "+question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("a scoped non-how count answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if len(view.Counts) != 0 {
				t.Fatalf("a scoped non-how count produced mailbox counts %v", view.Counts)
			}
			if w.prio.calls != 0 {
				t.Fatalf("a scoped non-how count read the priority ranking %d times", w.prio.calls)
			}
		})
	}

	plain := []struct {
		question string
		language string
		intent   string
		key      string
		id       int64
	}{
		{"Number of deadlines", "en", IntentDeadline, "deadline", 41},
		{"Total deadlines", "en", IntentDeadline, "deadline", 41},
		{"Count of urgent messages", "en", IntentUrgent, "urgent", 42},
	}
	for _, tc := range plain {
		t.Run("plain "+tc.question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.totals[priority.FilterDeadline] = 1
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterAll] = 7

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent || view.Counts[tc.key] != 1 {
				t.Fatalf("plain non-how count = %+v, want structured/%s count 1", view, tc.intent)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != tc.id {
				t.Fatalf("citations = %+v, want message %d", view.Citations, tc.id)
			}
			if w.gen.calls != 0 || w.search.calls != 0 {
				t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
			}
			if view.ID == 0 {
				t.Fatal("the plain non-how count was not persisted")
			}
		})
	}
}

// TestProviderlessCountScopeDefersOnResolverErrors proves both strict gate
// reads fail closed, and that the deferred path is the providerless scope
// check rather than only the tolerant freeform resolver.
func TestProviderlessCountScopeDefersOnResolverErrors(t *testing.T) {
	cases := []struct {
		name        string
		question    string
		set         func(*testWorld)
		wantCompany bool
		wantPerson  bool
	}{
		{"person store", "How many urgent messages?", func(w *testWorld) {
			w.store.peopleErr = errors.New("person store unavailable")
		}, false, true},
		{"company book", "How many urgent messages?", func(w *testWorld) {
			w.svc.SetCompanies(&fakeCompanies{listErr: errors.New("company book unavailable")})
		}, true, false},
		{"non-how person store", "Number of urgent messages", func(w *testWorld) {
			w.store.peopleErr = errors.New("person store unavailable")
		}, false, true},
		{"non-how company book", "Number of urgent messages", func(w *testWorld) {
			w.svc.SetCompanies(&fakeCompanies{listErr: errors.New("company book unavailable")})
		}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			logs, err := logx.Setup(w.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logs.Close() })

			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}
			w.prio.totals[priority.FilterUrgent] = 1
			w.prio.totals[priority.FilterAll] = 7
			tc.set(w)

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform || len(view.Counts) != 0 || w.prio.calls != 0 {
				t.Fatalf("a resolver error took the mailbox count: %+v (priority reads %d)", view, w.prio.calls)
			}

			var deferred, companyLog, personLog bool
			for _, entry := range logs.Ring().Snapshot() {
				switch entry.Message {
				case "qa providerless count scope deferred":
					deferred = true
				case "qa company book unavailable":
					companyLog = true
				case "qa person candidates unavailable":
					personLog = true
				}
			}
			if !deferred {
				t.Fatal("the strict providerless scope check did not log its deferral")
			}
			if tc.wantCompany && !companyLog {
				t.Fatal("the company-book gate failure was not logged")
			}
			if tc.wantPerson && !personLog {
				t.Fatal("the person-candidate gate failure was not logged")
			}
			t.Logf("strict providerless scope deferral observed (company=%v person=%v)", companyLog, personLog)
		})
	}
}

// TestProviderlessAggregateCountAvailabilityAndFailures covers the remaining
// error paths around the fallback: a ranking failure is returned, a history
// save failure does not hand back an unsaved answer, and the two "no usable
// generator" shapes (nil source, non-nil generator plus error) still take the
// local count without calling the generator.
func TestProviderlessAggregateCountAvailabilityAndFailures(t *testing.T) {
	seed := func(w *testWorld) {
		w.prio.byFilter[priority.FilterDeadline] = []priority.Item{item(41, "deadline")}
		w.prio.totals[priority.FilterDeadline] = 1
		w.prio.totals[priority.FilterAll] = 7
	}

	t.Run("priority error", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = &fakeSource{err: ErrUnavailable}
		w.prio.err = errors.New("priority unavailable")

		view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
		if err == nil {
			t.Fatalf("ranking failure was not returned: %+v", view)
		}
		if view.ID != 0 || len(view.Counts) != 0 || view.Mode != "" {
			t.Fatalf("ranking failure fabricated an answer: %+v", view)
		}
		if w.gen.calls != 0 || w.store.saved != nil {
			t.Fatalf("ranking failure reached generator=%d history=%d", w.gen.calls, len(w.store.saved))
		}
	})

	t.Run("history save error", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = &fakeSource{err: ErrUnavailable}
		w.store.saveErr = errors.New("history unavailable")
		seed(w)

		view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
		if err == nil {
			t.Fatalf("save failure was not returned: %+v", view)
		}
		if view.ID != 0 || len(view.Counts) != 0 || view.Mode != "" {
			t.Fatalf("save failure returned an unsaved answer: %+v", view)
		}
		if w.gen.calls != 0 {
			t.Fatalf("generator calls = %d, want 0", w.gen.calls)
		}
	})

	t.Run("nil source", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = nil
		seed(w)

		view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Mode != ModeStructured || view.Intent != IntentDeadline || view.Counts["deadline"] != 1 {
			t.Fatalf("nil source did not take the local count: %+v", view)
		}
		if w.gen.calls != 0 || w.search.calls != 0 {
			t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
		}
	})

	t.Run("generator lookup error", func(t *testing.T) {
		w := newWorld(t, false)
		w.svc.source = &fakeSource{gen: w.gen, err: ErrUnavailable}
		seed(w)

		view, err := w.svc.Ask(t.Context(), "How many deadlines do I have?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Mode != ModeStructured || view.Intent != IntentDeadline || view.Counts["deadline"] != 1 {
			t.Fatalf("a failed generator lookup did not take the local count: %+v", view)
		}
		if w.gen.calls != 0 || w.search.calls != 0 {
			t.Fatalf("generator=%d search=%d, want 0/0", w.gen.calls, w.search.calls)
		}
	})
}

// TestSenderLookupDegradationIsHonest: a sender answer is built from an
// exact address lookup, so it never depends on the semantic index. A lookup
// that misses its bound, fails, or cannot resolve the sender is disclosed
// with an honest notice instead of "no message from this sender".
func TestSenderLookupDegradationIsHonest(t *testing.T) {
	lookupFailure := errors.New("lookup failed")
	seedSender := func(w *testWorld) {
		w.store.people = []store.PersonCandidate{{Address: "alice@example.test", Name: "Alice Martin",
			Messages: 4, LastAt: "2026-10-25T10:00:00Z"}}
	}
	cases := []struct {
		name       string
		set        func(*testWorld)
		cancel     bool
		wantErr    error
		wantMode   string
		wantNotice string
		citations  int
		persisted  bool
	}{
		{
			name: "lookup deadline",
			set: func(w *testWorld) {
				seedSender(w)
				w.store.queryErr = context.DeadlineExceeded
			},
			wantMode:   ModeRetrieval,
			wantNotice: NoticeRetrievalTimeout,
		},
		{
			name: "parent canceled",
			set: func(w *testWorld) {
				seedSender(w)
				w.store.queryErr = context.Canceled
			},
			cancel:  true,
			wantErr: context.Canceled,
		},
		{
			name: "lookup failure",
			set: func(w *testWorld) {
				seedSender(w)
				w.store.queryErr = lookupFailure
			},
			wantErr: lookupFailure,
		},
		{
			name:      "known sender with no message",
			set:       seedSender,
			wantMode:  ModeStructured,
			persisted: true,
		},
		{
			name: "known sender with a message",
			set: func(w *testWorld) {
				seedSender(w)
				w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
					Rows: []mailquery.Row{{MessageID: 21, Subject: "subject 21", SenderName: "Alice Martin",
						Instant: "2026-10-25T10:00:00Z"}}}
			},
			wantMode:  ModeStructured,
			citations: 1,
			persisted: true,
		},
		{
			name:       "unknown sender",
			set:        func(*testWorld) {},
			wantMode:   ModeRetrieval,
			wantNotice: NoticeSenderUnknown,
			persisted:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			tc.set(w)
			ctx := t.Context()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			view, err := w.svc.Ask(ctx, "Messages de Alice Martin", "fr")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if len(w.store.saved) != 0 {
					t.Fatalf("a failed sender lookup was persisted: %+v", w.store.saved)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if view.Mode != tc.wantMode || view.Notice != tc.wantNotice {
					t.Fatalf("view mode=%q notice=%q, want %q/%q: %+v",
						view.Mode, view.Notice, tc.wantMode, tc.wantNotice, view)
				}
				if view.Answer != "" || view.Provider != "" || view.Model != "" {
					t.Fatalf("sender lookup invented an answer or a provider: %+v", view)
				}
				if len(view.Citations) != tc.citations {
					t.Fatalf("citations = %d, want %d: %+v", len(view.Citations), tc.citations, view.Citations)
				}
			}
			if w.gen.calls != 0 || w.source.calls != 0 {
				t.Fatalf("sender lookup called the planner: generator=%d source=%d", w.gen.calls, w.source.calls)
			}
			if w.search.calls != 0 {
				t.Fatalf("sender lookup used free-text search %d times", w.search.calls)
			}
			if tc.persisted {
				if len(w.store.saved) != 1 {
					t.Fatalf("persisted rows = %d, want 1: %+v", len(w.store.saved), w.store.saved)
				}
				if w.store.saved[0].Mode != tc.wantMode || w.store.saved[0].Notice != tc.wantNotice {
					t.Fatalf("persisted row = %+v", w.store.saved[0])
				}
			} else if tc.wantErr == nil && len(w.store.saved) != 0 {
				t.Fatalf("a degraded sender lookup was persisted: %+v", w.store.saved)
			}
		})
	}
}

func TestFreeformGeneratedAnswer(t *testing.T) {
	w := newWorld(t, true)
	w.search.result = search.Result{Hits: []search.Hit{hit(1), hit(2), hit(3)}}
	for _, id := range []int64{1, 2, 3} {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: fmt.Sprintf("subject %d", id), SenderName: "Alice",
			SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z", Body: "body " + fmt.Sprint(id)}
	}
	w.prio.top = []priority.Item{item(1, "reply"), item(2, "urgent")}
	w.gen.content = `{"route":"answer","answer":"The budget was approved [1], the deadline is Friday [3].","used":[1,3]}`
	view, err := w.svc.Ask(t.Context(), "What happened with the budget?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Intent != IntentFreeform || view.Answer == "" ||
		view.Provider != "fake" || view.Model != "fake-model" || !view.Preview {
		t.Fatalf("view %+v", view)
	}
	if len(view.Citations) != 2 || view.Citations[0].MessageID != 1 || view.Citations[1].MessageID != 3 {
		t.Fatalf("citations %+v", view.Citations)
	}
	if !w.search.query.Filter.OwnerOnly || w.search.query.Text != "What happened with the budget?" {
		t.Fatalf("freeform search must be owner-only and use the question: %+v", w.search.query)
	}
	if want := w.cfg.Int("qa.max_messages"); w.search.query.Limit != want {
		t.Fatalf("freeform search limit %d, want %d", w.search.query.Limit, want)
	}
	if !strings.Contains(w.gen.system, "mail query planner") || !strings.Contains(w.gen.system, "route") ||
		!strings.Contains(w.gen.user, "Language: English") ||
		!strings.Contains(w.gen.user, "What happened with the budget?") || !strings.Contains(w.gen.user, "[1]") {
		t.Fatalf("prompt\nsystem: %s\nuser: %s", w.gen.system, w.gen.user)
	}
	if view.ID == 0 || w.store.saved[len(w.store.saved)-1].Mode != ModeGenerated {
		t.Fatal("generated answer not persisted")
	}
}

const answerLanguageEnglishLong = "The invoice was approved by the finance team. The payment will be sent before the end of the week. [1]"

const answerLanguageFrenchLong = "La facture a été approuvée par l'équipe financière. Le paiement sera envoyé avant la fin de la semaine. [1]"

const answerLanguageFrenchQuotingEnglish = "Merci beaucoup, c'est bien noté : je m'occupe de la facture corrigée dès aujourd'hui [1].\n\n" +
	"> Could you send the revised invoice before Friday?"

// TestGeneratedAnswerLanguageNotice covers the JSON answer path: a matching
// language, a wrong language, a French answer quoting English mail and a
// short answer. The notice never rewrites the answer, and the persisted
// history row carries the same notice.
func TestGeneratedAnswerLanguageNotice(t *testing.T) {
	cases := []struct {
		name       string
		question   string
		language   string
		answer     string
		wantNotice string
	}{
		{name: "matching", question: "What happened with the budget in the matching case?", language: "en",
			answer: answerLanguageEnglishLong},
		{name: "wrong", question: "Que s'est-il passé avec le budget dans le cas fautif ?", language: "fr",
			answer: answerLanguageEnglishLong, wantNotice: NoticeAnswerLanguage},
		{name: "quoted-english-in-french", question: "Que s'est-il passé avec la facture ?", language: "fr",
			answer: answerLanguageFrenchQuotingEnglish},
		{name: "short", question: "Que s'est-il passé avec le dossier ?", language: "fr",
			answer: "Oui [1]."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			planFallback(w)
			setGeneratedAnswerContent(t, w, tc.answer)
			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeGenerated || view.Answer != tc.answer || len(view.Citations) != 1 {
				t.Fatalf("generated answer changed: mode=%s answer=%q citations=%+v",
					view.Mode, view.Answer, view.Citations)
			}
			if view.Notice != tc.wantNotice {
				t.Fatalf("notice = %q, want %q", view.Notice, tc.wantNotice)
			}
			last := w.store.saved[len(w.store.saved)-1]
			if last.Notice != tc.wantNotice || last.Answer != view.Answer || last.Language != tc.language {
				t.Fatalf("history row does not match the returned view: %+v", last)
			}
		})
	}
}

// TestGeneratedAnswerLanguageNoticeIsNotCached pins the campaign's decision:
// a noticed answer is a real answer that is saved to history, but the next
// identical ask calls the provider again instead of replaying the notice.
func TestGeneratedAnswerLanguageNoticeIsNotCached(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	setGeneratedAnswerContent(t, w, answerLanguageEnglishLong)
	question := "Que s'est-il passé avec le budget dans le cas non mis en cache ?"

	first, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Notice != NoticeAnswerLanguage || w.gen.calls != 1 {
		t.Fatalf("first ask: notice=%q generator=%d", first.Notice, w.gen.calls)
	}
	if len(w.store.saved) != 1 || w.store.saved[0].Notice != NoticeAnswerLanguage {
		t.Fatalf("the noticed answer was not saved to history: %+v", w.store.saved)
	}

	second, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if second.Notice != NoticeAnswerLanguage {
		t.Fatalf("second ask notice = %q", second.Notice)
	}
	if w.gen.calls != 2 {
		t.Fatalf("a noticed answer was cached: generator calls = %d, want 2", w.gen.calls)
	}
	if len(w.store.saved) != 2 || w.store.saved[1].Notice != NoticeAnswerLanguage {
		t.Fatalf("the second noticed answer was not saved: %+v", w.store.saved)
	}
	t.Logf("wrong-language generator calls after repeat: %d", w.gen.calls)
}

// TestGeneratedAnswerMatchingLanguageIsCached proves the language check did
// not disable the ordinary answer cache for accepted answers.
func TestGeneratedAnswerMatchingLanguageIsCached(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	setGeneratedAnswerContent(t, w, answerLanguageEnglishLong)
	question := "What happened with the budget in the cached case?"

	first, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if first.Notice != "" || w.gen.calls != 1 {
		t.Fatalf("first ask: notice=%q generator=%d", first.Notice, w.gen.calls)
	}
	second, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if second.Notice != "" {
		t.Fatalf("a matching answer gained notice %q", second.Notice)
	}
	if w.gen.calls != 1 {
		t.Fatalf("the matching answer was not cached: generator calls = %d, want 1", w.gen.calls)
	}
	if len(w.store.saved) != 2 {
		t.Fatalf("history rows = %d, want 2", len(w.store.saved))
	}
	t.Logf("matching generator calls after repeat: %d", w.gen.calls)
}

// TestGeneratedAnswerLanguageNoticeProseFallback covers the plain-text
// fallback: a prose answer with a valid citation keeps its citation and mode
// and still gets the language notice.
func TestGeneratedAnswerLanguageNoticeProseFallback(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.gen.content = "The invoice was approved by the finance team and the payment will follow this week [1]."
	view, err := w.svc.Ask(t.Context(), "Que s'est-il passé avec la facture dans le cas prose ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Notice != NoticeAnswerLanguage {
		t.Fatalf("prose fallback: mode=%s notice=%q", view.Mode, view.Notice)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("the prose citation was lost: %+v", view.Citations)
	}
	if !strings.Contains(view.Answer, "[1]") {
		t.Fatalf("the grounded prose changed: %q", view.Answer)
	}
}

func TestFreeformAnswerCacheSkipsGeneratorAndSearch(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1), hit(2), hit(3)}}
	for _, id := range []int64{1, 2, 3} {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: fmt.Sprintf("subject %d", id),
			SenderName: "Alice", SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z",
			Body: "body " + fmt.Sprint(id)}
	}
	w.gen.content = `{"route":"answer","answer":"The budget was approved [1].","used":[1]}`
	question := "What happened with the budget?"

	first, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeGenerated || w.gen.calls != 1 || w.search.calls != 1 || w.source.calls != 1 {
		t.Fatalf("first ask: mode=%s generator=%d search=%d source=%d",
			first.Mode, w.gen.calls, w.search.calls, w.source.calls)
	}

	second, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 || w.search.calls != 1 || w.source.calls != 1 {
		t.Fatalf("cached ask called generator=%d search=%d source=%d",
			w.gen.calls, w.search.calls, w.source.calls)
	}
	if second.ID == 0 || second.ID == first.ID || second.CreatedAt == "" {
		t.Fatalf("cached answer was not persisted as a fresh row: first=%d second=%d", first.ID, second.ID)
	}
	if second.Mode != first.Mode || second.Answer != first.Answer ||
		!reflect.DeepEqual(second.Citations, first.Citations) || second.Question != question {
		t.Fatalf("cached view diverged: first=%+v second=%+v", first, second)
	}
	if got := len(w.store.saved); got != 2 {
		t.Fatalf("history rows %d, want 2", got)
	}
	last := w.store.saved[len(w.store.saved)-1]
	if last.Question != question || last.Answer != first.Answer || last.Mode != ModeGenerated || last.ID != second.ID {
		t.Fatalf("history row is not the cached ask: %+v", last)
	}
}

func TestFreeformAnswerCacheSeparatesGenerationLanguageAndPreview(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Answer [1].","used":[1]}`
	question := "What happened with the budget?"

	ask := func(language string) AskView {
		t.Helper()
		view, err := w.svc.Ask(t.Context(), question, language)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}

	ask("en")
	ask("en")
	if w.gen.calls != 1 {
		t.Fatalf("same question and language called the generator %d times", w.gen.calls)
	}
	ask("fr")
	if w.gen.calls != 2 {
		t.Fatalf("another language reused the English answer: %d calls", w.gen.calls)
	}
	if err := w.cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	if view := ask("en"); view.Preview {
		t.Fatal("the raw setting labelled an answer preview while the gate was open")
	}
	if w.gen.calls != 2 {
		t.Fatalf("the raw setting rebuilt a certified answer: %d calls", w.gen.calls)
	}
	w.gate.preview = true
	if view := ask("en"); !view.Preview {
		t.Fatal("preview answer is not marked preview")
	}
	if w.gen.calls != 3 {
		t.Fatalf("a preview-rule flip reused a non-preview answer: %d calls", w.gen.calls)
	}
	ask("en")
	if w.gen.calls != 3 {
		t.Fatalf("preview answer was not cached: %d calls", w.gen.calls)
	}
	w.store.gen++
	ask("en")
	if w.gen.calls != 4 {
		t.Fatalf("a generation bump reused an old answer: %d calls", w.gen.calls)
	}
	ask("en")
	if w.gen.calls != 4 {
		t.Fatalf("the new-generation answer was not cached: %d calls", w.gen.calls)
	}
}

// The Q&A preview label follows the one effective rule, not the raw setting.
// A gate that does not implement learning.Previewer is preview off.
func TestAskPreviewFollowsTheGateRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate Gate
		want bool
	}{
		{"gate without preview", allowOnlyGate{}, false},
		{"gate says no preview", &fakeGate{}, false},
		{"gate says preview", &fakeGate{preview: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true)
			w.svc.gate = tc.gate
			w.prio.byFilter[priority.FilterAll] = []priority.Item{item(1, "reply")}
			view, err := w.svc.Ask(t.Context(), "hello there", "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Preview != tc.want {
				t.Fatalf("answer preview = %v, want %v", view.Preview, tc.want)
			}
		})
	}
}

func TestPlanCacheHitSkipsPlannerContext(t *testing.T) {
	w := newWorld(t, false)
	question := "Avec qui ai-je le plus échangé ?"
	w.store.plans[questionKey(w.svc.plannerIdentity(), "fr", question)] = mailquery.Plan{
		Kind: mailquery.KindContacts, Action: mailquery.ActionRank, Direction: mailquery.DirectionBoth,
		Metric: mailquery.MetricMessages, TimeRange: mailquery.TimeAll, Limit: 15, Order: mailquery.OrderDesc}
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindContacts, Action: mailquery.ActionRank,
		Metric: mailquery.MetricMessages, Total: 1,
		Rows: []mailquery.Row{{Key: "ada@example.com", Label: "Ada", Address: "ada@example.com",
			Messages: 1, Sent: 1, Received: 0, Threads: 1}}}

	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Plan == nil || view.Plan.Kind != mailquery.KindContacts {
		t.Fatalf("plan cache hit view %+v", view)
	}
	if w.search.calls != 0 {
		t.Fatalf("plan cache hit searched the mailbox: %d calls", w.search.calls)
	}
	if w.source.calls != 0 || w.gen.calls != 0 {
		t.Fatalf("plan cache hit called the planner: source=%d generator=%d", w.source.calls, w.gen.calls)
	}
	if w.store.queryCalls != 1 {
		t.Fatalf("plan cache hit executed %d queries", w.store.queryCalls)
	}
}

func TestFreeformAnswerCacheConcurrent(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Answer [1].","used":[1]}`
	question := "What happened with the budget?"
	if _, err := w.svc.Ask(t.Context(), question, "en"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.svc.Ask(t.Context(), question, "en")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if w.gen.calls != 1 || w.search.calls != 1 {
		t.Fatalf("concurrent cached asks called generator=%d search=%d", w.gen.calls, w.search.calls)
	}
	if got, want := len(w.store.saved), 17; got != want {
		t.Fatalf("history rows %d, want %d", got, want)
	}
}

func TestFreeformThinRetrievalMergesPriority(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.prio.top = []priority.Item{item(1, "reply"), item(2, "urgent"), item(3, "recent"), item(4, "recent")}
	for id := int64(1); id <= 4; id++ {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: "s", Body: "body"}
	}
	w.gen.content = `{"route":"answer","answer":"ok [1][2][3]","used":[1,2,3]}`
	view, err := w.svc.Ask(t.Context(), "Tell me about the project", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || len(view.Citations) != 3 {
		t.Fatalf("merged context: %+v", view)
	}
	if strings.Count(w.gen.user, "\n[") < 3 {
		t.Fatalf("priority items were not merged: %s", w.gen.user)
	}
}

func TestPlannedZeroHitsUsesPlanWithoutPriorityOrUnrelatedSources(t *testing.T) {
	w := newWorld(t, false)
	w.prio.top = []priority.Item{item(1, "reply"), item(2, "urgent"), item(3, "recent")}
	for id := int64(1); id <= 3; id++ {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: "s", Body: "body"}
	}
	w.gen.content = `{"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"both","metric":"messages","time_range":"all","limit":15,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindContacts, Action: mailquery.ActionRank,
		Metric: mailquery.MetricMessages, Total: 3,
		Rows: []mailquery.Row{{Key: "ada@example.com", Label: "Ada", Address: "ada@example.com",
			Messages: 3, Sent: 1, Received: 2, Threads: 2, LastAt: "2026-10-25T10:00:00Z"}}}
	question := "Avec qui ai-je le plus échangé ?"
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != string(mailquery.KindContacts) || view.Answer != "" ||
		view.Result == nil || len(view.Result.Rows) != 1 || view.Result.Total != 3 {
		t.Fatalf("computed view %+v", view)
	}
	if view.Plan == nil || view.Plan.Kind != mailquery.KindContacts || view.Plan.Limit != 15 {
		t.Fatalf("plan %+v", view.Plan)
	}
	if view.Counts["contacts"] != 1 || view.Counts["messages"] != 3 {
		t.Fatalf("counts %+v", view.Counts)
	}
	if len(view.Citations) != 0 {
		t.Fatalf("a contact ranking must not cite unrelated messages: %+v", view.Citations)
	}
	if w.search.calls != 1 || !w.search.query.Filter.OwnerOnly || w.search.query.Text != question {
		t.Fatalf("search %d calls, query %+v", w.search.calls, w.search.query)
	}
	if w.prio.topCalls != 0 {
		t.Fatalf("zero hits merged %d priority items", w.prio.topCalls)
	}
	if w.source.calls != 1 || w.gen.calls != 1 {
		t.Fatalf("planner calls source=%d generator=%d", w.source.calls, w.gen.calls)
	}
	if w.store.queryCalls != 1 || w.store.lastPlan.Kind != mailquery.KindContacts || w.store.lastPlan.Limit != 15 {
		t.Fatalf("executed plan %+v calls=%d", w.store.lastPlan, w.store.queryCalls)
	}
	if len(w.store.planSaves) != 1 || len(w.store.plans) != 1 {
		t.Fatalf("plan not cached: saves=%v plans=%d", w.store.planSaves, len(w.store.plans))
	}
	if view.ID == 0 || len(w.store.saved) != 1 || w.store.saved[0].Mode != ModeComputed ||
		w.store.saved[0].Plan == nil || w.store.saved[0].Result == nil {
		t.Fatalf("computed answer not persisted: %+v", w.store.saved)
	}

	// Drop the in-memory answer cache so this call exercises the persisted
	// plan cache instead: the exact question must execute the stored plan
	// without searching the mailbox or calling the model.
	w.svc.answersMu.Lock()
	w.svc.answers = nil
	w.svc.answersMu.Unlock()
	again, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if again.Mode != ModeComputed || w.gen.calls != 1 || w.store.queryCalls != 2 {
		t.Fatalf("cache miss: mode=%s generator=%d queries=%d", again.Mode, w.gen.calls, w.store.queryCalls)
	}
	if w.search.calls != 1 {
		t.Fatalf("plan cache hit searched the mailbox: %d calls", w.search.calls)
	}
}

func TestPlannedCompaniesExecutesAndCounts(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"plan","plan":{"kind":"companies","action":"rank","direction":"both","metric":"exchanges","since":"2026-07-26","until":"2026-10-26","limit":15,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindCompanies, Action: mailquery.ActionRank,
		Metric: mailquery.MetricExchanges, Total: 12,
		Rows: []mailquery.Row{
			{Key: "c:atelier nord", Label: "Atelier Nord", Address: "atelier-nord.test", Messages: 9, Sent: 4,
				Received: 5, Threads: 6, Contacts: 3, LastAt: "2026-10-20T09:00:00Z"},
			{Key: "d:beta.test", Label: "beta.test", Address: "beta.test", Messages: 3, Sent: 1,
				Received: 2, Threads: 2, Contacts: 2, LastAt: "2026-10-18T09:00:00Z"},
		}}
	view, err := w.svc.Ask(t.Context(), "Avec quelles compagnies ai-je échangé ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != string(mailquery.KindCompanies) || view.Answer != "" {
		t.Fatalf("computed view %+v", view)
	}
	if view.Counts["companies"] != 2 || view.Counts["messages"] != 12 {
		t.Fatalf("counts %+v", view.Counts)
	}
	if view.Plan == nil || view.Plan.Kind != mailquery.KindCompanies ||
		view.Plan.Metric != mailquery.MetricExchanges || view.Plan.Direction != mailquery.DirectionBoth {
		t.Fatalf("plan %+v", view.Plan)
	}
	if w.store.queryCalls != 1 || w.store.lastPlan.Kind != mailquery.KindCompanies {
		t.Fatalf("executed plan %+v calls=%d", w.store.lastPlan, w.store.queryCalls)
	}
	if len(w.store.planSaves) != 1 {
		t.Fatalf("companies plan not cached: %v", w.store.planSaves)
	}
	if len(view.Citations) != 0 {
		t.Fatalf("a company ranking must not cite unrelated messages: %+v", view.Citations)
	}
}

func TestFrenchCompanyQuestionIsCompaniesAndExchanges(t *testing.T) {
	w := newWorld(t, false)
	question := "avec quelle compagnie ai-je le plus discuté dans les 3 derniers mois ?"
	// A plan cached by the previous planner would answer the same question
	// with a contact ranking; the versioned key must miss it.
	w.store.plans[questionKeyFor(plannerVersion-1, w.svc.plannerIdentity(), "fr", question)] = mailquery.Plan{
		Kind: mailquery.KindContacts, Action: mailquery.ActionRank, Direction: mailquery.DirectionBoth,
		Metric: mailquery.MetricMessages, TimeRange: mailquery.TimeAll, Limit: 15, Order: mailquery.OrderDesc}
	w.gen.content = `{"route":"plan","plan":{"kind":"companies","action":"rank","direction":"both","metric":"exchanges","since":"2026-07-26","until":"2026-10-26","limit":15,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindCompanies, Action: mailquery.ActionRank,
		Metric: mailquery.MetricExchanges, Total: 7,
		Rows: []mailquery.Row{{Key: "c:atelier nord", Label: "Atelier Nord", Address: "atelier-nord.test",
			Messages: 7, Sent: 3, Received: 4, Threads: 4, Contacts: 2, LastAt: "2026-10-20T09:00:00Z"}}}
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 {
		t.Fatalf("the stale contacts plan was reused: %d generator calls", w.gen.calls)
	}
	if view.Plan == nil || view.Plan.Kind != mailquery.KindCompanies ||
		view.Plan.Metric != mailquery.MetricExchanges || view.Plan.Direction != mailquery.DirectionBoth {
		t.Fatalf("plan %+v", view.Plan)
	}
	if view.Plan.Since != "2026-07-26" || view.Plan.Until != "2026-10-26" {
		t.Fatalf("span %q..%q", view.Plan.Since, view.Plan.Until)
	}
	if w.store.lastPlan.Kind != mailquery.KindCompanies {
		t.Fatalf("executed plan %+v", w.store.lastPlan)
	}
	if view.Counts["companies"] != 1 || view.Counts["messages"] != 7 {
		t.Fatalf("counts %+v", view.Counts)
	}
	for _, want := range []string{
		`"companies"`,
		`never "contacts"`,
		question,
		`"since":"2026-07-26"`,
		`"until":"2026-10-26"`,
		`"metric":"exchanges"`,
	} {
		if !strings.Contains(w.gen.system, want) {
			t.Fatalf("planner prompt is missing %q:\n%s", want, w.gen.system)
		}
	}
}

func TestPlannedZeroHitsUnrelatedContentIsNoMatch(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"answer","answer":"I cannot answer from the mailbox.","used":[]}`
	view, err := w.svc.Ask(t.Context(), "What is the weather in Paris?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoMatches || len(view.Citations) != 0 || view.Answer != "" {
		t.Fatalf("zero-hit content: %+v", view)
	}
	if w.prio.topCalls != 0 {
		t.Fatalf("zero hits merged priority %d times", w.prio.topCalls)
	}
}

func TestPlannedClarifyAndInvalidPlan(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"clarify","question":"Voulez-vous les deux sens ou un seul ?","options":["Avec qui ai-je le plus échangé, envois et reçus ?","Qui m'écrit le plus ?"]}`
	view, err := w.svc.Ask(t.Context(), "Qui est le plus important ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeClarify || view.Clarify == nil || len(view.Clarify.Options) != 2 ||
		view.Answer != "Voulez-vous les deux sens ou un seul ?" {
		t.Fatalf("clarify %+v", view)
	}
	if w.store.queryCalls != 0 || w.store.saved[0].Clarify == nil {
		t.Fatalf("clarify executed a query or was not persisted: %+v", w.store.saved)
	}

	w2 := newWorld(t, false)
	w2.gen.content = `{"route":"plan","plan":{"kind":"contacts","action":"rank","limit":500}}`
	view, err = w2.svc.Ask(t.Context(), "Donne-moi les contacts", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeGenerationFailed || w2.store.queryCalls != 0 {
		t.Fatalf("invalid plan: %+v calls=%d", view, w2.store.queryCalls)
	}
}

// A clarification is one turn of a chat. The owner's answer in the same
// conversation sees the "Assistant asked" line and the current question,
// while another conversation never receives that context.
func TestConversationClarifyThenFollowUp(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"clarify","question":"Voulez-vous les deux sens ou un seul ?","options":["Avec qui ai-je le plus échangé, envois et reçus ?","Qui m'écrit le plus ?"]}`
	first, err := w.svc.Ask(t.Context(), "Qui est le plus important ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeClarify || first.Clarify == nil {
		t.Fatalf("clarify view %+v", first)
	}
	if len(first.ConversationID) != 16 {
		t.Fatalf("conversation id %q is not 16 hex characters", first.ConversationID)
	}
	if _, err := hex.DecodeString(first.ConversationID); err != nil {
		t.Fatalf("conversation id %q is not hex: %v", first.ConversationID, err)
	}
	if len(w.store.saved) != 1 || w.store.saved[0].ConversationID != first.ConversationID {
		t.Fatalf("clarify turn did not persist its conversation id: %+v", w.store.saved)
	}

	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Answer [1].","used":[1]}`
	second, err := w.svc.AskConversation(t.Context(), AskRequest{Question: "et pour l'autre ?", Language: "fr", ConversationID: first.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConversationID != first.ConversationID {
		t.Fatalf("follow-up changed conversation id: %q -> %q", first.ConversationID, second.ConversationID)
	}
	if w.store.turnCalls != 1 || w.store.lastTurnID != first.ConversationID || w.store.turnLimit != MaxConversationTurns {
		t.Fatalf("prior turns read: calls=%d id=%q limit=%d",
			w.store.turnCalls, w.store.lastTurnID, w.store.turnLimit)
	}
	for _, want := range []string{
		"Conversation so far (oldest first):",
		"Assistant asked: Voulez-vous les deux sens ou un seul ?",
		"Options: Avec qui ai-je le plus échangé, envois et reçus ?",
		"Current question: et pour l'autre ?",
	} {
		if !strings.Contains(w.gen.user, want) {
			t.Fatalf("follow-up prompt is missing %q:\n%s", want, w.gen.user)
		}
	}
	if !strings.Contains(w.gen.system, `Resolve follow-ups ("et pour l'autre ?", "and the second one?") against it`) {
		t.Fatal("the planner prompt lacks the conversation rule")
	}

	// Another conversation sees none of the first thread's turns.
	third, err := w.svc.AskConversation(t.Context(), AskRequest{Question: "et pour l'autre ?", Language: "fr", ConversationID: "autre-fil"})
	if err != nil {
		t.Fatal(err)
	}
	if third.ConversationID != "autre-fil" {
		t.Fatalf("different conversation id %q", third.ConversationID)
	}
	if strings.Contains(w.gen.user, "Assistant asked") || strings.Contains(w.gen.user, "Conversation so far") {
		t.Fatalf("another conversation received prior turns:\n%s", w.gen.user)
	}

	for _, bad := range []string{"bad id!", strings.Repeat("a", store.QAConversationIDMax+1)} {
		if _, err := w.svc.AskConversation(t.Context(), AskRequest{Question: "et pour l'autre ?", Language: "fr", ConversationID: bad}); !errors.Is(err, ErrInvalidConversation) {
			t.Fatalf("conversation id %q: %v", bad, err)
		}
	}
}

// The prior-turn block is bounded: six turns and 400 runes per question or
// answer, whatever the stored text.
func TestConversationBlockBounded(t *testing.T) {
	long := strings.Repeat("é", MaxConversationTurnRunes+50)
	turns := make([]store.QAHistory, 0, MaxConversationTurns+2)
	for i := range MaxConversationTurns + 2 {
		turns = append(turns, store.QAHistory{ID: int64(i + 1), Question: long, Answer: long})
	}
	block := conversationBlock(turns)
	if got := strings.Count(block, "Owner: "); got != MaxConversationTurns {
		t.Fatalf("conversation block has %d owner turns, want %d", got, MaxConversationTurns)
	}
	for _, line := range strings.Split(block, "\n") {
		var text string
		switch {
		case strings.HasPrefix(line, "Owner: "):
			text = strings.TrimPrefix(line, "Owner: ")
		case strings.HasPrefix(line, "Assistant: "):
			text = strings.TrimPrefix(line, "Assistant: ")
		default:
			continue
		}
		if n := utf8.RuneCountInString(text); n > MaxConversationTurnRunes {
			t.Fatalf("turn line has %d runes, want at most %d", n, MaxConversationTurnRunes)
		}
	}
}

// A plan or answer cached with one thread's context is never served to
// another conversation, even when the question text is identical.
func TestConversationCacheDoesNotCrossThreads(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	question := "What happened with the budget?"
	if _, err := w.svc.AskConversation(t.Context(), AskRequest{Question: question, Language: "en", ConversationID: "conv-a"}); err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 {
		t.Fatalf("first conversation turn called the planner %d times", w.gen.calls)
	}
	if _, err := w.svc.AskConversation(t.Context(), AskRequest{Question: question, Language: "en", ConversationID: "conv-a"}); err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 2 {
		t.Fatalf("a follow-up reused the prior turn's answer: %d calls", w.gen.calls)
	}
	if _, err := w.svc.AskConversation(t.Context(), AskRequest{Question: question, Language: "en", ConversationID: "conv-b"}); err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 3 {
		t.Fatalf("another conversation reused a cached answer: %d calls", w.gen.calls)
	}
}

func TestQuestionKeyIsStableContentFreeAndLanguageAware(t *testing.T) {
	a := questionKey("test", "fr", "  Donne-moi   les 15 destinataires  ")
	b := questionKey("test", "fr", "donne-moi les 15 destinataires")
	if a != b || len(a) != 64 || strings.Contains(a, "destinataires") {
		t.Fatalf("keys %q %q", a, b)
	}
	if a == questionKey("test", "en", "donne-moi les 15 destinataires") {
		t.Fatal("the language must separate cache entries")
	}
	if a == questionKeyFor(plannerVersion+1, "test", "fr", "donne-moi les 15 destinataires") {
		t.Fatal("a planner version bump must invalidate cached plans")
	}
	if a == questionKey("other-planner", "fr", "donne-moi les 15 destinataires") {
		t.Fatal("a planner change must invalidate cached plans")
	}
}

// TestCachedZeroPlanFallsThroughToFreshEvidence reproduces the Nadia
// question's failure mode: an earlier planner cached a message list with a
// text filter that matches nothing. The cached zero must not become the
// final answer; the ask re-plans with fresh evidence.
func TestCachedZeroPlanFallsThroughToFreshEvidence(t *testing.T) {
	w := newWorld(t, false)
	question := "quel est l'autre compagnie de nadia de pmc ?"
	key := questionKey(w.svc.plannerIdentity(), "fr", question)
	w.store.plans[key] = mailquery.Plan{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Direction: mailquery.DirectionBoth, TimeRange: "all", Text: "Nadia PMC", Limit: 10, Order: "desc"}
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Rows: []mailquery.Row{}, Total: 0}
	w.search.result = search.Result{Hits: []search.Hit{hit(107)}}
	w.store.summaries[107] = store.Summary{RowID: 107, Subject: "Poste Marc", SenderName: "Nadia Tremblay",
		SenderAddress: "nadia@aurore-groupe.test", Instant: "2026-10-19T17:01:44Z",
		Body: "Merci Nadia Tremblay Gestion Aurore inc."}
	w.gen.content = `{"route":"answer","answer":"Nadia travaille aussi pour Gestion Aurore inc. [1].","used":[1]}`

	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || !strings.Contains(view.Answer, "Gestion Aurore") {
		t.Fatalf("cached zero plan was not re-planned: %+v", view)
	}
	if w.gen.calls != 1 || w.search.calls == 0 {
		t.Fatalf("planner calls=%d search calls=%d", w.gen.calls, w.search.calls)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 107 {
		t.Fatalf("citations %+v", view.Citations)
	}
}

// nadiaMailbox is the reported mailbox: one person writes from three
// addresses, and only the display-name search reveals the two organizations
// the question's own PMC hits never show.
func nadiaMailbox(t *testing.T) (*testWorld, string) {
	t.Helper()
	w := newWorld(t, false)
	question := "quel est l'autre compagnie de nadia de pmc ?"
	pmc := store.Summary{RowID: 90, Subject: "Re: Poste Marc", SenderName: "Nadia Tremblay",
		SenderAddress: "nb@entreprises-dupont.test", Instant: "2026-10-15T14:00:00Z",
		Body: "Bonjour Marc, voici les informations pour le poste. Merci, Entreprises Dupont."}
	gesNewer := store.Summary{RowID: 105, Subject: "RE: Poste Marc", SenderName: "Nadia Tremblay",
		SenderAddress: "nadia@aurore-groupe.test", Instant: "2026-10-20T09:30:00Z",
		Body: "Bonjour Marc, c'est confirmé pour le poste. Merci Nadia Tremblay Gestion Aurore inc."}
	gesOlder := store.Summary{RowID: 107, Subject: "Poste Marc", SenderName: "Nadia Tremblay",
		SenderAddress: "nadia@aurore-groupe.test", Instant: "2026-10-19T17:01:44Z",
		Body: "Bonjour, voici le poste demandé. Merci Nadia Tremblay Gestion Aurore inc."}
	innov := store.Summary{RowID: 80, Subject: "Projet Innov", SenderName: "Nadia Tremblay",
		SenderAddress: "nadia@projet-alpha.test", Instant: "2025-05-12T15:00:00Z",
		Body: "Bonjour, projet Projet Alpha. Nadia Tremblay Projet Alpha"}
	// A LIKE contact match can return a message from another address; the
	// exact sender check must drop it.
	trap := store.Summary{RowID: 109, Subject: "Re: Poste Marc", SenderName: "Nadia Tremblay",
		SenderAddress: "nb@entreprises-dupont.test", Instant: "2026-10-20T08:00:00Z",
		Body: "Réponse envoyée depuis Entreprises Dupont."}
	for _, sum := range []store.Summary{pmc, gesNewer, gesOlder, innov, trap} {
		w.store.summaries[sum.RowID] = sum
	}
	w.search.results = map[string]search.Result{
		question: {Hits: []search.Hit{{Summary: pmc}}},
		"Nadia Tremblay": {Hits: []search.Hit{
			{Summary: pmc}, {Summary: gesNewer}, {Summary: gesOlder}, {Summary: innov},
		}},
	}
	w.store.queryResults = map[string]mailquery.Result{
		pmc.SenderAddress: {Kind: mailquery.KindMessages, Action: mailquery.ActionList, Total: 1,
			Rows: []mailquery.Row{{MessageID: pmc.RowID}}},
		"nadia@aurore-groupe.test": {Kind: mailquery.KindMessages, Action: mailquery.ActionList, Total: 3,
			Rows: []mailquery.Row{{MessageID: gesNewer.RowID}, {MessageID: gesOlder.RowID}, {MessageID: trap.RowID}}},
		"nadia@projet-alpha.test": {Kind: mailquery.KindMessages, Action: mailquery.ActionList, Total: 1,
			Rows: []mailquery.Row{{MessageID: innov.RowID}}},
	}
	w.gen.content = `{"route":"answer","answer":"Nadia Tremblay travaille aussi pour Gestion Aurore inc. [1].","used":[1]}`
	return w, question
}

func TestPersonNameMatchesQuestionToken(t *testing.T) {
	question := "quel est l'autre compagnie de nadia de pmc ?"
	hits := []search.Hit{
		{Summary: store.Summary{RowID: 1, SenderName: "Nadia Tremblay", SenderAddress: "nb@entreprises-dupont.test"}},
		{Summary: store.Summary{RowID: 2, SenderName: "Nadia Tremblay", SenderAddress: "nadia@aurore-groupe.test"}},
	}
	if got := personName(question, hits); got != "Nadia Tremblay" {
		t.Fatalf("person name %q, want Nadia Tremblay", got)
	}
	ambiguous := []search.Hit{
		{Summary: store.Summary{RowID: 3, SenderName: "Nadia Tremblay"}},
		{Summary: store.Summary{RowID: 4, SenderName: "Nadia Côté"}},
	}
	if got := personName(question, ambiguous); got != "" {
		t.Fatalf("ambiguous person name %q, want empty", got)
	}
}

func TestPersonEvidenceAddsRecentMessagesAcrossAddresses(t *testing.T) {
	w, question := nadiaMailbox(t)
	msgs, err := w.svc.personEvidence(t.Context(), question, []search.Hit{
		{Summary: w.store.summaries[90]}}, personEvidenceLimit)
	if err != nil {
		t.Fatal(err)
	}
	if w.search.query.Text != "Nadia Tremblay" || w.search.query.Limit != personSearchLimit ||
		!w.search.query.Filter.OwnerOnly {
		t.Fatalf("person search query %+v", w.search.query)
	}
	if len(msgs) != 4 {
		t.Fatalf("person messages %d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].ID != 105 || msgs[1].ID != 107 {
		t.Fatalf("person messages are not newest first: %+v", msgs)
	}
	for _, m := range msgs {
		if m.Person != "Nadia Tremblay" || m.Reason != "person" {
			t.Fatalf("person metadata %+v", m)
		}
		if m.ID == 109 {
			t.Fatalf("the exact sender check kept another address's message: %+v", m)
		}
	}
	if msgs[0].SenderAddress != "nadia@aurore-groupe.test" || !strings.Contains(msgs[0].Body, "Gestion Aurore inc.") {
		t.Fatalf("the GES message is missing: %+v", msgs[0])
	}

	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated {
		t.Fatalf("generated view %+v", view)
	}
	for _, want := range []string{"Person: Nadia Tremblay", "Gestion Aurore inc.", "RE: Poste Marc"} {
		if !strings.Contains(w.gen.user, want) {
			t.Fatalf("planner prompt lacks %q:\n%s", want, w.gen.user)
		}
	}
}

func TestAskNadiaQuestionUsesOtherCompany(t *testing.T) {
	w, question := nadiaMailbox(t)
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || !strings.Contains(view.Answer, "Gestion Aurore inc.") {
		t.Fatalf("generated answer %+v", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 105 || view.Citations[0].Reason != "person" {
		t.Fatalf("citations %+v", view.Citations)
	}
	for _, want := range []string{
		"Person: Nadia Tremblay",
		"Merci Nadia Tremblay Gestion Aurore inc.",
		"Direction: received",
	} {
		if !strings.Contains(w.gen.user, want) {
			t.Fatalf("planner prompt lacks %q:\n%s", want, w.gen.user)
		}
	}
}

// TestNoMatchIsNotCached proves a transient empty search does not freeze a
// question: the next ask re-searches and answers when the evidence appears.
func TestLatestPlanNormalizesToReceived(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","contact":"Nadia Tremblay","limit":1,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Total: 1, Rows: []mailquery.Row{{Key: "105", MessageID: 105, Subject: "RE: Poste Marc",
			SenderName: "Nadia Tremblay", Instant: "2026-10-20T13:06:27Z"}}}
	view, err := w.svc.Ask(t.Context(), "quel est le dernier message de Nadia Tremblay ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Plan == nil {
		t.Fatalf("view %+v", view)
	}
	if view.Plan.Direction != mailquery.DirectionReceived {
		t.Fatalf("latest plan direction = %q, want received", view.Plan.Direction)
	}
	if w.store.lastPlan.Direction != mailquery.DirectionReceived {
		t.Fatalf("executor saw direction %q", w.store.lastPlan.Direction)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 105 {
		t.Fatalf("citations %+v", view.Citations)
	}

	// An explicit sent question keeps its direction.
	w.store.plans = map[string]mailquery.Plan{}
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"sent","contact":"Nadia Tremblay","limit":1,"order":"desc"}}`
	view, err = w.svc.Ask(t.Context(), "quel est le dernier message que j'ai envoyé à Nadia Tremblay ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Plan == nil || view.Plan.Direction != mailquery.DirectionSent {
		t.Fatalf("sent plan %+v", view.Plan)
	}
}

func TestAnswerWithMarkersButEmptyUsedKeepsSources(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1), hit(2)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "one", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z", Body: "first"}
	w.store.summaries[2] = store.Summary{RowID: 2, Subject: "two", SenderName: "Bob",
		SenderAddress: "bob@example.com", Instant: "2026-10-26T10:00:00Z", Body: "second"}
	w.gen.content = `{"route":"answer","answer":"The answer is in [2].","used":[]}`

	view, err := w.svc.Ask(t.Context(), "what is the answer?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Answer != "The answer is in [1]." {
		t.Fatalf("view %+v", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 2 {
		t.Fatalf("citations %+v", view.Citations)
	}
}

func TestNoMatchIsNotCached(t *testing.T) {
	w := newWorld(t, false)
	question := "who is the laptop for?"
	w.search.result = search.Result{}
	w.gen.content = `{"route":"answer","answer":"I cannot answer from the mailbox.","used":[]}`
	view, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Notice != NoticeNoMatches || w.gen.calls != 1 {
		t.Fatalf("first ask notice=%q generator calls=%d", view.Notice, w.gen.calls)
	}

	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Laptop", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z",
		Body: "The laptop is for Sylvain."}
	w.gen.content = `{"route":"answer","answer":"The laptop is for Sylvain [1].","used":[1]}`
	view, err = w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || !strings.Contains(view.Answer, "Sylvain") {
		t.Fatalf("second ask did not answer: %+v", view)
	}
	if w.gen.calls != 2 || w.search.calls != 2 {
		t.Fatalf("generator calls=%d search calls=%d", w.gen.calls, w.search.calls)
	}
}

// TestDegradedSearchIsNotNoMatch proves a search whose semantic index is
// genuinely incomplete is reported as a warming index, never as an empty
// mailbox or a provider problem. A retrieval failure is not persisted: it is
// not an answer and must not replay after the index is healthy.
func TestDegradedSearchIsNotNoMatch(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Semantic: search.Semantic{Used: false, Reason: "index warming", Covered: 4, Eligible: 12}}
	view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeIndexWarming {
		t.Fatalf("degraded search view = %+v", view)
	}
	if view.Provider != "" || view.Model != "" || len(view.Citations) != 0 || view.Answer != "" {
		t.Fatalf("degraded search claimed a provider or evidence: %+v", view)
	}
	if w.gen.calls != 0 || w.source.calls != 0 {
		t.Fatalf("degraded search called the planner: source=%d generator=%d", w.source.calls, w.gen.calls)
	}
	if len(w.store.saved) != 0 {
		t.Fatalf("a transient retrieval notice was persisted: %+v", w.store.saved)
	}
}

// TestWarmIndexTimeoutIsNotWarming proves a timeout on a complete index is
// disclosed as a slow retrieval, not as "the search index is preparing". The
// live defect was exactly this: coverage 34938/34938, one 150 ms miss, and a
// permanent warming card.
func TestWarmIndexTimeoutIsNotWarming(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Semantic: search.Semantic{Used: false, TimedOut: true,
		Reason: "the semantic index did not finish within the semantic budget (150ms)", Covered: 12, Eligible: 12}}
	view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeRetrievalTimeout {
		t.Fatalf("timeout view = %+v", view)
	}
	if len(w.store.saved) != 0 {
		t.Fatalf("a retrieval timeout was persisted: %+v", w.store.saved)
	}
}

// TestSemanticUsedButNoCoverageIsNotNoMatch proves a query embedded against
// an empty/partial vector set is a warming index, not an empty mailbox. A
// genuinely empty mailbox (no eligible message) stays a no-match.
func TestSemanticUsedButNoCoverageIsNotNoMatch(t *testing.T) {
	t.Run("mail awaiting vectors", func(t *testing.T) {
		w := newWorld(t, false)
		w.search.result = search.Result{Semantic: search.Semantic{Used: true, Covered: 0, Eligible: 12}}
		view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Mode != ModeRetrieval || view.Notice != NoticeIndexWarming {
			t.Fatalf("empty coverage view = %+v", view)
		}
		if w.search.calls != 1 {
			t.Fatalf("a used semantic leg must not be retried: %d searches", w.search.calls)
		}
		if w.gen.calls != 0 {
			t.Fatalf("warming index called the planner %d times", w.gen.calls)
		}
	})
	t.Run("empty mailbox", func(t *testing.T) {
		w := newWorld(t, false)
		w.search.result = search.Result{Semantic: search.Semantic{Used: true, Covered: 0, Eligible: 0}}
		w.gen.content = `{"route":"answer","answer":"No evidence.","used":[]}`
		view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
		if err != nil {
			t.Fatal(err)
		}
		if view.Mode != ModeRetrieval || view.Notice != NoticeNoMatches {
			t.Fatalf("empty mailbox view = %+v", view)
		}
	})
}

// TestContractFailureIsNotRetried proves a provider contract failure is
// reported once. The output cap now covers a reasoning model's reasoning, so
// an identical retry cannot fix a truncated call; the retrieved sources are
// the honest fallback and the user is not kept waiting for a second failure.
func TestContractFailureIsNotRetried(t *testing.T) {
	w := newWorld(t, false)
	planFallback(w)
	w.gen.errOnce = generate.ErrContract

	view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if w.gen.calls != 1 {
		t.Fatalf("contract failure retried: calls=%d", w.gen.calls)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeGenerationFailed || len(view.Citations) == 0 {
		t.Fatalf("contract failure fallback %+v", view)
	}
}

// TestWaitingSearchIsNotRetried proves a waiting retrieval is one complete
// attempt, not two racing the same budget. A timed-out wait is disclosed as a
// retrieval timeout, never as a warming index.
func TestWaitingSearchIsNotRetried(t *testing.T) {
	w := newWorld(t, false)
	w.search.seq = []search.Result{
		{Semantic: search.Semantic{Used: false, TimedOut: true, Covered: 3, Eligible: 3,
			Reason: "context deadline exceeded"}},
	}

	view, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeRetrievalTimeout {
		t.Fatalf("timed-out wait view = %+v", view)
	}
	if w.search.calls != 1 {
		t.Fatalf("search calls=%d, want 1", w.search.calls)
	}
}

// TestMojibakeQuestionIsRepairedBeforeSearch proves the one question boundary
// repairs a double-encoded question before it reaches retrieval, the saved
// history, or the answer view, and that retrieval waits for a complete
// ranking instead of racing the search box's interactive budget.
func TestMojibakeQuestionIsRepairedBeforeSearch(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "messagerie-exemple", SenderName: "Messagerie Exemple",
		SenderAddress: "info@messagerie-exemple.test", Instant: "2026-10-26T09:00:00Z", Body: "We need the new quote."}
	w.gen.content = `{"route":"answer","answer":"Grounded [1].","used":[1]}`

	const want = "Résume le dernier échange avec messagerie-exemple.test"
	view, err := w.svc.Ask(t.Context(), "RÃ©sume le dernier Ã©change avec messagerie-exemple.test", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Question != want {
		t.Fatalf("view question = %q, want %q", view.Question, want)
	}
	if w.search.query.Text != want {
		t.Fatalf("searched %q, want %q", w.search.query.Text, want)
	}
	if !w.search.query.Wait {
		t.Fatal("question answering did not request a complete retrieval")
	}
	if len(w.store.saved) != 1 || w.store.saved[0].Question != want {
		t.Fatalf("saved question = %+v, want %q", w.store.saved, want)
	}
}

func TestRetrievalOnProviderFailures(t *testing.T) {
	cases := []struct {
		name     string
		set      func(*testWorld)
		notice   string
		provider string
		model    string
	}{
		{"missing generator", func(w *testWorld) { w.svc.source = nil },
			NoticeUnavailable, "", ""},
		{"missing key", func(w *testWorld) { w.source.gen = nil; w.source.err = fmt.Errorf("%w: no key", ErrUnavailable) },
			NoticeUnavailable, "", ""},
		{"budget", func(w *testWorld) { w.gen.err = engine.ErrBudget },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"unavailable", func(w *testWorld) { w.gen.err = engine.ErrUnavailable },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"timeout", func(w *testWorld) { w.gen.err = context.DeadlineExceeded },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"empty answer", func(w *testWorld) { w.gen.content = `{"route":"answer","answer":"","used":[1]}` },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"malformed JSON", func(w *testWorld) { w.gen.content = `{"route":` },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"invalid route", func(w *testWorld) { w.gen.content = `{"route":"unknown"}` },
			NoticeGenerationFailed, "fake", "fake-model"},
		{"no valid citation", func(w *testWorld) { w.gen.content = `{"route":"answer","answer":"claim","used":[9]}` },
			NoticeNoGroundedAnswer, "fake", "fake-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
			w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
			tc.set(w)
			view, err := w.svc.Ask(t.Context(), "What about this message?", "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeRetrieval || view.Notice != tc.notice || view.Answer != "" {
				t.Fatalf("view %+v, want notice %q", view, tc.notice)
			}
			if view.Provider != tc.provider || view.Model != tc.model {
				t.Fatalf("provider/model %q/%q, want %q/%q", view.Provider, view.Model, tc.provider, tc.model)
			}
			if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
				t.Fatalf("citations %+v", view.Citations)
			}
			if view.ID == 0 || w.store.saved[len(w.store.saved)-1].Mode != ModeRetrieval {
				t.Fatal("retrieval answer not persisted")
			}
			if got := w.store.saved[len(w.store.saved)-1].Notice; got != tc.notice {
				t.Fatalf("persisted notice %q, want %q", got, tc.notice)
			}
		})
	}
}

func TestSuccessfulAnswersKeepEmptyNotice(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = `{"route":"answer","answer":"Grounded [1].","used":[1]}`
	generated, err := w.svc.Ask(t.Context(), "What does the message say?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if generated.Mode != ModeGenerated || generated.Notice != "" || generated.Answer == "" {
		t.Fatalf("generated answer carries a fallback notice: %+v", generated)
	}
	if got := w.store.saved[len(w.store.saved)-1].Notice; got != "" {
		t.Fatalf("generated answer persisted notice %q", got)
	}

	w2 := newWorld(t, false)
	w2.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"count","direction":"both","time_range":"all","limit":10,"order":"desc"}}`
	computed, err := w2.svc.Ask(t.Context(), "How many messages did I receive?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if computed.Mode != ModeComputed || computed.Notice != "" {
		t.Fatalf("computed answer carries a fallback notice: %+v", computed)
	}
	if got := w2.store.saved[len(w2.store.saved)-1].Notice; got != "" {
		t.Fatalf("computed answer persisted notice %q", got)
	}
}

func TestRecentConversationsKeepsNewestDistinct(t *testing.T) {
	messages := []worldsvc.CompanyMessage{
		{MessageID: 5, ConversationID: "C-a"},
		{MessageID: 4, ConversationID: "C-b"},
		{MessageID: 3, ConversationID: "C-a"},
		{MessageID: 2, ConversationID: ""},
		{MessageID: 1, ConversationID: "C-c"},
	}
	got := recentConversations(messages, 2)
	if len(got) != 2 || got[0] != "C-a" || got[1] != "C-b" {
		t.Fatalf("recent conversations %v, want [C-a C-b]", got)
	}
	if empty := recentConversations(nil, 3); len(empty) != 0 {
		t.Fatalf("empty recent conversations %v", empty)
	}
}

func TestPlannerPromptSeparatesContentFromMetadata(t *testing.T) {
	owner := store.OwnerIdentity{Names: []string{"Amélie Durand"}, Addresses: []string{"amelie@example.test"}}
	system, _ := plannerPrompt("who is the laptop for?", "en", nil, nil, nil, nil, owner, nil, nil, time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), "UTC")
	for _, want := range []string{
		"who a task is for is a content question",
		"answer it from the numbered messages and cite them",
		"Never state a count or a ranking that is not in the numbered messages",
		"Never state a name or a date that is not in the numbered messages",
		"never from who replied later",
		"which organizations they work for",
		"exclude that one and name the other one(s)",
		"newest message that person sent",
		`action "list"`,
		`direction "received"`,
		"or the owner's own reply, the latest message from that person",
		"Owner: Amélie Durand <amelie@example.test>",
		"never describe the owner as a third party",
	} {
		if !strings.Contains(system, want) {
			t.Errorf("planner prompt lacks %q", want)
		}
	}
}

// TestAskPromptCarriesSentRecipients reproduces the reported AI-pilot
// question: the sent messages are the evidence, and every To/Cc recipient
// must reach the planner. Naming only the people who replied later is wrong.
func TestAskPromptCarriesSentRecipients(t *testing.T) {
	w := newWorld(t, false)
	question := "à quel client ai-je envoyé notre nouveau projet pilote ia ?"
	w.search.result = search.Result{Hits: []search.Hit{hit(400)}}
	w.store.summaries[400] = store.Summary{RowID: 400,
		Subject: "Acme - Projet pilote en intelligence artificielle", SenderName: "Alex Morgan",
		SenderAddress: "alex@acme.test", Instant: "2026-10-14T16:00:00Z", Role: mail.RoleSent,
		Body: "Bonjour, voici notre nouveau projet pilote en intelligence artificielle."}
	w.store.recipients[400] = []mail.Recipient{
		{Kind: mail.To, Name: "Nicolas Dupont", Address: "ncote@example.com"},
		{Kind: mail.To, Name: "Alex Vachon", Address: "avachon@example.com"},
		{Kind: mail.Cc, Name: "Marc Dupont", Address: "mcote@example.com"},
		{Kind: mail.Bcc, Name: "Direction", Address: "direction@example.com"},
	}
	w.gen.content = `{"route":"answer","answer":"Nicolas Dupont, Alex Vachon, Marc Dupont et Direction [1].","used":[1]}`

	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || !strings.Contains(view.Answer, "Nicolas Dupont") {
		t.Fatalf("generated answer %+v", view)
	}
	for _, want := range []string{
		"Direction: sent by the owner",
		"To Nicolas Dupont <ncote@example.com>",
		"Alex Vachon <avachon@example.com>",
		"Cc Marc Dupont <mcote@example.com>",
		"Bcc Direction <direction@example.com>",
	} {
		if !strings.Contains(w.gen.user, want) {
			t.Errorf("planner prompt lacks recipient %q:\n%s", want, w.gen.user)
		}
	}
}

// TestAskPromptCarriesEveryStoredDimension is the one context bundle Ask
// renders: direction, attachments, addressees, learned labels, the resolved
// thread state with its visibility and count, stored read/importance state
// and the organization profile facts. A manual label is the fact owner, so
// the judge's answer to the same question must never reach the prompt beside
// it.
func TestAskPromptCarriesEveryStoredDimension(t *testing.T) {
	w := newWorld(t, false)
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord",
		Domains: []string{"atelier-nord.test"}, Messages: 12, Threads: 4}
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{org}},
		profiles: map[string]worldsvc.ProfileView{org.Key: {
			Key: org.Key, Label: org.Label, Domains: org.Domains,
			Messages: 12, Sent: 5, Received: 7, Threads: 4,
			FirstAt: "2026-01-02T09:00:00Z", LastAt: "2026-10-21T10:00:00Z",
			ThreadStates: map[string]int{string(threadintel.AnsweredByOther): 1, string(threadintel.AwaitingOwner): 1},
			OpenThreads: []worldsvc.CompanyThread{{
				ConversationID: "C-500", Subject: "Spring launch",
				LastAt: "2026-10-21T10:00:00Z", State: string(threadintel.AnsweredByOther), MessageID: 500,
			}},
			People: []worldsvc.CompanyPerson{{
				Name: "Client", Address: "client@example.com", Messages: 7,
			}},
			Events: []worldsvc.CalendarEvent{{
				Subject: "Review", Start: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
			}},
			Deadlines: []worldsvc.Deadline{{
				Subject: "Contract", Due: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
			}},
			Recent: []worldsvc.CompanyMessage{{
				MessageID: 500, ConversationID: "C-500", Subject: "Spring launch",
				SenderName: "Alex Morgan", Instant: "2026-10-20T09:00:00Z", Sent: true,
			}},
		}},
	})

	question := "What did we send about the spring launch for Atelier Nord?"
	w.store.threads = map[string]store.ThreadMessagesResult{"C-500": {
		ConversationID: "C-500", Subject: "Spring launch", FirstAt: "2026-10-20T09:00:00Z",
		Participants: []store.ThreadParticipant{{Name: "Client", Address: "client@example.com", Messages: 2}},
		Messages: []store.ThreadMessage{{
			ID: 500, ConversationID: "C-500", Subject: "Spring launch",
			SenderName: "Alex Morgan", SenderAddress: "alex@example.com",
			ReceivedAt: "2026-10-20T09:00:00Z", Sent: true,
		}},
	}}
	w.store.summaries[500] = store.Summary{RowID: 500, Subject: "Spring launch",
		SenderName: "Alex Morgan", SenderAddress: "alex@example.com",
		Instant: "2026-10-20T09:00:00Z", Role: mail.RoleSent, HasAttachments: true,
		Body: "Here is the spring launch plan."}
	w.store.details[500] = store.ContextDetail{
		ConversationID: "C-500",
		Recipients: []mail.Recipient{
			{Kind: mail.To, Name: "Client", Address: "client@example.com"},
			{Kind: mail.Cc, Name: "Boss", Address: "boss@example.com"},
			{Kind: mail.Bcc, Name: "Archive", Address: "archive@example.com"},
		},
		Labels: map[string]store.LabelFact{
			"category":  {Value: `"marketing"`, Source: store.LabelManual},
			"sentiment": {Value: `"positive"`, Source: store.LabelJudge},
			"urgency":   {Value: `2`, Source: store.LabelJudge},
		},
		IsRead:     false,
		Importance: 2,
	}
	w.store.states = map[string]threadintel.Assessment{"C-500": {
		ConversationID: "C-500", State: threadintel.AnsweredByOther, Reason: threadintel.ReasonOtherReplied,
		ActorName: "Client", ActorAddress: "client@example.com",
		LastAt: "2026-10-21T10:00:00Z", LastMessageID: 500,
		OwnerActorName: "Client", OwnerActorAddress: "client@example.com",
		OwnerLastAt: "2026-10-21T10:00:00Z", OwnerLastMessageID: 500,
		Visibility: threadintel.Partial, Messages: 4,
	}}
	w.gen.content = `{"route":"answer","answer":"The spring launch went to the marketing list [1].","used":[1]}`

	view, err := w.svc.Ask(t.Context(), question, "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated {
		t.Fatalf("generated view %+v", view)
	}
	for _, want := range []string{
		"Direction: sent by the owner",
		"Unread: yes",
		"Importance: high",
		"Attachments: yes",
		"To/Cc: To Client <client@example.com> | Cc Boss <boss@example.com> | Bcc Archive <archive@example.com>",
		"Learned: manual category=marketing; judge sentiment=positive; judge urgency=2",
		"Organization facts:",
		"Organization: Atelier Nord; messages=12 (sent=5, received=7), threads=4, first=2026-01-02, last=2026-10-21",
		"Open threads: 1;",
		"answered_by_other=1",
		"People: Client <client@example.com> (7)",
		"Events: Review (2026-11-02 09:00)",
		"Deadlines: Contract (2026-10-30)",
		"Thread states (deterministic; newest first):",
		"answered_by_other",
		"4 messages",
		"visibility partial",
	} {
		if !strings.Contains(w.gen.user, want) {
			t.Errorf("planner prompt lacks %q:\n%s", want, w.gen.user)
		}
	}
	// ContextDetails collapses a manual+judge pair on one question to the
	// manual fact (TestContextDetailsReadsOwnerContext proves the precedence);
	// the prompt must carry neither a second label block nor a judge category.
	if strings.Contains(w.gen.user, "judge category") || strings.Contains(w.gen.user, "Conversation: C-500") {
		t.Fatalf("prompt leaked a shadowed label or a per-message conversation id:\n%s", w.gen.user)
	}
}

func TestMissingContextDetailsNeverInventReadState(t *testing.T) {
	w := newWorld(t, false)
	w.store.omitDetails = true
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Laptop", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z",
		Body: "The laptop is for Sylvain."}
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.gen.content = `{"route":"answer","answer":"The laptop is for Sylvain [1].","used":[1]}`

	if _, err := w.svc.Ask(t.Context(), "who is the laptop for?", "en"); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"Unread:", "Importance:"} {
		if strings.Contains(w.gen.user, absent) {
			t.Fatalf("a failed context read invented %q:\n%s", absent, w.gen.user)
		}
	}
}

func TestGroundAnswerRenumbersAndCompletesCitations(t *testing.T) {
	msgs := make([]contextMessage, 8)
	for i := range msgs {
		msgs[i] = contextMessage{ID: int64(i + 1), Subject: fmt.Sprintf("subject %d", i+1), Reason: "retrieved"}
	}
	answer := "A [3], B [5], C [3], D [8]."
	got, citations := groundAnswer(answer, msgs, []int{3, 5})
	if want := "A [1], B [2], C [1], D [3]."; got != want {
		t.Fatalf("answer %q, want %q", got, want)
	}
	if len(citations) != 3 || citations[0].MessageID != 3 || citations[1].MessageID != 5 || citations[2].MessageID != 8 {
		t.Fatalf("citations %+v", citations)
	}

	// The reported failure: the answer cites [5] but the model's used list
	// omitted it, so the UI showed no source for that chip. The cited
	// message must become a source.
	got, citations = groundAnswer("The project is in [5].", msgs, []int{2})
	if got != "The project is in [1]." {
		t.Fatalf("answer %q", got)
	}
	if len(citations) != 1 || citations[0].MessageID != 5 {
		t.Fatalf("missing cited message source: %+v", citations)
	}

	// An out-of-range marker is removed, never shown as a dead chip.
	got, citations = groundAnswer("Claim [9].", msgs, nil)
	if strings.Contains(got, "[9]") || len(citations) != 0 {
		t.Fatalf("invalid marker survived: %q %+v", got, citations)
	}

	// No marker: the model's declared sources still populate the list, once.
	_, citations = groundAnswer("No inline marker.", msgs, []int{2, 2, 4})
	if len(citations) != 2 || citations[0].MessageID != 2 || citations[1].MessageID != 4 {
		t.Fatalf("declared sources %+v", citations)
	}
}

func TestLabelLineFormatsJSONValues(t *testing.T) {
	got := labelLine(map[string]store.LabelFact{
		"sentiment": {Value: `"marketing"`, Source: store.LabelManual},
		"topic":     {Value: `null`, Source: store.LabelJudge},
		"urgency":   {Value: `2`, Source: store.LabelJudge},
		"urgent":    {Value: `true`, Source: store.LabelJudge},
	})
	if want := "manual sentiment=marketing; judge urgency=2; judge urgent=true"; got != want {
		t.Fatalf("label line %q, want %q", got, want)
	}
	if empty := labelLine(nil); empty != "" {
		t.Fatalf("empty labels rendered %q", empty)
	}
}

func TestLabelValueDecodesExactlyOneDocument(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{`"marketing"`, "marketing", true},
		{`2`, "2", true},
		{`true`, "true", true},
		{`{"a": 1}`, `{"a":1}`, true},
		{`null`, "", false},
		{``, "", false},
		{`{`, "", false},
		{`1 2`, "", false},
		{`{"a": 1}{"b": 2}`, "", false},
	} {
		got, ok := labelValue(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Errorf("labelValue(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCompanyProfileQuestionGuard(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord", Domains: []string{"atelier-nord.test"}}
	profile := []string{
		"Parle-moi d'Atelier Nord",
		"Qu'est-ce que je sais sur Atelier Nord ?",
		"Combien de messages avec Atelier Nord ?",
		"How many messages from Atelier Nord?",
		"Combien de personnes chez Atelier Nord ?",
		"Quel est le dernier contact avec Atelier Nord ?",
		"Quand ai-je parlé à Atelier Nord la dernière fois ?",
		"Quels sont les fils ouverts avec Atelier Nord ?",
		"Où en est Atelier Nord ?",
		"Fais-moi un topo d'Atelier Nord",
		"Atelier Nord",
		"What about Atelier Nord?",
		"Where does Atelier Nord stand?",
		"Show me atelier-nord.test",
		"How many conversations are waiting on my reply from Atelier Nord?",
	}
	for _, question := range profile {
		if !companyProfileQuestion(question, org) {
			t.Errorf("%q must be answered from the profile", question)
		}
	}
	specific := []string{
		"je dois préparer un portable pour rive-education, c'est pour qui déjà ?",
		"Je dois préparer un portable pour Atelier Nord, c'est pour qui déjà ?",
		"I need to prepare a laptop for Atelier Nord, who is it for again?",
		"Quel est le prix du portable chez Atelier Nord ?",
		"Est-ce que Atelier Nord a payé la facture ?",
		"Quel est l'état du projet avec Atelier Nord ?",
		"Depuis quand connais-je Atelier Nord ?",
		"Qui est le contact pour la facture chez Atelier Nord ?",
		"Qui est la personne responsable chez Atelier Nord ?",
		"Who is the contact at Atelier Nord?",
		"Quand a-t-on livré le serveur à Atelier Nord ?",
		"Quel est le dernier message d'Atelier Nord ?",
		"What did Atelier Nord say?",
		"What did Atelier Nord tell me?",
		"Did Atelier Nord respond?",
		"Est-ce que Atelier Nord a répondu ?",
		"Did Atelier Nord reply?",
		"Qui a répondu chez Atelier Nord ?",
		"Résume les derniers messages d'Atelier Nord",
		"Atelier Nord blah",
	}
	for _, question := range specific {
		if companyProfileQuestion(question, org) {
			t.Errorf("%q must not be answered from the profile", question)
		}
	}
}

// TestCompanyGuardsDeriveFromIntentVocabulary prevents the profile guard from
// drifting from the parser: every synonym the intent owners gain must reach
// the matching company guard too.
func TestCompanyGuardsDeriveFromIntentVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner []string
		guard map[string]bool
	}{
		{"message", messageWords, companyMessageWords},
		{"latest", latestWords, companyLatestWords},
		{"content", withoutWords(contentVerbs, "tell", "ask", "write", "mention"), companyContentVerbs},
		{"reply", intentWords[IntentReply], companyReplyWords},
	} {
		for _, word := range tc.owner {
			if !tc.guard[word] {
				t.Errorf("company %s guard is missing the intent word %q", tc.name, word)
			}
		}
	}
}

func TestLinkedProfileQuestionDoesNotFallThroughToMailboxFilter(t *testing.T) {
	w := newWorld(t, false)
	w.svc.SetCompanies(companyBook())
	view, err := w.svc.Ask(t.Context(), "Quelles sont les échéances d'Atelier Nord ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != IntentCompany {
		t.Fatalf("profile question fell through to %s/%s: %+v", view.Mode, view.Intent, view)
	}
	if w.prio.calls != 0 {
		t.Fatalf("a linked profile question read the mailbox-wide priority filters: %v", w.prio.filters)
	}
}

func TestCompanySpecificFactIsAnsweredFromTheOrganizationThread(t *testing.T) {
	w := newWorld(t, false)
	companies := &fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{{
			Key: "c:rive education", Label: "Rive Éducation", Domains: []string{"rive-education.test"}, Messages: 409, Threads: 110,
		}}},
		profiles: map[string]worldsvc.ProfileView{"c:rive education": {
			Key: "c:rive education", Label: "Rive Éducation", Messages: 409, Threads: 110,
			Recent: []worldsvc.CompanyMessage{{
				MessageID: 34967, ConversationID: "C-laptop", Subject: "RE: Paramétrer ordi",
				SenderName: "Alex Morgan", Instant: "2026-10-26T15:53:08Z",
			}},
		}},
	}
	w.svc.SetCompanies(companies)

	// The organization's latest conversation is the laptop task; its first
	// message names the employee the laptop is being prepared for.
	w.search.result = search.Result{Hits: []search.Hit{hit(34967)}}
	w.store.threads = map[string]store.ThreadMessagesResult{"C-laptop": {
		ConversationID: "C-laptop", Subject: "RE: Paramétrer ordi",
		Messages: []store.ThreadMessage{
			{ID: 97, Subject: "Paramétrer ordi", SenderName: "Jonathan Vachon",
				SenderAddress: "jvincent@rive-education.test", ReceivedAt: "2026-10-19T18:28:04Z",
				Snippet: "Lissa passera te porter l'ordinateur d'un nouvel employé. Son nom est Samuel Girard."},
			{ID: 34967, Subject: "RE: Paramétrer ordi", SenderName: "Alex Morgan",
				SenderAddress: "alex@acme.test", ReceivedAt: "2026-10-26T15:53:08Z", Sent: true,
				Snippet: "Parfait pour moi."},
		},
	}}
	w.store.summaries[97] = store.Summary{RowID: 97, Subject: "Paramétrer ordi", SenderName: "Jonathan Vachon",
		SenderAddress: "jvincent@rive-education.test", Instant: "2026-10-19T18:28:04Z",
		Body: "Lissa passera te porter l'ordinateur d'un nouvel employé (arrivée 2 nov). " +
			"J'aimerais que tu paramètres l'ordi comme un neuf et que tu crées son compte. Son nom est Samuel Girard."}
	w.store.summaries[34967] = store.Summary{RowID: 34967, Subject: "RE: Paramétrer ordi", SenderName: "Alex Morgan",
		SenderAddress: "alex@acme.test", Instant: "2026-10-26T15:53:08Z", Body: "Parfait pour moi."}
	w.gen.content = `{"route":"answer","answer":"Le portable est pour Samuel Girard [1].","used":[1]}`

	view, err := w.svc.Ask(t.Context(),
		"je dois préparer un portable pour rive-education, c'est pour qui déjà ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent == IntentCompany || view.Mode == ModeComputed {
		t.Fatalf("a specific fact was answered from the company profile: %+v", view)
	}
	if view.Mode != ModeGenerated || !strings.Contains(view.Answer, "Samuel Girard") {
		t.Fatalf("generated answer %+v", view)
	}
	if !strings.Contains(w.gen.user, "Samuel Girard") || !strings.Contains(w.gen.user, "Paramétrer ordi") {
		t.Fatalf("the planner prompt lacks the organization thread evidence:\n%s", w.gen.user)
	}
	if w.search.query.Filter.Sender != "rive-education.test" || !w.search.query.Filter.OwnerOnly {
		t.Fatalf("retrieval was not scoped to the organization: %+v", w.search.query.Filter)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 97 || view.Citations[0].Reason != "company" {
		t.Fatalf("citations %+v", view.Citations)
	}
	if _, profileCalls := companies.calls(); profileCalls != 1 {
		t.Fatalf("company profile reads %d, want one bounded retrieval hint", profileCalls)
	}
}

// TestCompanyEvidenceGroundsReplyState reproduces the reported Cirrus
// question at the retrieval boundary: the last exchange is the owner's sent
// request followed by the organization's received reply, and the prompt
// carries both directions plus the resolver's thread state. The planner can
// therefore answer a yes/no question instead of listing the organization's
// mail.
func TestCompanyEvidenceGroundsReplyState(t *testing.T) {
	w := newWorld(t, false)
	org := worldsvc.CompanyRow{Key: "d:cirrus-media.test", Label: "cirrus-media.test",
		Domains: []string{"cirrus-media.test"}, Messages: 107, Threads: 37}
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{org}},
		profiles: map[string]worldsvc.ProfileView{org.Key: {
			Key: org.Key, Label: org.Label, Domains: org.Domains, Messages: 107, Threads: 37,
			Recent: []worldsvc.CompanyMessage{
				{MessageID: 58, ConversationID: "C-cpanel", Subject: "Re: Accès cPanel - quartz-electronique.test",
					SenderName: "Dominic Marchand", SenderAddress: "dmorin@cirrus-media.test", Instant: "2026-10-21T18:25:19Z"},
				{MessageID: 20696, ConversationID: "C-cpanel", Subject: "Accès cPanel - quartz-electronique.test",
					SenderName: "Alex Morgan", SenderAddress: "alex@acme.test", Instant: "2026-10-19T16:52:32Z", Sent: true},
			},
		}},
	})
	w.store.threads = map[string]store.ThreadMessagesResult{"C-cpanel": {
		ConversationID: "C-cpanel", Subject: "Re: Accès cPanel - quartz-electronique.test",
		Messages: []store.ThreadMessage{
			{ID: 20696, ConversationID: "C-cpanel", Subject: "Accès cPanel - quartz-electronique.test",
				SenderName: "Alex Morgan", SenderAddress: "alex@acme.test",
				ReceivedAt: "2026-10-19T16:52:32Z", Sent: true},
			{ID: 58, ConversationID: "C-cpanel", Subject: "Re: Accès cPanel - quartz-electronique.test",
				SenderName: "Dominic Marchand", SenderAddress: "dmorin@cirrus-media.test",
				ReceivedAt: "2026-10-21T18:25:19Z"},
		},
	}}
	w.store.states = map[string]threadintel.Assessment{"C-cpanel": {
		ConversationID: "C-cpanel", State: threadintel.AnsweredByOther, Reason: threadintel.ReasonOtherReplied,
		ActorName: "Dominic Marchand", ActorAddress: "dmorin@cirrus-media.test",
		OwnerActorName: "Dominic Marchand", OwnerActorAddress: "dmorin@cirrus-media.test",
		LastAt: "2026-10-21T18:25:19Z", LastMessageID: 58, Visibility: threadintel.Complete,
	}}
	w.store.summaries[20696] = store.Summary{RowID: 20696, Subject: "Accès cPanel - quartz-electronique.test",
		SenderName: "Alex Morgan", SenderAddress: "alex@acme.test",
		Instant: "2026-10-19T16:52:32Z", Role: mail.RoleSent, Body: "Peux-tu me donner accès au cPanel ?"}
	w.store.summaries[58] = store.Summary{RowID: 58, Subject: "Re: Accès cPanel - quartz-electronique.test",
		SenderName: "Dominic Marchand", SenderAddress: "dmorin@cirrus-media.test",
		Instant: "2026-10-21T18:25:19Z", Body: "C'est fait, accès accordé."}
	w.search.result = search.Result{Hits: []search.Hit{hit(58)}}

	question := "est-ce que cirrus a répondu a ma dernière requête ?"
	msgs, threads, facts, err := w.svc.companyEvidence(t.Context(), question, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 {
		t.Fatalf("company evidence = %+v", msgs)
	}
	if msgs[0].ID != 20696 || !msgs[0].Sent {
		t.Fatalf("first evidence message is not the owner's request: %+v", msgs[0])
	}
	if msgs[1].ID != 58 || msgs[1].Sent {
		t.Fatalf("second evidence message is not the organization's reply: %+v", msgs[1])
	}
	if len(threads) != 1 || threads[0].State != threadintel.AnsweredByOther ||
		threads[0].ActorAddr != "dmorin@cirrus-media.test" || threads[0].MessageID != 58 {
		t.Fatalf("thread evidence = %+v", threads)
	}

	system, user := plannerPrompt(question, "fr", msgs, threads, facts, &org, store.OwnerIdentity{}, nil, nil,
		time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), "UTC")
	for _, want := range []string{
		"Organization scope: cirrus-media.test (cirrus-media.test)",
		"Thread states (deterministic; newest first):",
		"last message from Dominic Marchand <dmorin@cirrus-media.test>",
		`Direction: sent by the owner`,
		`Direction: received`,
		"Never return a message list or a count for a yes/no question",
		"Every numbered message states its direction",
	} {
		if !strings.Contains(system+"\n"+user, want) {
			t.Errorf("planner prompt lacks %q:\n%s\n%s", want, system, user)
		}
	}
}

func TestCompanyEvidenceCarriesOrganizationFacts(t *testing.T) {
	w := newWorld(t, false)
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord",
		Domains: []string{"atelier-nord.test"}, Messages: 9, Threads: 3}
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{org}},
		profiles: map[string]worldsvc.ProfileView{org.Key: {
			Key: org.Key, Label: org.Label, Domains: org.Domains,
			Messages: 9, Sent: 4, Received: 5, Threads: 3,
			FirstAt: "2026-01-02T09:00:00Z", LastAt: "2026-10-20T09:00:00Z",
			ThreadStates: map[string]int{
				string(threadintel.AwaitingOwner):   2,
				string(threadintel.AwaitingOther):   1,
				string(threadintel.AnsweredByOwner): 1,
			},
			OpenThreads: []worldsvc.CompanyThread{
				{ConversationID: "C-1", Subject: "Follow-up", MessageID: 31},
				{ConversationID: "C-2", Subject: "Quote", MessageID: 32},
			},
			People: []worldsvc.CompanyPerson{
				{Name: "Bob Smith", Address: "bob@atelier-nord.test", Messages: 7},
				{Address: "alice@atelier-nord.test", Messages: 3},
			},
			Events: []worldsvc.CalendarEvent{{
				Subject: "Review", Start: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
			}},
			Deadlines: []worldsvc.Deadline{{
				Subject: "Contract", Due: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
			}},
			Recent: []worldsvc.CompanyMessage{{
				MessageID: 31, ConversationID: "C-1", Subject: "Follow-up",
				SenderName: "Bob Smith", Instant: "2026-10-20T09:00:00Z",
			}},
		}},
	})
	w.store.threads = map[string]store.ThreadMessagesResult{"C-1": {
		ConversationID: "C-1", Subject: "Follow-up", FirstAt: "2026-10-20T09:00:00Z",
		Messages: []store.ThreadMessage{{
			ID: 31, ConversationID: "C-1", Subject: "Follow-up",
			SenderName: "Bob Smith", SenderAddress: "bob@atelier-nord.test",
			ReceivedAt: "2026-10-20T09:00:00Z",
		}},
	}}
	w.store.summaries[31] = store.Summary{RowID: 31, Subject: "Follow-up",
		SenderName: "Bob Smith", SenderAddress: "bob@atelier-nord.test",
		Instant: "2026-10-20T09:00:00Z", Body: "Here is the quote."}

	question := "What did Atelier Nord say about the launch?"
	msgs, threads, facts, err := w.svc.companyEvidence(t.Context(), question, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("company evidence is empty")
	}
	if facts == nil || facts.Label != "Atelier Nord" || facts.Messages != 9 || facts.Sent != 4 ||
		facts.Received != 5 || facts.Threads != 3 || facts.OpenThreads != 3 ||
		facts.FirstAt != "2026-01-02" || facts.LastAt != "2026-10-20" {
		t.Fatalf("organization facts %+v", facts)
	}
	if len(facts.People) != 2 || facts.People[1].Address != "alice@atelier-nord.test" ||
		facts.People[1].Name != "" {
		t.Fatalf("organization people %+v", facts.People)
	}

	system, user := plannerPrompt(question, "en", msgs, threads, facts, &org, store.OwnerIdentity{}, nil, nil,
		time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), "UTC")
	for _, want := range []string{
		"Organization facts:",
		"Organization: Atelier Nord; messages=9 (sent=4, received=5), threads=3, first=2026-01-02, last=2026-10-20",
		"Open threads: 3; awaiting_owner=2, awaiting_other=1, answered_by_owner=1, " +
			"answered_by_other=0, no_action=0, unknown=0",
		"People: Bob Smith <bob@atelier-nord.test> (7), <alice@atelier-nord.test> (3)",
		"Events: Review (2026-11-02 09:00); Deadlines: Contract (2026-10-30)",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("planner prompt lacks %q:\n%s", want, user)
		}
	}
	if !strings.Contains(system, "organization facts") {
		t.Errorf("system prompt lacks the organization-facts rule")
	}
}

// TestSignalsReachCompanyMessages proves the priority ranking annotates the
// organization evidence: a message the ranking carries renders its stored
// signals, and a message outside the ranking keeps none.
func TestSignalsReachCompanyMessages(t *testing.T) {
	w := newWorld(t, false)
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord",
		Domains: []string{"atelier-nord.test"}, Messages: 2, Threads: 1}
	w.svc.SetCompanies(&fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{org}},
		profiles: map[string]worldsvc.ProfileView{org.Key: {
			Key: org.Key, Label: org.Label, Domains: org.Domains, Messages: 2, Threads: 1,
			Recent: []worldsvc.CompanyMessage{{
				MessageID: 41, ConversationID: "C-sig", Subject: "Devis",
				SenderName: "Bob Smith", Instant: "2026-10-20T09:00:00Z",
			}},
		}},
	})
	w.store.threads = map[string]store.ThreadMessagesResult{"C-sig": {
		ConversationID: "C-sig", Subject: "Devis",
		Messages: []store.ThreadMessage{
			{ID: 41, ConversationID: "C-sig", Subject: "Devis", SenderName: "Bob Smith",
				SenderAddress: "bob@atelier-nord.test", ReceivedAt: "2026-10-19T09:00:00Z"},
			{ID: 42, ConversationID: "C-sig", Subject: "Devis", SenderName: "Bob Smith",
				SenderAddress: "bob@atelier-nord.test", ReceivedAt: "2026-10-20T09:00:00Z"},
		},
	}}
	for _, id := range []int64{41, 42} {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: "Devis", SenderName: "Bob Smith",
			SenderAddress: "bob@atelier-nord.test", Instant: "2026-10-20T09:00:00Z", Body: "body"}
	}
	w.prio.top = []priority.Item{{MessageID: 41, Subject: "Devis",
		Signals: priority.Signals{Urgency: 2, ReplyNeeded: 1, Category: "work", AsksFor: "quote"}}}

	question := "Quel est le prix du devis Atelier Nord ?"
	msgs, _, facts, err := w.svc.companyEvidence(t.Context(), question, org)
	if err != nil {
		t.Fatal(err)
	}
	var signalled bool
	for _, m := range msgs {
		if m.ID == 41 {
			signalled = m.Signals != nil
		}
		if m.ID == 42 && m.Signals != nil {
			t.Fatalf("an unranked message invented signals: %+v", m)
		}
	}
	if !signalled {
		t.Fatalf("the ranked company message lost its signals: %+v", msgs)
	}

	_, user := plannerPrompt(question, "fr", msgs, nil, facts, &org, store.OwnerIdentity{}, nil, nil,
		time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), "UTC")
	for _, want := range []string{"Priority signals:", "urgency=2", "reply_needed=1",
		"category=work", "asks_for=quote"} {
		if !strings.Contains(user, want) {
			t.Errorf("planner prompt lacks %q:\n%s", want, user)
		}
	}
}

func TestCompanySpecificFactWithoutEvidenceIsHonest(t *testing.T) {
	w := newWorld(t, false)
	companies := &fakeCompanies{list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{{
		Key: "c:rive education", Label: "Rive Éducation", Domains: []string{"rive-education.test"}, Messages: 409, Threads: 110,
	}}}}
	w.svc.SetCompanies(companies)
	w.source.err = ErrUnavailable
	w.source.calls = 0
	w.search.result = search.Result{}

	view, err := w.svc.Ask(t.Context(),
		"je dois préparer un portable pour rive-education, c'est pour qui déjà ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent == IntentCompany || view.Answer != "" || len(view.Citations) != 0 {
		t.Fatalf("a no-evidence question produced an answer: %+v", view)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoMatches {
		t.Fatalf("no-evidence view %+v", view)
	}
}

func TestCompanyProfileQuestionAnswersFromProfile(t *testing.T) {
	cases := []struct {
		name     string
		language string
		question string
		wants    []string
	}{
		{
			name: "french label", language: "fr", question: "Qu'est-ce que je sais sur Atelier Nord ?",
			wants: []string{"Avec Atelier Nord", "12 messages échangés", "3 personnes", "4 conversations",
				"2 conversations attendent votre réponse", "1 attend la leur", "Dernier contact le 20/10/2026",
				"1 échéance à venir", "1 rendez-vous à venir"},
		},
		{
			name: "english domain", language: "en", question: "What do I know about atelier-nord.test?",
			wants: []string{"With Atelier Nord", "12 messages exchanged", "3 people", "4 conversations",
				"2 conversations are waiting on your reply", "1 is waiting on theirs", "Last contact on 2026-10-20",
				"1 upcoming commitment", "1 upcoming event"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			companies := companyBook()
			w.svc.SetCompanies(companies)
			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeComputed || view.Intent != IntentCompany || view.Provider != "" || view.Model != "" ||
				view.Notice != "" {
				t.Fatalf("company view %+v", view)
			}
			for _, want := range tc.wants {
				if !strings.Contains(view.Answer, want) {
					t.Fatalf("answer is missing %q: %s", want, view.Answer)
				}
			}
			for _, raw := range []string{"c:atelier nord", "awaiting_owner", "awaiting_other", "answered_by_owner", "declared"} {
				if strings.Contains(view.Answer, raw) {
					t.Fatalf("answer leaked %q: %s", raw, view.Answer)
				}
			}
			wantCounts := map[string]int{"messages": 12, "people": 3, "threads": 4, "awaiting_owner": 2, "awaiting_other": 1}
			if !reflect.DeepEqual(view.Counts, wantCounts) {
				t.Fatalf("counts %+v, want %+v", view.Counts, wantCounts)
			}
			if len(view.Citations) != 3 || view.Citations[0].MessageID != 11 || view.Citations[1].MessageID != 12 ||
				view.Citations[2].MessageID != 13 {
				t.Fatalf("citations %+v", view.Citations)
			}
			if view.Citations[2].Subject != "Follow-up" || view.Citations[2].ReceivedAt != "2026-10-19T09:00:00Z" {
				t.Fatalf("open-thread citation %+v", view.Citations[2])
			}
			if w.gen.calls != 0 || w.search.calls != 0 || w.source.calls != 0 {
				t.Fatalf("company answer called the planner: generator=%d search=%d source=%d",
					w.gen.calls, w.search.calls, w.source.calls)
			}
			listCalls, profileCalls := companies.calls()
			if listCalls != 1 || profileCalls != 1 {
				t.Fatalf("company book calls list=%d profile=%d", listCalls, profileCalls)
			}
			if companies.lastKey != "c:atelier nord" || companies.lastMonths != worldsvc.DefaultCompanyMonths ||
				companies.lastEventDays != worldsvc.DefaultCompanyEventDays {
				t.Fatalf("profile call key=%q months=%d days=%d", companies.lastKey,
					companies.lastMonths, companies.lastEventDays)
			}
			if view.ID == 0 || len(w.store.saved) != 1 || w.store.saved[0].Intent != IntentCompany ||
				w.store.saved[0].Mode != ModeComputed {
				t.Fatalf("company answer not persisted: %+v", w.store.saved)
			}

			// The freeform answer cache serves the repeated question before
			// the organization book is read again.
			again, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			listCalls, profileCalls = companies.calls()
			if listCalls != 1 || profileCalls != 1 {
				t.Fatalf("cached company answer read the book: list=%d profile=%d", listCalls, profileCalls)
			}
			if again.ID == 0 || again.ID == view.ID || again.Intent != IntentCompany ||
				again.Answer != view.Answer || !reflect.DeepEqual(again.Citations, view.Citations) {
				t.Fatalf("cached company view diverged: first=%+v again=%+v", view, again)
			}
			if len(w.store.saved) != 2 {
				t.Fatalf("cached company ask saved %d rows", len(w.store.saved))
			}
		})
	}
}

func TestCompanyAnswerNeverCallsOverdueCommitmentsUpcoming(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	profile := companies.profiles["c:atelier nord"]
	profile.Deadlines = []worldsvc.Deadline{
		{MessageID: 21, Subject: "Overdue invoice", Due: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
		{MessageID: 22, Subject: "Renewal", Due: time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)},
	}
	companies.profiles["c:atelier nord"] = profile
	w.svc.SetCompanies(companies)

	fr, err := w.svc.Ask(t.Context(), "Où en est Atelier Nord ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fr.Answer, "1 échéance en retard, 1 échéance à venir.") {
		t.Fatalf("french deadlines not split: %s", fr.Answer)
	}
	if strings.Contains(fr.Answer, "2 échéances à venir") {
		t.Fatalf("overdue deadline narrated as upcoming: %s", fr.Answer)
	}

	en, err := w.svc.Ask(t.Context(), "Where does Atelier Nord stand?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(en.Answer, "1 overdue commitment, 1 upcoming commitment.") {
		t.Fatalf("english deadlines not split: %s", en.Answer)
	}
}

func TestCompanyBookCanBeInjectedAtConstruction(t *testing.T) {
	cfg := configtest.New(t, nil)
	companies := companyBook()
	svc, err := New(cfg, Deps{
		Store:     &fakeStore{summaries: map[int64]store.Summary{}, plans: map[string]mailquery.Plan{}},
		Priority:  &fakePriority{byFilter: map[priority.Filter][]priority.Item{}, totals: map[priority.Filter]int{}},
		Search:    &fakeSearch{},
		Gate:      &fakeGate{},
		Companies: companies,
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Ask(t.Context(), "Tell me about Atelier Nord", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent != IntentCompany || view.Mode != ModeComputed || !strings.Contains(view.Answer, "With Atelier Nord") {
		t.Fatalf("construction injection view %+v", view)
	}
}

func TestCompanyLinkRequiresTokenBoundary(t *testing.T) {
	for _, question := range []string{"What about atelieresque?", "The subatelier project"} {
		t.Run(question, func(t *testing.T) {
			w := newWorld(t, false)
			companies := companyBook()
			w.svc.SetCompanies(companies)
			planFallback(w)
			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Intent == IntentCompany {
				t.Fatalf("inner substring selected a company: %+v", view)
			}
			if _, profileCalls := companies.calls(); profileCalls != 0 {
				t.Fatalf("inner substring read %d profiles", profileCalls)
			}
			if w.gen.calls != 1 || w.search.calls != 1 || view.Mode != ModeGenerated {
				t.Fatalf("planner fallback: mode=%s generator=%d search=%d", view.Mode, w.gen.calls, w.search.calls)
			}
		})
	}
}

func TestCompanyLinkMatchesLabelAndDomainSpellings(t *testing.T) {
	companies := []worldsvc.CompanyRow{
		{Key: "c:clinique", Label: "Clinique Exemple", Domains: []string{"clinique-exemple.test"}},
	}
	for _, question := range []string{
		"Quel est l'état du projet avec Clinique Exemple ?",
		"Qu'est-ce que je dois à CLINIQUE EXEMPLE ?",
		"Show me clinique-exemple.test",
		"What is happening with clinique-exemple?",
	} {
		org, ok := matchCompany(question, companies)
		if !ok || org.Key != "c:clinique" {
			t.Errorf("%q selected %+v ok=%v, want c:clinique", question, org, ok)
		}
	}
	for _, question := range []string{"What about cliniqueexempleen?", "The subcliniqueexemple project"} {
		if org, ok := matchCompany(question, companies); ok {
			t.Errorf("%q matched inside a longer word: %+v", question, org)
		}
	}
}

func TestCompanyLinkMatchesUniqueDomainPrefix(t *testing.T) {
	companies := []worldsvc.CompanyRow{
		{Key: "d:cirrus-media.test", Label: "cirrus-media.test", Domains: []string{"cirrus-media.test"}},
	}
	for _, question := range []string{
		"est-ce que cirrus a répondu a ma dernière requête ?",
		"What did cirrus say?",
	} {
		org, ok := matchCompany(question, companies)
		if !ok || org.Key != "d:cirrus-media.test" {
			t.Errorf("%q selected %+v ok=%v, want the domain prefix", question, org, ok)
		}
	}
	for _, question := range []string{"proj", "cirrusical"} {
		if org, ok := matchCompany(question, companies); ok {
			t.Errorf("%q matched a non-distinctive prefix: %+v", question, org)
		}
	}
}

func TestCompanyLinkDomainPrefixAmbiguityFallsThrough(t *testing.T) {
	companies := []worldsvc.CompanyRow{
		{Key: "d:quartz-electronique.test", Label: "quartz-electronique.test", Domains: []string{"quartz-electronique.test"}},
		{Key: "d:controle-quartz.test", Label: "controle-quartz.test", Domains: []string{"controle-quartz.test"}},
	}
	if org, ok := matchCompany("What did quartz say?", companies); ok {
		t.Fatalf("two domain prefixes selected %+v", org)
	}
}

func TestCompanyProfileQuestionAcceptsDomainPrefix(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "d:cirrus-media.test", Label: "cirrus-media.test", Domains: []string{"cirrus-media.test"}}
	if !companyProfileQuestion("Parle-moi de cirrus", org) {
		t.Fatal("the profile guard rejected the distinctive domain prefix")
	}
	if companyProfileQuestion("est-ce que cirrus a répondu a ma dernière requête ?", org) {
		t.Fatal("the profile guard accepted an unexplained content word (requête)")
	}
}

// TestStructuredKeywordDoesNotOverrideCompanyScope reproduces the reported
// Ask: "a répondu" is a reply-intent keyword, but with one named organization
// the question asks for a fact about that organization, not for a mailbox-
// wide reply filter. The planner must answer it from the organization's mail.
func TestStructuredKeywordDoesNotOverrideCompanyScope(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	companies.list.Companies = append(companies.list.Companies, worldsvc.CompanyRow{
		Key: "d:cirrus-media.test", Label: "cirrus-media.test", Domains: []string{"cirrus-media.test"},
		Messages: 107, Threads: 20})
	companies.profiles["d:cirrus-media.test"] = worldsvc.ProfileView{
		Key: "d:cirrus-media.test", Label: "cirrus-media.test", Domains: []string{"cirrus-media.test"},
		Messages: 107, Threads: 20,
	}
	w.svc.SetCompanies(companies)
	planFallback(w)

	question := "est-ce que cirrus a répondu a ma dernière requête ?"
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Intent != IntentFreeform || view.Answer == "" {
		t.Fatalf("structured keyword won over the company scope: %+v", view)
	}
	if w.gen.calls != 1 || w.search.calls == 0 {
		t.Fatalf("planner path: generator=%d search=%d", w.gen.calls, w.search.calls)
	}
	if view.Intent == IntentCompany {
		t.Fatalf("a specific question returned the company profile: %+v", view)
	}
}

func TestCompanyLinkAmbiguityAsksForOneClick(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	companies.list.Companies = append(companies.list.Companies, worldsvc.CompanyRow{
		Key: "c:club elan", Label: "Club Élan", Domains: []string{"club-elan.test"}, Messages: 5})
	companies.profiles["c:club elan"] = worldsvc.ProfileView{Key: "c:club elan", Label: "Club Élan", Messages: 5, Threads: 1}
	w.svc.SetCompanies(companies)
	planFallback(w)

	view, err := w.svc.Ask(t.Context(), "Compare Atelier Nord and Club Élan", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent == IntentCompany {
		t.Fatalf("two organizations were shortcut: %+v", view)
	}
	if _, profileCalls := companies.calls(); profileCalls != 0 {
		t.Fatalf("ambiguous question read %d profiles", profileCalls)
	}
	if view.Mode != ModeClarify || view.Clarify == nil || len(view.Clarify.Options) != 2 {
		t.Fatalf("two named organizations must offer a one-click pick: %+v", view)
	}
	if w.gen.calls != 0 || w.search.calls != 0 {
		t.Fatalf("ambiguous question called the planner: generator=%d search=%d", w.gen.calls, w.search.calls)
	}
}

func TestCompanyRankingWithoutOrganizationFallsThrough(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	w.svc.SetCompanies(companies)
	planFallback(w)

	view, err := w.svc.Ask(t.Context(), "Which company do I email most?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Intent == IntentCompany {
		t.Fatalf("a company ranking was answered from one profile: %+v", view)
	}
	if _, profileCalls := companies.calls(); profileCalls != 0 {
		t.Fatalf("a company ranking read %d profiles", profileCalls)
	}
	if w.gen.calls != 1 || w.search.calls != 1 {
		t.Fatalf("planner fallback: generator=%d search=%d", w.gen.calls, w.search.calls)
	}
}

func TestCompanyProfileFailureIsExplicitAndNotCached(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	companies.profileErr = errors.New("profile down")
	w.svc.SetCompanies(companies)

	view, err := w.svc.Ask(t.Context(), "Tell me about Atelier Nord", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed || view.Intent != IntentCompany || view.Answer != "" ||
		view.Notice != NoticeCompanyUnavailable || view.Provider != "" || len(view.Citations) != 0 ||
		len(view.Counts) != 0 {
		t.Fatalf("profile failure view %+v", view)
	}
	if w.gen.calls != 0 || w.search.calls != 0 {
		t.Fatalf("profile failure fell through to the planner: generator=%d search=%d", w.gen.calls, w.search.calls)
	}
	if view.ID == 0 || w.store.saved[len(w.store.saved)-1].Notice != NoticeCompanyUnavailable {
		t.Fatalf("profile failure not persisted: %+v", w.store.saved)
	}

	// A failed profile is retried, never frozen in the answer cache.
	if _, err := w.svc.Ask(t.Context(), "Tell me about Atelier Nord", "en"); err != nil {
		t.Fatal(err)
	}
	if _, profileCalls := companies.calls(); profileCalls != 2 {
		t.Fatalf("failed company answer was cached: %d profile calls", profileCalls)
	}
}

func TestCompanyDirectoryEntryWithoutMail(t *testing.T) {
	cases := []struct {
		language string
		question string
		want     string
	}{
		{"en", "What about Solo LLC?", "Solo LLC is a directory entry with no mail."},
		{"fr", "Parle-moi de Solo LLC", "Solo LLC est une fiche du répertoire, sans message échangé."},
	}
	for _, tc := range cases {
		t.Run(tc.language, func(t *testing.T) {
			w := newWorld(t, false)
			companies := &fakeCompanies{
				list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{{Key: "c:solo", Label: "Solo LLC"}}},
				profiles: map[string]worldsvc.ProfileView{"c:solo": {
					Key: "c:solo", Label: "Solo LLC", Declared: true, ThreadStates: map[string]int{},
				}},
			}
			w.svc.SetCompanies(companies)
			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeComputed || view.Intent != IntentCompany || view.Answer != tc.want ||
				view.Counts["messages"] != 0 || len(view.Citations) != 0 {
				t.Fatalf("directory entry view %+v", view)
			}
		})
	}
}

func TestCompanyCitationsDeduplicateAndCap(t *testing.T) {
	w := newWorld(t, false)
	recent := make([]worldsvc.CompanyMessage, 0, 10)
	for id := int64(1); id <= 10; id++ {
		recent = append(recent, worldsvc.CompanyMessage{MessageID: id, Subject: fmt.Sprintf("subject %d", id),
			SenderName: "sender", Instant: "2026-10-20T09:00:00Z"})
	}
	companies := &fakeCompanies{
		list: worldsvc.ListResult{Companies: []worldsvc.CompanyRow{{Key: "c:cap", Label: "Cap Co"}}},
		profiles: map[string]worldsvc.ProfileView{"c:cap": {
			Key: "c:cap", Label: "Cap Co", Messages: 20, Threads: 5, PeopleContacted: 2, Recent: recent,
			OpenThreads: []worldsvc.CompanyThread{
				{MessageID: 5, Subject: "duplicate", LastAt: "2026-10-19T09:00:00Z"},
				{MessageID: 11, Subject: "open 11", LastAt: "2026-10-19T09:00:00Z"},
				{MessageID: 12, Subject: "open 12", LastAt: "2026-10-19T09:00:00Z"},
			},
		}},
	}
	w.svc.SetCompanies(companies)
	view, err := w.svc.Ask(t.Context(), "Tell me about Cap Co", "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Citations) != maxCompanyCitations {
		t.Fatalf("citations %d, want %d", len(view.Citations), maxCompanyCitations)
	}
	seen := map[int64]bool{}
	for _, c := range view.Citations {
		if seen[c.MessageID] {
			t.Fatalf("duplicate citation %d", c.MessageID)
		}
		seen[c.MessageID] = true
	}
	if view.Citations[0].MessageID != 1 || view.Citations[maxCompanyCitations-1].MessageID != 8 {
		t.Fatalf("citation order %+v", view.Citations)
	}
}

func TestCompanyShortcutConcurrent(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	w.svc.SetCompanies(companies)
	question := "Qu'est-ce que je sais sur Atelier Nord ?"

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.svc.Ask(t.Context(), question, "fr")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if w.gen.calls != 0 || w.search.calls != 0 {
		t.Fatalf("concurrent company asks called the planner: generator=%d search=%d", w.gen.calls, w.search.calls)
	}
	if listCalls, profileCalls := companies.calls(); listCalls < 1 || profileCalls < 1 {
		t.Fatalf("concurrent company asks never read the book: list=%d profile=%d", listCalls, profileCalls)
	}
	if got, want := len(w.store.saved), 16; got != want {
		t.Fatalf("history rows %d, want %d", got, want)
	}
}

func TestNoQuestionAnswerOrContentInLogs(t *testing.T) {
	cfg := configtest.New(t, nil)
	logs, err := logx.Setup(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	w := &testWorld{cfg: cfg, store: &fakeStore{summaries: map[int64]store.Summary{1: {RowID: 1,
		Subject: "LOG_MARKER_SUBJECT", Body: "LOG_MARKER_BODY"}}},
		prio:   &fakePriority{byFilter: map[priority.Filter][]priority.Item{}, totals: map[priority.Filter]int{}},
		search: &fakeSearch{result: search.Result{Hits: []search.Hit{hit(1)}}}, gate: &fakeGate{},
		gen: &fakeGenerator{name: "fake", model: "m", content: `{"route":"answer","answer":"LOG_MARKER_ANSWER [1]","used":[1]}`}}
	w.source = &fakeSource{gen: w.gen}
	w.svc, err = New(cfg, Deps{Store: w.store, Priority: w.prio, Search: w.search, Gate: w.gate, Generator: w.source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Ask(t.Context(), "LOG_MARKER_QUESTION about the budget", "en"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Ask(t.Context(), "LOG_MARKER_QUESTION two", "en"); err != nil {
		t.Fatal(err)
	}
	// A failing provider error must not be logged either.
	w.gen.err = errors.New("LOG_MARKER_QUESTION failed")
	if _, err := w.svc.Ask(t.Context(), "LOG_MARKER_QUESTION three", "en"); err != nil {
		t.Fatal(err)
	}
	for _, e := range logs.Ring().Snapshot() {
		all := e.Message + " " + fmt.Sprint(e.Attrs)
		for _, marker := range []string{"LOG_MARKER_QUESTION", "LOG_MARKER_ANSWER", "LOG_MARKER_SUBJECT", "LOG_MARKER_BODY"} {
			if strings.Contains(all, marker) {
				t.Fatalf("qa content leaked into logs: %s %v", e.Message, e.Attrs)
			}
		}
	}
}

func TestWriteContextMessagesKeepsTheQueryCenteredExcerpt(t *testing.T) {
	body := strings.Repeat("filler ", 200) + "the approval code is ZX-42 and the deadline is Friday"
	var b strings.Builder
	writeContextMessages(&b, []contextMessage{{
		ID: 1, Subject: "Approval", SenderName: "Alice", SenderAddress: "alice@example.test",
		ReceivedAt: "2026-10-26T09:00:00Z", Body: body, Excerpt: "the approval code is ZX-42 and the deadline",
	}})
	out := b.String()
	if !strings.Contains(out, "ZX-42") {
		t.Fatalf("the prompt lost the passage beyond the 600-rune head:\n%s", out)
	}
	if !strings.Contains(out, "Relevant excerpt:") {
		t.Fatalf("the prompt did not mark the query-centered excerpt:\n%s", out)
	}

	var short strings.Builder
	writeContextMessages(&short, []contextMessage{{
		ID: 2, Subject: "Short", Body: "The approval code is ZX-42.", Excerpt: "The approval code is ZX-42.",
	}})
	if strings.Contains(short.String(), "Relevant excerpt:") {
		t.Fatalf("a short body duplicated its excerpt:\n%s", short.String())
	}
}

func TestGroundAnswerRejectsInvalidMarkersEvenWithDeclaredSources(t *testing.T) {
	msgs := []contextMessage{{ID: 1, Subject: "one"}, {ID: 2, Subject: "two"}}
	answer, citations := groundAnswer("The budget was approved [9].", msgs, []int{1})
	if strings.Contains(answer, "[9]") {
		t.Fatalf("invalid marker survived: %q", answer)
	}
	if len(citations) != 0 {
		t.Fatalf("invalid-only markers fell back to the declared sources: %+v", citations)
	}

	// A valid marker still wins over the declared list.
	answer, citations = groundAnswer("Approved [2].", msgs, []int{1})
	if answer != "Approved [1]." || len(citations) != 1 || citations[0].MessageID != 2 {
		t.Fatalf("valid marker: %q %+v", answer, citations)
	}
}

func TestUnmarkedProseIsNotAutomaticallyGrounded(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Budget", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z", Body: "The budget was approved."}
	w.gen.content = "The budget was approved."
	view, err := w.svc.Ask(t.Context(), "Tell me about the budget approval", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoGroundedAnswer {
		t.Fatalf("markerless prose was accepted as grounded: %+v", view)
	}
	if len(view.Citations) != 1 || view.Citations[0].MessageID != 1 {
		t.Fatalf("the retrieved evidence must still be offered: %+v", view.Citations)
	}
}

func TestContentQuestionMessagePlanIsRejected(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Budget", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z", Body: "The budget was approved."}
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","limit":5,"order":"desc"}}`
	view, err := w.svc.Ask(t.Context(), "What did Alice say?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeComputed || view.Plan != nil {
		t.Fatalf("a message list answered a content question: %+v", view)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoGroundedAnswer {
		t.Fatalf("content plan fell through to %s/%q: %+v", view.Mode, view.Notice, view)
	}

	// A genuine count question still executes its plan.
	computed := newWorld(t, false)
	computed.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"count","direction":"both","time_range":"all","limit":10,"order":"desc"}}`
	view, err = computed.svc.Ask(t.Context(), "How many messages did I receive?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed {
		t.Fatalf("a count question was blocked: %+v", view)
	}
}

func TestCompanyProfileQuestionRejectsNewsAndHappening(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord", Domains: []string{"atelier-nord.test"}}
	for _, question := range []string{
		"What is happening with Atelier Nord?",
		"Any news from Atelier Nord?",
		"Quelles sont les actualités d'Atelier Nord ?",
	} {
		if companyProfileQuestion(question, org) {
			t.Errorf("%q must be answered from the mail, not the profile counts", question)
		}
	}
	// A status question still uses the profile: it asks for the relationship
	// state the profile owns.
	for _, question := range []string{"Where does Atelier Nord stand?", "Quel est le statut d'Atelier Nord ?"} {
		if !companyProfileQuestion(question, org) {
			t.Errorf("%q must stay a profile question", question)
		}
	}
}

func TestCompanyDomainPrefixNeedsAnEntityCue(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "d:solsticealiments.test", Label: "solsticealiments.test",
		Domains: []string{"solsticealiments.test"}}
	if linked, ok := matchCompany("Combien de solstice ?", []worldsvc.CompanyRow{org}); ok {
		t.Fatalf("a count of a common noun selected an organization: %+v", linked)
	}
	if linked, ok := matchCompany("Combien de messages de solsticealiments ?", []worldsvc.CompanyRow{org}); !ok {
		t.Fatalf("the exact domain did not link: %+v", linked)
	}

	cirrus := worldsvc.CompanyRow{Key: "d:cirrus-media.test", Label: "cirrus-media.test",
		Domains: []string{"cirrus-media.test"}}
	for _, question := range []string{
		"Combien de messages de cirrus ?",
		"Parle-moi de cirrus",
		"What did cirrus say?",
		"est-ce que cirrus a répondu ?",
	} {
		if linked, ok := matchCompany(question, []worldsvc.CompanyRow{cirrus}); !ok || linked.Key != cirrus.Key {
			t.Errorf("%q did not link the distinctive prefix: %+v ok=%v", question, linked, ok)
		}
	}
}

func TestCompanyAliasPrefixLeavesTwoRunes(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "d:cirrus-media.test", Label: "cirrus-media.test",
		Domains: []string{"cirrus-media.test"}}
	words := companyAliasWords(org)
	for _, want := range []string{"cirrus", "cirrusmed"} {
		if !words[want] {
			t.Fatalf("the alias vocabulary lacks %q: %v", want, words)
		}
	}
	if words["cirrusmedi"] {
		t.Fatal("the alias vocabulary accepted a prefix one rune short of the base; the linker never would")
	}
}

func TestStructuredQuestionWithTwoCompaniesAsksWhichOne(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	companies.list.Companies = append(companies.list.Companies, worldsvc.CompanyRow{
		Key: "c:club elan", Label: "Club Élan", Domains: []string{"club-elan.test"}, Messages: 5})
	w.svc.SetCompanies(companies)
	planFallback(w)

	view, err := w.svc.Ask(t.Context(), "Quelles sont mes échéances avec Atelier Nord et Club Élan ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeClarify || view.Clarify == nil || len(view.Clarify.Options) != 2 {
		t.Fatalf("two named organizations did not offer a choice: %+v", view)
	}
	if w.prio.calls != 0 || w.search.calls != 0 || w.gen.calls != 0 {
		t.Fatalf("an ambiguous structured question ran a mailbox-wide answer: priority=%d search=%d generator=%d",
			w.prio.calls, w.search.calls, w.gen.calls)
	}
}

func TestCountQuestionStillExecutesAMessagePlan(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"count","direction":"both","time_range":"all","limit":10,"order":"desc"}}`
	view, err := w.svc.Ask(t.Context(), "How many messages did Alice write?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed {
		t.Fatalf("a count question was blocked by the content guard: %+v", view)
	}
}

func TestFrenchContentQuestionsNeverExecuteAPlan(t *testing.T) {
	for _, tc := range []struct{ name, question, plan string }{
		{"french approve message list", "Qui a approuvé la facture ?", `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","limit":5,"order":"desc"}}`},
		{"french say message list", "Qu'a dit Alice ?", `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","limit":5,"order":"desc"}}`},
		{"french pay message list", "Qui a payé la facture ?", `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"both","time_range":"all","limit":5,"order":"desc"}}`},
		{"french approve contacts ranking", "Qui a approuvé la facture ?", `{"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"both","metric":"messages","time_range":"all","limit":5,"order":"desc"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
			w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Facture", SenderName: "Alice",
				SenderAddress: "alice@example.com", Instant: "2026-10-26T09:00:00Z", Body: "La facture est approuvée."}
			w.gen.content = tc.plan
			view, err := w.svc.Ask(t.Context(), tc.question, "fr")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeComputed {
				t.Fatalf("%q executed a computed plan: %+v", tc.question, view)
			}
			if view.Mode != ModeRetrieval || view.Notice != NoticeNoGroundedAnswer {
				t.Fatalf("%q: %s/%q", tc.question, view.Mode, view.Notice)
			}
		})
	}
}

func TestContentGuardDoesNotBlockAListRequest(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"plan","plan":{"kind":"messages","action":"list","direction":"sent","time_range":"all","limit":5,"order":"desc"}}`
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Total: 1, Rows: []mailquery.Row{{Key: "1", MessageID: 1, Subject: "Sent mail"}}}
	view, err := w.svc.Ask(t.Context(), "Which emails did I write last month?", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeComputed {
		t.Fatalf("a list request was blocked: %+v", view)
	}
}

func TestContentGuardIsNotFooledByASubstring(t *testing.T) {
	if !asksForMessageContent("What did Alice say about the account?") {
		t.Fatal("\"account\" contains \"count\" but the question asks for content")
	}
	if !asksForMessageContent("Who approved the country invoice?") {
		t.Fatal("\"country\" contains \"count\" but the question asks for content")
	}
}

func TestCompanyContentGuardHandlesFrenchInversionAndExchanges(t *testing.T) {
	org := worldsvc.CompanyRow{Key: "c:atelier nord", Label: "Atelier Nord", Domains: []string{"atelier-nord.test"}}
	for _, question := range []string{
		"Atelier Nord a-t-elle répondu ?",
		"Atelier Nord vous a-t-il répondu ?",
		"Quel est le dernier échange avec Atelier Nord ?",
	} {
		if companyProfileQuestion(question, org) {
			t.Errorf("%q must be answered from the mail, not the profile", question)
		}
	}
}

// TestCompanyReplyFrames pins the company reply guard at the route level:
// a profile-answerable contact/count question stays computed, while a
// specific reply-state question goes to the planner.
func TestCompanyReplyFrames(t *testing.T) {
	cases := []struct {
		name       string
		question   string
		wantMode   string
		wantIntent string
		wantGen    int
	}{
		{"contact frame", "Do you have a contact at Acme?", ModeComputed, IntentCompany, 0},
		{"client frame", "Is this company a client?", ModeGenerated, IntentFreeform, 1},
		{"inverted specific reply", "Atelier Nord a-t-elle répondu ?", ModeGenerated, IntentFreeform, 1},
		{"reply count", "Combien y a-t-il de réponses d'Atelier Nord ?", ModeComputed, IntentCompany, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			companies := companyBook()
			companies.list.Companies = append(companies.list.Companies, worldsvc.CompanyRow{
				Key: "c:acme", Label: "Acme", Domains: []string{"acme.test"}, Messages: 4, Threads: 2})
			companies.profiles["c:acme"] = worldsvc.ProfileView{
				Key: "c:acme", Label: "Acme", Messages: 4, Threads: 2, PeopleContacted: 2,
				LastAt: "2026-10-20T09:00:00Z",
			}
			w.svc.SetCompanies(companies)
			planFallback(w)

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != tc.wantMode || view.Intent != tc.wantIntent {
				t.Fatalf("%q -> %s/%s, want %s/%s: %+v",
					tc.question, view.Mode, view.Intent, tc.wantMode, tc.wantIntent, view)
			}
			if w.gen.calls != tc.wantGen {
				t.Fatalf("%q planner calls = %d, want %d", tc.question, w.gen.calls, tc.wantGen)
			}
			if view.Mode == ModeStructured || view.Intent == IntentReply {
				t.Fatalf("%q was diverted to a reply list: %+v", tc.question, view)
			}
		})
	}
}

func TestCompanyClarificationOptionResolvesToOneOrganization(t *testing.T) {
	w := newWorld(t, false)
	companies := companyBook()
	companies.list.Companies = append(companies.list.Companies, worldsvc.CompanyRow{
		Key: "c:projet alpha", Label: "Projet Alpha", Domains: []string{"projet-alpha.test"}, Messages: 5})
	w.svc.SetCompanies(companies)
	planFallback(w)

	first, err := w.svc.Ask(t.Context(), "Quelles sont mes échéances avec Atelier Nord et Projet Alpha ?", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if first.Mode != ModeClarify || first.Clarify == nil || len(first.Clarify.Options) != 2 {
		t.Fatalf("expected a two-option clarification: %+v", first)
	}
	second, err := w.svc.Ask(t.Context(), first.Clarify.Options[0], "fr")
	if err != nil {
		t.Fatal(err)
	}
	if second.Mode == ModeClarify || second.Mode != ModeGenerated {
		t.Fatalf("the chosen company did not resolve: %+v", second)
	}
	if second.Intent != IntentFreeform {
		t.Fatalf("the chosen company was not scoped: %+v", second)
	}
}

func TestCompanyNewsDoesNotBecomeAPersonSearchTerm(t *testing.T) {
	mentions := entityMention("Any news from Atelier Nord?")
	for _, term := range mentions.terms {
		if term == "news" {
			t.Fatalf("a content word became a person term: %v", mentions.terms)
		}
	}
}

func TestPersonEvidenceBudgetLeavesRoomForTheRankedHit(t *testing.T) {
	cases := map[int]int{1: 0, 2: 1, 4: 2, 8: 4, 12: 6, 20: 6}
	for limit, want := range cases {
		if got := personEvidenceBudget(limit); got != want {
			t.Errorf("personEvidenceBudget(%d) = %d, want %d", limit, got, want)
		}
	}

	w := newWorld(t, false)
	question := "What did Alice Martin say about the ZX-42 approval?"
	person := func(id int64, address, at string) store.Summary {
		return store.Summary{RowID: id, Subject: "Person note", SenderName: "Alice Martin",
			SenderAddress: address, Instant: at, Body: "A person message."}
	}
	people := []store.Summary{
		person(101, "alice@one.example", "2026-10-10T09:00:00Z"),
		person(102, "alice@one.example", "2026-10-11T09:00:00Z"),
		person(103, "alice@two.example", "2026-10-12T09:00:00Z"),
		person(104, "alice@two.example", "2026-10-13T09:00:00Z"),
		person(105, "alice@three.example", "2026-10-14T09:00:00Z"),
		person(106, "alice@three.example", "2026-10-15T09:00:00Z"),
	}
	for _, sum := range people {
		w.store.summaries[sum.RowID] = sum
	}
	hits := make([]search.Hit, 5)
	for i := range hits {
		id := int64(201 + i)
		sender, address := fmt.Sprintf("Sender %d", i+1), fmt.Sprintf("sender%d@example.com", i+1)
		if i == 0 {
			sender, address = "Alice Martin", "alice@one.example"
		}
		sum := store.Summary{RowID: id, Subject: fmt.Sprintf("Topic %d", i+1), SenderName: sender,
			SenderAddress: address, Instant: fmt.Sprintf("2026-10-0%dT09:00:00Z", i+1), Body: "Topic body."}
		w.store.summaries[id] = sum
		hits[i] = search.Hit{Summary: sum}
	}
	w.search.results = map[string]search.Result{
		question:       {Hits: hits},
		"Alice Martin": {Hits: append([]search.Hit{{Summary: w.store.summaries[201]}}, hitSummaries(people)...)},
	}
	w.store.queryResults = map[string]mailquery.Result{
		"alice@one.example":   {Rows: []mailquery.Row{{MessageID: 102}, {MessageID: 101}}},
		"alice@two.example":   {Rows: []mailquery.Row{{MessageID: 104}, {MessageID: 103}}},
		"alice@three.example": {Rows: []mailquery.Row{{MessageID: 106}, {MessageID: 105}}},
	}

	msgs, _, _, err := w.svc.plannerContext(t.Context(), question, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 8 {
		t.Fatalf("context has %d messages, want 8", len(msgs))
	}
	personCount, target := 0, false
	for _, msg := range msgs {
		if msg.Reason == "person" {
			personCount++
		}
		if msg.ID == 203 {
			target = true
		}
	}
	if personCount != 4 {
		t.Fatalf("the person leg took %d of 8 slots, want 4", personCount)
	}
	if !target {
		t.Fatalf("the third ranked hit was crowded out by person evidence: %+v", msgIDs(msgs))
	}
}

func hitSummaries(sums []store.Summary) []search.Hit {
	out := make([]search.Hit, len(sums))
	for i, sum := range sums {
		out[i] = search.Hit{Summary: sum}
	}
	return out
}

func msgIDs(msgs []contextMessage) []int64 {
	out := make([]int64, len(msgs))
	for i, msg := range msgs {
		out[i] = msg.ID
	}
	return out
}
