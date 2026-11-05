package brief

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

var testNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type fakeCommitments struct {
	res        commitments.Result
	loops      commitments.OrganizationOpenLoopsResult
	err        error
	loopsErr   error
	calls      int
	loopCalls  int
	lastFilter commitments.Filter
	started    chan struct{}
	release    chan struct{}
}

func (f *fakeCommitments) List(_ context.Context, filter commitments.Filter) (commitments.Result, error) {
	f.calls++
	f.lastFilter = filter
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return f.res, f.err
}

func (f *fakeCommitments) OpenLoops(context.Context) (commitments.OrganizationOpenLoopsResult, error) {
	f.loopCalls++
	return f.loops, f.loopsErr
}

type fakeConversations struct {
	facts     conversations.Facts
	err       error
	calls     int
	lastSince string
	lastRecent,
	lastOrgs int
}

func (f *fakeConversations) Facts(_ context.Context, since string, recent, orgs int) (conversations.Facts, error) {
	f.calls++
	f.lastSince, f.lastRecent, f.lastOrgs = since, recent, orgs
	return f.facts, f.err
}

type fakePriority struct {
	census priority.TopicCensus
	err    error
	calls  int
}

type fakePreviewGate struct {
	preview bool
}

func (g *fakePreviewGate) PreviewAnswers(context.Context) bool { return g.preview }

type fakeStore struct {
	gen      uint64
	routeGen uint64
}

func (f *fakeStore) MailGeneration() uint64  { return f.gen }
func (f *fakeStore) RouteGeneration() uint64 { return f.routeGen }

func (f *fakePriority) Topics(context.Context) (priority.TopicCensus, error) {
	f.calls++
	return f.census, f.err
}

func newTestService(t *testing.T, c Commitments, cv Conversations, p Priority) (*Service, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.New(testNow)
	svc, err := New(Deps{Commitments: c, Conversations: cv, Priority: p, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	return svc, clk
}

func owner(id string, status commitments.Status, title, subject string) commitments.Commitment {
	return commitments.Commitment{
		ID: id, Kind: commitments.KindDue, Direction: commitments.DirectionOwner, Status: status,
		Title: title, Subject: subject, Evidence: []int64{1},
	}
}

func other(id string, status commitments.Status) commitments.Commitment {
	return commitments.Commitment{
		ID: id, Kind: commitments.KindReply, Direction: commitments.DirectionOther, Status: status,
		Title: "Reply", Subject: "Reply", ConversationID: "C-" + id, Evidence: []int64{2},
	}
}

func TestNewRequiresTheComposingSources(t *testing.T) {
	clk := clocktest.New(testNow)
	if _, err := New(Deps{Conversations: &fakeConversations{}, Clock: clk}); err == nil {
		t.Fatal("missing commitments accepted")
	}
	if _, err := New(Deps{Commitments: &fakeCommitments{}, Clock: clk}); err == nil {
		t.Fatal("missing conversations accepted")
	}
	if _, err := New(Deps{Commitments: &fakeCommitments{}, Conversations: &fakeConversations{}}); err == nil {
		t.Fatal("missing clock accepted")
	}
}

func TestBriefSplitsCommitmentsAndKeepsOrder(t *testing.T) {
	due := owner("due-1", commitments.StatusOverdue, "Invoice", "Invoice")
	due.OrganizationKey, due.OrganizationLabel = "c:atelier nord", "Atelier Nord"
	due.Person, due.Due = "alice@atelier-nord.test", "2026-11-01T09:00:00Z"
	due.ConversationID, due.MessageID = "C1", 11
	decision := owner("decision-1", commitments.StatusOpen, "", "Fallback subject")
	decision.Kind = commitments.KindDecision
	extra := owner("due-2", commitments.StatusOpen, "Follow up", "Follow up")
	doneOwner := owner("done-owner", commitments.StatusDone, "Finished", "Finished")
	reply := other("reply-1", commitments.StatusOpen)
	reply.Person, reply.LastAt = "bob@example.test", "2026-10-27T07:00:00Z"
	doneOther := other("done-other", commitments.StatusDone)

	commitmentsFake := &fakeCommitments{res: commitments.Result{
		Items: []commitments.Commitment{due, decision, doneOwner, reply, extra, doneOther},
		Counts: map[string]int{
			"owner": 7, "other": 3, "done": 2,
		},
	}}
	conversationsFake := &fakeConversations{}
	priorityFake := &fakePriority{}
	svc, _ := newTestService(t, commitmentsFake, conversationsFake, priorityFake)

	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 1 || commitmentsFake.loopCalls != 1 ||
		conversationsFake.calls != 1 || priorityFake.calls != 1 {
		t.Fatalf("composition calls: commitments %d/%d, conversations %d, priority %d",
			commitmentsFake.calls, commitmentsFake.loopCalls, conversationsFake.calls, priorityFake.calls)
	}
	if want := (commitments.Filter{Limit: CommitmentScan}); commitmentsFake.lastFilter != want {
		t.Fatalf("commitment filter %+v, want %+v", commitmentsFake.lastFilter, want)
	}
	if len(result.Must.Items) != 3 || len(result.Waiting.Items) != 1 {
		t.Fatalf("split: must %d, waiting %d", len(result.Must.Items), len(result.Waiting.Items))
	}
	gotIDs := []string{result.Must.Items[0].ID, result.Must.Items[1].ID, result.Must.Items[2].ID}
	if want := []string{"due-1", "decision-1", "due-2"}; fmt.Sprint(gotIDs) != fmt.Sprint(want) {
		t.Fatalf("must order %v, want %v", gotIDs, want)
	}
	if result.Must.Count != 7 || result.Waiting.Count != 3 {
		t.Fatalf("direction totals: must %d, waiting %d", result.Must.Count, result.Waiting.Count)
	}
	first := result.Must.Items[0]
	if first.Kind != string(commitments.KindDue) || first.Title != "Invoice" || first.Due != "2026-11-01T09:00:00Z" ||
		first.Status != "overdue" || first.Direction != "owner" || first.ConversationID != "C1" ||
		first.MessageID != 11 || first.Person != "alice@atelier-nord.test" ||
		first.Organization == nil || first.Organization.Key != "c:atelier nord" ||
		len(first.Evidence) != 1 || first.Evidence[0] != 1 {
		t.Fatalf("first owner item %+v", first)
	}
	if fallback := result.Must.Items[1]; fallback.Title != "Fallback subject" {
		t.Fatalf("missing title was invented: %q", fallback.Title)
	}
	waiting := result.Waiting.Items[0]
	if waiting.Direction != "other" || waiting.ConversationID != "C-reply-1" || waiting.Person != "bob@example.test" ||
		waiting.LastAt != "2026-10-27T07:00:00Z" {
		t.Fatalf("waiting item %+v", waiting)
	}
	wantSince := testNow.Truncate(24 * time.Hour).Add(-DefaultSince).Format(time.RFC3339)
	if conversationsFake.lastSince != wantSince || conversationsFake.lastRecent != ChangedLimit ||
		conversationsFake.lastOrgs != OrganizationLimit {
		t.Fatalf("facts request %q/%d/%d, want %q/%d/%d", conversationsFake.lastSince,
			conversationsFake.lastRecent, conversationsFake.lastOrgs, wantSince, ChangedLimit, OrganizationLimit)
	}
	if result.Changed.Since != wantSince {
		t.Fatalf("changed since %q, want %q", result.Changed.Since, wantSince)
	}
	if result.GeneratedAt != testNow.Format(time.RFC3339) {
		t.Fatalf("generated_at %q", result.GeneratedAt)
	}
}

func TestBriefCachesCompositionAndServesStale(t *testing.T) {
	commitmentsFake := &fakeCommitments{}
	conversationsFake := &fakeConversations{}
	priorityFake := &fakePriority{}
	svc, clk := newTestService(t, commitmentsFake, conversationsFake, priorityFake)

	first, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 1 || conversationsFake.calls != 1 || priorityFake.calls != 1 {
		t.Fatalf("first composition calls: %d/%d/%d", commitmentsFake.calls, conversationsFake.calls, priorityFake.calls)
	}
	second, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.GeneratedAt != first.GeneratedAt || commitmentsFake.calls != 1 {
		t.Fatalf("fresh cache missed: generated %q/%q calls %d", second.GeneratedAt, first.GeneratedAt, commitmentsFake.calls)
	}

	clk.Advance(CacheTTL + time.Second)
	stale, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stale.GeneratedAt != first.GeneratedAt {
		t.Fatalf("expired Brief was not served stale: %q", stale.GeneratedAt)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := svc.Brief(t.Context(), "")
		if err == nil && got.GeneratedAt != first.GeneratedAt {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if commitmentsFake.calls < 2 {
		t.Fatalf("expired Brief never refreshed: %d calls", commitmentsFake.calls)
	}
}

// Eight concurrent cold readers of one Brief share one composition: every
// source is called once.
func TestBriefColdBuildIsSingleFlight(t *testing.T) {
	commitmentsFake := &fakeCommitments{started: make(chan struct{}, 8), release: make(chan struct{})}
	conversationsFake := &fakeConversations{}
	priorityFake := &fakePriority{}
	svc, _ := newTestService(t, commitmentsFake, conversationsFake, priorityFake)

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
			results[i], errs[i] = svc.Brief(context.Background(), "")
		}()
	}
	for range n {
		<-entered
	}
	<-commitmentsFake.started
	for range 100 {
		runtime.Gosched()
	}
	close(commitmentsFake.release)
	wg.Wait()

	if commitmentsFake.calls != 1 || conversationsFake.calls != 1 || priorityFake.calls != 1 {
		t.Fatalf("a cold Brief composed %d/%d/%d times, want 1/1/1",
			commitmentsFake.calls, conversationsFake.calls, priorityFake.calls)
	}
	for i := range n {
		if errs[i] != nil || results[i].GeneratedAt != testNow.Format(time.RFC3339) {
			t.Fatalf("caller %d: %+v %v", i, results[i], errs[i])
		}
	}
}

func TestBriefCacheKeyedByGenerationAndBounded(t *testing.T) {
	commitmentsFake := &fakeCommitments{}
	conversationsFake := &fakeConversations{}
	priorityFake := &fakePriority{}
	store := &fakeStore{gen: 1}
	clk := clocktest.New(testNow)
	svc, err := New(Deps{Commitments: commitmentsFake, Conversations: conversationsFake,
		Priority: priorityFake, Store: store, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	store.gen = 2
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 2 {
		t.Fatalf("generation bump reused the old Brief: %d calls", commitmentsFake.calls)
	}

	for i := 0; i < cacheSize+8; i++ {
		clk.Advance(500 * time.Millisecond)
		since := testNow.Add(-time.Duration(i+1) * time.Hour).Format(time.RFC3339)
		if _, err := svc.Brief(t.Context(), since); err != nil {
			t.Fatal(err)
		}
	}
	// The bound evicted the oldest Brief: its key builds again.
	before := commitmentsFake.calls
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != before+1 {
		t.Fatalf("the evicted Brief was still served: %d → %d calls", before, commitmentsFake.calls)
	}
}

// The Brief has no displayed preview flag: its only preview-sensitive state
// is the cache identity, which follows the one effective rule rather than the
// raw setting. Without a gate the preview is off.
func TestBriefPreviewFollowsTheGateRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate learning.Previewer
	}{
		{"no gate", nil},
		{"gate says no preview", &fakePreviewGate{}},
		{"gate says preview", &fakePreviewGate{preview: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commitmentsFake := &fakeCommitments{}
			svc, err := New(Deps{Commitments: commitmentsFake, Conversations: &fakeConversations{},
				Gate: tc.gate, Clock: clocktest.New(testNow)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Brief(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Brief(t.Context(), ""); err != nil {
				t.Fatal(err)
			}
			if commitmentsFake.calls != 1 {
				t.Fatalf("one preview state composed %d Briefs, want 1", commitmentsFake.calls)
			}
		})
	}
}

// A gate flip changes the brief cache identity, so a new composition runs
// with the new preview state under its own key.
func TestBriefPreviewFlipChangesTheCacheKey(t *testing.T) {
	commitmentsFake := &fakeCommitments{}
	svc, _ := newTestService(t, commitmentsFake, &fakeConversations{}, &fakePriority{})
	gate := &fakePreviewGate{}
	svc.gate = gate
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 1 {
		t.Fatalf("first composition calls = %d", commitmentsFake.calls)
	}
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 1 {
		t.Fatalf("same preview state rebuilt the Brief: %d calls", commitmentsFake.calls)
	}
	gate.preview = true
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 2 {
		t.Fatalf("a preview flip reused the old Brief: %d calls", commitmentsFake.calls)
	}
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 2 {
		t.Fatalf("the preview Brief was rebuilt: %d calls", commitmentsFake.calls)
	}
	gate.preview = false
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 2 {
		t.Fatalf("flipping preview back rebuilt the cached Brief: %d calls", commitmentsFake.calls)
	}
}

func TestBriefCapsEachCommitmentDirection(t *testing.T) {
	items := make([]commitments.Commitment, 0, 40)
	for i := 0; i < 20; i++ {
		items = append(items, owner(fmt.Sprintf("owner-%02d", i), commitments.StatusOpen, "Owner", "Owner"))
	}
	for i := 0; i < 20; i++ {
		items = append(items, other(fmt.Sprintf("other-%02d", i), commitments.StatusOpen))
	}
	svc, _ := newTestService(t,
		&fakeCommitments{res: commitments.Result{Items: items, Counts: map[string]int{"owner": 20, "other": 20}}},
		&fakeConversations{}, &fakePriority{})

	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Must.Items) != MustLimit || result.Must.Count != 20 {
		t.Fatalf("must cap: %d items, count %d", len(result.Must.Items), result.Must.Count)
	}
	if len(result.Waiting.Items) != WaitingLimit || result.Waiting.Count != 20 {
		t.Fatalf("waiting cap: %d items, count %d", len(result.Waiting.Items), result.Waiting.Count)
	}
	for i, item := range result.Must.Items {
		if want := fmt.Sprintf("owner-%02d", i); item.ID != want {
			t.Fatalf("must[%d] = %q, want %q", i, item.ID, want)
		}
	}
	for i, item := range result.Waiting.Items {
		if want := fmt.Sprintf("other-%02d", i); item.ID != want {
			t.Fatalf("waiting[%d] = %q, want %q", i, item.ID, want)
		}
	}
}

func TestBriefMapsChangedSinceAndRelationships(t *testing.T) {
	facts := conversations.Facts{
		Total: 2,
		Counts: map[string]int{
			string(threadintel.AwaitingOwner): 1,
			string(threadintel.AwaitingOther): 1,
		},
		RecentTotal: 5,
		Recent: []conversations.Fact{{
			ConversationID: "C1", Subject: "Latest activity", SenderName: "Alice Martin",
			SenderAddress: "alice@atelier-nord.test", LastAt: "2026-10-27T10:00:00Z", LastMessageID: 42,
			Messages: 4, Unread: 1,
			State:        threadintel.Assessment{State: threadintel.AwaitingOwner, Reason: threadintel.ReasonInboundLast},
			Organization: &conversations.Organization{Key: "c:atelier nord", Label: "Atelier Nord", Declared: true},
		}},
		// The conversations rollup is deliberately not the relationship
		// source anymore: these open organizations must not create a row.
		OrganizationsOpenTotal: 6,
		Organizations: []conversations.OrganizationFact{{
			Organization:  conversations.Organization{Key: "c:atelier nord", Label: "Atelier Nord", Declared: true},
			Conversations: 3, Messages: 12, AwaitingOwner: 2, AwaitingOther: 1,
			LastAt: "2026-10-27T10:00:00Z", LastMessageID: 42, LastSubject: "Latest activity",
		}},
	}
	conversationsFake := &fakeConversations{facts: facts}
	commitmentsFake := &fakeCommitments{loops: commitments.OrganizationOpenLoopsResult{
		GeneratedAt: "2026-10-27T12:00:00Z",
		Total:       4,
		Organizations: []commitments.OrganizationOpenLoops{
			{Key: "d:acme.example", Label: "Acme", Total: 6, Owner: 5, Other: 1, Overdue: 3, StoredDue: 2},
			{Key: "c:atelier nord", Label: "Atelier Nord", Total: 4, Owner: 3, Other: 1, Overdue: 1, StoredDue: 1},
		},
	}}
	svc, _ := newTestService(t, commitmentsFake, conversationsFake, &fakePriority{})

	result, err := svc.Brief(t.Context(), "2026-10-26T02:00:00+02:00")
	if err != nil {
		t.Fatal(err)
	}
	if conversationsFake.lastSince != "2026-10-26T00:00:00Z" {
		t.Fatalf("since forwarded as %q", conversationsFake.lastSince)
	}
	if result.Changed.Since != "2026-10-26T00:00:00Z" || result.Changed.Count != 5 || len(result.Changed.Items) != 1 {
		t.Fatalf("changed section %+v", result.Changed)
	}
	changed := result.Changed.Items[0]
	if changed.ID != "activity:C1" || changed.Kind != "activity" || changed.Title != "Latest activity" ||
		changed.Subject != "Latest activity" || changed.ConversationID != "C1" || changed.MessageID != 42 ||
		changed.LastAt != "2026-10-27T10:00:00Z" || changed.State != string(threadintel.AwaitingOwner) ||
		changed.StateReason != string(threadintel.ReasonInboundLast) || changed.Person != "alice@atelier-nord.test" {
		t.Fatalf("changed item %+v", changed)
	}
	if changed.Organization == nil || changed.Organization.Key != "c:atelier nord" ||
		len(changed.Evidence) != 1 || changed.Evidence[0] != 42 {
		t.Fatalf("changed evidence %+v", changed)
	}

	// The section reads the loops result, in its order and with its honest
	// total; the conversations rollup above contributes nothing.
	if result.Relationships.Count != 4 || len(result.Relationships.Items) != 2 {
		t.Fatalf("relationships section %+v", result.Relationships)
	}
	first, second := result.Relationships.Items[0], result.Relationships.Items[1]
	if first.ID != "organization:d:acme.example" || first.Kind != "organization" ||
		first.Title != "Acme" || first.Organization == nil || first.Organization.Key != "d:acme.example" ||
		first.Organization.Label != "Acme" || first.Count != 6 || first.Overdue != 3 || first.StoredDue != 2 {
		t.Fatalf("first relationship item %+v", first)
	}
	if second.ID != "organization:c:atelier nord" || second.Title != "Atelier Nord" ||
		second.Count != 4 || second.Overdue != 1 || second.StoredDue != 1 {
		t.Fatalf("second relationship item %+v", second)
	}
	// A loop row carries no thread or rollup facts: it is the organization
	// and its commitment partition, nothing the conversation digest said.
	if first.Conversations != 0 || first.Messages != 0 || first.AwaitingOwner != 0 ||
		first.AwaitingOther != 0 || first.MessageID != 0 || first.LastAt != "" ||
		first.Evidence != nil {
		t.Fatalf("a loop row invented digest facts: %+v", first)
	}
}

// An empty loops result keeps the relationship section empty: the section is
// always present with an array, and the conversation digest's organizations
// do not leak into it.
func TestBriefEmptyOpenLoopsKeepTheRelationshipSectionEmpty(t *testing.T) {
	commitmentsFake := &fakeCommitments{loops: commitments.OrganizationOpenLoopsResult{}}
	conversationsFake := &fakeConversations{facts: conversations.Facts{
		OrganizationsOpenTotal: 2,
		Organizations: []conversations.OrganizationFact{{
			Organization:  conversations.Organization{Key: "c:quiet.test", Label: "Quiet"},
			Conversations: 2, AwaitingOwner: 1, LastMessageID: 9,
		}},
	}}
	svc, _ := newTestService(t, commitmentsFake, conversationsFake, &fakePriority{})

	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Relationships.Count != 0 || result.Relationships.Items == nil || len(result.Relationships.Items) != 0 {
		t.Fatalf("empty loops relationships %+v", result.Relationships)
	}
	if commitmentsFake.loopCalls != 1 {
		t.Fatalf("loops called %d times, want 1", commitmentsFake.loopCalls)
	}
}

// A loops read failure fails the Brief instead of quietly dropping the
// section: the relationship list is an obligation view, not a suggestion.
func TestBriefOpenLoopsFailureFailsTheBrief(t *testing.T) {
	commitmentsFake := &fakeCommitments{loopsErr: errors.New("loops unavailable")}
	svc, _ := newTestService(t, commitmentsFake, &fakeConversations{}, &fakePriority{})
	if _, err := svc.Brief(t.Context(), ""); err == nil {
		t.Fatal("a loops read failure must fail the Brief")
	}
}

func TestBriefUsesWholeMailboxTopicCensus(t *testing.T) {
	priorityFake := &fakePriority{census: priority.TopicCensus{
		Classified: 7,
		Scanned:    100,
		Topics: []priority.TopicCount{
			{Topic: "project_work", Count: 4, Evidence: []int64{9, 5, 1}, Subtopics: []priority.SubtopicCount{
				{Subtopic: "status_update", Count: 3, Evidence: []int64{9, 5}},
				{Subtopic: "approval", Count: 1, Evidence: []int64{1}},
			}},
			{Topic: "billing_account", Count: 2, Evidence: []int64{8, 4}},
			{Topic: "hiring", Count: 1, Evidence: []int64{3}},
		},
	}}
	conversationsFake := &fakeConversations{facts: conversations.Facts{Messages: 120}}
	svc, _ := newTestService(t, &fakeCommitments{}, conversationsFake, priorityFake)

	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if priorityFake.calls != 1 {
		t.Fatalf("priority census calls %d, want 1", priorityFake.calls)
	}
	topics := result.Topics
	if !topics.Suggested || topics.Scope != TopicScope || topics.Count != 7 || topics.Total != 120 ||
		len(topics.Items) != 3 {
		t.Fatalf("topics section %+v", topics)
	}
	if topics.Items[0].Title != "project_work" || topics.Items[0].Count != 4 ||
		topics.Items[1].Title != "billing_account" || topics.Items[1].Count != 2 ||
		topics.Items[2].Title != "hiring" || topics.Items[2].Count != 1 {
		t.Fatalf("topic order/counts %+v", topics.Items)
	}
	if !topics.Items[0].Suggested || topics.Items[0].Kind != "topic" || topics.Items[0].ID != "topic:project_work" {
		t.Fatalf("topic item %+v", topics.Items[0])
	}
	if want := []int64{9, 5, 1}; fmt.Sprint(topics.Items[0].Evidence) != fmt.Sprint(want) {
		t.Fatalf("topic evidence %v, want %v", topics.Items[0].Evidence, want)
	}
	subs := topics.Items[0].Subtopics
	if len(subs) != 2 || subs[0].ID != "subtopic:project_work/status_update" || subs[0].Count != 3 ||
		subs[0].Topic != "project_work" || subs[0].Subtopic != "status_update" ||
		fmt.Sprint(subs[0].Evidence) != fmt.Sprint([]int64{9, 5}) {
		t.Fatalf("subtopics %+v", subs)
	}
}

func TestBriefCapsTopicsAndCountsAllClassifiedMail(t *testing.T) {
	census := priority.TopicCensus{Classified: 20}
	for i := 0; i < 20; i++ {
		census.Topics = append(census.Topics, priority.TopicCount{
			Topic: fmt.Sprintf("c%02d", i), Count: 1, Evidence: []int64{int64(i + 1)},
		})
	}
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{}, &fakePriority{census: census})
	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Topics.Count != 20 || len(result.Topics.Items) != TopicLimit {
		t.Fatalf("topics cap: count %d, items %d", result.Topics.Count, len(result.Topics.Items))
	}
	for i, item := range result.Topics.Items {
		if want := fmt.Sprintf("topic:c%02d", i); item.ID != want {
			t.Fatalf("topics[%d] = %q, want %q", i, item.ID, want)
		}
	}
}

func TestBriefTopicCensusCannotExceedMailboxTotal(t *testing.T) {
	// A stale conversation snapshot must not produce the impossible claim
	// that more messages are classified than exist.
	priorityFake := &fakePriority{census: priority.TopicCensus{
		Classified: 12, Scanned: 15,
		Topics: []priority.TopicCount{{Topic: "project_work", Count: 12, Evidence: []int64{1}}},
	}}
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{
		facts: conversations.Facts{Messages: 10},
	}, priorityFake)
	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Topics.Total != 15 {
		t.Fatalf("topics total %d, want the scanned lower bound 15", result.Topics.Total)
	}
}

func TestBriefWithoutPriorityHasEmptyTopics(t *testing.T) {
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{
		facts: conversations.Facts{Messages: 42},
	}, nil)
	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Topics.Items == nil || len(result.Topics.Items) != 0 || result.Topics.Count != 0 ||
		!result.Topics.Suggested || result.Topics.Scope != TopicScope || result.Topics.Total != 42 {
		t.Fatalf("nil priority topics %+v", result.Topics)
	}
}

func TestBriefTopicFailureDoesNotFailTheBrief(t *testing.T) {
	priorityFake := &fakePriority{err: errors.New("gate unavailable")}
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{
		facts: conversations.Facts{Messages: 42},
	}, priorityFake)
	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatalf("priority failure failed the Brief: %v", err)
	}
	if result.Topics.Items == nil || len(result.Topics.Items) != 0 || result.Topics.Count != 0 ||
		!result.Topics.Suggested || result.Topics.Scope != TopicScope || result.Topics.Total != 42 {
		t.Fatalf("failed topics %+v", result.Topics)
	}
	if result.Must.Items == nil || result.Waiting.Items == nil || result.Changed.Items == nil ||
		result.Relationships.Items == nil {
		t.Fatalf("a priority failure emptied another section: %+v", result)
	}
}

func TestBriefInvalidSince(t *testing.T) {
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{}, nil)
	for _, since := range []string{
		"yesterday",
		"2026-10-27",
		"2026-10-27T12:00:00",
		testNow.Add(time.Minute).Format(time.RFC3339),
	} {
		if _, err := svc.Brief(t.Context(), since); !errors.Is(err, ErrInvalidSince) {
			t.Fatalf("since %q: err %v, want ErrInvalidSince", since, err)
		}
	}
	// The exact boundary is not in the future.
	if _, err := svc.Brief(t.Context(), testNow.Format(time.RFC3339)); err != nil {
		t.Fatalf("the exact now boundary was rejected: %v", err)
	}
}

func TestBriefEmptyResultHasEveryArray(t *testing.T) {
	svc, _ := newTestService(t, &fakeCommitments{}, &fakeConversations{}, &fakePriority{})
	result, err := svc.Brief(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.GeneratedAt != testNow.Format(time.RFC3339) {
		t.Fatalf("generated_at %q", result.GeneratedAt)
	}
	sections := map[string]Section{
		"must": result.Must, "waiting": result.Waiting, "changed": result.Changed,
		"relationships": result.Relationships, "topics": result.Topics,
	}
	for name, section := range sections {
		if section.Items == nil {
			t.Fatalf("%s.items is nil", name)
		}
		if section.Count != 0 || len(section.Items) != 0 {
			t.Fatalf("%s is not empty: %+v", name, section)
		}
	}
	if result.Changed.Since == "" {
		t.Fatal("changed.since is empty with the default boundary")
	}
}

// A route commit moves the Brief's route-derived sections even when no mail
// arrived, so the route generation is part of the key.
func TestBriefCacheKeyedByRouteGeneration(t *testing.T) {
	commitmentsFake := &fakeCommitments{}
	conversationsFake := &fakeConversations{}
	store := &fakeStore{gen: 1}
	svc, err := New(Deps{Commitments: commitmentsFake, Conversations: conversationsFake,
		Store: store, Clock: clocktest.New(testNow)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	store.routeGen = 1
	if _, err := svc.Brief(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if commitmentsFake.calls != 2 {
		t.Fatalf("a route generation move reused the old Brief: %d calls", commitmentsFake.calls)
	}
}
