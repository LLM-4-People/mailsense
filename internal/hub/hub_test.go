package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

type fakeSync struct{ status syncer.Status }

func (f *fakeSync) Status() syncer.Status { return f.status }

type fakeLearning struct {
	status learning.Status
	open   bool
	err    error
	calls  atomic.Int64
}

func (f *fakeLearning) Status(context.Context) (learning.Status, learning.GateState, error) {
	f.calls.Add(1)
	return f.status, learning.GateState{Open: f.open}, f.err
}

type world struct {
	store *store.Store
	cfg   *config.Config
	learn *fakeLearning
	sync  *fakeSync
	svc   *Service
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 9, 20, 0, 0, time.UTC)
	learn := &fakeLearning{}
	fresh := &fakeSync{}
	return &world{store: st, cfg: cfg, learn: learn, sync: fresh, now: now,
		svc: New(Deps{Store: st, Learning: learn, Sync: fresh, Now: func() time.Time { return now }})}
}

// TestHubMailboxSyncIsRunTruth protects the user-visible honesty contract:
// "synced" follows the sync owner's last run, not a learning stage that can
// survive a stalled connector.
func TestHubMailboxSyncIsRunTruth(t *testing.T) {
	w := newWorld(t)
	w.message(t, "fresh-1", scope.Owner)
	w.sync.status = syncer.Status{}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Mailbox.Synced || !view.Mailbox.Stale || view.Mailbox.LastSuccess != "" || view.Mailbox.LastAttempt != "" {
		t.Fatalf("a boot with no run must be stale and unsynced: %+v", view.Mailbox)
	}
	run := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	w.sync.status = syncer.Status{Synced: true, LastAttempt: run, LastSuccess: run}
	view, err = w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Mailbox.Synced || view.Mailbox.Stale || view.Mailbox.LastSuccess == "" ||
		view.Mailbox.NewestMessage == "" {
		t.Fatalf("a successful run must carry its evidence: %+v", view.Mailbox)
	}
}

// message seeds one message and returns its row id.
func (w *world) message(t *testing.T, id string, verdict scope.Verdict) int64 {
	t.Helper()
	m := mailtest.Message("src", id)
	m.Subject = "HUB-SECRET-SUBJECT-" + id
	m.Body = "HUB-SECRET-BODY-" + id
	m.SenderAddress = "hub-secret-sender@example.org"
	if err := w.store.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var row int64
	if err := w.store.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == id {
			row = r.RowID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if row == 0 {
		t.Fatalf("message %s not stored", id)
	}
	if err := w.store.WriteVerdicts(t.Context(), "test", []store.VerdictRow{
		{RowID: row, Verdict: verdict, Reason: scope.ReasonAddressedFlag}}); err != nil {
		t.Fatal(err)
	}
	return row
}

func (w *world) label(t *testing.T, id int64, question, value, source string) {
	t.Helper()
	if err := w.store.PutLabel(t.Context(), id, question, value, source); err != nil {
		t.Fatal(err)
	}
}

func blocker(question string, need float64) learning.Blocker {
	return learning.Blocker{Code: layer1.BlockTooFewEval, Subject: question, Detail: "diagnostic only",
		Measure: &learning.Measure{Have: 0, Need: need, Unit: "evaluation labels"}}
}

func labelsStage(blockers ...learning.Blocker) learning.StageStatus {
	state := learning.Done
	if len(blockers) > 0 {
		state = learning.Blocked
	}
	return learning.StageStatus{ID: learning.StageLabels, State: state, Blockers: blockers}
}

func TestHubAggregatesBlockersAndReview(t *testing.T) {
	w := newWorld(t)
	w.sync.status = syncer.Status{Synced: true, LastAttempt: w.now, LastSuccess: w.now}
	a := w.message(t, "a", scope.Owner)
	b := w.message(t, "b", scope.Owner)
	w.label(t, a, "q00", `true`, store.LabelManual)
	w.label(t, a, "q00", `false`, store.LabelJudge)
	w.label(t, a, "q01", `true`, store.LabelJudge)
	w.label(t, a, "q02", `1`, store.LabelJudge)
	w.label(t, b, "q01", `false`, store.LabelJudge)
	w.label(t, b, "q02", `2`, store.LabelJudge)
	w.label(t, b, "q03", `true`, store.LabelJudge)
	w.label(t, a, "q03", `false`, store.LabelManual)
	w.label(t, b, "q04", `"work"`, store.LabelManual)

	var blockers []learning.Blocker
	for i := range 17 {
		blockers = append(blockers, blocker("q"+twoDigits(i), 30))
	}
	w.learn.status = learning.Status{State: learning.Stuck, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Done},
		{ID: learning.StageEvidence, State: learning.Blocked, Blockers: []learning.Blocker{
			{Code: "other", Subject: "ignored", Detail: "not the labels stage"}}},
		labelsStage(blockers...),
	}}

	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !view.GeneratedAt.Equal(w.now) || view.Gate.Open || view.Gate.State != string(learning.Stuck) {
		t.Fatalf("generated/gate: %+v", view)
	}
	if len(view.Gate.Questions) != 17 {
		t.Fatalf("questions %d, want 17: %+v", len(view.Gate.Questions), view.Gate.Questions)
	}
	if len(view.Gate.Blockers) != 3 {
		t.Fatalf("blockers %+v", view.Gate.Blockers)
	}
	for i, got := range view.Gate.Blockers {
		want := blockers[i]
		if got.Code != want.Code || got.Subject != want.Subject || got.Detail != want.Detail ||
			got.Measure == nil || *got.Measure != *want.Measure {
			t.Fatalf("blocker %d %+v, want %+v", i, got, want)
		}
	}
	if view.Gate.Labels.Manual != 3 || view.Gate.Labels.Judge != 6 || view.Gate.Labels.Needed != 510 || view.Gate.Labels.Review != 5 {
		t.Fatalf("totals %+v", view.Gate.Labels)
	}
	byName := map[string]Question{}
	for _, q := range view.Gate.Questions {
		byName[q.Question] = q
	}
	if q := byName["q00"]; q.Manual != 1 || q.Judge != 1 || q.Needed != 30 || q.Review != 0 {
		t.Fatalf("q00 %+v", q)
	}
	if q := byName["q01"]; q.Manual != 0 || q.Judge != 2 || q.Needed != 30 || q.Review != 2 {
		t.Fatalf("q01 %+v", q)
	}
	if q := byName["q02"]; q.Manual != 0 || q.Judge != 2 || q.Needed != 30 || q.Review != 2 {
		t.Fatalf("q02 %+v", q)
	}
	if q := byName["q03"]; q.Manual != 1 || q.Judge != 1 || q.Needed != 30 || q.Review != 1 {
		t.Fatalf("q03 %+v", q)
	}
	if q := byName["q04"]; q.Manual != 1 || q.Judge != 0 || q.Needed != 30 || q.Review != 0 {
		t.Fatalf("q04 %+v", q)
	}
	for i := 1; i < len(view.Gate.Questions); i++ {
		if !ordered(view.Gate.Questions[i-1], view.Gate.Questions[i]) {
			t.Fatalf("questions out of order at %d: %+v then %+v", i, view.Gate.Questions[i-1], view.Gate.Questions[i])
		}
	}
	if view.Mailbox.Messages != 2 || view.Mailbox.Stores != 1 || !view.Mailbox.Synced {
		t.Fatalf("mailbox %+v", view.Mailbox)
	}
	if len(view.Recent) != 3 {
		t.Fatalf("recent %d, want the 3 manual labels: %+v", len(view.Recent), view.Recent)
	}
	for _, answer := range view.Recent {
		if answer.Source != store.LabelManual {
			t.Fatalf("the hub rail carried a non-manual answer: %+v", answer)
		}
	}
	for i := 1; i < len(view.Recent); i++ {
		if view.Recent[i].ID >= view.Recent[i-1].ID {
			t.Fatalf("recent not newest first: %+v", view.Recent)
		}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"HUB-SECRET-SUBJECT", "HUB-SECRET-BODY", "hub-secret-sender"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("hub leaked %q: %s", secret, raw)
		}
	}
	for _, key := range []string{`"sender"`, `"body"`, `"snippet"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("hub carries a content field %s: %s", key, raw)
		}
	}
}

func TestHubQuestionOrder(t *testing.T) {
	w := newWorld(t)
	m1 := w.message(t, "a", scope.Owner)
	m2 := w.message(t, "b", scope.Owner)
	w.label(t, m1, "gamma", `true`, store.LabelManual)
	w.label(t, m1, "beta", `true`, store.LabelJudge)
	w.label(t, m2, "beta", `false`, store.LabelJudge)
	w.label(t, m1, "alpha", `1`, store.LabelJudge)
	w.label(t, m2, "delta", `true`, store.LabelManual)
	w.learn.status = learning.Status{State: learning.Stuck, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Done},
		labelsStage(blocker("gamma", 3), blocker("beta", 2), blocker("alpha", 2), blocker("aardvark", 2), blocker("delta", 1)),
	}}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, q := range view.Gate.Questions {
		got = append(got, q.Question)
	}
	want := []string{"beta", "alpha", "aardvark", "gamma", "delta"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestHubFallsBackToOtherBlockers(t *testing.T) {
	w := newWorld(t)
	w.learn.status = learning.Status{State: learning.Stuck, Stages: []learning.StageStatus{
		labelsStage(),
		{ID: learning.StageSync, State: learning.Blocked, Blockers: []learning.Blocker{
			{Code: "sync_one", Subject: "store", Detail: "s1"},
			{Code: "sync_two", Subject: "store", Detail: "s2"},
			{Code: "sync_three", Subject: "store", Detail: "s3"},
			{Code: "sync_four", Subject: "store", Detail: "s4"},
		}},
	}}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Gate.Blockers) != 3 || view.Gate.Blockers[0].Code != "sync_one" ||
		view.Gate.Blockers[2].Code != "sync_three" || view.Mailbox.Synced {
		t.Fatalf("fallback blockers %+v synced %v", view.Gate.Blockers, view.Mailbox.Synced)
	}
	if len(view.Gate.Questions) != 0 || view.Gate.Labels != (LabelTotals{}) {
		t.Fatalf("questions %+v totals %+v", view.Gate.Questions, view.Gate.Labels)
	}
}

func TestHubOpenGate(t *testing.T) {
	w := newWorld(t)
	a := w.message(t, "a", scope.Owner)
	w.label(t, a, "category", `"work"`, store.LabelManual)
	w.learn.status = learning.Status{State: learning.Learned, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Done},
		labelsStage(),
	}}
	w.learn.open = true
	w.sync.status = syncer.Status{Synced: true, LastAttempt: w.now, LastSuccess: w.now}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Gate.Open || view.Gate.State != string(learning.Learned) || len(view.Gate.Blockers) != 0 {
		t.Fatalf("gate %+v", view.Gate)
	}
	if len(view.Gate.Questions) != 1 || view.Gate.Questions[0].Question != "category" ||
		view.Gate.Questions[0].Manual != 1 || view.Gate.Questions[0].Needed != 0 {
		t.Fatalf("questions %+v", view.Gate.Questions)
	}
	if !view.Mailbox.Synced || view.Mailbox.Messages != 1 {
		t.Fatalf("mailbox %+v", view.Mailbox)
	}
}

func TestHubWithoutBlockersOrLabels(t *testing.T) {
	w := newWorld(t)
	w.learn.status = learning.Status{State: learning.Learning, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Running},
		{ID: learning.StageLabels, State: learning.Pending},
	}}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Gate.Open || len(view.Gate.Questions) != 0 || len(view.Gate.Blockers) != 0 ||
		view.Gate.Labels != (LabelTotals{}) || view.Mailbox.Synced || len(view.Recent) != 0 {
		t.Fatalf("empty hub %+v", view)
	}
	if view.Gate.Questions == nil || view.Gate.Blockers == nil || view.Recent == nil {
		t.Fatalf("empty arrays must not be null: %+v", view)
	}
}

// TestHubCountsStayEmptyWithoutLabels seeds a mailbox whose labels table is
// empty. Both label counts and the hub must answer empty without depending on
// the mail population: the labels-driven read stops after the empty table,
// which is the startup cost class this pin protects.
func TestHubCountsStayEmptyWithoutLabels(t *testing.T) {
	w := newWorld(t)
	for i := range 64 {
		w.message(t, fmt.Sprintf("m%02d", i), scope.Owner)
	}
	w.message(t, "shared", scope.Shared)
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if view.Gate.Labels != (LabelTotals{}) || len(view.Gate.Questions) != 0 {
		t.Fatalf("hub invented label counts without labels: %+v", view.Gate)
	}
	if len(view.Recent) != 0 {
		t.Fatalf("hub recent answers without labels: %+v", view.Recent)
	}
	summary, err := w.svc.LabelSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if summary.LabelTotals != (LabelTotals{}) || len(summary.Questions) != 0 {
		t.Fatalf("label summary without labels: %+v", summary)
	}
	if summary.Questions == nil {
		t.Fatalf("empty questions must not be null: %+v", summary)
	}
}

func TestLabelSummaryMatchesHub(t *testing.T) {
	w := newWorld(t)
	a := w.message(t, "a", scope.Owner)
	w.label(t, a, "q00", `true`, store.LabelManual)
	w.label(t, a, "q00", `false`, store.LabelJudge)
	w.learn.status = learning.Status{State: learning.Stuck, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Done},
		labelsStage(blocker("q00", 30), blocker("q01", 20)),
	}}
	view, err := w.svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	summary, err := w.svc.LabelSummary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if summary.LabelTotals != view.Gate.Labels || len(summary.Questions) != len(view.Gate.Questions) {
		t.Fatalf("summary %+v hub %+v", summary, view.Gate)
	}
	if summary.Questions[0] != view.Gate.Questions[0] {
		t.Fatalf("summary order differs: %+v vs %+v", summary, view.Gate)
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mailbox", "recent_answers", "generated_at", "sender", "body"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("label summary carries %q: %s", key, raw)
		}
	}
}

func TestAnswersPaginationFiltersAndVisibility(t *testing.T) {
	w := newWorld(t)
	a := w.message(t, "a", scope.Owner)
	b := w.message(t, "b", scope.Owner)
	shared := w.message(t, "shared", scope.Shared)
	w.label(t, a, "category", `"work"`, store.LabelManual)
	w.label(t, a, "category", `"spam"`, store.LabelJudge)
	w.label(t, a, "urgency", `2`, store.LabelJudge)
	w.label(t, b, "category", `"finance"`, store.LabelManual)
	w.label(t, b, "urgency", `1`, store.LabelJudge)
	w.label(t, shared, "category", `"work"`, store.LabelManual)

	first, err := w.svc.Answers(t.Context(), AnswerQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.NextCursor != first.Items[1].ID {
		t.Fatalf("first page %+v", first)
	}
	if first.Items[0].Question != "urgency" || first.Items[0].MessageID != b ||
		first.Items[1].Question != "category" || first.Items[1].MessageID != b {
		t.Fatalf("first page order %+v", first.Items)
	}
	second, err := w.svc.Answers(t.Context(), AnswerQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || !second.HasMore {
		t.Fatalf("second page %+v", second)
	}
	last, err := w.svc.Answers(t.Context(), AnswerQuery{Limit: 2, Cursor: second.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 1 || last.HasMore || last.Items[0].MessageID != a || last.Items[0].Question != "category" ||
		last.Items[0].Source != store.LabelManual {
		t.Fatalf("last page %+v", last)
	}
	judge, err := w.svc.Answers(t.Context(), AnswerQuery{Source: store.LabelJudge})
	if err != nil || len(judge.Items) != 3 {
		t.Fatalf("judge %+v: %v", judge, err)
	}
	onA, err := w.svc.Answers(t.Context(), AnswerQuery{MessageID: a})
	if err != nil || len(onA.Items) != 3 {
		t.Fatalf("message a %+v: %v", onA, err)
	}
	both, err := w.svc.Answers(t.Context(), AnswerQuery{MessageID: b, Question: "category", Source: store.LabelManual})
	if err != nil || len(both.Items) != 1 || both.Items[0].MessageID != b {
		t.Fatalf("combined %+v: %v", both, err)
	}
	for _, item := range first.Items {
		if item.MessageID == shared {
			t.Fatalf("answer carries shared mail: %+v", item)
		}
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"HUB-SECRET-SUBJECT", "HUB-SECRET-BODY", "hub-secret-sender"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("answers leaked %q: %s", secret, raw)
		}
	}
}

func TestAnswersHideUnpickedStore(t *testing.T) {
	w := newWorld(t)
	a := w.message(t, "a", scope.Owner)
	if err := w.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})); err != nil {
		t.Fatal(err)
	}
	cMessage := mailtest.Message("src", "c")
	cMessage.StoreID = "store-2"
	if err := w.store.UpsertMessages(t.Context(), 0, []mail.Message{cMessage}); err != nil {
		t.Fatal(err)
	}
	var c int64
	if err := w.store.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == "c" {
			c = r.RowID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c == 0 {
		t.Fatal("store-2 message not stored")
	}
	if err := w.store.WriteVerdicts(t.Context(), "test", []store.VerdictRow{
		{RowID: c, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
		t.Fatal(err)
	}
	w.label(t, a, "category", `"work"`, store.LabelManual)
	w.label(t, c, "category", `"spam"`, store.LabelManual)
	if err := w.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	page, err := w.svc.Answers(t.Context(), AnswerQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].MessageID != a {
		t.Fatalf("unpicked store's answer survived: %+v", page)
	}
	messages, stores, err := w.store.MailboxCounts(t.Context())
	if err != nil || messages != 1 || stores != 1 {
		t.Fatalf("mailbox %d/%d: %v", messages, stores, err)
	}
}

func TestHubServesCachedCountsAfterStoreClose(t *testing.T) {
	w := newWorld(t)
	if _, err := w.svc.Hub(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Hub(t.Context()); err != nil {
		t.Fatalf("a warm hub reread the closed store: %v", err)
	}
	if _, err := w.svc.LabelSummary(t.Context()); err != nil {
		t.Fatalf("a warm label summary reread the closed store: %v", err)
	}
}

func TestHubServesStaleCountsWhileRefreshRuns(t *testing.T) {
	w := newWorld(t)
	clk := clocktest.New(w.now)
	svc := New(Deps{Store: w.store, Learning: w.learn, Now: clk.Now})
	first, err := svc.Hub(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	if err := w.store.Close(); err != nil {
		t.Fatal(err)
	}
	stale, err := svc.Hub(t.Context())
	if err != nil {
		t.Fatalf("an expired hub blocked on its refresh: %v", err)
	}
	if stale.Mailbox.Messages != first.Mailbox.Messages || stale.Mailbox.Stores != first.Mailbox.Stores ||
		len(stale.Recent) != len(first.Recent) {
		t.Fatalf("stale hub diverged: first %+v stale %+v", first, stale)
	}
}

// countingStore counts one cold label read and holds it so concurrent cold
// readers are all in flight before it completes.
type countingStore struct {
	*store.Store
	labelCounts atomic.Int64
	started     chan struct{}
	release     chan struct{}
}

func (c *countingStore) LabelCounts(ctx context.Context) ([]store.LabelCount, error) {
	c.labelCounts.Add(1)
	if c.started != nil {
		c.started <- struct{}{}
	}
	if c.release != nil {
		<-c.release
	}
	return c.Store.LabelCounts(ctx)
}

// Eight concurrent cold label summaries share one store read. Before the
// shared cache every caller ran its own LabelCounts query.
func TestLabelSummaryColdBuildIsSingleFlight(t *testing.T) {
	w := newWorld(t)
	a := w.message(t, "a", scope.Owner)
	w.label(t, a, "q00", `true`, store.LabelManual)
	spy := &countingStore{Store: w.store, started: make(chan struct{}, 8), release: make(chan struct{})}
	w.svc.store = spy

	const n = 8
	entered := make(chan struct{}, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered <- struct{}{}
			_, errs[i] = w.svc.LabelSummary(context.Background())
		}()
	}
	for range n {
		<-entered
	}
	<-spy.started
	for range 100 {
		runtime.Gosched()
	}
	close(spy.release)
	wg.Wait()

	if got := spy.labelCounts.Load(); got != 1 {
		t.Fatalf("a cold label summary read labels %d times, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

// ordered reports whether a sorts before b: remaining desc, review desc,
// question asc.
func ordered(a, b Question) bool {
	ra, rb := remaining(a), remaining(b)
	if ra != rb {
		return ra > rb
	}
	if a.Review != b.Review {
		return a.Review > b.Review
	}
	return a.Question < b.Question
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
