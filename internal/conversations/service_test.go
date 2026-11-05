package conversations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

type fakePreviewGate struct {
	preview bool
}

func (g *fakePreviewGate) PreviewAnswers(context.Context) bool { return g.preview }

type fakePriority struct {
	mu      sync.Mutex
	states  map[string]threadintel.Assessment
	err     error
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *fakePriority) ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error) {
	f.mu.Lock()
	f.calls++
	started, release := f.started, f.release
	f.mu.Unlock()
	if release != nil {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]threadintel.Assessment, len(f.states))
	for id, state := range f.states {
		out[id] = state
	}
	return out, nil
}

func (f *fakePriority) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakePriority) setState(id string, state threadintel.Assessment) {
	f.mu.Lock()
	if f.states == nil {
		f.states = map[string]threadintel.Assessment{}
	}
	f.states[id] = state
	f.mu.Unlock()
}

func (f *fakePriority) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakePriority) block(started, release chan struct{}) {
	f.mu.Lock()
	f.started, f.release = started, release
	f.mu.Unlock()
}

func newTestService(t *testing.T, priority Priority) (*Service, *store.Store, *config.Config, *clocktest.Fake) {
	t.Helper()
	return newTestServiceWith(t, priority, nil)
}

// newTestServiceWith is newTestService with a seam over the real store, so a
// test can count or fail one read without changing the service.
func newTestServiceWith(t *testing.T, priority Priority, seam func(*store.Store) storeReader) (*Service, *store.Store, *config.Config, *clocktest.Fake) {
	t.Helper()
	cfg, st := fixturetest.OpenStore(t, map[string]any{
		// store-2 is picked so the cross-store reply test sees it.
		source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"}),
		"store.max_open_conns": 1,
		// The Facts tests use a Gmail sender as the unresolved case.
		"mail.domains.shared": []any{"gmail.com"},
	})
	clk := clocktest.New(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))
	var readStore storeReader = st
	if seam != nil {
		readStore = seam(st)
	}
	return newService(Deps{Store: st, Priority: priority, Clock: clk}, readStore), st, cfg, clk
}

// seedStoreMessage stores one message and returns its row id.
func seedStoreMessage(t *testing.T, st *store.Store, m mail.Message) int64 {
	t.Helper()
	if err := st.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var rowID int64
	err := st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.SourceID == m.SourceID && r.Message.StoreID == m.StoreID && r.Message.ID == m.ID {
			rowID = r.RowID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rowID == 0 {
		t.Fatalf("seeded message %q not found", m.ID)
	}
	return rowID
}

// writeVerdict records the scope verdict of one stored message.
func writeVerdict(t *testing.T, st *store.Store, rowID int64, verdict scope.Verdict, reason scope.Reason) {
	t.Helper()
	if err := st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: rowID, Verdict: verdict, Reason: reason}}); err != nil {
		t.Fatal(err)
	}
}

func seedConversation(t *testing.T, st *store.Store, id, conversationID, subject, instant string, mutate func(*mail.Message)) int64 {
	t.Helper()
	m := mailtest.Message("src", id)
	m.ConversationID = conversationID
	m.Subject = subject
	m.SenderName, m.SenderAddress = "Sender "+id, "sender-"+id+"@example.org"
	m.SentRepresentingAddress = m.SenderAddress
	m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
	m.ReceivedAt, m.SentOn = instant, instant
	m.IsRead = true
	m.HasAttachments = false
	m.TaskDueDate = ""
	m.FlagStatus = mail.OptInt{}
	if mutate != nil {
		mutate(&m)
	}
	rowID := seedStoreMessage(t, st, m)
	reason := scope.ReasonRecipientMatch
	if m.FolderRole == mail.RoleSent {
		reason = scope.ReasonSentByOwner
	}
	writeVerdict(t, st, rowID, scope.Owner, reason)
	return rowID
}

func stateFor(id string, st threadintel.State) threadintel.Assessment {
	return threadintel.Assessment{ConversationID: id, State: st, Reason: threadintel.ReasonInboundLast,
		Visibility: threadintel.Complete, Confidence: 0.85}
}

// pageUncached exercises the snapshot path directly. The snapshot tests must
// not be masked by the enriched-page cache, which is a separate layer.
func pageUncached(t *testing.T, svc *Service, q Query) Page {
	t.Helper()
	norm, err := normalizeQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.buildPage(t.Context(), norm, svc.preview(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// pageUncachedErr is pageUncached for the calls a test expects to fail.
func pageUncachedErr(t *testing.T, svc *Service, q Query) error {
	t.Helper()
	norm, err := normalizeQuery(q)
	if err != nil {
		t.Fatalf("normalize %+v: %v", q, err)
	}
	_, err = svc.buildPage(t.Context(), norm, svc.preview(t.Context()))
	return err
}

func pageRow(t *testing.T, page Page, conversationID string) Row {
	t.Helper()
	for _, row := range page.Conversations {
		if row.ConversationID == conversationID {
			return row
		}
	}
	t.Fatalf("no row for %q in %+v", conversationID, page.Conversations)
	return Row{}
}

func TestPageServesStaleWhilePageRefreshRuns(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, clk := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Cached page", "2026-11-05T09:00:00Z", nil)

	first, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	firstAt := first.GeneratedAt

	clk.Advance(PageCacheTTL + time.Second)
	release := make(chan struct{})
	priority.block(make(chan struct{}, 1), release)

	started := time.Now()
	stale, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("an expired page blocked for %s instead of serving stale", elapsed)
	}
	if stale.GeneratedAt != firstAt {
		t.Fatalf("stale page generated_at %q, want %q", stale.GeneratedAt, firstAt)
	}

	close(release)
	fixturetest.WaitUntil(t, 5*time.Second, "the page cache refresh", func() bool {
		page, err := svc.Page(t.Context(), Query{})
		return err == nil && page.GeneratedAt != firstAt
	})
}

func TestPageCacheIsBounded(t *testing.T) {
	svc, _, _, clk := newTestService(t, &fakePriority{})
	var first Page
	for i := 0; i <= pageCacheSize; i++ {
		clk.Advance(time.Second)
		page, err := svc.Page(t.Context(), Query{Q: fmt.Sprintf("query-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = page
		}
	}
	// The first page is the oldest of pageCacheSize+1 entries, so the bound
	// evicted it and the request rebuilds instead of serving it.
	clk.Advance(time.Second)
	again, err := svc.Page(t.Context(), Query{Q: "query-0"})
	if err != nil {
		t.Fatal(err)
	}
	if again.GeneratedAt == first.GeneratedAt {
		t.Fatalf("the oldest page survived a full cache: %q", again.GeneratedAt)
	}
}

func TestPagePriorityWinsAndStoreFillsMissing(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AnsweredByOwner),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Priority thread", "2026-11-05T09:00:00Z", nil)
	seedConversation(t, st, "c2", "C2", "Store thread", "2026-11-04T09:00:00Z", nil)

	page, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if priority.callCount() != 1 {
		t.Fatalf("priority calls %d", priority.callCount())
	}
	c1 := pageRow(t, page, "C1")
	if c1.State.State != threadintel.AnsweredByOwner || c1.StateSource != "priority" {
		t.Fatalf("priority state lost: %+v", c1)
	}
	c2 := pageRow(t, page, "C2")
	if c2.StateSource != "store" {
		t.Fatalf("missing priority id did not use the store: %+v", c2)
	}
	want, err := st.ThreadStates(t.Context(), []string{"C2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c2.State.State != want["C2"].State {
		t.Fatalf("store fallback state %q, want %q", c2.State.State, want["C2"].State)
	}
	// The same store message resolves differently without the priority
	// overlay, so the assertion above is not vacuous.
	storeC1, err := st.ThreadStates(t.Context(), []string{"C1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if storeC1["C1"].State == threadintel.AnsweredByOwner {
		t.Fatalf("the store unexpectedly resolved C1 as answered by owner: %+v", storeC1["C1"])
	}
}

func TestPageStoreFallbackWhenPriorityFailsOrIsNil(t *testing.T) {
	for _, tc := range []struct {
		name     string
		priority Priority
		calls    int
	}{
		{"nil", nil, 0},
		{"error", &fakePriority{err: errors.New("priority unavailable")}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _, _ := newTestService(t, tc.priority)
			seedConversation(t, st, "c1", "C1", "Fallback", "2026-11-05T09:00:00Z", nil)
			page, err := svc.Page(t.Context(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			row := pageRow(t, page, "C1")
			if row.StateSource != "store" {
				t.Fatalf("state source %q", row.StateSource)
			}
			want, err := st.ThreadStates(t.Context(), []string{"C1"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if row.State.State != want["C1"].State {
				t.Fatalf("fallback state %q, want %q", row.State.State, want["C1"].State)
			}
			if fake, ok := tc.priority.(*fakePriority); ok && fake.calls != tc.calls {
				t.Fatalf("priority calls %d, want %d", fake.calls, tc.calls)
			}
		})
	}
}

func TestPageStoreBatchResolvesMissingConversations(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AnsweredByOwner),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Priority thread", "2026-11-05T09:00:00Z", nil)
	seedConversation(t, st, "c2", "C2", "Owner inbound", "2026-11-04T09:00:00Z", nil)
	inboundID := seedConversation(t, st, "c3", "C3", "Shared answer", "2026-11-03T09:00:00Z", nil)

	// The colleague's reply lives in the picked shared store: only the
	// thread evidence sees it, and the batch fallback must carry it.
	reply := mailtest.Message("src", "c3-reply")
	reply.ConversationID = "C3"
	reply.StoreID = "store-2"
	reply.FolderRole, reply.FolderPath = mail.RoleSent, "Sent"
	reply.SenderName, reply.SenderAddress = "Colleague", "colleague@example.test"
	reply.SentRepresentingAddress = reply.SenderAddress
	reply.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
	reply.ReceivedAt, reply.SentOn = "2026-11-03T10:00:00Z", "2026-11-03T10:00:00Z"
	writeVerdict(t, st, seedStoreMessage(t, st, reply), scope.Shared, scope.ReasonSentByOther)

	page, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if priority.callCount() != 1 {
		t.Fatalf("priority calls %d", priority.callCount())
	}
	if c1 := pageRow(t, page, "C1"); c1.StateSource != "priority" || c1.State.State != threadintel.AnsweredByOwner {
		t.Fatalf("priority state lost: %+v", c1)
	}
	for _, id := range []string{"C2", "C3"} {
		row := pageRow(t, page, id)
		if row.StateSource != "store" {
			t.Fatalf("%s source %q, want store", id, row.StateSource)
		}
		want, err := st.ThreadStates(t.Context(), []string{id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.State.State != want[id].State {
			t.Fatalf("%s state %q, want %q", id, row.State.State, want[id].State)
		}
	}
	if c3 := pageRow(t, page, "C3"); c3.State.State != threadintel.AnsweredByOther ||
		c3.State.ActorName != "Colleague" || c3.State.ReplyToMessageID != inboundID {
		t.Fatalf("shared reply state %+v", c3.State)
	}
}

// TestPageStoreBatchSpansMultipleReads pins the batch boundary: one priority
// conversation leaves more than one full store batch missing, and Page fails
// if any id is dropped between reads.
func TestPageStoreBatchSpansMultipleReads(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C0000": stateFor("C0000", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestService(t, priority)
	msgs := make([]mail.Message, 0, store.ThreadStatesBatch+2)
	for i := 0; i <= store.ThreadStatesBatch+1; i++ {
		m := mailtest.Message("src", fmt.Sprintf("m%04d", i))
		m.ConversationID = fmt.Sprintf("C%04d", i)
		m.Subject = fmt.Sprintf("Subject %04d", i)
		m.SenderName, m.SenderAddress = "Sender", fmt.Sprintf("sender%04d@example.org", i)
		m.SentRepresentingAddress = m.SenderAddress
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: mailtest.Owner}}
		m.ReceivedAt, m.SentOn = "2026-11-05T09:00:00Z", "2026-11-05T09:00:00Z"
		m.IsRead = true
		msgs = append(msgs, m)
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var verdicts []store.VerdictRow
	err := st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		verdicts = append(verdicts, store.VerdictRow{
			RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteVerdicts(t.Context(), "test", verdicts); err != nil {
		t.Fatal(err)
	}

	page, err := svc.Page(t.Context(), Query{Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != store.ThreadStatesBatch+2 || len(page.Conversations) != MaxLimit {
		t.Fatalf("page total %d rows %d, want total %d rows %d",
			page.Total, len(page.Conversations), store.ThreadStatesBatch+2, MaxLimit)
	}
	for _, row := range page.Conversations {
		if row.StateSource != "store" {
			t.Fatalf("%s source %q, want store", row.ConversationID, row.StateSource)
		}
	}
}

func TestPageCountsFilterAndCursor(t *testing.T) {
	states := map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
		"C2": stateFor("C2", threadintel.AwaitingOwner),
		"C3": stateFor("C3", threadintel.AnsweredByOwner),
		"C4": stateFor("C4", threadintel.NoAction),
		"C5": stateFor("C5", threadintel.Unknown),
		"C6": stateFor("C6", threadintel.AwaitingOther),
	}
	svc, st, _, _ := newTestService(t, &fakePriority{states: states})
	seed := []struct {
		id, subject, instant string
	}{
		{"c1", "Budget review", "2026-11-06T09:00:00Z"},
		{"c2", "Budget plan", "2026-11-05T09:00:00Z"},
		{"c3", "Invoice", "2026-11-04T09:00:00Z"},
		{"c4", "Budget archive", "2026-11-03T09:00:00Z"},
		{"c5", "Chatter", "2026-11-02T09:00:00Z"},
		{"c6", "Budget old", "2026-11-01T09:00:00Z"},
	}
	for i, msg := range seed {
		seedConversation(t, st, msg.id, fmt.Sprintf("C%d", i+1), msg.subject, msg.instant, nil)
	}

	first, err := svc.Page(t.Context(), Query{Limit: 1, Q: " budget ", State: threadintel.AwaitingOwner})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Conversations) != 1 || first.Conversations[0].ConversationID != "C1" ||
		first.Total != 2 || !first.HasMore || first.NextBefore == "" {
		t.Fatalf("first page %+v", first)
	}
	wantCounts := map[string]int{
		"awaiting_owner": 2, "awaiting_other": 1, "answered_by_owner": 0,
		"answered_by_other": 0, "no_action": 1, "unknown": 0,
	}
	if len(first.Counts) != len(threadintel.States) {
		t.Fatalf("counts %+v", first.Counts)
	}
	for state, want := range wantCounts {
		if got := first.Counts[state]; got != want {
			t.Errorf("counts[%q] = %d, want %d", state, got, want)
		}
	}
	if !first.Preview {
		// preview is off by default; this branch only documents the value.
	} else {
		t.Fatal("preview must default off")
	}

	second, err := svc.Page(t.Context(), Query{Limit: 1, Q: "budget", State: threadintel.AwaitingOwner, Before: first.NextBefore})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Conversations) != 1 || second.Conversations[0].ConversationID != "C2" ||
		second.Total != 2 || second.HasMore || second.NextBefore != "" {
		t.Fatalf("second page %+v", second)
	}

	all, err := svc.Page(t.Context(), Query{Limit: 10, Q: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 4 || len(all.Conversations) != 4 {
		t.Fatalf("all matching %+v", all)
	}
	wantOrder := []string{"C1", "C2", "C4", "C6"}
	for i, id := range wantOrder {
		if all.Conversations[i].ConversationID != id {
			t.Fatalf("order %+v, want %v", all.Conversations, wantOrder)
		}
	}

	if _, err := svc.Page(t.Context(), Query{Limit: 1, Q: "invoice", State: threadintel.AwaitingOwner, Before: first.NextBefore}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("q-mismatched cursor error %v", err)
	}
	if _, err := svc.Page(t.Context(), Query{Limit: 1, Q: "budget", State: threadintel.AnsweredByOwner, Before: first.NextBefore}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("state-mismatched cursor error %v", err)
	}
	for _, raw := range []string{"", "!!!", "bm90LWpzb24"} {
		if raw == "" {
			continue
		}
		if _, err := svc.Page(t.Context(), Query{Before: raw}); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("cursor %q error %v", raw, err)
		}
	}
}

func TestPageSearchSharesOneCacheEntryAcrossFolds(t *testing.T) {
	svc, st, _, clk := newTestService(t, &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}})
	seedConversation(t, st, "c1", "C1", "Réunion Budget", "2026-11-05T09:00:00Z", nil)
	first, err := svc.Page(t.Context(), Query{Q: "reunion"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"RÉUNION", "Reunion"} {
		clk.Advance(time.Second)
		page, err := svc.Page(t.Context(), Query{Q: q})
		if err != nil {
			t.Fatalf("q %q: %v", q, err)
		}
		if page.GeneratedAt != first.GeneratedAt {
			t.Fatalf("q %q rebuilt instead of sharing the folded page", q)
		}
	}
}

func TestPageSearchFoldsAndValidation(t *testing.T) {
	svc, st, _, _ := newTestService(t, &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}})
	seedConversation(t, st, "c1", "C1", "Réunion Budget", "2026-11-05T09:00:00Z", func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Élodie MARTIN", "Elodie@Example.ORG"
	})
	for _, q := range []string{"reunion", "RÉUNION", "elodie", "example.org"} {
		page, err := svc.Page(t.Context(), Query{Q: q})
		if err != nil {
			t.Fatalf("q %q: %v", q, err)
		}
		if page.Total != 1 || len(page.Conversations) != 1 {
			t.Fatalf("q %q matched %+v", q, page)
		}
	}
	page, err := svc.Page(t.Context(), Query{Q: "zzz"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || page.Conversations == nil || len(page.Conversations) != 0 {
		t.Fatalf("no-match page %+v", page)
	}
	for state, want := range map[string]int{
		"awaiting_owner": 0, "awaiting_other": 0, "answered_by_owner": 0,
		"answered_by_other": 0, "no_action": 0, "unknown": 0,
	} {
		if got := page.Counts[state]; got != want {
			t.Errorf("no-match counts[%q] = %d", state, got)
		}
	}

	for _, tc := range []struct {
		name  string
		query Query
		want  error
	}{
		{"limit low", Query{Limit: -1}, ErrInvalidLimit},
		{"limit high", Query{Limit: MaxLimit + 1}, ErrInvalidLimit},
		{"state", Query{State: "awaiting"}, ErrInvalidState},
		{"query long", Query{Q: strings.Repeat("x", MaxQueryRunes+1)}, ErrInvalidQuery},
		{"query control", Query{Q: "bad\x00query"}, ErrInvalidQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Page(t.Context(), tc.query); !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
		})
	}
	if page, err := svc.Page(t.Context(), Query{Limit: MaxLimit}); err != nil || page.Conversations == nil {
		t.Fatalf("max limit %+v %v", page, err)
	}
	svc.gate = &fakePreviewGate{preview: true}
	if page, err := svc.Page(t.Context(), Query{}); err != nil || !page.Preview {
		t.Fatalf("preview not reported: %+v %v", page, err)
	}
}

// The page preview flag is the one effective rule, not the raw setting: the
// config may say test mode while the gate says preview is not applicable.
func TestPagePreviewFollowsTheGateRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate learning.Previewer
		want bool
	}{
		{"no gate", nil, false},
		{"gate says no preview", &fakePreviewGate{}, false},
		{"gate says preview", &fakePreviewGate{preview: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, cfg, _ := newTestService(t, &fakePriority{})
			if err := cfg.Set("learning.test_mode", true); err != nil {
				t.Fatal(err)
			}
			svc.gate = tc.gate
			page, err := svc.Page(t.Context(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			if page.Preview != tc.want {
				t.Fatalf("page preview = %v, want %v", page.Preview, tc.want)
			}
		})
	}
}

// A gate flip changes the page and snapshot identity, so an old page is
// rebuilt rather than reused with the wrong preview label, and the two
// preview snapshots coexist instead of overwriting each other.
func TestPagePreviewFlipChangesTheCacheKey(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, cfg, clk := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Preview thread", "2026-11-05T09:00:00Z", nil)
	if err := cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	gate := &fakePreviewGate{}
	svc.gate = gate
	first, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Preview {
		t.Fatal("an open gate must not label the page preview")
	}
	if row := pageRow(t, first, "C1"); row.State.State != threadintel.AwaitingOwner {
		t.Fatalf("open-gate state %+v", row.State)
	}
	if got := priority.callCount(); got != 1 {
		t.Fatalf("open-gate snapshot reads %d, want 1", got)
	}

	// The preview ranking resolves the same conversation differently. A
	// snapshot built for the open gate must never be served under the
	// preview label, so the flip builds its own snapshot and page.
	priority.setState("C1", stateFor("C1", threadintel.AnsweredByOther))
	clk.Advance(time.Second)
	gate.preview = true
	second, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Preview || second.GeneratedAt == first.GeneratedAt {
		t.Fatalf("a preview flip reused the open-gate page: %+v", second)
	}
	if row := pageRow(t, second, "C1"); row.State.State != threadintel.AnsweredByOther {
		t.Fatalf("preview state %+v, want the preview ranking", row.State)
	}
	if got := priority.callCount(); got != 2 {
		t.Fatalf("preview snapshot reads %d, want 2", got)
	}

	// Going back to the open gate hits the page and the snapshot it already
	// built, so the two preview states never overwrite each other.
	clk.Advance(time.Second)
	gate.preview = false
	back, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if back.Preview || back.GeneratedAt != first.GeneratedAt {
		t.Fatalf("the open-gate page was not kept: %+v", back)
	}
	if row := pageRow(t, back, "C1"); row.State.State != threadintel.AwaitingOwner {
		t.Fatalf("the preview snapshot overwrote the open-gate snapshot: %+v", row.State)
	}
	if got := priority.callCount(); got != 2 {
		t.Fatalf("returning to the open gate rebuilt its snapshot: %d reads", got)
	}
}

func TestPageEmptySnapshot(t *testing.T) {
	svc, _, _, _ := newTestService(t, &fakePriority{})
	page, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Conversations == nil || len(page.Conversations) != 0 {
		t.Fatalf("conversations %+v", page.Conversations)
	}
	if len(page.Counts) != len(threadintel.States) {
		t.Fatalf("counts %+v", page.Counts)
	}
	for _, state := range threadintel.States {
		if got := page.Counts[string(state)]; got != 0 {
			t.Errorf("counts[%q] = %d", state, got)
		}
	}
	if page.Total != 0 || page.NextBefore != "" || page.HasMore || page.Preview {
		t.Fatalf("empty page %+v", page)
	}
	if page.Coverage != (store.ConversationCoverage{}) {
		t.Fatalf("coverage %+v", page.Coverage)
	}
	if ts, err := time.Parse(time.RFC3339, page.GeneratedAt); err != nil || !ts.IsZero() && ts.UTC().Format(time.RFC3339) != page.GeneratedAt {
		t.Fatalf("generated_at %q: %v", page.GeneratedAt, err)
	}
}

func TestPageOrganizationUsesMostRecentExternalParticipant(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-org":      stateFor("C-org", threadintel.NoAction),
		"C-internal": stateFor("C-internal", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestService(t, priority)
	if err := st.SaveReported(t.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"}, []string{"alex@acme.test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceContacts(t.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"}, "Contacts", []contacts.Contact{{
		EntryID: "alice", DisplayName: "Alice Martin", Company: "Atelier Nord", Emails: []string{"alice@atelier-nord.test"},
	}}); err != nil {
		t.Fatal(err)
	}
	seedConversation(t, st, "org", "C-org", "External", "2026-11-05T09:00:00Z", func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Alice Martin", "alice@atelier-nord.test"
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: "alex@acme.test"}}
	})
	seedConversation(t, st, "internal", "C-internal", "Internal", "2026-11-04T09:00:00Z", func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Colleague", "colleague@acme.test"
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Owner", Address: "alex@acme.test"}}
	})

	page, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	orgRow := pageRow(t, page, "C-org")
	if orgRow.Organization == nil || orgRow.Organization.Key != "c:atelier nord" ||
		orgRow.Organization.Label != "Atelier Nord" || !orgRow.Organization.Declared {
		t.Fatalf("external organization %+v", orgRow.Organization)
	}
	if len(orgRow.Participants) == 0 || orgRow.Participants[0].Address != "alice@atelier-nord.test" {
		t.Fatalf("participant order %+v", orgRow.Participants)
	}
	if internalRow := pageRow(t, page, "C-internal"); internalRow.Organization != nil {
		t.Fatalf("an internal colleague became an organization: %+v", internalRow.Organization)
	}
}

func TestPageOrdersUndatedLast(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-dated":   stateFor("C-dated", threadintel.NoAction),
		"C-undated": stateFor("C-undated", threadintel.NoAction),
		"C-empty":   stateFor("C-empty", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "dated", "C-dated", "Dated", "2026-11-05T09:00:00Z", nil)
	seedConversation(t, st, "undated", "C-undated", "Undated", "", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "", ""
	})
	seedConversation(t, st, "empty", "C-empty", "Empty", "", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "", ""
	})
	page, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"C-dated", "C-undated", "C-empty"}
	if len(page.Conversations) != len(want) {
		t.Fatalf("rows %+v", page.Conversations)
	}
	for i, id := range want {
		if page.Conversations[i].ConversationID != id {
			t.Fatalf("order %+v, want %v", page.Conversations, want)
		}
		if page.Conversations[i].LastAt != "" && id != "C-dated" {
			t.Fatalf("undated row has an instant: %+v", page.Conversations[i])
		}
	}
}

func TestPageSnapshotCacheAndGeneration(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, clk := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Cached", "2026-11-05T09:00:00Z", nil)

	queries := []string{"", "cached", "zzz"}
	for i, q := range queries {
		page := pageUncached(t, svc, Query{Q: q})
		if i == 0 && page.Total != 1 {
			t.Fatalf("first page %+v", page)
		}
	}
	if priority.callCount() != 1 {
		t.Fatalf("cache did not reuse the snapshot: %d priority calls", priority.callCount())
	}

	// A priority change picked up by the TTL refresh: the expired snapshot is
	// served stale first, then the one detached rebuild replaces it.
	priority.setState("C1", stateFor("C1", threadintel.AnsweredByOther))
	clk.Advance(SnapshotTTL + time.Second)
	page := pageUncached(t, svc, Query{})
	if row := pageRow(t, page, "C1"); row.State.State != threadintel.AwaitingOwner {
		t.Fatalf("an expired snapshot did not serve stale: %+v", row.State)
	}
	fixturetest.WaitUntil(t, 5*time.Second, "the TTL snapshot refresh", func() bool {
		return pageRow(t, pageUncached(t, svc, Query{}), "C1").State.State == threadintel.AnsweredByOther
	})
	if priority.callCount() != 2 {
		t.Fatalf("TTL did not refresh the snapshot once: %d priority calls", priority.callCount())
	}

	// A mail write moves the generation. The installed snapshot is served
	// stale while one detached rebuild reads the new mail.
	priority.setState("C2", stateFor("C2", threadintel.NoAction))
	seedConversation(t, st, "c2", "C2", "New", "2026-11-06T09:00:00Z", nil)
	page = pageUncached(t, svc, Query{})
	if page.Total != 1 {
		t.Fatalf("a changed generation did not serve stale: %+v", page)
	}
	fixturetest.WaitUntil(t, 5*time.Second, "the generation snapshot refresh", func() bool {
		return pageUncached(t, svc, Query{}).Total == 2
	})
	if priority.callCount() != 3 {
		t.Fatalf("a mail write did not refresh the snapshot: %d priority calls", priority.callCount())
	}
	page = pageUncached(t, svc, Query{})
	if page.Total != 2 || len(page.Conversations) != 2 {
		t.Fatalf("new mail missing after refresh: %+v", page)
	}
	if row := pageRow(t, page, "C2"); row.State.State != threadintel.NoAction {
		t.Fatalf("new conversation state %+v", row.State)
	}
}

func TestPageServesStaleWhileSnapshotRefreshRuns(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, clk := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Stale", "2026-11-05T09:00:00Z", nil)
	first := pageUncached(t, svc, Query{})
	if row := pageRow(t, first, "C1"); row.State.State != threadintel.AwaitingOwner {
		t.Fatalf("warm state %+v", row.State)
	}
	if priority.callCount() != 1 {
		t.Fatalf("warm priority calls %d", priority.callCount())
	}

	clk.Advance(SnapshotTTL)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	priority.block(started, release)

	type paged struct {
		page Page
		err  error
	}
	served := make(chan paged, 1)
	go func() {
		norm, _ := normalizeQuery(Query{})
		page, err := svc.buildPage(context.Background(), norm, svc.preview(context.Background()))
		served <- paged{page: page, err: err}
	}()
	select {
	case got := <-served:
		if got.err != nil {
			close(release)
			t.Fatal(got.err)
		}
		if row := pageRow(t, got.page, "C1"); row.State.State != threadintel.AwaitingOwner {
			close(release)
			t.Fatalf("stale state %+v", row.State)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("an expired snapshot blocked instead of serving the old cache")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the snapshot refresh never reached the priority resolver")
	}

	// A second request is still served from the old snapshot and does not
	// start a second read.
	stale := pageUncached(t, svc, Query{})
	if row := pageRow(t, stale, "C1"); row.State.State != threadintel.AwaitingOwner {
		close(release)
		t.Fatalf("second stale state %+v", row.State)
	}
	if got := priority.callCount(); got != 2 {
		close(release)
		t.Fatalf("the snapshot refresh was not single-flight: %d priority calls", got)
	}

	priority.setState("C1", stateFor("C1", threadintel.AnsweredByOther))
	close(release)
	fixturetest.WaitUntil(t, 5*time.Second, "the refreshed conversation snapshot", func() bool {
		return pageRow(t, pageUncached(t, svc, Query{}), "C1").State.State == threadintel.AnsweredByOther
	})
	refreshed := pageUncached(t, svc, Query{})
	if row := pageRow(t, refreshed, "C1"); row.State.State != threadintel.AnsweredByOther || row.StateSource != "priority" {
		t.Fatalf("refreshed row %+v", row)
	}
}

// countingStore counts the store reads one mailbox request performs. It is
// the query counter of the cache tests: a read that did not happen is the
// proof that the warmed page was served without touching the store.
type countingStore struct {
	storeReader
	mu    sync.Mutex
	reads map[string]int
}

func newCountingStore(st storeReader) *countingStore {
	return &countingStore{storeReader: st, reads: map[string]int{}}
}

func (c *countingStore) count(name string) {
	c.mu.Lock()
	c.reads[name]++
	c.mu.Unlock()
}

func (c *countingStore) readCounts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.reads))
	for name, n := range c.reads {
		out[name] = n
	}
	return out
}

func (c *countingStore) ConversationDigests(ctx context.Context) (store.ConversationDigestSet, error) {
	c.count("digests")
	return c.storeReader.ConversationDigests(ctx)
}

func (c *countingStore) ThreadStates(ctx context.Context, ids []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	c.count("states")
	return c.storeReader.ThreadStates(ctx, ids, signals)
}

func (c *countingStore) CompanyBook(ctx context.Context) (*store.CompanyBook, error) {
	c.count("book")
	return c.storeReader.CompanyBook(ctx)
}

func (c *countingStore) ConversationParticipants(ctx context.Context, ids []string, per int) (map[string]store.ConversationParticipants, error) {
	c.count("participants")
	return c.storeReader.ConversationParticipants(ctx, ids, per)
}

func (c *countingStore) ConversationSnippets(ctx context.Context, messageIDs []int64) (map[int64]string, error) {
	c.count("snippets")
	return c.storeReader.ConversationSnippets(ctx, messageIDs)
}

// flakyDigestStore fails the digest read while fail is set. It is the failed
// refresh seam: the cache must keep serving the last snapshot and retry.
type flakyDigestStore struct {
	storeReader
	mu      sync.Mutex
	fail    bool
	digests int
}

func (f *flakyDigestStore) setFail(fail bool) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

func (f *flakyDigestStore) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.digests
}

func (f *flakyDigestStore) ConversationDigests(ctx context.Context) (store.ConversationDigestSet, error) {
	f.mu.Lock()
	f.digests++
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return store.ConversationDigestSet{}, errors.New("conversation digest read failed")
	}
	return f.storeReader.ConversationDigests(ctx)
}

// TestPageRepeatedCallScansNothing pins the cache owner of the request cost:
// the second identical page is served without one store read, and a mail
// write keeps serving that page while the one detached snapshot rebuild runs.
// Only after the rebuild lands does the page rebuild, once.
func TestPageRepeatedCallScansNothing(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
		"C2": stateFor("C2", threadintel.NoAction),
		"C3": stateFor("C3", threadintel.AnsweredByOther),
	}}
	var counter *countingStore
	svc, st, _, clk := newTestServiceWith(t, priority, func(st *store.Store) storeReader {
		counter = newCountingStore(st)
		return counter
	})
	for i, id := range []string{"C1", "C2", "C3"} {
		seedConversation(t, st, fmt.Sprintf("m-%s", id), id, "Scanned "+id,
			fmt.Sprintf("2026-11-%02dT09:00:00Z", i+1), nil)
	}

	first, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 3 || len(first.Conversations) != 3 {
		t.Fatalf("warm page %+v", first)
	}
	warm := counter.readCounts()
	if want := map[string]int{"digests": 1, "book": 1, "participants": 1, "snippets": 1}; !reflect.DeepEqual(warm, want) {
		t.Fatalf("warm reads %v, want %v", warm, want)
	}

	// A repeated identical request inside the TTL is one page hit: not one
	// digest, participant, snippet or company read.
	clk.Advance(time.Second)
	second, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if second.GeneratedAt != first.GeneratedAt {
		t.Fatalf("the repeated page rebuilt: %q then %q", first.GeneratedAt, second.GeneratedAt)
	}
	if got := counter.readCounts(); !reflect.DeepEqual(got, warm) {
		t.Fatalf("a repeated identical Page scanned the store: %v, want %v", got, warm)
	}

	// Facts shares that same snapshot generation and its company book, so it
	// scans nothing either.
	facts, err := svc.Facts(t.Context(), "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Total != 3 {
		t.Fatalf("facts total %d, want the page's 3", facts.Total)
	}
	if got := counter.readCounts(); !reflect.DeepEqual(got, warm) {
		t.Fatalf("Facts rescanned the store instead of sharing the page snapshot: %v, want %v", got, warm)
	}

	// A mail write moves the generation. The installed snapshot and its page
	// are served while one detached snapshot rebuild runs, so the request
	// still scans nothing and does not show the new conversation yet.
	priority.setState("C4", stateFor("C4", threadintel.AwaitingOwner))
	seedConversation(t, st, "m-C4", "C4", "Scanned C4", "2026-11-05T09:00:00Z", nil)
	stale, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Total != 3 {
		t.Fatalf("a changed generation did not serve stale: %+v", stale)
	}
	// The request itself built no page: participants, snippets and the
	// company book are unchanged. The digest read belongs to the detached
	// rebuild, not to this request.
	staleReads := counter.readCounts()
	for _, name := range []string{"participants", "snippets", "book"} {
		if staleReads[name] != warm[name] {
			t.Fatalf("a request during the snapshot rebuild read %s %d times, want %d", name, staleReads[name], warm[name])
		}
	}
	if staleReads["digests"] < warm["digests"] || staleReads["digests"] > warm["digests"]+1 {
		t.Fatalf("digest reads %d, want the warm read plus at most the detached rebuild", staleReads["digests"])
	}

	// The detached rebuild lands and the next page rebuilds once with the new
	// conversation. Polling Page is the observable completion: until the
	// snapshot is installed every call serves the stale page with no scan.
	fixturetest.WaitUntil(t, 5*time.Second, "the detached snapshot rebuild", func() bool {
		page, err := svc.Page(t.Context(), Query{Limit: 10})
		return err == nil && page.Total == 4
	})
	if got := counter.readCounts()["digests"]; got != warm["digests"]+1 {
		t.Fatalf("digest reads %d, want exactly the warm read plus one rebuild", got)
	}
	rebuilt, err := svc.Page(t.Context(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Total != 4 || len(rebuilt.Conversations) != 4 {
		t.Fatalf("new mail missing after the rebuild: %+v", rebuilt)
	}
	afterRebuild := counter.readCounts()
	for _, name := range []string{"participants", "snippets", "book"} {
		if afterRebuild[name] != warm[name]+1 {
			t.Fatalf("the rebuilt page read %s %d times, want %d", name, afterRebuild[name], warm[name]+1)
		}
	}
	// The page that was just rebuilt is a hit again.
	if _, err := svc.Page(t.Context(), Query{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if got := counter.readCounts(); !reflect.DeepEqual(got, afterRebuild) {
		t.Fatalf("the rebuilt page was not cached: %v, want %v", got, afterRebuild)
	}
}

func TestPageFailedSnapshotRefreshKeepsCacheAndRetries(t *testing.T) {
	var flaky *flakyDigestStore
	svc, _, _, clk := newTestServiceWith(t, &fakePriority{}, func(st *store.Store) storeReader {
		flaky = &flakyDigestStore{storeReader: st}
		return flaky
	})
	if _, err := svc.Page(t.Context(), Query{}); err != nil {
		t.Fatal(err)
	}
	if got := flaky.attempts(); got != 1 {
		t.Fatalf("warm snapshot reads %d, want 1", got)
	}

	flaky.setFail(true)
	clk.Advance(SnapshotTTL)

	// The stale empty snapshot is still served while the detached digest
	// read fails.
	first := pageUncached(t, svc, Query{})
	if first.Total != 0 || len(first.Conversations) != 0 {
		t.Fatalf("stale page after a failed refresh: %+v", first)
	}
	fixturetest.WaitUntil(t, 5*time.Second, "the failed refresh", func() bool { return flaky.attempts() == 2 })

	// The failure cleared the in-flight flag, so a later request retries
	// instead of serving the expired snapshot as fresh forever.
	fixturetest.WaitUntil(t, 5*time.Second, "the retried refresh", func() bool {
		pageUncached(t, svc, Query{})
		return flaky.attempts() == 3
	})

	// A recovered store refreshes the same stale entry, and serving never
	// failed while it was unavailable.
	flaky.setFail(false)
	fixturetest.WaitUntil(t, 5*time.Second, "the recovered refresh", func() bool {
		pageUncached(t, svc, Query{})
		return flaky.attempts() == 4
	})
}

func TestPageEnrichmentIsBatchShaped(t *testing.T) {
	states := map[string]threadintel.Assessment{}
	for i := 1; i <= 50; i++ {
		states[fmt.Sprintf("C%02d", i)] = stateFor(fmt.Sprintf("C%02d", i), threadintel.NoAction)
	}
	svc, st, _, _ := newTestService(t, &fakePriority{states: states})
	for i := 1; i <= 50; i++ {
		seedConversation(t, st, fmt.Sprintf("m%02d", i), fmt.Sprintf("C%02d", i),
			fmt.Sprintf("Conversation %02d", i), fmt.Sprintf("2026-11-%02dT09:00:00Z", i%28+1), func(m *mail.Message) {
				m.Body = "A short body for the page snippet."
				m.SenderName, m.SenderAddress = fmt.Sprintf("Person %02d", i), fmt.Sprintf("person%02d@example.org", i)
			})
	}
	one, err := svc.Page(t.Context(), Query{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Conversations) != 1 || len(one.Conversations[0].Participants) == 0 || one.Conversations[0].Snippet == "" {
		t.Fatalf("one-row page %+v", one)
	}
	defaultPage, err := svc.Page(t.Context(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultPage.Conversations) != DefaultLimit || !defaultPage.HasMore {
		t.Fatalf("default limit page: %d rows, more=%v", len(defaultPage.Conversations), defaultPage.HasMore)
	}
	all, err := svc.Page(t.Context(), Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Conversations) != 50 || all.HasMore {
		t.Fatalf("50-row page: %d rows, more=%v", len(all.Conversations), all.HasMore)
	}
	for _, row := range all.Conversations {
		if len(row.Participants) == 0 || row.ParticipantCount == 0 || row.Snippet == "" {
			t.Fatalf("row %q was not enriched: %+v", row.ConversationID, row)
		}
	}
}

func TestFactsRecentCutoffOrderAndCap(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-new":     stateFor("C-new", threadintel.AwaitingOwner),
		"C-old":     stateFor("C-old", threadintel.AwaitingOther),
		"C-undated": stateFor("C-undated", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "new", "C-new", "Newest thread", "2026-11-05T09:00:00Z", nil)
	seedConversation(t, st, "old", "C-old", "Older thread", "2026-11-02T09:00:00Z", nil)
	seedConversation(t, st, "undated", "C-undated", "Undated thread", "", func(m *mail.Message) {
		m.ReceivedAt, m.SentOn = "", ""
	})

	facts, err := svc.Facts(t.Context(), "2026-11-03T00:00:00Z", 12)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Total != 3 || facts.Messages != 3 || facts.RecentTotal != 1 || len(facts.Recent) != 1 || facts.Recent[0].ConversationID != "C-new" {
		t.Fatalf("cutoff facts: total %d messages %d recent total %d, recent %+v", facts.Total, facts.Messages, facts.RecentTotal, facts.Recent)
	}
	if facts.Recent[0].Messages != 1 || facts.Recent[0].Unread != 0 ||
		facts.Recent[0].State.State != threadintel.AwaitingOwner {
		t.Fatalf("recent fact mapping: %+v", facts.Recent[0])
	}
	if facts.Counts[string(threadintel.AwaitingOwner)] != 1 ||
		facts.Counts[string(threadintel.AwaitingOther)] != 1 ||
		facts.Counts[string(threadintel.NoAction)] != 1 {
		t.Fatalf("counts over the whole digest: %+v", facts.Counts)
	}

	facts, err = svc.Facts(t.Context(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Recent) != 2 || facts.RecentTotal != 3 || facts.Recent[0].ConversationID != "C-new" || facts.Recent[1].ConversationID != "C-old" {
		t.Fatalf("recent order/cap: total %d, %+v", facts.RecentTotal, facts.Recent)
	}

	facts, err = svc.Facts(t.Context(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Recent) != 3 || facts.Recent[2].ConversationID != "C-undated" || facts.Recent[2].LastAt != "" {
		t.Fatalf("undated mail must sort last without a cutoff: %+v", facts.Recent)
	}
}

func TestFactsInvalidSince(t *testing.T) {
	svc, _, _, _ := newTestService(t, &fakePriority{})
	for _, since := range []string{"yesterday", "2026-11-03", "2026-11-03T00:00:00", "soon"} {
		if _, err := svc.Facts(t.Context(), since, 12); !errors.Is(err, ErrInvalidSince) {
			t.Fatalf("since %q: err %v, want ErrInvalidSince", since, err)
		}
	}
}

func TestFactsEmptySnapshot(t *testing.T) {
	svc, _, _, clk := newTestService(t, &fakePriority{})
	facts, err := svc.Facts(t.Context(), "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Total != 0 || facts.Recent == nil || len(facts.Recent) != 0 {
		t.Fatalf("empty facts: %+v", facts)
	}
	if len(facts.Counts) != len(threadintel.States) {
		t.Fatalf("counts keys: %+v", facts.Counts)
	}
	for _, state := range threadintel.States {
		if count, ok := facts.Counts[string(state)]; !ok || count != 0 {
			t.Fatalf("state %q count %d/%v", state, count, ok)
		}
	}
	want := clk.Now().UTC().Format(time.RFC3339)
	if facts.GeneratedAt != want {
		t.Fatalf("generated_at %q, want %q", facts.GeneratedAt, want)
	}
}

func TestFactsReusesSnapshotWithinTTL(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Cached", "2026-11-05T09:00:00Z", nil)

	if _, err := svc.Facts(t.Context(), "", 12); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Facts(t.Context(), "", 12); err != nil {
		t.Fatal(err)
	}
	if calls := priority.callCount(); calls != 1 {
		t.Fatalf("the digest snapshot was rebuilt: priority calls %d", calls)
	}
}

func TestFactsResolvesOrganizationWhenOwnerRepliedLast(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C-sentlast": stateFor("C-sentlast", threadintel.AnsweredByOwner),
	}}
	svc, st, _, _ := newTestService(t, priority)
	if err := st.SaveReported(t.Context(), store.StoreRef{SourceID: "src", StoreID: "store-1"},
		[]string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	seedConversation(t, st, "client-in", "C-sentlast", "Client asks", "2026-11-01T09:00:00Z", func(m *mail.Message) {
		m.SenderName, m.SenderAddress = "Client", "client@client-exemple.test"
	})
	seedConversation(t, st, "owner-out", "C-sentlast", "Owner replies", "2026-11-02T09:00:00Z", func(m *mail.Message) {
		m.FolderRole, m.FolderPath = mail.RoleSent, "Sent"
		m.SenderName, m.SenderAddress, m.SentRepresentingAddress = "Owner", mailtest.Owner, mailtest.Owner
		m.AddressedToMe = mail.AddressedNo
		m.Recipients = []mail.Recipient{{Kind: mail.To, Name: "Client", Address: "client@client-exemple.test"}}
	})

	facts, err := svc.Facts(t.Context(), "", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Recent) != 1 {
		t.Fatalf("recent %+v", facts.Recent)
	}
	// The latest received sender still names the organization even though the
	// owner sent the newest message; the Changed section links on it.
	if org := facts.Recent[0].Organization; org == nil || org.Key != "d:client-exemple.test" {
		t.Fatalf("owner-last conversation lost its organization: %+v", facts.Recent[0])
	}
}

func TestPageStateChipsMatchIndependentBuilds(t *testing.T) {
	states := map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
		"C2": stateFor("C2", threadintel.AwaitingOther),
		"C3": stateFor("C3", threadintel.AnsweredByOwner),
		"C4": stateFor("C4", threadintel.AnsweredByOther),
		"C5": stateFor("C5", threadintel.NoAction),
		"C6": stateFor("C6", threadintel.Unknown),
		"C7": stateFor("C7", threadintel.AwaitingOwner),
	}
	svc, st, _, clk := newTestService(t, &fakePriority{states: states})
	seed := []struct{ id, conv, subject, instant string }{
		{"c1", "C1", "Budget kickoff", "2026-11-07T09:00:00Z"},
		{"c2", "C2", "Budget reply", "2026-11-06T09:00:00Z"},
		{"c3", "C3", "Invoice", "2026-11-05T09:00:00Z"},
		{"c4", "C4", "Invoice reply", "2026-11-04T09:00:00Z"},
		{"c5", "C5", "Chatter", "2026-11-03T09:00:00Z"},
		{"c6", "C6", "Old chatter", "2026-11-02T09:00:00Z"},
		{"c7", "C7", "Budget follow-up", "2026-11-01T09:00:00Z"},
	}
	for _, msg := range seed {
		seedConversation(t, st, msg.id, msg.conv, msg.subject, msg.instant, nil)
	}
	queries := []Query{{Limit: 10}}
	for _, state := range threadintel.States {
		queries = append(queries, Query{Limit: 10, State: state})
	}
	for _, q := range queries {
		// One service serves the chip from the snapshot projection.
		got := pageUncached(t, svc, q)
		// A fresh service builds the same page from its own snapshot.
		fresh, err := New(Deps{Store: st, Priority: &fakePriority{states: states}, Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		want := pageUncached(t, fresh, q)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("state %q page differs from an independent build:\ngot  %+v\nwant %+v", q.State, got, want)
		}
	}
}

// barrierGate holds every Page caller inside preview until n have arrived, so
// a cold-cache test starts every caller before the first build runs.
type barrierGate struct {
	n       int
	mu      sync.Mutex
	arrived int
	open    chan struct{}
}

func newBarrierGate(n int) *barrierGate {
	return &barrierGate{n: n, open: make(chan struct{})}
}

func (g *barrierGate) PreviewAnswers(context.Context) bool {
	g.mu.Lock()
	if g.arrived < g.n {
		g.arrived++
		if g.arrived == g.n {
			close(g.open)
		}
	}
	g.mu.Unlock()
	<-g.open
	return false
}

func TestPageColdBuildIsSingleFlight(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	_, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Cold", "2026-11-05T09:00:00Z", nil)

	const n = 8
	svc, err := New(Deps{Store: st, Priority: priority, Gate: newBarrierGate(n),
		Clock: clocktest.New(time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, n)
	priority.block(started, release)

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Page(context.Background(), Query{})
		}()
	}
	<-started
	// Every caller has passed the preview barrier; let the rest reach the
	// cache before the one build finishes.
	for range 100 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	if got := priority.callCount(); got != 1 {
		t.Fatalf("%d builds for one cold page, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

// A write that moves the mail generation while the first snapshot build is
// blocked installs no snapshot for the generation it read, and a page built
// from that rejected snapshot is never installed under another snapshot's key.
// The next request builds a fresh snapshot and page instead of serving the
// raced one.
func TestPageGenerationMoveDuringBuildKeepsSnapshotIdentity(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "Raced", "2026-11-05T09:00:00Z", nil)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	priority.block(started, release)
	racedGen := st.MailGeneration()
	type result struct {
		page Page
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := svc.Page(context.Background(), Query{})
		done <- result{page: page, err: err}
	}()
	<-started

	// The build read the old generation; move it before the build returns.
	seedConversation(t, st, "c2", "C2", "Written", "2026-11-06T09:00:00Z", nil)
	close(release)
	first := <-done
	if first.err != nil {
		t.Fatal(first.err)
	}
	if gen := st.MailGeneration(); gen == racedGen {
		t.Fatal("the write did not move the mail generation")
	}

	// The snapshot read that raced the write was refused: a probe of the
	// preview key finds no installed snapshot and runs its own build. The
	// probe carries the raced generation, so its own install is refused too
	// and the real request below still starts cold.
	snapshotProbe := &snapshot{gen: racedGen, preview: false, digests: []store.ConversationDigest{{ConversationID: "PROBE"}}}
	gotSnapshot, err := svc.snapshots.Get(context.Background(), snapshotKey{}, func(context.Context) (*snapshot, error) {
		return snapshotProbe, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotSnapshot != snapshotProbe {
		t.Fatalf("the raced snapshot was installed: gen %d", gotSnapshot.gen)
	}

	// The raced page belongs to the snapshot generation it read. A page keyed
	// by the new generation must miss instead of serving that page; the probe
	// carries the raced generation so it is not installed either.
	pageProbe := pageValue{view: Page{GeneratedAt: "probe"}, gen: racedGen}
	gotPage, err := svc.pages.Get(context.Background(), pageKey{gen: st.MailGeneration(), limit: DefaultLimit}, func(context.Context) (pageValue, error) {
		return pageProbe, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPage.view.GeneratedAt != "probe" {
		t.Fatalf("the raced page was installed under the new generation: %+v", gotPage.view)
	}

	// The next request uses the new generation; the stale snapshot is served
	// once while its refresh runs, then the page rebuilds with the write.
	fixturetest.WaitUntil(t, 5*time.Second, "the post-write page", func() bool {
		page, err := svc.Page(t.Context(), Query{})
		return err == nil && len(page.Conversations) == 2
	})
	if got := priority.callCount(); got != 2 {
		t.Fatalf("the post-write page reads: %d, want the first build and one refresh", got)
	}
}

func TestPageStateChipsShareOneSnapshot(t *testing.T) {
	priority := &fakePriority{states: map[string]threadintel.Assessment{
		"C1": stateFor("C1", threadintel.AwaitingOwner),
		"C2": stateFor("C2", threadintel.NoAction),
	}}
	svc, st, _, _ := newTestService(t, priority)
	seedConversation(t, st, "c1", "C1", "One", "2026-11-05T09:00:00Z", nil)
	seedConversation(t, st, "c2", "C2", "Two", "2026-11-04T09:00:00Z", nil)

	for _, state := range append([]threadintel.State{""}, threadintel.States...) {
		if _, err := svc.Page(t.Context(), Query{Limit: 10, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if calls := priority.callCount(); calls != 1 {
		t.Fatalf("the seven chips read priority states %d times, want one snapshot", calls)
	}
}
