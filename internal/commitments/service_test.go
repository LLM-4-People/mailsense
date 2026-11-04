package commitments

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// --- fakes ------------------------------------------------------------------

// fakeStore is the commitments read side. Every read is mutex-guarded because
// a background refresh reads it while the test inspects call counts.
type fakeStore struct {
	mu        sync.Mutex
	gen       uint64
	routeGen  uint64
	deadlines []agenda.Deadline
	summaries map[string]agenda.ThreadSummary
	book      *store.CompanyBook
	err       error

	dueCalls     int
	summaryCalls int
	bookCalls    int
	dueSince     time.Time
	dueUntil     time.Time
	summaryIDs   []string
}

func (f *fakeStore) DueDeadlines(_ context.Context, since, until time.Time) ([]agenda.Deadline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dueCalls++
	f.dueSince, f.dueUntil = since, until
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.deadlines), nil
}

func (f *fakeStore) ThreadSummaries(_ context.Context, ids []string) (map[string]agenda.ThreadSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaryCalls++
	f.summaryIDs = slices.Clone(ids)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]agenda.ThreadSummary{}
	for _, id := range ids {
		if s, ok := f.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func (f *fakeStore) CompanyBook(context.Context) (*store.CompanyBook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bookCalls++
	return f.book, f.err
}

func (f *fakeStore) MailGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeStore) RouteGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routeGen
}

func (f *fakeStore) setRouteGen(gen uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routeGen = gen
}

func (f *fakeStore) calls() (due, summary, book int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dueCalls, f.summaryCalls, f.bookCalls
}

func (f *fakeStore) window() (time.Time, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dueSince, f.dueUntil
}

func (f *fakeStore) requestedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.summaryIDs)
}

func (f *fakeStore) deadlineRows() []agenda.Deadline {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deadlines)
}

func (f *fakeStore) setDeadlines(d []agenda.Deadline) {
	f.mu.Lock()
	f.deadlines = slices.Clone(d)
	f.mu.Unlock()
}

func (f *fakeStore) setSummaries(s map[string]agenda.ThreadSummary) {
	f.mu.Lock()
	f.summaries = maps.Clone(s)
	f.mu.Unlock()
}

func (f *fakeStore) setBook(b *store.CompanyBook) {
	f.mu.Lock()
	f.book = b
	f.mu.Unlock()
}

func (f *fakeStore) setGen(gen uint64) {
	f.mu.Lock()
	f.gen = gen
	f.mu.Unlock()
}

func (f *fakeStore) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// fakeThreads is the thread-truth resolver. A test can hold ThreadStates
// open to prove that List serves the stale cache while a refresh is running.
type fakeThreads struct {
	mu      sync.Mutex
	states  map[string]threadintel.Assessment
	err     error
	calls   int
	block   <-chan struct{}
	started chan<- struct{}
}

func (f *fakeThreads) ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error) {
	f.mu.Lock()
	f.calls++
	block, started := f.block, f.started
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return maps.Clone(f.states), nil
}

func (f *fakeThreads) setStates(states map[string]threadintel.Assessment) {
	f.mu.Lock()
	f.states = maps.Clone(states)
	f.mu.Unlock()
}

func (f *fakeThreads) setBlock(block <-chan struct{}, started chan<- struct{}) {
	f.mu.Lock()
	f.block, f.started = block, started
	f.mu.Unlock()
}

func (f *fakeThreads) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeThreads) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeDecisions is the priority decision source.
type fakeDecisions struct {
	mu     sync.Mutex
	items  []priority.Item
	err    error
	calls  int
	filter priority.Filter
	limit  int
}

func (f *fakeDecisions) Decisions(_ context.Context, limit int) ([]priority.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.items), nil
}

func (f *fakeDecisions) setItems(items []priority.Item) {
	f.mu.Lock()
	f.items = slices.Clone(items)
	f.mu.Unlock()
}

func (f *fakeDecisions) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeDecisions) decisionRows() []priority.Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.items)
}

func (f *fakeDecisions) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeDecisions) lastCall() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limit
}

// fakeActions is the priority concrete-action source.
type fakeActions struct {
	mu    sync.Mutex
	items []priority.Item
	err   error
	calls int
	limit int
}

func (f *fakeActions) Actions(_ context.Context, limit int) ([]priority.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return slices.Clone(f.items), nil
}

func (f *fakeActions) setItems(items []priority.Item) {
	f.mu.Lock()
	f.items = slices.Clone(items)
	f.mu.Unlock()
}

func (f *fakeActions) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// --- fixture ----------------------------------------------------------------

type fixture struct {
	svc       *Service
	st        *fakeStore
	threads   *fakeThreads
	decisions *fakeDecisions
	actions   *fakeActions
	clk       *clocktest.Fake
	now       time.Time
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// newFixture builds one deterministic evidence set:
//
//	due 11  overdue, Alice
//	due 12  open,    Bob
//	C1      reply awaiting owner
//	C2      reply awaiting other
//	C3      answered, must not become a reply item
//	21      decision, Boss
func newFixture(t *testing.T) *fixture {
	t.Helper()
	now := mustTime("2026-11-03T09:00:00Z")
	st := &fakeStore{
		deadlines: []agenda.Deadline{
			{MessageID: 11, ConversationID: "CD", Subject: "Send invoice", SenderName: "Alice",
				Due: now.Add(-2 * time.Hour), Participants: []string{"alice@atelier-nord.test"}},
			{MessageID: 12, ConversationID: "CE", Subject: "Prepare quote", SenderName: "Bob",
				Due: now.Add(time.Hour), Participants: []string{"bob@delta.co"}},
		},
		summaries: map[string]agenda.ThreadSummary{
			"C1": {ConversationID: "C1", Subject: "Plan review", MessageID: 7,
				LastAt: stamp(now.Add(-time.Hour)), Participants: []string{"client@atelier-nord.test"}},
			"C2": {ConversationID: "C2", Subject: "Waiting on quote", MessageID: 8,
				LastAt: stamp(now.Add(-90 * time.Minute)), Participants: []string{"vendor@delta.co"}},
		},
	}
	threads := &fakeThreads{states: map[string]threadintel.Assessment{
		"C1": {ConversationID: "C1", State: threadintel.AwaitingOwner, LastMessageID: 7,
			LastAt: stamp(now.Add(-time.Hour)), ActorName: "Client"},
		"C2": {ConversationID: "C2", State: threadintel.AwaitingOther, LastMessageID: 8,
			LastAt: stamp(now.Add(-90 * time.Minute)), ActorAddress: "vendor@delta.co"},
		"C3": {ConversationID: "C3", State: threadintel.AnsweredByOwner, LastMessageID: 9,
			LastAt: stamp(now.Add(-30 * time.Minute)), ActorName: "Owner"},
	}}
	decisions := &fakeDecisions{items: []priority.Item{
		{MessageID: 21, Subject: "Choose vendor", SenderName: "Boss", SenderAddress: "boss@atelier-nord.test",
			ReceivedAt: stamp(now.Add(-30 * time.Minute)),
			Action:     action.Class{Primary: action.Decide, Waiting: action.WaitingOwner}},
	}}
	actions := &fakeActions{}
	clk := clocktest.New(now)
	svc := New(Deps{Store: st, Threads: threads, Decisions: decisions, Actions: actions, Clock: clk})
	return &fixture{svc: svc, st: st, threads: threads, decisions: decisions, actions: actions, clk: clk, now: now}
}

// --- helpers ----------------------------------------------------------------

func findByMessage(t *testing.T, res Result, messageID int64) Commitment {
	t.Helper()
	for _, c := range res.Items {
		if c.MessageID == messageID {
			return c
		}
	}
	t.Fatalf("commitment for message %d missing from %+v", messageID, res.Items)
	return Commitment{}
}

func wantOrder(t *testing.T, res Result, ids ...int64) {
	t.Helper()
	got := make([]int64, len(res.Items))
	for i, c := range res.Items {
		got[i] = c.MessageID
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("order %v, want %v (items %+v)", got, ids, res.Items)
	}
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testBook builds a real CompanyBook with one declared external company so
// the counterpart rule is exercised against the real resolver rather than a
// hand-built struct (which the store deliberately cannot construct).
func testBook(t *testing.T) *store.CompanyBook {
	t.Helper()
	cfg := configtest.New(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}),
		"mail.domains.shared":  []any{"gmail.com"},
	})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	ref := store.StoreRef{SourceID: "src", StoreID: "store-1"}
	if err := st.ReplaceContacts(t.Context(), ref, "Contacts", []contacts.Contact{
		{EntryID: "alice", DisplayName: "Alice Martin", Company: "Atelier Nord",
			Emails: []string{"alice@atelier-nord.test"}},
	}); err != nil {
		t.Fatal(err)
	}
	book, err := st.CompanyBook(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return book
}

// --- merge, evidence and sort ----------------------------------------------

func TestListMergesDueReplyAndDecisionEvidence(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("items %d, want 5: %+v", len(res.Items), res.Items)
	}
	wantOrder(t, res, 11, 8, 7, 21, 12)

	due := findByMessage(t, res, 11)
	if due.Kind != KindDue || due.Direction != DirectionOwner || due.Status != StatusOverdue {
		t.Fatalf("overdue due item %+v", due)
	}
	if due.Title != "Send invoice" || due.Subject != "Send invoice" ||
		due.Due != stamp(f.now.Add(-2*time.Hour)) || due.ConversationID != "CD" ||
		due.Person != "alice@atelier-nord.test" || !slices.Equal(due.Evidence, []int64{11}) {
		t.Fatalf("overdue due fields %+v", due)
	}

	future := findByMessage(t, res, 12)
	if future.Status != StatusOpen || future.Due != stamp(f.now.Add(time.Hour)) ||
		future.Person != "bob@delta.co" || !slices.Equal(future.Evidence, []int64{12}) {
		t.Fatalf("open due item %+v", future)
	}

	ownerReply := findByMessage(t, res, 7)
	if ownerReply.Kind != KindReply || ownerReply.Direction != DirectionOwner ||
		ownerReply.Status != StatusOpen || ownerReply.Title != "Plan review" ||
		ownerReply.ConversationID != "C1" || ownerReply.Person != "client@atelier-nord.test" ||
		ownerReply.LastAt != stamp(f.now.Add(-time.Hour)) ||
		!slices.Equal(ownerReply.Evidence, []int64{7}) {
		t.Fatalf("owner reply %+v", ownerReply)
	}

	otherReply := findByMessage(t, res, 8)
	if otherReply.Direction != DirectionOther || otherReply.ConversationID != "C2" ||
		otherReply.Person != "vendor@delta.co" || !slices.Equal(otherReply.Evidence, []int64{8}) {
		t.Fatalf("other reply %+v", otherReply)
	}

	decision := findByMessage(t, res, 21)
	if decision.Kind != KindDecision || decision.Direction != DirectionOwner ||
		decision.Status != StatusOpen || decision.Title != "Choose vendor" ||
		decision.LastAt != stamp(f.now.Add(-30*time.Minute)) ||
		decision.Person != "boss@atelier-nord.test" || !slices.Equal(decision.Evidence, []int64{21}) {
		t.Fatalf("decision %+v", decision)
	}

	want := map[string]int{
		"due": 2, "reply": 2, "decision": 1, "action": 0,
		"owner": 4, "other": 1,
		"open": 4, "overdue": 1, "done": 0, "unknown": 0,
	}
	if !maps.Equal(res.Counts, want) {
		t.Fatalf("counts %v, want %v", res.Counts, want)
	}
	ids := map[string]bool{}
	for _, c := range res.Items {
		if len(c.Evidence) == 0 {
			t.Fatalf("item without evidence: %+v", c)
		}
		if c.ID == "" || ids[c.ID] {
			t.Fatalf("empty or duplicate id on %+v", c)
		}
		ids[c.ID] = true
	}
	if res.GeneratedAt != f.now.UTC().Format(time.RFC3339) {
		t.Fatalf("generated at %q", res.GeneratedAt)
	}

	since, until := f.st.window()
	if !since.Equal(f.now.Add(-dueLookback)) || !until.Equal(f.now.Add(dueHorizon)) {
		t.Fatalf("due window %v..%v", since, until)
	}
	if limit := f.decisions.lastCall(); limit != decisionLimit {
		t.Fatalf("decision call limit %d, want %d", limit, decisionLimit)
	}
}

func TestListFallsBackToSummaryEvidenceAndSkipsUnlinkableReplies(t *testing.T) {
	f := newFixture(t)
	states := map[string]threadintel.Assessment{
		"C4": {ConversationID: "C4", State: threadintel.AwaitingOwner,
			LastAt: stamp(f.now.Add(-10 * time.Minute)), ActorAddress: "new@example.test"},
		"C5": {ConversationID: "C5", State: threadintel.AwaitingOwner,
			LastAt: stamp(f.now.Add(-5 * time.Minute)), ActorAddress: "ghost@example.test"},
	}
	f.threads.setStates(states)
	f.st.setSummaries(map[string]agenda.ThreadSummary{
		"C4": {ConversationID: "C4", Subject: "No last id", MessageID: 44,
			LastAt: stamp(f.now.Add(-10 * time.Minute)), Participants: []string{"new@example.test"}},
		// C5 has no visible summary: it cannot carry a subject or a link.
	})
	f.st.setDeadlines(nil)
	f.decisions.setItems(nil)

	res, err := f.svc.List(t.Context(), Filter{Kind: KindReply})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("replies %+v", res.Items)
	}
	item := res.Items[0]
	if item.ConversationID != "C4" || item.MessageID != 44 || item.LastAt != stamp(f.now.Add(-10*time.Minute)) ||
		!slices.Equal(item.Evidence, []int64{44}) {
		t.Fatalf("fallback evidence item %+v", item)
	}
}

func TestListUsesCompanyBookForOrganizationAndPerson(t *testing.T) {
	f := newFixture(t)
	f.st.setBook(testBook(t))
	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}

	due := findByMessage(t, res, 11)
	if due.OrganizationKey != "c:atelier nord" || due.OrganizationLabel != "Atelier Nord" ||
		due.Person != "alice@atelier-nord.test" {
		t.Fatalf("declared organization on a deadline: %+v", due)
	}
	domain := findByMessage(t, res, 12)
	if domain.OrganizationKey != "d:delta.co" || domain.OrganizationLabel != "delta.co" ||
		domain.Person != "bob@delta.co" {
		t.Fatalf("domain organization on a deadline: %+v", domain)
	}
	reply := findByMessage(t, res, 7)
	if reply.OrganizationKey != "c:atelier nord" || reply.OrganizationLabel != "Atelier Nord" ||
		reply.Person != "client@atelier-nord.test" {
		t.Fatalf("organization on a reply: %+v", reply)
	}
	decision := findByMessage(t, res, 21)
	if decision.OrganizationKey != "c:atelier nord" || decision.OrganizationLabel != "Atelier Nord" ||
		decision.Person != "boss@atelier-nord.test" {
		t.Fatalf("organization on a decision: %+v", decision)
	}
}

func TestListTieBreaksByKindThenID(t *testing.T) {
	f := newFixture(t)
	at := stamp(f.now.Add(2 * time.Hour))
	f.st.setDeadlines([]agenda.Deadline{
		{MessageID: 31, ConversationID: "CD", Subject: "Due first", Due: f.now.Add(2 * time.Hour),
			Participants: []string{"a@example.test"}},
	})
	f.threads.setStates(map[string]threadintel.Assessment{
		"C7": {ConversationID: "C7", State: threadintel.AwaitingOwner, LastMessageID: 32,
			LastAt: at, ActorAddress: "b@example.test"},
	})
	f.st.setSummaries(map[string]agenda.ThreadSummary{
		"C7": {ConversationID: "C7", Subject: "Reply second", MessageID: 32, LastAt: at,
			Participants: []string{"b@example.test"}},
	})
	f.decisions.setItems([]priority.Item{
		{MessageID: 33, Subject: "Decision third", SenderAddress: "c@example.test", ReceivedAt: at,
			Action: action.Class{Primary: action.Decide, Waiting: action.WaitingOwner}},
	})

	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder(t, res, 31, 32, 33)
}

func TestDueStatusBoundary(t *testing.T) {
	f := newFixture(t)
	f.st.setDeadlines([]agenda.Deadline{
		{MessageID: 41, ConversationID: "C41", Subject: "Due now", Due: f.now,
			Participants: []string{"a@example.test"}},
		{MessageID: 42, ConversationID: "C42", Subject: "One second late", Due: f.now.Add(-time.Second),
			Participants: []string{"a@example.test"}},
		{MessageID: 43, ConversationID: "C43", Subject: "No stored date",
			Participants: []string{"a@example.test"}},
	})
	f.threads.setStates(nil)
	f.decisions.setItems(nil)

	res, err := f.svc.List(t.Context(), Filter{Kind: KindDue})
	if err != nil {
		t.Fatal(err)
	}
	if got := findByMessage(t, res, 41); got.Status != StatusOpen || got.Due != stamp(f.now) {
		t.Fatalf("a due date exactly now is open: %+v", got)
	}
	if got := findByMessage(t, res, 42); got.Status != StatusOverdue {
		t.Fatalf("a past due date is overdue: %+v", got)
	}
	if got := findByMessage(t, res, 43); got.Status != StatusOpen || got.Due != "" {
		t.Fatalf("a missing due date is open, never overdue: %+v", got)
	}
}

func TestListFailsWhenAnEnabledSourceFails(t *testing.T) {
	cases := []struct {
		name string
		fail func(*fixture)
	}{
		{"store", func(f *fixture) { f.st.setErr(errors.New("store failed")) }},
		{"threads", func(f *fixture) { f.threads.setErr(errors.New("threads failed")) }},
		{"decisions", func(f *fixture) { f.decisions.setErr(errors.New("decisions failed")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.fail(f)
			if _, err := f.svc.List(t.Context(), Filter{}); err == nil {
				t.Fatal("a source failure must fail the build, not drop a kind")
			}
			// The failed build installed nothing: with every source healthy
			// again the next call builds a set.
			f.st.setErr(nil)
			f.threads.setErr(nil)
			f.decisions.setErr(nil)
			if _, err := f.svc.List(t.Context(), Filter{}); err != nil {
				t.Fatalf("a failed build left a broken entry: %v", err)
			}
		})
	}
}

func TestListTieBreaksSameKindByID(t *testing.T) {
	f := newFixture(t)
	due := stamp(f.now.Add(time.Hour))
	f.st.setDeadlines([]agenda.Deadline{
		{MessageID: 51, ConversationID: "C51", Subject: "One", Due: f.now.Add(time.Hour),
			Participants: []string{"a@example.test"}},
		{MessageID: 52, ConversationID: "C52", Subject: "Two", Due: f.now.Add(time.Hour),
			Participants: []string{"a@example.test"}},
	})
	f.threads.setStates(nil)
	f.decisions.setItems(nil)

	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	id51 := itemID(KindDue, DirectionOwner, 51, "C51", due)
	id52 := itemID(KindDue, DirectionOwner, 52, "C52", due)
	first, second := int64(51), int64(52)
	if id52 < id51 {
		first, second = second, first
	}
	wantOrder(t, res, first, second)
}

func TestListSkipsRowsWithoutEvidence(t *testing.T) {
	f := newFixture(t)
	f.st.setDeadlines([]agenda.Deadline{
		{MessageID: 0, ConversationID: "CD", Subject: "No message", Due: f.now.Add(time.Hour)},
	})
	f.threads.setStates(map[string]threadintel.Assessment{
		"C0": {ConversationID: "C0", State: threadintel.AwaitingOwner,
			LastAt: stamp(f.now.Add(-time.Minute)), ActorAddress: "a@example.test"},
		"": {State: threadintel.AwaitingOwner, LastMessageID: 9, LastAt: stamp(f.now)},
		"C9": {ConversationID: "C9", State: threadintel.AnsweredByOwner,
			LastMessageID: 9, LastAt: stamp(f.now)},
	})
	f.st.setSummaries(map[string]agenda.ThreadSummary{
		"C0": {ConversationID: "C0", Subject: "No evidence", MessageID: 0},
	})
	f.decisions.setItems([]priority.Item{
		{MessageID: 0, Subject: "No evidence", SenderAddress: "b@example.test", ReceivedAt: stamp(f.now),
			Action: action.Class{Primary: action.Decide, Waiting: action.WaitingOwner}},
	})

	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 0 || res.Counts["due"] != 0 || res.Counts["reply"] != 0 || res.Counts["decision"] != 0 {
		t.Fatalf("unlinked rows became commitments: %+v", res)
	}
}

func TestListNamesAPersonWithoutInventingAnOrganization(t *testing.T) {
	f := newFixture(t)
	f.st.setBook(testBook(t))
	f.st.setDeadlines([]agenda.Deadline{
		{MessageID: 61, ConversationID: "C61", Subject: "No participants",
			SenderName: "Alice Martin", Due: f.now.Add(time.Hour)},
		{MessageID: 62, ConversationID: "C62", Subject: "Shared domain",
			SenderName: "Personal", Due: f.now.Add(time.Hour), Participants: []string{"personal@gmail.com"}},
	})
	f.threads.setStates(nil)
	f.decisions.setItems(nil)

	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	item := findByMessage(t, res, 61)
	if item.Person != "Alice Martin" || item.OrganizationKey != "" || item.OrganizationLabel != "" {
		t.Fatalf("a person without a placeable organization: %+v", item)
	}
	shared := findByMessage(t, res, 62)
	if shared.Person != "personal@gmail.com" || shared.OrganizationKey != "" || shared.OrganizationLabel != "" {
		t.Fatalf("a shared domain must not invent an organization: %+v", shared)
	}
}

func TestActionItemsMergeWithTheSameConversationOnce(t *testing.T) {
	f := newFixture(t)
	f.actions.setItems([]priority.Item{{
		MessageID: 31, ConversationID: "C1", Subject: "Pay the invoice", SenderName: "Client",
		SenderAddress: "client@atelier-nord.test", ReceivedAt: stamp(f.now.Add(-20 * time.Minute)),
		Action: action.Class{Primary: action.Pay, Waiting: action.WaitingOwner,
			Reasons: []string{"ask:payment_money"}},
		Signals: priority.Signals{Subject: subjectOf("billing_account", "payment")},
	}})
	res, err := f.svc.List(t.Context(), Filter{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	// C1 is the reply thread and the action target. One loop, one row.
	var c1 []Commitment
	for _, item := range res.Items {
		if item.ConversationID == "C1" {
			c1 = append(c1, item)
		}
	}
	if len(c1) != 1 {
		t.Fatalf("C1 appears %d times: %+v", len(c1), c1)
	}
	got := c1[0]
	if got.Kind != KindAction || got.Action != string(action.Pay) || got.Waiting != string(action.WaitingOwner) ||
		got.Topic != "billing_account" || got.Subtopic != "payment" ||
		!slices.Equal(got.Evidence, []int64{7, 31}) {
		t.Fatalf("merged action %+v", got)
	}
	if res.Counts["action"] != 1 || res.Counts["reply"] != 1 {
		t.Fatalf("counts %+v", res.Counts)
	}
	if f.actions.callCount() != 1 {
		t.Fatalf("action source calls %d", f.actions.callCount())
	}
}

func subjectOf(topic, subtopic string) subject.Subject {
	return subject.Subject{Topic: topic, Subtopic: subtopic, Path: topic + "/" + subtopic}
}

// A decision source row whose action is not a live owner decision (history,
// automated, already answered) is not a commitment, even when the raw
// decision signal is still stored.
// The read-model page must fit the ranking cache the source can serve. The
// live Round 55 boot failed because this bound was raised past the API page
// limit while the source validated it; the fake could not catch it.
func TestReadModelBoundsFitTheRankingCache(t *testing.T) {
	if decisionLimit < 1 || decisionLimit > priority.MaxCached {
		t.Fatalf("decision limit %d outside 1..%d", decisionLimit, priority.MaxCached)
	}
	if actionLimit < 1 || actionLimit > priority.MaxCached {
		t.Fatalf("action limit %d outside 1..%d", actionLimit, priority.MaxCached)
	}
	if replyCandidates < 1 || replyCandidates > 10000 {
		t.Fatalf("reply candidate bound %d", replyCandidates)
	}
}

func TestDecisionSourceRequiresTheActionTruth(t *testing.T) {
	f := newFixture(t)
	f.decisions.setItems([]priority.Item{
		{MessageID: 61, Subject: "Old choice", SenderAddress: "a@example.test", ReceivedAt: stamp(f.now),
			Action: action.Class{Primary: action.Read, Waiting: action.WaitingNone}},
		{MessageID: 62, Subject: "Live choice", SenderAddress: "a@example.test", ReceivedAt: stamp(f.now),
			Action: action.Class{Primary: action.Decide, Waiting: action.WaitingOwner}},
	})
	res, err := f.svc.List(t.Context(), Filter{Kind: KindDecision})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].MessageID != 62 ||
		res.Items[0].Action != string(action.Decide) {
		t.Fatalf("decision items %+v", res.Items)
	}
}

// An overlapping due date (an owner reminder) must not turn a kept
// awaiting_other reply into an owner obligation.
func TestMergeKeepsTheKeptItemsDirection(t *testing.T) {
	f := newFixture(t)
	// C2 is awaiting_other; adding a due date on the same conversation must
	// keep the item pointing at the other side.
	f.st.setDeadlines(append(f.st.deadlineRows(), agenda.Deadline{
		MessageID: 8, ConversationID: "C2", Subject: "Waiting on quote",
		Due: f.now.Add(time.Hour), Participants: []string{"vendor@delta.co"},
	}))
	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range res.Items {
		if item.ConversationID != "C2" {
			continue
		}
		found = true
		if item.Direction != DirectionOther || item.Action != string(action.Wait) ||
			item.Waiting != string(action.WaitingOther) {
			t.Fatalf("merged C2 %+v", item)
		}
	}
	if !found {
		t.Fatal("C2 missing from the merged set")
	}
}

func TestListNewestReplyCandidatesAreBounded(t *testing.T) {
	f := newFixture(t)
	states := map[string]threadintel.Assessment{}
	summaries := map[string]agenda.ThreadSummary{}
	for i := 0; i < replyCandidates+5; i++ {
		id := fmt.Sprintf("C%03d", i)
		last := stamp(f.now.Add(-time.Duration(i) * time.Minute))
		states[id] = threadintel.Assessment{ConversationID: id, State: threadintel.AwaitingOwner,
			LastMessageID: int64(1000 + i), LastAt: last, ActorAddress: "person@example.test"}
		summaries[id] = agenda.ThreadSummary{ConversationID: id, Subject: "Subject " + id,
			MessageID: int64(1000 + i), LastAt: last, Participants: []string{"person@example.test"}}
	}
	f.threads.setStates(states)
	f.st.setSummaries(summaries)
	f.st.setDeadlines(nil)
	f.decisions.setItems(nil)

	res, err := f.svc.List(t.Context(), Filter{Kind: KindReply, Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != MaxLimit {
		t.Fatalf("reply page %d, want the %d-item page bound", len(res.Items), MaxLimit)
	}
	if res.Counts["reply"] != replyCandidates {
		t.Fatalf("reply count %d, want the full %d", res.Counts["reply"], replyCandidates)
	}
	ids := f.st.requestedIDs()
	if len(ids) != replyCandidates {
		t.Fatalf("summary ids %d, want the full %d", len(ids), replyCandidates)
	}
	for _, oldest := range []string{"C2000", "C2001", "C2002", "C2003", "C2004"} {
		if slices.Contains(ids, oldest) {
			t.Fatalf("oldest candidate %s must be outside the newest %d: %v", oldest, replyCandidates, ids)
		}
	}
}

// --- identity, dedupe, filters and bounds -----------------------------------

func TestListDedupesAndProducesStableIDs(t *testing.T) {
	f := newFixture(t)
	due := f.st.deadlineRows()
	f.st.setDeadlines(append(due, due[0]))
	decisions := f.decisions.decisionRows()
	f.decisions.setItems(append(decisions, decisions[0]))

	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 || res.Counts["due"] != 2 || res.Counts["decision"] != 1 {
		t.Fatalf("dedupe counts items=%d counts=%v", len(res.Items), res.Counts)
	}
	seen := map[string]bool{}
	for _, c := range res.Items {
		if c.ID == "" || len(c.ID) != 32 {
			t.Fatalf("id %q", c.ID)
		}
		if seen[c.ID] {
			t.Fatalf("duplicate id %s in %+v", c.ID, res.Items)
		}
		seen[c.ID] = true
	}

	other := newFixture(t)
	second, err := other.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := make([]string, len(res.Items))
	for i, c := range res.Items {
		firstIDs[i] = c.ID
	}
	secondIDs := make([]string, len(second.Items))
	for i, c := range second.Items {
		secondIDs[i] = c.ID
	}
	slices.Sort(firstIDs)
	slices.Sort(secondIDs)
	if !slices.Equal(firstIDs, secondIDs) {
		t.Fatalf("ids are not stable:\n%v\n%v", firstIDs, secondIDs)
	}
}

func TestItemIDIsEvidenceScoped(t *testing.T) {
	base := itemID(KindDue, DirectionOwner, 7, "C1", "2026-11-03T09:00:00Z")
	if base != itemID(KindDue, DirectionOwner, 7, "C1", "2026-11-03T09:00:00Z") {
		t.Fatal("the same evidence must produce the same id")
	}
	if len(base) != 32 {
		t.Fatalf("id %q is not a 128-bit hex digest", base)
	}
	for name, id := range map[string]string{
		"kind":         itemID(KindReply, DirectionOwner, 7, "C1", "2026-11-03T09:00:00Z"),
		"direction":    itemID(KindDue, DirectionOther, 7, "C1", "2026-11-03T09:00:00Z"),
		"message":      itemID(KindDue, DirectionOwner, 8, "C1", "2026-11-03T09:00:00Z"),
		"conversation": itemID(KindDue, DirectionOwner, 7, "C2", "2026-11-03T09:00:00Z"),
		"due":          itemID(KindDue, DirectionOwner, 7, "C1", "2026-11-04T09:00:00Z"),
	} {
		if id == base {
			t.Fatalf("changing the %s must change the id", name)
		}
	}
}

func TestListFiltersAndSorts(t *testing.T) {
	f := newFixture(t)
	warm, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder(t, warm, 11, 8, 7, 21, 12)

	cases := []struct {
		name   string
		filter Filter
		want   []int64
	}{
		{"kind due", Filter{Kind: KindDue}, []int64{11, 12}},
		{"direction other", Filter{Direction: DirectionOther}, []int64{8}},
		{"status overdue", Filter{Status: StatusOverdue}, []int64{11}},
		{"status done", Filter{Status: StatusDone}, []int64{}},
		{"reply owed by owner", Filter{Kind: KindReply, Direction: DirectionOwner}, []int64{7}},
		{"limit two", Filter{Limit: 2}, []int64{11, 8}},
		{"negative limit uses the default", Filter{Limit: -1}, []int64{11, 8, 7, 21, 12}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.svc.List(t.Context(), tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Items) != len(tc.want) {
				t.Fatalf("items %+v, want messages %v", res.Items, tc.want)
			}
			if len(tc.want) > 0 {
				wantOrder(t, res, tc.want...)
			}
			if res.Counts["due"] != 2 || res.Counts["reply"] != 2 || res.Counts["decision"] != 1 {
				t.Fatalf("facet counts must cover the whole set: %v", res.Counts)
			}
		})
	}
}

func TestListClampsThePage(t *testing.T) {
	const messages = 520
	now := mustTime("2026-11-03T09:00:00Z")
	deadlines := make([]agenda.Deadline, 0, messages)
	for i := 1; i <= messages; i++ {
		deadlines = append(deadlines, agenda.Deadline{
			MessageID: int64(i), ConversationID: fmt.Sprintf("C%d", i), Subject: fmt.Sprintf("Due %d", i),
			SenderName: "Sender", Due: now.Add(time.Duration(i) * time.Minute),
			Participants: []string{"sender@example.test"},
		})
	}
	st := &fakeStore{deadlines: deadlines}
	svc := New(Deps{Store: st, Clock: clocktest.New(now)})

	res, err := svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != DefaultLimit || res.Counts["due"] != messages {
		t.Fatalf("default page items=%d count=%d", len(res.Items), res.Counts["due"])
	}

	res, err = svc.List(t.Context(), Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != MaxLimit {
		t.Fatalf("max page %d, want %d", len(res.Items), MaxLimit)
	}
	res, err = svc.List(t.Context(), Filter{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder(t, res, 1, 2, 3)
}

func TestListRejectsUnknownFilters(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name   string
		filter Filter
		want   error
	}{
		{"kind", Filter{Kind: "promise"}, ErrInvalidKind},
		{"direction", Filter{Direction: "them"}, ErrInvalidDirection},
		{"status", Filter{Status: "closed"}, ErrInvalidStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.List(t.Context(), tc.filter)
			if !errors.Is(err, tc.want) {
				t.Fatalf("filter %+v: error %v, want %v", tc.filter, err, tc.want)
			}
		})
	}
}

// The service keeps its documented clamp; the HTTP layer owns the explicit
// 1…MaxLimit request bound and maps ErrInvalidLimit for adapters that enforce
// it before List.
func TestListClampsInsteadOfRejectingTheLimit(t *testing.T) {
	f := newFixture(t)
	for _, limit := range []int{-1, 0, MaxLimit + 1} {
		if _, err := f.svc.List(t.Context(), Filter{Limit: limit}); err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
}

func TestListRequiresAStore(t *testing.T) {
	if _, err := New(Deps{}).List(t.Context(), Filter{}); err == nil {
		t.Fatal("a missing store must fail")
	}
}

func TestListOmitsAMissingOptionalSource(t *testing.T) {
	f := newFixture(t)
	svc := New(Deps{Store: f.st, Clock: f.clk})
	res, err := svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || res.Counts["due"] != 2 ||
		res.Counts["reply"] != 0 || res.Counts["decision"] != 0 {
		t.Fatalf("due-only result %+v", res)
	}
}

// --- cache ------------------------------------------------------------------

func TestListColdBuildIsSingleFlight(t *testing.T) {
	f := newFixture(t)
	const n = 8
	svc := New(Deps{Store: f.st, Threads: f.threads, Decisions: f.decisions, Actions: f.actions,
		Clock: clocktest.NewBarrier(clocktest.New(f.now), n)})
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.threads.setBlock(release, started)

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.List(context.Background(), Filter{})
		}()
	}
	<-started
	// Every caller has passed the clock barrier; let the rest reach the
	// cache before the one build finishes.
	for range 100 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	if _, _, book := f.st.calls(); book != 1 {
		t.Fatalf("%d builds for one cold set, want 1", book)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

func TestListReusesTheFreshCache(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, book := f.st.calls()
	if book != 1 {
		t.Fatalf("book calls %d", book)
	}

	f.clk.Advance(CacheTTL - time.Second)
	f.st.setDeadlines(nil)
	second, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != len(first.Items) || second.GeneratedAt != first.GeneratedAt {
		t.Fatalf("a fresh cache changed: %+v -> %+v", first, second)
	}
	if _, _, book := f.st.calls(); book != 1 {
		t.Fatalf("a fresh cache read the store: book calls %d", book)
	}
}

func TestListServesStaleWhileOneRefreshRuns(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.threads.setBlock(release, started)
	f.clk.Advance(CacheTTL)

	stale, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Items) != len(first.Items) || stale.GeneratedAt != first.GeneratedAt {
		t.Fatalf("stale result %+v, want %+v", stale, first)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the refresh never reached the thread resolver")
	}

	// The refresh is single-flight: a second request is served from the old
	// set and does not start another read.
	f.threads.setStates(map[string]threadintel.Assessment{
		"C1": {ConversationID: "C1", State: threadintel.AnsweredByOwner, LastMessageID: 7,
			LastAt: stamp(f.now.Add(-time.Hour)), ActorName: "Client"},
		"C2": {ConversationID: "C2", State: threadintel.AwaitingOther, LastMessageID: 8,
			LastAt: stamp(f.now.Add(-90 * time.Minute)), ActorAddress: "vendor@delta.co"},
	})
	second, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != len(first.Items) {
		t.Fatalf("second stale result %+v", second)
	}
	if got := f.threads.callCount(); got != 2 {
		close(release)
		t.Fatalf("refreshes are not single-flight: thread calls %d", got)
	}

	close(release)
	var fresh Result
	waitUntil(t, "the refreshed set", func() bool {
		res, err := f.svc.List(t.Context(), Filter{})
		if err != nil {
			return false
		}
		fresh = res
		return len(res.Items) == 4 && res.Counts["reply"] == 1
	})
	if fresh.GeneratedAt != f.clk.Now().UTC().Format(time.RFC3339) {
		t.Fatalf("generated at %q", fresh.GeneratedAt)
	}
}

func TestListRefreshesWhenTheGenerationMoves(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.threads.setBlock(release, started)
	f.st.setGen(1)
	f.threads.setStates(map[string]threadintel.Assessment{})

	stale, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GeneratedAt != first.GeneratedAt || len(stale.Items) != len(first.Items) {
		t.Fatalf("a generation move rebuilt synchronously: %+v", stale)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a generation move did not start a refresh")
	}
	// The refresh is single-flight: a second request is served from the old
	// set and does not start another read.
	again, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != len(first.Items) {
		t.Fatalf("the stale set changed while refreshing: %+v", again)
	}
	if got := f.threads.callCount(); got != 2 {
		close(release)
		t.Fatalf("a generation move started %d builds, want one refresh", got-1)
	}
	close(release)

	waitUntil(t, "the generation refresh", func() bool {
		res, err := f.svc.List(t.Context(), Filter{})
		return err == nil && len(res.Items) == 3 && res.Counts["reply"] == 0
	})
	if _, _, book := f.st.calls(); book != 2 {
		t.Fatalf("the generation refresh read the store %d times, want 2", book)
	}
	if got := f.threads.callCount(); got != 2 {
		t.Fatalf("the generation move built %d times, want one refresh", got-1)
	}
}

func TestListKeepsStaleAfterAFailedRefreshAndRetries(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.List(t.Context(), Filter{}); err != nil {
		t.Fatal(err)
	}
	_, _, bookBefore := f.st.calls()

	f.clk.Advance(CacheTTL)
	f.st.setErr(errors.New("store read failed"))

	stale, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Items) != 5 {
		t.Fatalf("stale result %+v", stale)
	}
	waitUntil(t, "the failed refresh", func() bool {
		_, _, book := f.st.calls()
		return book >= bookBefore+1
	})

	// The set is still stale, so the next request retries instead of serving
	// it as fresh forever.
	waitUntil(t, "the retried failed refresh", func() bool {
		res, err := f.svc.List(t.Context(), Filter{})
		if err != nil {
			return false
		}
		if len(res.Items) != 5 {
			t.Fatalf("a failed refresh replaced the cached set: %+v", res)
		}
		_, _, book := f.st.calls()
		return book >= bookBefore+2
	})

	f.st.setErr(nil)
	remaining := f.st.deadlineRows()
	f.st.setDeadlines(remaining[1:])
	var res Result
	waitUntil(t, "the successful retry", func() bool {
		got, err := f.svc.List(t.Context(), Filter{})
		if err != nil {
			return false
		}
		res = got
		return len(got.Items) == 4 && got.Counts["due"] == 1
	})
	if len(res.Items) != 4 || res.Counts["due"] != 1 {
		t.Fatalf("result after recovery %+v", res)
	}
}

func TestListRefreshSurvivesACancelledRequestContext(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.List(t.Context(), Filter{}); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f.threads.setBlock(release, started)
	f.threads.setStates(map[string]threadintel.Assessment{
		"C2": {ConversationID: "C2", State: threadintel.AwaitingOther, LastMessageID: 8,
			LastAt: stamp(f.now.Add(-90 * time.Minute)), ActorAddress: "vendor@delta.co"},
	})
	f.clk.Advance(CacheTTL)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stale, err := f.svc.List(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Items) != 5 {
		t.Fatalf("stale result %+v", stale)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the detached refresh never started")
	}
	close(release)
	waitUntil(t, "the detached refresh", func() bool {
		res, err := f.svc.List(t.Context(), Filter{})
		return err == nil && len(res.Items) == 4
	})
}

func TestListFailsTheFirstBuildWithoutCachingIt(t *testing.T) {
	f := newFixture(t)
	f.st.setErr(errors.New("store read failed"))
	if _, err := f.svc.List(t.Context(), Filter{}); err == nil {
		t.Fatal("a failed first build must fail the request")
	}

	f.st.setErr(nil)
	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("recovered result %+v", res)
	}
}

// The decision source is the route, so a new route commit must rebuild the
// set even when mail and threads did not move.
func TestListRefreshesWhenTheRouteGenerationMoves(t *testing.T) {
	f := newFixture(t)
	first, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, bookBefore := f.st.calls()
	f.st.setRouteGen(1)
	stale, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GeneratedAt != first.GeneratedAt {
		t.Fatal("a route generation move rebuilt synchronously")
	}
	waitUntil(t, "the route generation refresh", func() bool {
		_, _, book := f.st.calls()
		return book == bookBefore+1
	})
	if _, err := f.svc.List(t.Context(), Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, _, book := f.st.calls(); book != bookBefore+1 {
		t.Fatalf("the route-fresh set was rebuilt: book calls %d", book)
	}
}

// The commitment view is one projection of the action owner: every due and
// reply row names the same canonical action/waiting/due-source values, never
// a local copy of the vocabulary.
func TestCommitmentsUseTheActionVocabulary(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.List(t.Context(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[Kind]bool{}
	for _, c := range res.Items {
		switch c.Kind {
		case KindDue:
			if c.Action != string(action.FollowUp) ||
				c.Waiting != string(action.WaitingOwner) ||
				c.DueSource != string(action.DueStored) {
				t.Fatalf("due vocabulary %+v", c)
			}
			seen[KindDue] = true
		case KindReply:
			if c.Action != string(action.Reply) && c.Action != string(action.Wait) {
				t.Fatalf("reply action %q", c.Action)
			}
			if c.Waiting != string(action.WaitingOwner) && c.Waiting != string(action.WaitingOther) {
				t.Fatalf("reply waiting %q", c.Waiting)
			}
			seen[KindReply] = true
		}
	}
	if !seen[KindDue] || !seen[KindReply] {
		t.Fatalf("fixture did not exercise both kinds: %+v", seen)
	}
}

// The API reference must name every kind the service can return: a client
// implementing from the docs would otherwise never request an action.
func TestAPIDocsListEveryCommitmentKind(t *testing.T) {
	doc, err := os.ReadFile("../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range Kinds {
		want := "`" + string(kind) + "`"
		if !strings.Contains(string(doc), want) {
			t.Errorf("docs/API.md does not name commitment kind %s", want)
		}
	}
}
