package threads

import (
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestBehaviourAndHistoryReportTheSameExample: the behaviour profile and the
// causal history describe the same reply, so they must produce byte-identical
// examples. Each used to build the literal itself, which let a field change
// reach one surface only. newExample is the one builder.
func TestBehaviourAndHistoryReportTheSameExample(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	asked := in(1, "a@y.com", "q", 0, "me@x.com")
	asked.ConversationID = "A"
	reply := out(2, "q", 95*time.Minute, "a@y.com")
	reply.ConversationID = "A"
	reply.Excerpt = "reply q"
	a := Analyze([]Message{asked, reply}, norm(t))

	profile, err := a.Behaviour(Options{Zone: newYork, BucketMinutes: []int{60, 240, 1440}, ExamplesPerSender: 3})
	if err != nil {
		t.Fatal(err)
	}
	examples := profile.Examples["a@y.com"]
	if len(examples) != 1 {
		t.Fatalf("behaviour examples %+v", profile.Examples)
	}

	h := a.KnownHistory([]float64{60, 240, 1440}, 3, newYork, mail.NewDomains(nil))
	point, ok := h.Sender("a@y.com", reply.At.Add(time.Second))
	if !ok || len(point.Examples) != 1 {
		t.Fatalf("history point %+v %v", point, ok)
	}
	if !reflect.DeepEqual(examples[0], point.Examples[0]) {
		t.Fatalf("the two surfaces disagree:\n behaviour %+v\n history   %+v", examples[0], point.Examples[0])
	}

	// And the example is the reply itself, in the owner's zone with the one
	// latency rounding.
	ex := examples[0]
	if ex.MessageID != asked.RowID || ex.Subject != asked.Subject || ex.ReplyPreview != reply.Excerpt ||
		ex.RespondedMinutes != 95 || ex.ReceivedAt != "2026-01-05T09:00:00-05:00" {
		t.Fatalf("example %+v", ex)
	}
}

// TestProfileMonthsUseTheOwnerMonthKey: the volume months are bucketed in the
// owner's zone with the one month key, so a late instant lands in the local
// month the UI and the feedback volume analysis read.
func TestProfileMonthsUseTheOwnerMonthKey(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	late := Message{RowID: 1, Store: "s", Role: mail.RoleInbox, Sender: "a@y.com", Domain: "y.com", Subject: "late",
		At: time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)} // 22:00 on Dec 31 in New York
	p, err := Analyze([]Message{late}, norm(t)).Behaviour(Options{Zone: newYork, BucketMinutes: []int{60}})
	if err != nil {
		t.Fatal(err)
	}
	key := mail.MonthKey(late.At.In(newYork))
	if key != "2025-12" {
		t.Fatalf("the test instant buckets to %q, want 2025-12", key)
	}
	if _, ok := p.Months[key]; !ok || len(p.Months) != 1 {
		t.Fatalf("months %+v do not use the owner key %q", p.Months, key)
	}
}

// TestRoundMinutesIsTheOneLatencyRounding pins the shared rounding at its
// half-up edge: 1.25 minutes is 75 seconds and reports as 1.3, never 1.2.
func TestRoundMinutesIsTheOneLatencyRounding(t *testing.T) {
	cases := map[float64]float64{0: 0, 0.04: 0, 1.25: 1.3, 1.24: 1.2, 89.96: 90, 1499.95: 1500}
	for in, want := range cases {
		if got := RoundMinutes(in); got != want {
			t.Errorf("RoundMinutes(%v) = %v, want %v", in, got, want)
		}
	}
}
