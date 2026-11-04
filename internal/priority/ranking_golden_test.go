package priority

// The packed ranking table is the behavior lock for the build's one scan: a
// mailbox large enough to overflow the cache, spread over many conversations,
// with every branch the scoring pass reads. The expected snapshot lives in
// testdata/ranking_packed.json and was captured from the build before the
// scan retention was reduced, so a refactor must reproduce it byte for byte:
// items and their order, every filter count, the topic census, the resolved
// conversation states and the exact ids/signals the thread resolver receives.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

var updateGolden = flag.Bool("update-priority-golden", false, "rewrite the packed ranking golden")

const (
	goldenNow   = "2026-11-03T12:00:00Z"
	goldenRows  = 420
	goldenConvs = 61
)

// goldenSnapshot is everything the build produces that a consumer can read.
type goldenSnapshot struct {
	Items  []Item                            `json:"items"`
	Counts map[string]int                    `json:"counts"`
	Topics TopicCensus                       `json:"topics"`
	States map[string]threadintel.Assessment `json:"states"`
	// ResolverIDs and ResolverSignals pin the one thread-resolver call: every
	// conversation with an owner row, and every owner row's directed signal.
	ResolverIDs     []string       `json:"resolver_ids"`
	ResolverSignals []goldenSignal `json:"resolver_signals"`
}

type goldenSignal struct {
	MessageID int64  `json:"message_id"`
	Reply     bool   `json:"reply_needed"`
	Question  bool   `json:"is_question"`
	AsksFor   string `json:"asks_for"`
	Topic     string `json:"topic"`
	Subtopic  string `json:"subtopic"`
	Automated bool   `json:"automated"`
}

// goldenFixture is one deterministic packed mailbox. Rows are ordered as the
// fake store streams them; the real store is deliberately unordered because
// the ranking is order-independent, and TestPackedRankingIgnoresTheScanOrder
// reuses this fixture reversed to prove it.
func goldenFixture(t *testing.T) ([]store.PriorityRow, *fakeThreads, time.Time) {
	t.Helper()
	now, err := time.Parse(time.RFC3339, goldenNow)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]store.PriorityRow, 0, goldenRows)
	convOf := func(i int) string {
		if i%17 == 0 {
			return ""
		}
		return fmt.Sprintf("C-%02d", i%goldenConvs)
	}
	received := func(i int) string {
		return now.Add(-time.Duration(i) * 37 * time.Minute).Format(mail.TimeLayout)
	}
	for i := 0; i < goldenRows; i++ {
		answers := map[string]route.Answer{
			"urgency":         score("urgency", float64(i%5)),
			"deadline":        score("deadline", float64(i%4)),
			"effort":          score("effort", float64(i%3)),
			"request_count":   score("request_count", float64(i%4)),
			"reply_needed":    noul("reply_needed", 0.2),
			"is_question":     noul("is_question", 0.1),
			"decision_needed": noul("decision_needed", 0.1),
			"suspicious":      noul("suspicious", 0.1),
			"call_to_action":  noul("call_to_action", 0.1),
		}
		answers["topic"] = choiceAns("topic", []string{"project_work", "billing_account", "support_issue", "meeting_scheduling", "other"}[i%5])
		if i%2 == 0 {
			answers["stance"] = choiceAns("stance", "direct_request")
		}
		switch {
		case i%7 == 0:
			answers["category"] = choiceAns("category", "action")
		case i%3 == 0:
			answers["category"] = choiceAns("category", "work")
		case i%11 == 0:
			answers["category"] = choiceAns("category", "marketing")
		case i%13 == 0:
			answers["category"] = choiceAns("category", "notifications")
		}
		if i%4 == 0 {
			answers["reply_needed"] = noul("reply_needed", 0.9)
		}
		if i%5 == 0 {
			answers["is_question"] = noul("is_question", 0.9)
		}
		if i%6 == 0 {
			answers["decision_needed"] = noul("decision_needed", 0.9)
		}
		if i%29 == 0 {
			answers["suspicious"] = noul("suspicious", 0.9)
		}
		if i%8 == 0 {
			answers["urgency"] = score("urgency", 4)
		}
		if i%10 == 0 {
			answers["deadline"] = score("deadline", 2)
		}
		if i%12 == 0 {
			answers["asks_for"] = choiceAns("asks_for", "payment_money")
		}
		if i%9 == 0 {
			answers["asks_for"] = choiceAns("asks_for", "nothing_fyi")
		}
		if i%23 == 0 {
			answers["relationship"] = choiceAns("relationship", "automated_service")
		}
		if i%31 == 0 {
			answers["call_to_action"] = noul("call_to_action", 0.9)
		}
		body := fmt.Sprintf("  Body for message %d with  some   spacing.  ", i+1)
		if i%97 == 0 {
			// A body whose head is all whitespace exercises the full-body
			// snippet fallback without changing the score.
			body = strings.Repeat(" ", testHeadChars+200) + "after whitespace"
		}
		role := mail.RoleInbox
		if i%19 == 0 {
			role = mail.RoleSent
		}
		rows = append(rows, store.PriorityRow{
			MessageID: int64(i + 1), Revision: 1, ConversationID: convOf(i),
			Subject: fmt.Sprintf("Subject %d", i+1), SenderName: fmt.Sprintf("Sender %d", i%7),
			SenderAddress: fmt.Sprintf("sender%d@example.com", i%7), ReceivedAt: received(i),
			FolderRole: role, HasAttachments: i%14 == 0, Body: body, Route: answers,
		})
	}

	states := make(map[string]threadintel.Assessment, goldenConvs)
	newest := make(map[string]int64, goldenConvs)
	for _, row := range rows {
		if row.ConversationID != "" {
			newest[row.ConversationID] = max(newest[row.ConversationID], row.MessageID)
		}
	}
	order := make([]string, 0, goldenConvs)
	for id := range newest {
		order = append(order, id)
	}
	slices.Sort(order)
	for i, id := range order {
		last := newest[id]
		state := threadintel.Assessment{
			ConversationID: id, LastMessageID: last, Messages: int(last % 9),
			LastAt: now.Add(-time.Duration(i) * time.Minute).Format(mail.TimeLayout),
			Reason: threadintel.ReasonInboundLast, Visibility: threadintel.Partial,
			Confidence: 0.9, Topic: "project_work", Subtopic: "design_review",
		}
		switch i % 6 {
		case 0:
			state.State = threadintel.AwaitingOwner
			state.ReplyToMessageID = last
			state.ActorName, state.ActorAddress = "Sender 1", "sender1@example.com"
			state.OwnerActorName, state.OwnerActorAddress = "Sender 1", "sender1@example.com"
		case 1:
			state.State = threadintel.AwaitingOther
			state.ActorName, state.ActorAddress = "Owner", "owner@example.com"
		case 2:
			state.State = threadintel.AnsweredByOther
			state.Reason = threadintel.ReasonOtherReplied
			state.ActorName, state.ActorAddress = "Colleague", "colleague@example.com"
		case 3:
			state.State = threadintel.AnsweredByOwner
			state.Reason = threadintel.ReasonOwnerReplied
		case 4:
			state.State = threadintel.NoAction
			state.Reason = threadintel.ReasonCcOnly
		default:
			state.State = threadintel.Unknown
			state.Reason = threadintel.ReasonNoMessages
		}
		states[id] = state
	}
	return rows, &fakeThreads{states: states}, now
}

// goldenBuild runs one build over the packed fixture and returns the cached
// snapshot, the resolver's recorded call and the conversations the scan read.
func goldenBuild(t *testing.T) (*cachedRanking, *fakeThreads, map[string]bool) {
	t.Helper()
	rows, threads, now := goldenFixture(t)
	cfg := configtest.New(t, map[string]any{"learning.test_mode": false})
	fs := &fakeStore{rows: rows}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clocktest.New(now),
		Threads: threads, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.buildRanking(t.Context(), false, now, "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	convs := map[string]bool{}
	for _, row := range rows {
		if row.ConversationID != "" {
			convs[row.ConversationID] = true
		}
	}
	return c, threads, convs
}

func TestPackedRankingMatchesTheGoldenSnapshot(t *testing.T) {
	c, threads, wantConvs := goldenBuild(t)
	got := marshalGolden(t, c, threads)
	path := filepath.Join("testdata", "ranking_packed.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the packed golden: %v (run with -update-priority-golden)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the packed ranking changed; diff:\n%s", goldenDiff(want, got))
	}

	// The contract the states map must satisfy for its consumers: one entry
	// per conversation the scan read, and nothing else. Agenda enumerates
	// every entry to build obligations, so a truncated map would silently
	// drop the owner's waiting threads.
	if len(c.states) != len(wantConvs) {
		t.Fatalf("cached %d conversation states, want every %d conversation with an owner row", len(c.states), len(wantConvs))
	}
	for id := range wantConvs {
		if _, ok := c.states[id]; !ok {
			t.Fatalf("cached states omit conversation %q", id)
		}
	}
	if _, ok := c.states[""]; ok {
		t.Fatal("cached states contain the empty conversation id")
	}
}

// TestPackedRankingIgnoresTheScanOrder is the same golden from the same
// mailbox streamed in reverse. The real store streams unordered, so the
// ranking and its state set must not depend on the order the callback reads.
func TestPackedRankingIgnoresTheScanOrder(t *testing.T) {
	rows, threads, now := goldenFixture(t)
	slices.Reverse(rows)
	cfg := configtest.New(t, map[string]any{"learning.test_mode": false})
	svc, err := New(cfg, Deps{Store: &fakeStore{rows: rows}, Gate: allowGate{}, Config: cfg,
		Clock: clocktest.New(now), Threads: threads, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.buildRanking(t.Context(), false, now, "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	got := marshalGolden(t, c, threads)
	want, err := os.ReadFile(filepath.Join("testdata", "ranking_packed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the ranking depends on the scan order; diff:\n%s", goldenDiff(want, got))
	}
}

// marshalGolden renders every consumer-visible part of one build.
func marshalGolden(t *testing.T, c *cachedRanking, threads *fakeThreads) []byte {
	t.Helper()
	ids := slices.Clone(threads.ids)
	slices.Sort(ids)
	signals := make([]goldenSignal, 0, len(threads.signals))
	for id, sig := range threads.signals {
		signals = append(signals, goldenSignal{MessageID: id, Reply: sig.ReplyNeeded, Question: sig.IsQuestion,
			AsksFor: sig.AsksFor, Topic: sig.Topic, Subtopic: sig.Subtopic, Automated: sig.Automated})
	}
	slices.SortFunc(signals, func(a, b goldenSignal) int { return int(a.MessageID - b.MessageID) })
	counts := make(map[string]int, len(c.counts))
	for f, n := range c.counts {
		counts[string(f)] = n
	}
	snapshot := goldenSnapshot{Items: make([]Item, 0, len(c.items)), Counts: counts,
		Topics: cloneTopicCensus(c.topics), States: c.states, ResolverIDs: ids, ResolverSignals: signals}
	for _, r := range c.items {
		snapshot.Items = append(snapshot.Items, r.item)
	}
	got, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(got, '\n')
}

// goldenDiff prints the first differing line with a little context.
func goldenDiff(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	var b strings.Builder
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d:\n want %s\n  got %s\n", i+1, wl, gl)
			for j := i + 1; j < min(i+6, len(g)); j++ {
				fmt.Fprintf(&b, "  got %s\n", g[j])
			}
			break
		}
	}
	return b.String()
}
