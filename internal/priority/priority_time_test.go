package priority

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The received instant is parsed once per row and both entry points keep
// Score's contract: a future timestamp clamps to age zero (recent), while a
// malformed or missing one stays undated (no recency). The row still enters
// on its reply signal.
func TestTimestampContractAtBothEntryPoints(t *testing.T) {
	cases := []struct {
		name     string
		received string
		score    float64
		reasons  []string
	}{
		{"undated reply", "", 30, []string{"reply"}},
		{"invalid timestamp stays undated", "not-a-mail-instant", 30, []string{"reply"}},
		{"future reply clamps to the newest age", receivedAgo(-48 * time.Hour), 42, []string{"reply", "recent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := scoreOne(t, map[string]route.Answer{
				"reply_needed": noul("reply_needed", 0.9),
			}, tc.received)
			if !ok {
				t.Fatal("the reply signal must still enter")
			}
			if math.Abs(item.Score-tc.score) > 1e-6 {
				t.Fatalf("score %.6f, want %.6f", item.Score, tc.score)
			}
			if !slices.Equal(item.Reasons, tc.reasons) {
				t.Fatalf("reasons %v, want %v", item.Reasons, tc.reasons)
			}
		})
	}

	rows := []store.PriorityRow{
		{MessageID: 1, ConversationID: "C1", Subject: "future", ReceivedAt: receivedAgo(-48 * time.Hour),
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
		{MessageID: 2, ConversationID: "C2", Subject: "invalid", ReceivedAt: "not-a-mail-instant",
			Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}},
	}
	svc := newServiceAt(t, rows, clocktest.New(testNow))
	page, err := svc.RankPage(t.Context(), FilterAll, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ranked %d items, want both rows", len(page.Items))
	}
	for _, item := range page.Items {
		recent := slices.Contains(item.Reasons, "recent")
		switch item.MessageID {
		case 1:
			if !recent || math.Abs(item.Score-42) > 1e-6 {
				t.Fatalf("future row lost its clamped recency: %+v", item)
			}
		case 2:
			if recent || math.Abs(item.Score-30) > 1e-6 {
				t.Fatalf("invalid timestamp became dated: %+v", item)
			}
		default:
			t.Fatalf("unexpected message %d", item.MessageID)
		}
	}
}
