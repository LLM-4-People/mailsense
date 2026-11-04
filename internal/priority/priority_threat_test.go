package priority

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// Automated bulk mail owns no action, so its urgency, deadline and call to
// action cannot force it into Important. The suspicious answer overrides the
// veto: phishing inside a bulk category is still a threat.
func TestBulkMailWithUrgentOrDeadlineDoesNotEnterImportant(t *testing.T) {
	cases := []struct {
		name  string
		route map[string]route.Answer
	}{
		{"marketing category", map[string]route.Answer{
			"category":       choiceAns("category", "marketing"),
			"urgency":        score("urgency", 4),
			"deadline":       score("deadline", 5),
			"call_to_action": noul("call_to_action", 0.9),
			"reply_needed":   noul("reply_needed", 0.9),
			"asks_for":       choiceAns("asks_for", "payment_money"),
		}},
		{"automated service relationship", map[string]route.Answer{
			"relationship":   choiceAns("relationship", "automated_service"),
			"urgency":        score("urgency", 4),
			"deadline":       score("deadline", 5),
			"call_to_action": noul("call_to_action", 0.9),
			"reply_needed":   noul("reply_needed", 0.9),
			"asks_for":       choiceAns("asks_for", "payment_money"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t, []store.PriorityRow{{MessageID: 1, ReceivedAt: receivedAgo(0), Route: tc.route}}, false)
			items, total, _, err := svc.Rank(t.Context(), FilterAll, 5)
			if err != nil || len(items) != 0 || total != 0 {
				t.Fatalf("bulk mail entered Important: %v total=%d err=%v", items, total, err)
			}
		})
	}

	t.Run("suspicious marketing stays a threat", func(t *testing.T) {
		svc, _ := newService(t, []store.PriorityRow{{MessageID: 2, ReceivedAt: receivedAgo(0),
			Route: map[string]route.Answer{
				"category":       choiceAns("category", "marketing"),
				"suspicious":     noul("suspicious", 0.9),
				"urgency":        score("urgency", 4),
				"deadline":       score("deadline", 5),
				"call_to_action": noul("call_to_action", 0.9),
			}}}, false)
		items, total, _, err := svc.Rank(t.Context(), FilterAll, 5)
		if err != nil || len(items) != 1 || total != 1 {
			t.Fatalf("suspicious marketing: %v total=%d err=%v", items, total, err)
		}
		if items[0].Action.Primary != action.Verify {
			t.Fatalf("a suspicious marketing message is %+v, want Verify", items[0].Action)
		}
		threats, threatTotal, _, err := svc.Rank(t.Context(), FilterSecurity, 5)
		if err != nil || threatTotal != 1 || len(threats) != 1 || threats[0].MessageID != 2 {
			t.Fatalf("security filter: %v total=%d err=%v", threats, threatTotal, err)
		}
	})
}

// The security bucket is the threat judgment, not the subject category.
func TestFilterSecurityIsThreatBased(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "security")}},
		{MessageID: 2, ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
			"category": choiceAns("category", "security"), "suspicious": noul("suspicious", 0.9)}},
	}
	svc, _ := newService(t, rows, false)
	items, total, _, err := svc.Rank(t.Context(), FilterSecurity, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].MessageID != 2 {
		t.Fatalf("security filter = %v total=%d, want only the suspicious message 2", items, total)
	}
	if FilterSecurity.matches(Item{Signals: Signals{Category: "security"}}) {
		t.Fatal("the security subject category alone matched the threat filter")
	}
}

// Automated bulk mail never becomes an owner obligation, even when its text
// contains a question and a call to action. The relationship answer is as
// authoritative as a bulk category, and an abstained category does not erase
// the relationship's automated-service judgment.
func TestAutomatedMailNeverCreatesAReplyOrAction(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ConversationID: "C1", Subject: "Weekly news",
			ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
				"category":       choiceAns("category", "marketing"),
				"is_question":    noul("is_question", 0.9),
				"reply_needed":   noul("reply_needed", 0.9),
				"call_to_action": noul("call_to_action", 0.9),
				"asks_for":       choiceAns("asks_for", "payment_money"),
			}},
		{MessageID: 2, ConversationID: "C2", Subject: "Service alert",
			ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
				"relationship":   choiceAns("relationship", "automated_service"),
				"reply_needed":   noul("reply_needed", 0.9),
				"call_to_action": noul("call_to_action", 0.9),
				"asks_for":       choiceAns("asks_for", "payment_money"),
			}},
		{MessageID: 3, ConversationID: "C3", Subject: "Receipt",
			ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{
				"category":       notCertified(engine.Choice),
				"relationship":   choiceAns("relationship", "automated_service"),
				"reply_needed":   noul("reply_needed", 0.9),
				"call_to_action": noul("call_to_action", 0.9),
			}},
	}
	states := map[string]threadintel.Assessment{
		"C1": {State: threadintel.NoAction, Reason: threadintel.ReasonAutomated, LastMessageID: 1},
		"C2": {State: threadintel.NoAction, Reason: threadintel.ReasonAutomated, LastMessageID: 2},
		"C3": {State: threadintel.NoAction, Reason: threadintel.ReasonAutomated, LastMessageID: 3},
	}
	svc, _, ft := newThreadService(t, rows, states)
	items, total, _, err := svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(items) != 0 || total != 0 {
		t.Fatalf("automated mail entered the ranking: %v total=%d err=%v", items, total, err)
	}
	for _, id := range []int64{1, 2, 3} {
		sig := ft.signals[id]
		if sig.ReplyNeeded {
			t.Fatalf("message %d was handed to the thread resolver as a reply obligation", id)
		}
		if !sig.Automated {
			t.Fatalf("message %d was not marked automated for the thread resolver", id)
		}
	}
}
