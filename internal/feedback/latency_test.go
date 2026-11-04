package feedback

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// TestFeedbackAndThreadsReportTheSameLatency: a reply's response time is one
// fact. The feedback correlations report it as a median of minutes while the
// behaviour profile reports median seconds converted to minutes; both must
// round through threads.RoundMinutes, so the same reply cannot show 1.3
// minutes on one screen and 1.2 on another. The reply set is deliberately not
// a whole number of minutes (75 s and 80 s), where a second rounding rule
// would disagree.
func TestFeedbackAndThreadsReportTheSameLatency(t *testing.T) {
	norm, err := mail.NewSubjectNormalizer(1, []string{"re"})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)
	var msgs []threads.Message
	for i, wait := range []time.Duration{75 * time.Second, 80 * time.Second, 5 * time.Minute} {
		conv := string(rune('A' + i))
		msgs = append(msgs,
			threads.Message{RowID: int64(10*i + 1), Store: "s", Role: mail.RoleInbox, Sender: "a@client.com",
				Domain: "client.com", ConversationID: conv, At: t0.Add(time.Duration(i) * time.Hour)},
			threads.Message{RowID: int64(10*i + 2), Store: "s", Role: mail.RoleSent, Sender: "me@x.com",
				Domain: "x.com", ConversationID: conv, Recipients: []string{"a@client.com"},
				At: t0.Add(time.Duration(i)*time.Hour + wait)},
		)
	}
	a := threads.Analyze(msgs, norm)

	profile, err := a.Behaviour(threads.Options{Zone: time.UTC, BucketMinutes: []int{1, 60, 1440}})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Totals.MedianMinutes == nil {
		t.Fatal("the behaviour profile has no median latency")
	}

	latencies := make([]float64, 0, len(msgs))
	for i := range a.Messages {
		if r, ok := a.KnownReplies[a.Messages[i].RowID]; ok {
			latencies = append(latencies, r.Seconds/60)
		}
	}
	if len(latencies) != 3 {
		t.Fatalf("%d replies, want 3", len(latencies))
	}
	got := median1(latencies)
	if got == nil {
		t.Fatal("feedback has no median latency")
	}
	if *got != *profile.Totals.MedianMinutes {
		t.Fatalf("feedback median %v, threads median %v", *got, *profile.Totals.MedianMinutes)
	}
	// The value is the half-up edge: 80 s is 1.333 minutes, 1.3.
	if *got != 1.3 {
		t.Fatalf("median %v, want 1.3", *got)
	}

	groups := medianGroups(map[string][]float64{"client": latencies}, 1)
	if len(groups) != 1 || groups[0].MedianMinutes == nil || *groups[0].MedianMinutes != *got {
		t.Fatalf("group median %+v disagrees with the single median %v", groups, *got)
	}
}
