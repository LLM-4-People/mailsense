package priority

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/fixturetest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

var testNow = time.Now().UTC().Truncate(time.Second)

// testHeadChars is the store's bounded body head in the test doubles. It is
// deliberately a local copy: the production constant is unexported and the
// ranking must not depend on the projection size.
const testHeadChars = 2000

func headOf(body string) (string, bool) {
	runes := []rune(body)
	if len(runes) > testHeadChars {
		return string(runes[:testHeadChars]), true
	}
	return body, false
}

func defaultProfile(t *testing.T) *taxonomy.Profile {
	t.Helper()
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noul(name string, value float64) route.Answer {
	return route.Answer{Answer: engine.Answer{Type: engine.Noul, Value: value}}
}

func score(name string, value float64) route.Answer {
	return route.Answer{Answer: engine.Answer{Type: engine.Score, Value: value}}
}

func choiceAns(name, option string) route.Answer {
	return route.Answer{Answer: engine.Answer{Type: engine.Choice, Choice: option}}
}

func notCertified(typ engine.QuestionType) route.Answer {
	return route.Answer{Answer: engine.Answer{Type: typ, Abstain: route.AbstainNotCertified}}
}

func testMessage(received string) Message {
	return Message{MessageID: 7, Subject: "subject", SenderName: "sender", SenderAddress: "sender@example.com",
		ReceivedAt: received, FolderRole: mail.RoleInbox, Body: "  hello\n\n  world  "}
}

func scoreOne(t *testing.T, routeAns map[string]route.Answer, received string) (Item, bool) {
	t.Helper()
	item, ok, err := Score(Input{Message: testMessage(received), Route: routeAns, Profile: defaultProfile(t), Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return item, ok
}

func receivedAgo(d time.Duration) string { return testNow.Add(-d).Format(mail.TimeLayout) }

func TestScoreSignalsAndWeights(t *testing.T) {
	cases := []struct {
		name     string
		route    map[string]route.Answer
		received string
		ok       bool
		score    float64
		reasons  []string
	}{
		{"suspicious overrides marketing", map[string]route.Answer{
			"suspicious": noul("suspicious", 0.9), "category": choiceAns("category", "marketing")},
			receivedAgo(0), true, 112, []string{"security", "recent"}},
		{"security topic adds no threat points", map[string]route.Answer{
			"suspicious": noul("suspicious", 0.9), "category": choiceAns("category", "security")},
			receivedAgo(0), true, 112, []string{"security", "recent"}},
		{"security topic alone is not a threat", map[string]route.Answer{"category": choiceAns("category", "security")},
			receivedAgo(0), false, 0, nil},
		{"security topic with a verdict", map[string]route.Answer{
			"category": choiceAns("category", "security"), "decision_needed": noul("decision_needed", 0.8)},
			receivedAgo(0), true, 57, []string{"decision", "recent"}},
		{"decision", map[string]route.Answer{"decision_needed": noul("decision_needed", 0.8)},
			receivedAgo(0), true, 57, []string{"decision", "recent"}},
		{"deadline five", map[string]route.Answer{"deadline": score("deadline", 5)},
			receivedAgo(0), true, 57, []string{"deadline", "recent"}},
		{"urgency four", map[string]route.Answer{"urgency": score("urgency", 4)},
			receivedAgo(0), true, 52, []string{"urgent", "recent"}},
		{"reply", map[string]route.Answer{"reply_needed": noul("reply_needed", 0.7)},
			receivedAgo(0), true, 42, []string{"reply", "recent"}},
		{"question only reaches twenty", map[string]route.Answer{"is_question": noul("is_question", 0.6)},
			receivedAgo(0), true, 20, []string{"question", "recent"}},
		{"cta", map[string]route.Answer{"call_to_action": noul("call_to_action", 0.9)},
			receivedAgo(0), true, 24, []string{"action", "recent"}},
		{"action category", map[string]route.Answer{"category": choiceAns("category", "action")},
			receivedAgo(0), true, 24, []string{"action", "recent"}},
		{"work category suppressed", map[string]route.Answer{"category": choiceAns("category", "work")},
			receivedAgo(0), false, 0, nil},
		{"notifications suppressed", map[string]route.Answer{"category": choiceAns("category", "notifications")},
			receivedAgo(0), false, 0, nil},
		{"marketing suppressed", map[string]route.Answer{"category": choiceAns("category", "marketing")},
			receivedAgo(0), false, 0, nil},
		{"spam suppressed", map[string]route.Answer{"category": choiceAns("category", "spam")},
			receivedAgo(0), false, 0, nil},
		{"workload", map[string]route.Answer{"effort": score("effort", 3), "request_count": score("request_count", 3)},
			receivedAgo(0), true, 28, []string{"workload", "recent"}},
		{"old reply keeps its signal without recent", map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)},
			receivedAgo(28 * 24 * time.Hour), true, 30 + 12*math.Exp(-2), []string{"reply"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := scoreOne(t, tc.route, tc.received)
			if ok != tc.ok {
				t.Fatalf("included %v, want %v (item %+v)", ok, tc.ok, item)
			}
			if !ok {
				return
			}
			if math.Abs(item.Score-tc.score) > 1e-6 {
				t.Fatalf("score %.6f, want %.6f", item.Score, tc.score)
			}
			if !slices.Equal(item.Reasons, tc.reasons) {
				t.Fatalf("reasons %v, want %v", item.Reasons, tc.reasons)
			}
		})
	}
}

func TestScoreRareCombinedSignalsAndSnippet(t *testing.T) {
	item, ok := scoreOne(t, map[string]route.Answer{
		"suspicious": noul("suspicious", 0.95), "category": choiceAns("category", "security"),
		"decision_needed": noul("decision_needed", 0.9), "deadline": score("deadline", 5),
		"urgency": score("urgency", 4), "reply_needed": noul("reply_needed", 0.9),
		"call_to_action": noul("call_to_action", 0.8), "effort": score("effort", 3),
		"request_count": score("request_count", 3), "is_question": noul("is_question", 0.8),
	}, receivedAgo(0))
	if !ok {
		t.Fatal("the combined rare signals must enter")
	}
	want := 100 + 45 + 45 + 40 + 30 + 12 + 8 + 8 + 8 + 12
	if math.Abs(item.Score-float64(want)) > 1e-6 {
		t.Fatalf("score %.2f, want %d", item.Score, want)
	}
	if item.Snippet != "hello world" {
		t.Fatalf("snippet %q", item.Snippet)
	}
	if item.Preview {
		t.Fatal("a route-only answer is not a preview")
	}
}

func TestFilters(t *testing.T) {
	base := Item{Signals: Signals{Urgency: 1}}
	cases := []struct {
		filter Filter
		item   Item
		match  bool
	}{
		{FilterAll, base, true},
		{FilterReply, Item{Signals: Signals{ReplyNeeded: 1}, ReplyOpen: true}, true},
		{FilterReply, Item{ReplyOpen: true}, true},
		{FilterReply, Item{ReplyOpen: true, Signals: Signals{Urgency: 4}}, true},
		{FilterReply, Item{Signals: Signals{ReplyNeeded: 1}}, false},
		{FilterReply, Item{Signals: Signals{Urgency: 4}}, false},
		{FilterUrgent, Item{Signals: Signals{Urgency: 3}}, true},
		{FilterUrgent, Item{Signals: Signals{Deadline: 3}}, true},
		{FilterUrgent, Item{Signals: Signals{Urgency: 2}}, false},
		{FilterSecurity, Item{Signals: Signals{Suspicious: 1}}, true},
		{FilterSecurity, Item{Signals: Signals{Category: "security"}}, false},
		{FilterSecurity, Item{Signals: Signals{Category: "security", Suspicious: 1}}, true},
		{FilterSecurity, Item{Signals: Signals{Category: "spam"}}, true},
		{FilterSecurity, Item{Signals: Signals{Category: "marketing"}}, false},
		{FilterDecision, Item{Signals: Signals{DecisionNeeded: 1}, Action: action.Class{Primary: action.Decide, Waiting: action.WaitingOwner}}, true},
		{FilterDecision, Item{Signals: Signals{DecisionNeeded: 1}, Action: action.Class{Primary: action.Read}}, false},
		{FilterDeadline, Item{Signals: Signals{Deadline: 3}}, true},
		{FilterDeadline, Item{Signals: Signals{Deadline: 2}}, false},
		{FilterWorkload, Item{Signals: Signals{Effort: 2}}, true},
		{FilterWorkload, Item{Signals: Signals{RequestCount: 2}}, true},
		{FilterWorkload, Item{Signals: Signals{Effort: 1}}, false},
	}
	for _, tc := range cases {
		if got := tc.filter.matches(tc.item); got != tc.match {
			t.Errorf("%s: match %v, want %v", tc.filter, got, tc.match)
		}
	}
}

func TestRankLessStableTieBreak(t *testing.T) {
	at := testNow
	older := rankedItem{item: Item{MessageID: 1, Score: 50}, received: at.Add(-time.Hour)}
	newer := rankedItem{item: Item{MessageID: 2, Score: 50}, received: at}
	low := rankedItem{item: Item{MessageID: 3, Score: 49}, received: at}
	if rankLess(newer, older) >= 0 {
		t.Fatal("a newer message must rank before an older one at the same score")
	}
	if rankLess(low, newer) <= 0 {
		t.Fatal("a lower score must rank after")
	}
	sameTimeLowID := rankedItem{item: Item{MessageID: 4, Score: 50}, received: at}
	if rankLess(sameTimeLowID, newer) >= 0 {
		t.Fatal("a higher message id must rank before at the same score and time")
	}
}

func TestPreviewSubstitution(t *testing.T) {
	p := defaultProfile(t)
	received := receivedAgo(0)
	reply := func(a engine.Answer) map[string]engine.Answer { return map[string]engine.Answer{"reply_needed": a} }
	cases := []struct {
		name        string
		route       map[string]route.Answer
		preview     map[string]engine.Answer
		wantSignal  int
		wantPreview bool
		wantErr     bool
	}{
		{"uncertified is replaced", map[string]route.Answer{"reply_needed": notCertified(engine.Noul), "urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Noul, Value: 0.9}), 1, true, false},
		{"certified is untouched", map[string]route.Answer{"reply_needed": noul("reply_needed", 0.2), "urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Noul, Value: 0.9}), 0, false, false},
		{"below target is untouched", map[string]route.Answer{
			"reply_needed": {Answer: engine.Answer{Type: engine.Noul, Abstain: engine.AbstainBelowTarget}}, "urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Noul, Value: 0.9}), 0, false, false},
		{"no answer is untouched", map[string]route.Answer{
			"reply_needed": {Answer: engine.Answer{Type: engine.Noul, Abstain: route.AbstainNoAnswer}}, "urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Noul, Value: 0.9}), 0, false, false},
		{"no route answer is never filled", map[string]route.Answer{"urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Noul, Value: 0.9}), 0, false, false},
		{"wrong type fails", map[string]route.Answer{"reply_needed": notCertified(engine.Noul), "urgency": score("urgency", 4)},
			reply(engine.Answer{Type: engine.Score, Value: 1}), 0, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok, err := Score(Input{Message: testMessage(received), Route: tc.route, Preview: tc.preview, Profile: p, Now: testNow})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a malformed preview must fail")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("item did not enter (signal %d)", tc.wantSignal)
			}
			if item.Signals.ReplyNeeded != tc.wantSignal || item.Preview != tc.wantPreview {
				t.Fatalf("signal %d preview %v, want %d/%v", item.Signals.ReplyNeeded, item.Preview, tc.wantSignal, tc.wantPreview)
			}
		})
	}
}

// answerOf is the ranking's one usability check: a typed answered value is
// used, while a refusal and every abstention are not, even when the stored
// answer still carries a value.
func TestAnswerOfRejectsEveryNonAnswer(t *testing.T) {
	p := defaultProfile(t)
	reply := p.QuestionName(taxonomy.RoleReply)
	cases := []struct {
		name   string
		answer engine.Answer
		usable bool
	}{
		{"answered", engine.Answer{Type: engine.Noul, Value: 0.9}, true},
		{"refused", engine.Answer{Type: engine.Noul, Value: 0.9, Refused: true}, false},
		{"below target", engine.Answer{Type: engine.Noul, Value: 0.9, Abstain: engine.AbstainBelowTarget}, false},
		{"not certified", engine.Answer{Type: engine.Noul, Value: 0.9, Abstain: route.AbstainNotCertified}, false},
		{"below coverage", engine.Answer{Type: engine.Noul, Value: 0.9, Abstain: route.AbstainBelowCoverage}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, q, ok := answerOf(p, map[string]engine.Answer{reply: tc.answer}, reply)
			if ok != tc.usable {
				t.Fatalf("answerOf(%+v) usable = %v, want %v", tc.answer, ok, tc.usable)
			}
			if ok && (q == nil || got.Value != 0.9) {
				t.Fatalf("answerOf returned %+v, question %+v", got, q)
			}
		})
	}
}

func TestMalformedChoicePreviewFails(t *testing.T) {
	p := defaultProfile(t)
	conf := 0.9
	for _, preview := range []engine.Answer{
		{Type: engine.Choice, Choice: "work", Confidence: &conf, Probabilities: map[string]float64{"work": 1}},
		{Type: engine.Choice, Choice: "not-an-option", Confidence: &conf, Probabilities: map[string]float64{"work": 1}},
	} {
		_, _, err := Score(Input{Message: testMessage(receivedAgo(0)),
			Route:   map[string]route.Answer{"category": notCertified(engine.Choice), "urgency": score("urgency", 4)},
			Preview: map[string]engine.Answer{"category": preview}, Profile: p, Now: testNow})
		if err == nil {
			t.Fatalf("malformed category preview %+v must fail", preview)
		}
	}
	q, _ := p.Question("category")
	good := selective.Given(q, categoryProbs(q, "work", 0.9), 0.5)
	_, ok, err := Score(Input{Message: testMessage(receivedAgo(0)),
		Route:   map[string]route.Answer{"category": notCertified(engine.Choice), "urgency": score("urgency", 4)},
		Preview: map[string]engine.Answer{"category": good}, Profile: p, Now: testNow})
	if err != nil || !ok {
		t.Fatalf("a valid category preview failed: ok=%v err=%v", ok, err)
	}
}

func categoryProbs(q *engine.Question, pick string, confidence float64) []float64 {
	out := make([]float64, len(q.Options))
	fill := (1 - confidence) / float64(len(q.Options)-1)
	for i := range out {
		out[i] = fill
	}
	for i := range q.Options {
		if q.Options[i].Name == pick {
			out[i] = confidence
		}
	}
	return out
}

// testPriorityHeads is the shared body-head projection of both store
// doubles: the first testHeadChars runes, a truncation flag, and the row's
// revision (optionally overridden to exercise a mismatch).
func testPriorityHeads(rows []store.PriorityRow, bodies map[int64]string, ids []int64,
	missing map[int64]bool, revisions map[int64]int64) (map[int64]store.PriorityBody, int) {
	out := make(map[int64]store.PriorityBody, len(ids))
	chars := 0
	for _, id := range ids {
		if missing[id] {
			continue
		}
		idx := -1
		for i := range rows {
			if rows[i].MessageID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			continue
		}
		full := rows[idx].Body
		if body, ok := bodies[id]; ok {
			full = body
		}
		head, truncated := headOf(full)
		revision := rows[idx].Revision
		if rev, ok := revisions[id]; ok {
			revision = rev
		}
		chars += utf8.RuneCountInString(head)
		out[id] = store.PriorityBody{Body: head, Truncated: truncated, Revision: revision}
	}
	return out, chars
}

// testRouteKey is the fixture's route storage identity (the fake store
// ignores the value; the cache compares it).
func testRouteKey() (string, error) { return "priority-test-route", nil }

type allowGate struct{ err error }

func (g allowGate) Allow(context.Context) error { return g.err }

func newService(t *testing.T, rows []store.PriorityRow, preview bool) (*Service, *fakeStore) {
	t.Helper()
	cfg := configtest.New(t, map[string]any{"learning.test_mode": preview})
	fs := &fakeStore{rows: rows}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: cfg.Preview}, Config: cfg, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	return svc, fs
}

type fakeThreads struct {
	states  map[string]threadintel.Assessment
	err     error
	calls   int
	ids     []string
	signals map[int64]threadintel.Signal
}

func (f *fakeThreads) ThreadStates(_ context.Context, ids []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	f.calls++
	f.ids = append([]string(nil), ids...)
	f.signals = map[int64]threadintel.Signal{}
	for k, v := range signals {
		f.signals[k] = v
	}
	return f.states, f.err
}

func newThreadService(t *testing.T, rows []store.PriorityRow, states map[string]threadintel.Assessment) (*Service, *fakeStore, *fakeThreads) {
	t.Helper()
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: rows}
	ft := &fakeThreads{states: states}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: cfg.Preview}, Config: cfg, Threads: ft, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	return svc, fs, ft
}

func TestTopicsCensusCountsEveryReadMessageNotJustRanked(t *testing.T) {
	// Every row here carries a topic but no positive signal strong enough to
	// enter the ranking: the census must still count them. Ordering by mail
	// instant makes the evidence the three newest messages.
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(3 * 24 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work")}},
		{MessageID: 2, ReceivedAt: receivedAgo(24 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "marketing"), "topic": choiceAns("topic", "project_work")}},
		{MessageID: 3, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work")}},
		{MessageID: 4, ReceivedAt: receivedAgo(2 * 24 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "billing_account")}},
		{MessageID: 5, ReceivedAt: receivedAgo(4 * 24 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work")}},
	}
	svc, _ := newService(t, rows, false)

	ranked, err := svc.Top(t.Context(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranked) != 0 {
		t.Fatalf("the fixture must not rank: %+v", ranked)
	}
	census, err := svc.Topics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if census.Scanned != 5 || census.Classified != 4 {
		t.Fatalf("census totals %+v", census)
	}
	if len(census.Topics) != 2 {
		t.Fatalf("topics %+v", census.Topics)
	}
	if got := census.Topics[0]; got.Topic != "project_work" || got.Count != 3 ||
		fmt.Sprint(got.Evidence) != fmt.Sprint([]int64{3, 2, 1}) {
		t.Fatalf("project_work %+v", got)
	}
	if got := census.Topics[1]; got.Topic != "billing_account" || got.Count != 1 ||
		fmt.Sprint(got.Evidence) != fmt.Sprint([]int64{4}) {
		t.Fatalf("billing_account %+v", got)
	}
}

func TestTopicsCensusCarriesTheSubtopicHierarchy(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work"),
			"asks_for": choiceAns("asks_for", "information_document")}},
		{MessageID: 2, ReceivedAt: receivedAgo(time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work"),
			"asks_for": choiceAns("asks_for", "approval_decision")}},
		{MessageID: 3, ReceivedAt: receivedAgo(2 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work")}},
		{MessageID: 4, ReceivedAt: receivedAgo(3 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "finance"), "topic": choiceAns("topic", "billing_account"),
			"asks_for": choiceAns("asks_for", "payment_money")}},
	}
	svc, _ := newService(t, rows, false)
	census, err := svc.Topics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(census.Topics) != 2 {
		t.Fatalf("topics %+v", census.Topics)
	}
	project := census.Topics[0]
	if project.Topic != "project_work" || project.Count != 3 || len(project.Subtopics) != 3 {
		t.Fatalf("project_work %+v", project)
	}
	wantSubs := map[string]int{"deliverable": 1, "approval": 1, "status_update": 1}
	for _, sub := range project.Subtopics {
		if wantSubs[sub.Subtopic] != sub.Count {
			t.Fatalf("subtopic %q count %d, want %d", sub.Subtopic, sub.Count, wantSubs[sub.Subtopic])
		}
		delete(wantSubs, sub.Subtopic)
	}
	if len(wantSubs) != 0 {
		t.Fatalf("missing subtopics %v", wantSubs)
	}
	billing := census.Topics[1]
	if billing.Topic != "billing_account" || billing.Count != 1 || len(billing.Subtopics) != 1 ||
		billing.Subtopics[0].Subtopic != "payment" || billing.Subtopics[0].Count != 1 {
		t.Fatalf("billing_account %+v", billing)
	}
}

func TestTopicsCensusUsesPreviewAtTheTrustBoundary(t *testing.T) {
	p := defaultProfile(t)
	q, ok := p.Question("topic")
	if !ok {
		t.Fatal("the default profile has no topic question")
	}
	preview := selective.Given(q, categoryProbs(q, "hiring", 0.9), 0.5)
	rows := []store.PriorityRow{{
		MessageID: 1, ReceivedAt: receivedAgo(0),
		Route:   map[string]route.Answer{"topic": notCertified(engine.Choice)},
		Preview: map[string]engine.Answer{"topic": preview},
	}}
	svc, _ := newService(t, rows, true)
	census, err := svc.Topics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if census.Scanned != 1 || census.Classified != 1 || len(census.Topics) != 1 ||
		census.Topics[0].Topic != "hiring" || census.Topics[0].Count != 1 {
		t.Fatalf("preview census %+v", census)
	}
}

func TestColleagueReplySuppressesReplyFilterAndReason(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9), "urgency": score("urgency", 2)}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.AnsweredByOther,
		ReplyToMessageID: 1, ActorName: "Colleague", OwnerActorName: "Colleague", LastAt: receivedAgo(time.Hour)}}
	svc, fs, ft := newThreadService(t, rows, states)
	items, total, _, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("reply filter: %v total=%d err=%v", items, total, err)
	}
	if fs.callCount() != 1 || ft.calls != 1 || !ft.signals[1].ReplyNeeded {
		t.Fatalf("thread pass calls=%d threadCalls=%d reply=%v", fs.callCount(), ft.calls, ft.signals)
	}
	all, _, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(all) != 1 {
		t.Fatalf("all: %v %v", all, err)
	}
	if all[0].ThreadState != string(threadintel.AnsweredByOther) || all[0].ThreadActor != "Colleague" ||
		slices.Contains(all[0].Reasons, "reply") || !slices.Contains(all[0].Reasons, "thread_answered") {
		t.Fatalf("answered item %+v", all[0])
	}
}

func TestThreadStateFlagsOnlyTheOpenReplyTarget(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ConversationID: "C1", Subject: "s1", ReceivedAt: receivedAgo(2),
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9), "urgency": score("urgency", 2)}},
		{MessageID: 2, ConversationID: "C1", Subject: "s2", ReceivedAt: receivedAgo(0),
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9), "urgency": score("urgency", 2)}},
	}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner, ReplyToMessageID: 2}}
	svc, _, _ := newThreadService(t, rows, states)
	items, total, _, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || len(items) != 1 || total != 1 || items[0].MessageID != 2 {
		t.Fatalf("reply target: %v total=%d err=%v", items, total, err)
	}
	all, _, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	for _, it := range all {
		if it.MessageID == 1 && it.ReplyOpen {
			t.Fatalf("an older thread message stayed reply-open: %+v", it)
		}
	}
}

func TestFYIAnswerVetoesAReplyObligation(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ConversationID: "C1", Subject: "Verify your address",
		ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"reply_needed": noul("reply_needed", 0.95),
			"asks_for":     choiceAns("asks_for", "nothing_fyi"),
			"urgency":      score("urgency", 2),
		}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	ft := &fakeThreads{states: map[string]threadintel.Assessment{}}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Threads: ft, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	items, total, _, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("an FYI cannot be in the reply filter: %v total=%d err=%v", items, total, err)
	}
	if ft.signals[1].ReplyNeeded {
		t.Fatal("an FYI was handed to the thread resolver as a reply obligation")
	}
	all, _, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(all) != 1 {
		t.Fatalf("all: %v %v", all, err)
	}
	if slices.Contains(all[0].Reasons, "reply") || all[0].ReplyOpen {
		t.Fatalf("FYI stayed reply-open: %+v", all[0])
	}
}

func TestCallToActionAloneIsNotAReplyObligation(t *testing.T) {
	item, ok := scoreOne(t, map[string]route.Answer{
		"reply_needed":   noul("reply_needed", 0.16),
		"call_to_action": noul("call_to_action", 0.95),
		"asks_for":       choiceAns("asks_for", "task_work"),
		"category":       choiceAns("category", "security"),
		"urgency":        score("urgency", 2),
	}, receivedAgo(0))
	if !ok {
		t.Fatal("the message should still be important")
	}
	if item.ReplyOpen {
		t.Fatalf("a verification CTA became a reply obligation: %+v", item)
	}
	if !slices.Contains(item.Reasons, "action") {
		t.Fatalf("the action reason was lost: %+v", item.Reasons)
	}
}

func TestThreadVisibilityUnknownKeepsSignalWithWarning(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.Unknown, Reason: threadintel.ReasonVisibilityGap}}
	svc, _, _ := newThreadService(t, rows, states)
	// An incomplete thread cannot prove the owner owes a reply, so it is
	// not in the reply filter. It stays in the ranking with its warning.
	items, total, _, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("unknown thread reply filter: %v total=%d err=%v", items, total, err)
	}
	all, total, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(all) != 1 || total != 1 {
		t.Fatalf("unknown thread: %v total=%d err=%v", all, total, err)
	}
	if !slices.Contains(all[0].Reasons, "thread_unknown") || all[0].Action.Waiting != action.WaitingUnknown {
		t.Fatalf("missing uncertainty evidence: %+v %+v", all[0].Reasons, all[0].Action)
	}
}

func TestThreadStateErrorFailsClosed(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	svc, _, _ := newThreadService(t, rows, map[string]threadintel.Assessment{})
	ft := svc.threads.(*fakeThreads)
	ft.err = errors.New("thread read failed")
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 1); err == nil {
		t.Fatal("a thread read failure must fail the ranking, not silently keep a false reply flag")
	}
}

func TestThreadStatesAreReusedUntilMailOrAnswersMove(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner}}
	svc, fs, ft := newThreadService(t, rows, states)
	now := svc.clock.Now()
	if _, err := svc.buildRanking(t.Context(), false, now, "priority-test-route"); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 1 {
		t.Fatalf("first build resolved threads %d times", ft.calls)
	}
	// A ranking rebuild for the same mail and answers reuses the resolution.
	if _, err := svc.buildRanking(t.Context(), false, now, "priority-test-route"); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 1 {
		t.Fatalf("an unchanged mailbox recomputed thread states (%d calls)", ft.calls)
	}
	// A new message can change a thread's last actor: the cache must miss.
	fs.mailGen++
	if _, err := svc.buildRanking(t.Context(), false, now, "priority-test-route"); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 2 {
		t.Fatalf("a new mail generation reused stale thread states (%d calls)", ft.calls)
	}
	// A new route decision can change the reply signals: same mail, new answers.
	fs.routeGen++
	if _, err := svc.buildRanking(t.Context(), false, now, "priority-test-route"); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 3 {
		t.Fatalf("a new route generation reused stale thread states (%d calls)", ft.calls)
	}
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.now
	c.now = c.now.Add(c.step)
	return at
}

func (c *steppingClock) Sleep(context.Context, time.Duration) error { return nil }

func TestSnippetMatchesWhitespaceCollapse(t *testing.T) {
	cases := []string{
		"",
		"hello world",
		"  leading and trailing  ",
		"one\n\ntwo\r\nthree\tfour",
		strings.Repeat("word ", 100),
		strings.Repeat("é ", 120),
		strings.Repeat(" ", 5000) + "after whitespace",
	}
	for _, body := range cases {
		want := []rune(strings.Join(strings.Fields(body), " "))
		if len(want) > snippetRunes {
			want = want[:snippetRunes]
		}
		if got := Snippet(body, snippetRunes); got != string(want) {
			t.Fatalf("Snippet(%q) = %q, want %q", body, got, string(want))
		}
	}
}

// A stable build ranks exactly the rows its one scan read, and records the
// generation those rows belong to: the snapshot and its key cannot disagree.
func TestBuildRanksItsScannedRowsInOnePass(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	svc, fs, _ := newThreadService(t, rows, map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner}})
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("the ranking scanned the mailbox %d times, want one", got)
	}
	if len(c.items) != 1 || c.items[0].item.MessageID != 1 {
		t.Fatalf("items %+v, want the scanned row", c.items)
	}
	if c.mailGen != fs.mailGen || c.routeGen != fs.routeGen {
		t.Fatalf("snapshot generations %d/%d, want %d/%d", c.mailGen, c.routeGen, fs.mailGen, fs.routeGen)
	}
}

func TestBuildRetriesWhenMailChangesDuringTheReplyPass(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	svc, fs, ft := newThreadService(t, rows, map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner}})
	fs.bumpOnReply = 1
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if fs.callCount() < 2 {
		t.Fatalf("a mailbox that moved during the reply pass was not retried (%d scans)", fs.callCount())
	}
	if ft.calls != 1 {
		t.Fatalf("thread states resolved %d times, want one stable resolution", ft.calls)
	}
	if len(c.items) != 1 {
		t.Fatalf("items %d, want the ranked message", len(c.items))
	}
}

func TestSnippetFallsBackWhenTheBodyHeadCannotFillIt(t *testing.T) {
	full := strings.Repeat(" ", testHeadChars) + strings.Repeat("x", snippetRunes+20)
	row := store.PriorityRow{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0),
		Body: strings.Repeat(" ", testHeadChars), BodyTruncated: true,
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	svc, fs := newService(t, []store.PriorityRow{row}, false)
	fs.bodies = map[int64]string{1: full}
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 1 {
		t.Fatalf("items %d, want one", len(c.items))
	}
	if got := c.items[0].item.Snippet; utf8.RuneCountInString(got) != snippetRunes {
		t.Fatalf("snippet has %d runes, want %d from the full body", utf8.RuneCountInString(got), snippetRunes)
	}
	if fs.headCalls != 1 {
		t.Fatalf("head reads %d, want one batch", fs.headCalls)
	}
	if len(fs.fullBodyIDs) != 1 || fs.fullBodyIDs[0] != 1 {
		t.Fatalf("full-body fallbacks %v, want exactly once for id 1", fs.fullBodyIDs)
	}
}

func TestCachedTopSnippetsMatchTheFullBody(t *testing.T) {
	const count = 200
	rows := make([]store.PriorityRow, 0, count)
	bodies := map[int64]string{}
	for i := 1; i <= count; i++ {
		body := fmt.Sprintf("%sbody %d %s", strings.Repeat(" ", (i%5)*3), i, strings.Repeat("tail ", 500))
		if i%17 == 0 {
			body = strings.Repeat(" ", testHeadChars) + strings.Repeat("z", 300)
		}
		id := int64(i)
		bodies[id] = body
		rows = append(rows, store.PriorityRow{MessageID: id, Subject: "s", ReceivedAt: receivedAgo(0),
			Body: body, Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}})
	}
	svc, _ := newService(t, rows, false)
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != count {
		t.Fatalf("items %d, want %d", len(c.items), count)
	}
	for _, r := range c.items {
		want := Snippet(bodies[r.item.MessageID], snippetRunes)
		if r.item.Snippet != want {
			t.Fatalf("id %d snippet has %d runes, want the %d-rune full-body snippet",
				r.item.MessageID, utf8.RuneCountInString(r.item.Snippet), utf8.RuneCountInString(want))
		}
	}
}

func TestPriorityBuildKeepsOrderFiltersCounts(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, Subject: "s1", ReceivedAt: receivedAgo(0), Body: "body one",
			Route: map[string]route.Answer{"category": choiceAns("category", "action")}},
		{MessageID: 2, Subject: "s2", ReceivedAt: receivedAgo(0), Body: "body two",
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
		{MessageID: 3, Subject: "s3", ReceivedAt: receivedAgo(0), Body: "body three",
			Route: map[string]route.Answer{"urgency": score("urgency", 4)}},
		{MessageID: 4, Subject: "s4", ReceivedAt: receivedAgo(0), Body: "body four",
			Route: map[string]route.Answer{"decision_needed": noul("decision_needed", 0.9)}},
		{MessageID: 5, Subject: "s5", ReceivedAt: receivedAgo(0), Body: "body five",
			Route: map[string]route.Answer{"deadline": score("deadline", 5)}},
		{MessageID: 6, Subject: "s6", ReceivedAt: receivedAgo(0), Body: "body six",
			Route: map[string]route.Answer{"category": choiceAns("category", "security")}},
		{MessageID: 7, Subject: "s7", ReceivedAt: receivedAgo(0), Body: "body seven",
			Route: map[string]route.Answer{"category": choiceAns("category", "marketing")}},
		{MessageID: 8, Subject: "s8", ReceivedAt: receivedAgo(0), Body: "body eight",
			Route: map[string]route.Answer{"category": choiceAns("category", "spam")}},
		{MessageID: 9, Subject: "s9", ReceivedAt: receivedAgo(0), Body: "body nine",
			Route: map[string]route.Answer{"effort": score("effort", 3), "request_count": score("request_count", 3)}},
	}
	svc, _ := newService(t, rows, false)
	want := map[Filter][]int64{
		FilterAll:      {5, 4, 3, 2, 9, 1},
		FilterReply:    {2},
		FilterUrgent:   {5, 3},
		FilterSecurity: {},
		FilterDecision: {4},
		FilterDeadline: {5},
		FilterWorkload: {9},
	}
	for _, filter := range Filters {
		items, total, _, err := svc.Rank(t.Context(), filter, MaxLimit)
		if err != nil {
			t.Fatal(err)
		}
		if total != len(want[filter]) {
			t.Fatalf("%s total %d, want %d", filter, total, len(want[filter]))
		}
		if len(items) != len(want[filter]) {
			t.Fatalf("%s items %d, want %d", filter, len(items), len(want[filter]))
		}
		for i, id := range want[filter] {
			if items[i].MessageID != id {
				t.Fatalf("%s item %d is %d, want %d", filter, i, items[i].MessageID, id)
			}
			if items[i].Snippet == "" {
				t.Fatalf("%s id %d lost its snippet", filter, id)
			}
		}
	}
}

func TestRankingBuildReadsOnlyBoundedHeads(t *testing.T) {
	const count = 250
	rows := make([]store.PriorityRow, 0, count)
	bodies := map[int64]string{}
	for i := 1; i <= count; i++ {
		body := strings.Repeat("word ", 500)
		if i == count {
			body = strings.Repeat(" ", testHeadChars) + strings.Repeat("x", 500)
			bodies[int64(i)] = body
		}
		rows = append(rows, store.PriorityRow{MessageID: int64(i), Subject: "s", ReceivedAt: receivedAgo(0),
			Body: body, Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}})
	}
	svc, fs := newService(t, rows, false)
	fs.bodies = bodies
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != MaxCached {
		t.Fatalf("items %d, want the %d cached", len(c.items), MaxCached)
	}
	if fs.fullScans != 0 {
		t.Fatalf("the scoring build issued %d full-body scans, want none", fs.fullScans)
	}
	if fs.headCalls != 1 {
		t.Fatalf("head reads %d, want one batch", fs.headCalls)
	}
	if len(fs.headIDs) > MaxCached {
		t.Fatalf("head ids %d, want at most %d", len(fs.headIDs), MaxCached)
	}
	if fs.headChars > MaxCached*testHeadChars {
		t.Fatalf("head chars %d, want at most %d", fs.headChars, MaxCached*testHeadChars)
	}
	if len(fs.fullBodyIDs) != 1 || fs.fullBodyIDs[0] != count {
		t.Fatalf("full-body fallbacks %v, want exactly id %d", fs.fullBodyIDs, count)
	}
	found := false
	for _, r := range c.items {
		if r.item.MessageID != count {
			continue
		}
		found = true
		if got := utf8.RuneCountInString(r.item.Snippet); got != snippetRunes {
			t.Fatalf("fallback snippet has %d runes, want %d", got, snippetRunes)
		}
	}
	if !found {
		t.Fatal("the fallback row is missing from the cached ranking")
	}
}

func TestRankingSurvivesAMissingHead(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, Subject: "s1", ReceivedAt: receivedAgo(0), Body: "one",
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
		{MessageID: 2, Subject: "s2", ReceivedAt: receivedAgo(0), Body: "two",
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
	}
	svc, fs := newService(t, rows, false)
	fs.headMissing = map[int64]bool{2: true}
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 2 {
		t.Fatalf("items %d, want both rows", len(c.items))
	}
	for _, r := range c.items {
		switch r.item.MessageID {
		case 1:
			if r.item.Snippet == "" {
				t.Fatal("a present head lost its snippet")
			}
		case 2:
			if r.item.Snippet != "" {
				t.Fatalf("a missing head kept a %d-rune snippet", utf8.RuneCountInString(r.item.Snippet))
			}
		default:
			t.Fatalf("unexpected id %d", r.item.MessageID)
		}
	}
}

func TestRankingRevisionMismatchLeavesSnippetEmpty(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, Subject: "s1", ReceivedAt: receivedAgo(0), Body: "one",
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
		{MessageID: 2, Subject: "s2", ReceivedAt: receivedAgo(0), Body: "two", Revision: 3,
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
	}
	svc, fs := newService(t, rows, false)
	fs.headRevision = map[int64]int64{2: 4}
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 2 {
		t.Fatalf("items %d, want both rows", len(c.items))
	}
	for _, r := range c.items {
		switch r.item.MessageID {
		case 1:
			if r.item.Snippet == "" {
				t.Fatal("a matching revision lost its snippet")
			}
		case 2:
			if r.item.Snippet != "" {
				t.Fatalf("a revision mismatch kept a %d-rune snippet", utf8.RuneCountInString(r.item.Snippet))
			}
		default:
			t.Fatalf("unexpected id %d", r.item.MessageID)
		}
	}
}

func TestRankingHeadBatchErrorFailsTheBuild(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0), Body: "one",
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	svc, fs := newService(t, rows, false)
	boom := errors.New("heads offline")
	fs.setHeadErr(boom)
	c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
	if c != nil {
		t.Fatal("a failed head batch installed a partial cache")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("build error %v, want the head error", err)
	}
}

func TestRankingHandlesEmptyAndFewerThanCachedItems(t *testing.T) {
	for _, count := range []int{0, 1, 199, 200, 201} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			rows := make([]store.PriorityRow, 0, count)
			for i := 1; i <= count; i++ {
				rows = append(rows, store.PriorityRow{MessageID: int64(i), Subject: "s", ReceivedAt: receivedAgo(0),
					Body:  fmt.Sprintf("body %d", i),
					Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}})
			}
			svc, fs := newService(t, rows, false)
			c, err := svc.buildRanking(t.Context(), false, svc.clock.Now(), "priority-test-route")
			if err != nil {
				t.Fatal(err)
			}
			want := min(count, MaxCached)
			if len(c.items) != want {
				t.Fatalf("items %d, want %d", len(c.items), want)
			}
			if want == 0 {
				if fs.headCalls != 0 {
					t.Fatalf("head reads %d for an empty cache, want none", fs.headCalls)
				}
			} else if fs.headCalls != 1 {
				t.Fatalf("head reads %d, want one batch", fs.headCalls)
			}
			if len(fs.headIDs) != want {
				t.Fatalf("head ids %d, want %d", len(fs.headIDs), want)
			}
			unique := map[int64]bool{}
			for _, id := range fs.headIDs {
				unique[id] = true
			}
			if len(unique) != want {
				t.Fatalf("head ids are not unique: %d ids, %d unique", len(fs.headIDs), len(unique))
			}
			if len(fs.fullBodyIDs) != 0 {
				t.Fatalf("unexpected full-body fallbacks %v", fs.fullBodyIDs)
			}
		})
	}
}

func TestServiceEmptyAndTieBreak(t *testing.T) {
	svc, _ := newService(t, nil, false)
	items, total, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || items == nil || len(items) != 0 || total != 0 {
		t.Fatalf("empty ranking: %#v %d %v", items, total, err)
	}
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{"category": choiceAns("category", "action")}},
		{MessageID: 2, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{"category": choiceAns("category", "action")}},
	}
	svc, _ = newService(t, rows, false)
	items, _, _, err = svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || len(items) != 2 || items[0].MessageID != 2 || items[1].MessageID != 1 {
		t.Fatalf("tie-break order: %v %v", items, err)
	}
}

func TestSignalsAreThresholdedPlainValues(t *testing.T) {
	item, ok := scoreOne(t, map[string]route.Answer{
		"urgency": score("urgency", 3.6), "reply_needed": noul("reply_needed", 0.5),
		"suspicious": noul("suspicious", 0.49), "decision_needed": noul("decision_needed", 0.5),
		"deadline": score("deadline", 2.5), "effort": score("effort", 1.4),
		"request_count": score("request_count", 2.6), "category": choiceAns("category", "work"),
		"asks_for": choiceAns("asks_for", "approval_decision"),
	}, receivedAgo(0))
	if !ok {
		t.Fatal("the message must enter")
	}
	want := Signals{Urgency: 4, ReplyNeeded: 1, Suspicious: 0, DecisionNeeded: 1, Deadline: 3,
		Effort: 1, RequestCount: 3, Category: "work", AsksFor: "approval_decision"}
	if item.Signals != want {
		t.Fatalf("signals %+v, want %+v", item.Signals, want)
	}
}

func TestItemCarriesTheUnifiedAction(t *testing.T) {
	cases := []struct {
		name     string
		route    map[string]route.Answer
		primary  action.Primary
		waiting  action.Waiting
		due      action.DueSource
		dueLevel int
		enters   bool
	}{
		{"decision", map[string]route.Answer{"decision_needed": noul("decision_needed", 0.8)},
			action.Decide, action.WaitingOwner, action.DueNone, 0, true},
		{"reply", map[string]route.Answer{"reply_needed": noul("reply_needed", 0.8)},
			action.Reply, action.WaitingOwner, action.DueNone, 0, true},
		{"payment", map[string]route.Answer{"asks_for": choiceAns("asks_for", "payment_money")},
			action.Pay, action.WaitingOwner, action.DueNone, 0, true},
		{"automated is excluded before classification", map[string]route.Answer{
			"category": choiceAns("category", "notifications"), "call_to_action": noul("call_to_action", 0.9)},
			action.Primary(""), action.Waiting(""), action.DueNone, 0, false},
		{"classifier due", map[string]route.Answer{"category": choiceAns("category", "finance"),
			"deadline": score("deadline", 4)},
			action.FollowUp, action.WaitingOwner, action.DueClassifier, 4, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := scoreOne(t, tc.route, receivedAgo(0))
			if ok != tc.enters {
				t.Fatalf("entered=%v, want %v: %+v", ok, tc.enters, item)
			}
			if !ok {
				return
			}
			if item.Action.Primary != tc.primary || item.Action.Waiting != tc.waiting ||
				item.Action.DueSource != tc.due || item.Action.DueLevel != tc.dueLevel {
				t.Fatalf("action %+v, want %s/%s/%s/%d", item.Action, tc.primary, tc.waiting, tc.due, tc.dueLevel)
			}
		})
	}
}

// A thread that needs no reply is not a thread that needs no work. An
// invoice with no answer owed still asks the owner to pay, and the waiting
// direction follows the concrete action.
func TestNoReplyInvoiceStillAsksForPayment(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "Invoice 42",
		ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"asks_for": choiceAns("asks_for", "payment_money"),
			"topic":    choiceAns("topic", "billing_account"),
		}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.NoAction,
		Reason: threadintel.ReasonNoReplyExpected, LastMessageID: 1}}
	svc, _, _ := newThreadService(t, rows, states)
	items, _, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("all: %v err=%v", items, err)
	}
	if items[0].Action.Primary != action.Pay || items[0].Action.Waiting != action.WaitingOwner {
		t.Fatalf("action %+v", items[0].Action)
	}
	replies, total, _, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || len(replies) != 0 || total != 0 {
		t.Fatalf("a payment is not a reply: %v total=%d err=%v", replies, total, err)
	}
}

// A verdict request on an older message of a thread is not a live decision
// once the conversation moved on.
func TestHistoryDecisionIsNotADecisionCommitment(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "Old choice",
		ReceivedAt: receivedAgo(time.Hour), Route: map[string]route.Answer{
			"decision_needed": noul("decision_needed", 0.9),
			"urgency":         score("urgency", 2),
		}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner,
		ReplyToMessageID: 2, LastMessageID: 2}}
	svc, _, _ := newThreadService(t, rows, states)
	items, _, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("all: %v err=%v", items, err)
	}
	if items[0].Action.Primary != action.Read || items[0].Action.Waiting != action.WaitingNone {
		t.Fatalf("history row action %+v", items[0].Action)
	}
	decisions, total, _, err := svc.Rank(t.Context(), FilterDecision, 5)
	if err != nil || len(decisions) != 0 || total != 0 {
		t.Fatalf("history decision: %v total=%d err=%v", decisions, total, err)
	}
}

// Decisions is the read-model source for commitments: it is not the API
// page, so it can return up to the ranking cache and it only contains live
// owner decisions.
func TestDecisionsReturnsOnlyLiveOwnerDecisions(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ConversationID: "C1", Subject: "Live", ReceivedAt: receivedAgo(0),
			Route: map[string]route.Answer{"decision_needed": noul("decision_needed", 0.9)}},
		{MessageID: 2, ConversationID: "C2", Subject: "History", ReceivedAt: receivedAgo(time.Hour),
			Route: map[string]route.Answer{"decision_needed": noul("decision_needed", 0.9)}},
		{MessageID: 3, ConversationID: "C3", Subject: "Bulk", ReceivedAt: receivedAgo(2 * time.Hour),
			Route: map[string]route.Answer{"decision_needed": noul("decision_needed", 0.9),
				"category": choiceAns("category", "marketing")}},
	}
	states := map[string]threadintel.Assessment{
		"C1": {State: threadintel.AwaitingOwner, ReplyToMessageID: 1, LastMessageID: 1},
		"C2": {State: threadintel.AwaitingOwner, ReplyToMessageID: 4, LastMessageID: 4},
		"C3": {State: threadintel.NoAction, Reason: threadintel.ReasonAutomated, LastMessageID: 3},
	}
	svc, _, _ := newThreadService(t, rows, states)
	items, err := svc.Decisions(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].MessageID != 1 || items[0].Action.Primary != action.Decide {
		t.Fatalf("decisions %+v", items)
	}
	if _, err := svc.Decisions(t.Context(), MaxCached+1); err == nil {
		t.Fatal("an unbounded decision read was accepted")
	}
}

func TestActionsReturnsOnlyOwnerActions(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"reply_needed": noul("reply_needed", 0.9)}},
		{MessageID: 2, ReceivedAt: receivedAgo(time.Hour), Route: map[string]route.Answer{
			"asks_for": choiceAns("asks_for", "payment_money")}},
		{MessageID: 3, ReceivedAt: receivedAgo(2 * time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "notifications"), "call_to_action": noul("call_to_action", 0.9)}},
	}
	svc, _ := newService(t, rows, false)
	items, err := svc.Actions(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].MessageID != 1 || items[1].MessageID != 2 {
		t.Fatalf("actions %+v", items)
	}
	if _, err := svc.Actions(t.Context(), 0); err == nil {
		t.Fatal("zero action limit accepted")
	}
}

// waitPriorityIdle waits for any background refresh to finish and returns the
// installed cache. Callers invoke it after a read that started a refresh.
func waitPriorityIdle(t *testing.T, svc *Service) *cachedRanking {
	t.Helper()
	var c *cachedRanking
	fixturetest.WaitUntil(t, 5*time.Second, "the priority refresh to finish", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		c = svc.cached
		return !svc.refreshing
	})
	return c
}

// blockingStore is a Store whose reads can be held at the store boundary,
// so a test can observe the service while a background refresh is in flight.
type blockingStore struct {
	mu           sync.Mutex
	rows         []store.PriorityRow
	calls        int
	err          error
	started      chan struct{}
	release      chan struct{}
	stubborn     bool
	headCalls    int
	headIDs      []int64
	fullBodyIDs  []int64
	headErr      error
	headMissing  map[int64]bool
	headRevision map[int64]int64
	mailGen      uint64
	routeGen     uint64
}

func (f *blockingStore) MailGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mailGen
}

func (f *blockingStore) RouteGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routeGen
}

func (f *blockingStore) MessageBody(_ context.Context, messageID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullBodyIDs = append(f.fullBodyIDs, messageID)
	for _, r := range f.rows {
		if r.MessageID == messageID {
			return r.Body, nil
		}
	}
	return "", fmt.Errorf("message %d not found", messageID)
}

func (f *blockingStore) PriorityHeads(_ context.Context, ids []int64) (map[int64]store.PriorityBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	f.headIDs = append(f.headIDs, ids...)
	if f.headErr != nil {
		return nil, f.headErr
	}
	out, _ := testPriorityHeads(f.rows, nil, ids, f.headMissing, f.headRevision)
	return out, nil
}

func (f *blockingStore) EachPriorityReply(ctx context.Context, _ string, includePreview bool, _ []store.QuestionKey, fn func(store.PriorityRow) error) error {
	return f.each(ctx, includePreview, false, true, fn)
}

func (f *blockingStore) EachPriorityMessage(ctx context.Context, _ string, includePreview bool, _ []store.QuestionKey, fn func(store.PriorityRow) error) error {
	return f.each(ctx, includePreview, true, true, fn)
}

func (f *blockingStore) each(ctx context.Context, includePreview, body, decode bool, fn func(store.PriorityRow) error) error {
	f.mu.Lock()
	f.calls++
	started, release, stubborn := f.started, f.release, f.stubborn
	f.mu.Unlock()
	if release != nil {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if stubborn {
			<-release
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	f.mu.Lock()
	rows := slices.Clone(f.rows)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !body {
			r.Body, r.BodyTruncated = "", false
		}
		if !decode {
			r.Route, r.Preview = nil, nil
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *blockingStore) block(started, release chan struct{}) {
	f.mu.Lock()
	f.started, f.release = started, release
	f.stubborn = false
	f.mu.Unlock()
}

// blockUncooperative holds the read without watching ctx, modelling a store
// call that cannot be cancelled, so Close's bounded wait is observable.
func (f *blockingStore) blockUncooperative(started, release chan struct{}) {
	f.mu.Lock()
	f.started, f.release = started, release
	f.stubborn = true
	f.mu.Unlock()
}

func (f *blockingStore) setRows(rows []store.PriorityRow) {
	f.mu.Lock()
	f.rows = slices.Clone(rows)
	f.mu.Unlock()
}

func (f *blockingStore) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *blockingStore) setHeadErr(err error) {
	f.mu.Lock()
	f.headErr = err
	f.mu.Unlock()
}

func (f *blockingStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newBlockingService(t *testing.T, rows []store.PriorityRow, clk *clocktest.Fake) (*Service, *blockingStore) {
	t.Helper()
	cfg := configtest.New(t, nil)
	fs := &blockingStore{rows: rows}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: cfg.Preview}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	return svc, fs
}

// A sync batch bumps MailGeneration. The first request after it must serve
// the current ranking immediately and rebuild behind it, exactly like a TTL
// expiry; it must not block the first screen on a full scan.
func TestSignalsFollowRenamedRoleQuestions(t *testing.T) {
	p := &taxonomy.Profile{
		Questions: []engine.Question{{Name: "needs_reply", Type: engine.Noul}},
		Roles:     map[taxonomy.Role]string{taxonomy.RoleReply: "needs_reply"},
	}
	sig := signals(p, map[string]engine.Answer{
		"needs_reply": {Type: engine.Noul, Value: 0.9},
	})
	if sig.ReplyNeeded != 1 {
		t.Fatalf("renamed reply question was not read: %+v", sig)
	}
}

// A score's level is the class selective.Decide chose, not the rounded
// expected level: the same message must show one level in Mail, Triage,
// Priority and Brief.
func TestScoreLevelIsTheDecidedClass(t *testing.T) {
	p := &taxonomy.Profile{
		Questions:  []engine.Question{{Name: "urgency", Type: engine.Score, Levels: []string{"low", "mid", "high"}}},
		Roles:      map[taxonomy.Role]string{taxonomy.RoleUrgency: "urgency"},
		Thresholds: map[taxonomy.Threshold]float64{},
	}
	answers := map[string]engine.Answer{"urgency": {Type: engine.Score, Value: 0.85,
		Probabilities: map[string]float64{"0": 0.40, "1": 0.35, "2": 0.25}}}
	if got := level(p, answers, "urgency"); got != 0 {
		t.Fatalf("level %d, want the decided class 0 (rounding the expected level gives 1)", got)
	}
}

// StoredDue is a mailbox fact, not a classifier estimate: priority must turn
// an otherwise informational message with an open follow-up date into the
// owner's follow-up, with the stored due source, so the ranked list cannot
// contradict commitments and agenda.
func TestStoredDuePromotesReadToFollowUp(t *testing.T) {
	open := store.PriorityRow{MessageID: 1, Subject: "Invoice", ReceivedAt: receivedAgo(0),
		StoredDue: true, Route: map[string]route.Answer{"urgency": score("urgency", 4)}}
	closed := open
	closed.MessageID, closed.StoredDue = 2, false
	svc, _ := newService(t, []store.PriorityRow{open, closed}, false)
	items, _, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("ranked %d items, want the urgent informational row and its control", len(items))
	}
	byID := map[int64]Item{items[0].MessageID: items[0], items[1].MessageID: items[1]}
	if got := byID[1].Action; got.Primary != action.FollowUp || got.Waiting != action.WaitingOwner ||
		got.DueSource != action.DueStored || !slices.Contains(got.Reasons, "due") {
		t.Fatalf("open stored due action = %+v, want follow_up/owner/stored with reason due", got)
	}
	if got := byID[2].Action; got.Primary != action.Read || got.DueSource != action.DueNone {
		t.Fatalf("control action = %+v, want read without a due source", got)
	}
}

// A stale-while-revalidate serve keeps the snapshot's build time: the API
// must not stamp a fresh request time on an older ranking.
