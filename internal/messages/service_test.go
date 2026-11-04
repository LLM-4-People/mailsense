package messages

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// fakeStore is the one read the service owns. It can hold the first build
// open so a test can count how many callers joined it.
type fakeStore struct {
	mu      sync.Mutex
	gen     uint64
	page    store.MessageListPage
	err     error
	block   chan struct{}
	started chan struct{}
	calls   int32
	queries []store.MessageListQuery
}

func (f *fakeStore) MessageList(ctx context.Context, q store.MessageListQuery) (store.MessageListPage, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.queries = append(f.queries, q)
	page, err, block, started := f.page, f.err, f.block, f.started
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
			return store.MessageListPage{Messages: []store.MessageListRow{}}, ctx.Err()
		}
	}
	return testClonePage(page), err
}

func (f *fakeStore) MailGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeStore) set(gen uint64, page store.MessageListPage, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gen = gen
	f.page = page
	f.err = err
}

func (f *fakeStore) callCount() int { return int(atomic.LoadInt32(&f.calls)) }

func testClonePage(in store.MessageListPage) store.MessageListPage {
	out := in
	out.Messages = append([]store.MessageListRow(nil), in.Messages...)
	if in.Counts != nil {
		out.Counts = make(map[mail.FolderRole]int, len(in.Counts))
		for role, n := range in.Counts {
			out.Counts[role] = n
		}
	}
	return out
}

func unfilteredPage() store.MessageListQuery {
	return store.MessageListQuery{Limit: store.MessageListDefaultLimit, Counts: true}
}

func pageWith(subject string) store.MessageListPage {
	return store.MessageListPage{
		Messages: []store.MessageListRow{{RowID: 1, Subject: subject}, {RowID: 2, Subject: subject + " 2"}},
		Total:    2,
		Counts:   map[mail.FolderRole]int{mail.RoleInbox: 1, mail.RoleSent: 1},
		HasMore:  false,
	}
}

// Four concurrent first-page reads share one store build. The old tree had
// no package at all; a naive service would call the store four times.
func TestFirstPageColdBuildIsSingleFlight(t *testing.T) {
	block := make(chan struct{})
	st := &fakeStore{
		page:    pageWith("one"),
		block:   block,
		started: make(chan struct{}, 1),
	}
	svc := New(st)

	const readers = 4
	results := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			page, err := svc.List(context.Background(), unfilteredPage())
			if err == nil && len(page.Messages) != 2 {
				err = errors.New("first page lost its rows")
			}
			results <- err
		}()
	}

	select {
	case <-st.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first build never started")
	}
	// Let the other three callers reach the in-flight entry.
	time.Sleep(100 * time.Millisecond)
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times while the first build was held, want 1", got)
	}
	close(block)
	for i := 0; i < readers; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("reader %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reader %d did not join the build", i)
		}
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times after four readers, want 1", got)
	}
}

// The caller that starts the one build may end its request early; the build
// is detached from that context, so a joined live waiter still receives the
// page instead of the leader's context.Canceled.
func TestFirstPageWaiterSurvivesLeaderCancel(t *testing.T) {
	block := make(chan struct{})
	st := &fakeStore{
		page:    pageWith("survives"),
		block:   block,
		started: make(chan struct{}, 1),
	}
	svc := New(st)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := svc.List(leaderCtx, unfilteredPage())
		leader <- err
	}()
	select {
	case <-st.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first build never started")
	}

	waiter := make(chan store.MessageListPage, 1)
	waiterErr := make(chan error, 1)
	go func() {
		page, err := svc.List(context.Background(), unfilteredPage())
		waiter <- page
		waiterErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times while the first build was held, want 1", got)
	}

	cancelLeader()
	select {
	case err := <-leader:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled leader got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled leader did not leave the build")
	}
	// The build must survive the leader's cancellation: the waiter is still
	// waiting and has not received the leader's error.
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-waiterErr:
		t.Fatalf("the waiter received the leader's cancellation: %v", err)
	default:
	}

	close(block)
	select {
	case page := <-waiter:
		if len(page.Messages) != 2 || page.Messages[0].Subject != "survives" {
			t.Fatalf("waiter page: %+v", page)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not receive the shared page")
	}
	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter: %v", err)
	}
	// The detached build also installed the page for the next reader.
	if page, err := svc.List(context.Background(), unfilteredPage()); err != nil || len(page.Messages) != 2 {
		t.Fatalf("cached page after the cancelled leader: %+v %v", page, err)
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times for one detached build, want 1", got)
	}
}

// A cache entry belongs to the mail generation that produced it: a write
// that changes mail invalidates the first page even if no TTL expired.
func TestFirstPageCacheFollowsMailGeneration(t *testing.T) {
	st := &fakeStore{}
	st.set(7, pageWith("seven"), nil)
	svc := New(st)

	if page, err := svc.List(context.Background(), unfilteredPage()); err != nil || page.Messages[0].Subject != "seven" {
		t.Fatalf("first read: %+v %v", page, err)
	}
	if page, err := svc.List(context.Background(), unfilteredPage()); err != nil || page.Messages[0].Subject != "seven" {
		t.Fatalf("cached read: %+v %v", page, err)
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times for two reads at one generation, want 1", got)
	}

	st.set(8, pageWith("eight"), nil)
	page, err := svc.List(context.Background(), unfilteredPage())
	if err != nil {
		t.Fatal(err)
	}
	if page.Messages[0].Subject != "eight" {
		t.Fatalf("page after generation 8 is %q, want eight", page.Messages[0].Subject)
	}
	if got := st.callCount(); got != 2 {
		t.Fatalf("store called %d times after the generation changed, want 2", got)
	}
}

// A failed cold build is shared by everyone waiting on it, then forgotten:
// the next caller retries instead of receiving a cached error.
func TestFirstPageFailureIsSharedButNotCached(t *testing.T) {
	wantErr := errors.New("store unavailable")
	block := make(chan struct{})
	st := &fakeStore{
		page:    pageWith("one"),
		err:     wantErr,
		block:   block,
		started: make(chan struct{}, 1),
	}
	svc := New(st)

	const readers = 3
	results := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			_, err := svc.List(context.Background(), unfilteredPage())
			results <- err
		}()
	}
	select {
	case <-st.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first build never started")
	}
	time.Sleep(100 * time.Millisecond)
	close(block)
	for i := 0; i < readers; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, wantErr) {
				t.Fatalf("reader %d got %v, want the shared failure", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reader %d did not receive the shared failure", i)
		}
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times for one failed build, want 1", got)
	}

	st.set(7, pageWith("recovered"), nil)
	page, err := svc.List(context.Background(), unfilteredPage())
	if err != nil {
		t.Fatalf("a failed build was cached: %v", err)
	}
	if page.Messages[0].Subject != "recovered" {
		t.Fatalf("retry page is %q, want recovered", page.Messages[0].Subject)
	}
	if got := st.callCount(); got != 2 {
		t.Fatalf("store called %d times after the retry, want 2", got)
	}
}

// Callers cannot mutate the cache through the page they received.
func TestFirstPageCacheReturnsIndependentCopies(t *testing.T) {
	st := &fakeStore{}
	st.set(1, pageWith("original"), nil)
	svc := New(st)

	first, err := svc.List(context.Background(), unfilteredPage())
	if err != nil {
		t.Fatal(err)
	}
	first.Messages[0].Subject = "mutated"
	first.Messages = append(first.Messages, store.MessageListRow{RowID: 99})
	first.Counts[mail.RoleInbox] = 99
	first.Total = 99

	second, err := svc.List(context.Background(), unfilteredPage())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.Messages[0].Subject != "original" {
		t.Fatalf("second page saw the first caller's mutation: %+v", second.Messages)
	}
	if second.Counts[mail.RoleInbox] != 1 || second.Total != 2 {
		t.Fatalf("second page counts were mutated: %+v total %d", second.Counts, second.Total)
	}

	second.Messages[1].Subject = "mutated again"
	third, err := svc.List(context.Background(), unfilteredPage())
	if err != nil {
		t.Fatal(err)
	}
	if third.Messages[1].Subject != "original 2" {
		t.Fatalf("third page saw the second caller's mutation: %q", third.Messages[1].Subject)
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times for three cached reads, want 1", got)
	}
}

// Only the unfiltered first page is cached; a role, store, sender, unread,
// cursor or a no-counts page always reaches the store.
func TestNonFirstPageBypassesTheCache(t *testing.T) {
	st := &fakeStore{}
	st.set(1, pageWith("page"), nil)
	svc := New(st)

	cases := map[string]store.MessageListQuery{
		"role":      {Role: mail.RoleInbox, Limit: store.MessageListDefaultLimit, Counts: true},
		"no counts": {Limit: store.MessageListDefaultLimit},
		"cursor":    {Before: store.Cursor{Instant: "2026-01-01T00:00:00Z", ID: 5}, Limit: store.MessageListDefaultLimit, Counts: true},
		"store":     {Store: "source:store", Limit: store.MessageListDefaultLimit, Counts: true},
		"sender":    {Sender: "demo@example.test", Limit: store.MessageListDefaultLimit, Counts: true},
		"unread":    {Unread: true, Limit: store.MessageListDefaultLimit, Counts: true},
		"limit":     {Limit: 50, Counts: true},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			before := st.callCount()
			if _, err := svc.List(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.List(context.Background(), q); err != nil {
				t.Fatal(err)
			}
			if got := st.callCount() - before; got != 2 {
				t.Fatalf("store called %d times for two %s reads, want 2", got, name)
			}
		})
	}
	if got := st.callCount(); got != 14 {
		t.Fatalf("store called %d times, want 14", got)
	}
	// The first page itself is still cacheable and is not poisoned by the
	// bypassing reads.
	if _, err := svc.List(context.Background(), unfilteredPage()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(context.Background(), unfilteredPage()); err != nil {
		t.Fatal(err)
	}
	if got := st.callCount(); got != 15 {
		t.Fatalf("store called %d times after the first-page pair, want 15", got)
	}
}

// TestFirstPageBuildIsJoinableAfterTheLeaderLeft pins the one guarantee the
// service cannot borrow from viewcache: the build is detached from the caller
// that started it. A caller arriving after the leader walked away must join
// the same build and receive its page, with the store read once. Under a
// cache whose first build runs on the caller's context (viewcache.buildFirst),
// the leader's cancellation would abort the read and this caller would pay for
// a second one, or receive the cancellation as its answer.
func TestFirstPageBuildIsJoinableAfterTheLeaderLeft(t *testing.T) {
	block := make(chan struct{})
	st := &fakeStore{
		page:    pageWith("detached"),
		block:   block,
		started: make(chan struct{}, 1),
	}
	svc := New(st)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() {
		_, err := svc.List(leaderCtx, unfilteredPage())
		leader <- err
	}()
	select {
	case <-st.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first build never started")
	}

	cancelLeader()
	select {
	case err := <-leader:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled leader got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled leader did not leave the build")
	}

	// The build is still running and still joinable: a fresh caller arrives
	// only after the leader has gone.
	joined := make(chan store.MessageListPage, 1)
	joinedErr := make(chan error, 1)
	go func() {
		page, err := svc.List(context.Background(), unfilteredPage())
		joined <- page
		joinedErr <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times, want the one detached build", got)
	}
	// It is waiting on the shared build, not starting its own.
	select {
	case err := <-joinedErr:
		t.Fatalf("the joined caller returned before the build finished: %v", err)
	default:
	}
	close(block)
	select {
	case page := <-joined:
		if len(page.Messages) != 2 || page.Messages[0].Subject != "detached" {
			t.Fatalf("joined page: %+v", page)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the joined caller never received the detached page")
	}
	if err := <-joinedErr; err != nil {
		t.Fatalf("the caller that joined after the leader left got %v", err)
	}
	if got := st.callCount(); got != 1 {
		t.Fatalf("store called %d times, want the one detached build", got)
	}
}
