package action

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func TestPrimaryPrecedence(t *testing.T) {
	cases := []struct {
		name string
		sig  Signals
		want Primary
	}{
		{"suspicious outranks a verdict", Signals{Suspicious: 1, DecisionNeeded: 1}, Verify},
		{"a security topic with a verdict is a verdict", Signals{Category: "security", DecisionNeeded: 1}, Decide},
		{"a security topic with a payment ask is a payment", Signals{Category: "security", AsksFor: "payment_money"}, Pay},
		{"a security topic alone asks nothing", Signals{Category: "security"}, Read},
		{"an automated service relationship outranks a reply", Signals{Relationship: "automated_service", ReplyNeeded: 1}, Read},
		{"spam never asks for action", Signals{Category: "spam", CallToAction: 1, AsksFor: "payment_money"}, Read},
		{"marketing never asks for action", Signals{Category: "marketing", CallToAction: 1}, Read},
		{"notification never asks for action", Signals{Category: "notifications", DecisionNeeded: 1, CallToAction: 1}, Read},
		{"a verdict outranks a reply", Signals{DecisionNeeded: 1, ReplyNeeded: 1}, Decide},
		{"a named ask outranks a generic reply", Signals{ReplyNeeded: 1, AsksFor: "task_work"}, Do},
		{"a reply without a named ask", Signals{ReplyNeeded: 1}, Reply},
		{"payment ask", Signals{AsksFor: "payment_money"}, Pay},
		{"meeting ask", Signals{AsksFor: "meeting_availability"}, Schedule},
		{"document ask", Signals{AsksFor: "information_document"}, Send},
		{"task ask", Signals{AsksFor: "task_work"}, Do},
		{"approval ask", Signals{AsksFor: "approval_decision"}, Decide},
		{"answer ask", Signals{AsksFor: "answer_question"}, Reply},
		{"fyi ask", Signals{AsksFor: "nothing_fyi", CallToAction: 1}, Read},
		{"generic cta", Signals{CallToAction: 1}, Do},
		{"finance deadline", Signals{Category: "finance", Deadline: 4}, FollowUp},
		{"informational", Signals{Category: "work"}, Read},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Input{Signals: tc.sig})
			if got.Primary != tc.want {
				t.Fatalf("primary = %q, want %q (%+v)", got.Primary, tc.want, got)
			}
		})
	}
}

func TestSecurityCategoryWithoutSuspicionIsNotVerify(t *testing.T) {
	cases := []struct {
		name string
		sig  Signals
		want Primary
	}{
		{"no ask", Signals{Category: "security"}, Read},
		{"reply ask", Signals{Category: "security", ReplyNeeded: 1}, Reply},
		{"payment ask", Signals{Category: "security", AsksFor: "payment_money"}, Pay},
		{"verdict ask", Signals{Category: "security", DecisionNeeded: 1}, Decide},
		{"suspicious in the security topic", Signals{Category: "security", Suspicious: 1, DecisionNeeded: 1}, Verify},
		{"suspicious in another topic", Signals{Category: "work", Suspicious: 1}, Verify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Input{Signals: tc.sig})
			if got.Primary != tc.want {
				t.Fatalf("primary = %q, want %q (%+v)", got.Primary, tc.want, got)
			}
		})
	}
}

func TestAutomatedServiceRelationshipVetoesAction(t *testing.T) {
	cases := []struct {
		name string
		sig  Signals
		want Primary
	}{
		{"reply request", Signals{Relationship: "automated_service", ReplyNeeded: 1}, Read},
		{"call to action", Signals{Relationship: "automated_service", CallToAction: 1}, Read},
		{"receipt with a payment ask", Signals{Category: "finance", Relationship: "automated_service",
			AsksFor: "payment_money"}, Read},
		{"notification with a verdict", Signals{Category: "notifications",
			Relationship: "existing_client", DecisionNeeded: 1}, Read},
		{"suspicious overrides the relationship veto", Signals{Category: "marketing",
			Relationship: "automated_service", Suspicious: 1, AsksFor: "payment_money"}, Verify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Input{Signals: tc.sig})
			if got.Primary != tc.want {
				t.Fatalf("primary = %q, want %q (%+v)", got.Primary, tc.want, got)
			}
		})
	}

	// The same veto owns due pressure: an automated service cannot turn its
	// deadline into a follow-up.
	got := Classify(Input{Signals: Signals{Category: "finance", Relationship: "automated_service", Deadline: 5}})
	if got.Primary != Read || got.DueSource != DueNone || got.DueLevel != 0 {
		t.Fatalf("automated service with a deadline = %+v", got)
	}
}

func TestAutomatedPredicateReadsBothAnswers(t *testing.T) {
	cases := []struct {
		category     string
		relationship string
		want         bool
	}{
		{"spam", "", true},
		{"marketing", "", true},
		{"notifications", "", true},
		{"finance", "", false},
		{"security", "", false},
		{"", "", false},
		{"", "automated_service", true},
		{"work", "automated_service", true},
		{"finance", "vendor_partner", false},
	}
	for _, tc := range cases {
		if got := Automated(tc.category, tc.relationship); got != tc.want {
			t.Errorf("Automated(%q, %q) = %v, want %v", tc.category, tc.relationship, got, tc.want)
		}
	}
}

func TestBulkVetoIsOverriddenBySuspicion(t *testing.T) {
	cases := []struct {
		name string
		sig  Signals
		want bool
	}{
		{"bulk category", Signals{Category: "marketing"}, true},
		{"automated relationship", Signals{Relationship: "automated_service"}, true},
		{"abstained category", Signals{}, false},
		{"suspicious bulk category", Signals{Category: "marketing", Suspicious: 1}, false},
		{"suspicious automated relationship", Signals{Relationship: "automated_service", Suspicious: 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Bulk(tc.sig); got != tc.want {
				t.Fatalf("Bulk(%+v) = %v, want %v", tc.sig, got, tc.want)
			}
		})
	}
}

func TestThreadStateIsTerminal(t *testing.T) {
	cases := []struct {
		name    string
		state   threadintel.State
		reason  threadintel.Reason
		sig     Signals
		want    Primary
		waiting Waiting
	}{
		{"owner answered", threadintel.AnsweredByOwner, "", Signals{DecisionNeeded: 1}, Read, WaitingNone},
		{"other answered", threadintel.AnsweredByOther, "", Signals{DecisionNeeded: 1}, Read, WaitingNone},
		{"cc only", threadintel.NoAction, threadintel.ReasonCcOnly, Signals{DecisionNeeded: 1}, Read, WaitingNone},
		{"owner closing note", threadintel.NoAction, threadintel.ReasonOwnerLastClosed, Signals{ReplyNeeded: 1}, Read, WaitingNone},
		{"automated mail", threadintel.NoAction, threadintel.ReasonAutomated, Signals{CallToAction: 1}, Read, WaitingNone},
		{"no reply but a payment", threadintel.NoAction, threadintel.ReasonNoReplyExpected, Signals{AsksFor: "payment_money"}, Pay, WaitingOwner},
		{"waiting on them", threadintel.AwaitingOther, "", Signals{DecisionNeeded: 1, ReplyNeeded: 1}, Wait, WaitingOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Input{Signals: tc.sig, Thread: threadintel.Assessment{State: tc.state, Reason: tc.reason}})
			if got.Primary != tc.want || got.Waiting != tc.waiting {
				t.Fatalf("class = %+v, want %s/%s", got, tc.want, tc.waiting)
			}
		})
	}
}

func TestOnlyTheCurrentThreadMessageOwesTheAction(t *testing.T) {
	thread := threadintel.Assessment{State: threadintel.AwaitingOwner, ReplyToMessageID: 7, LastMessageID: 7}
	if got := Classify(Input{MessageID: 6, Signals: Signals{ReplyNeeded: 1}, Thread: thread}); got.Primary != Read ||
		!slices.Contains(got.Reasons, "thread_context") {
		t.Fatalf("older inbound = %+v", got)
	}
	if got := Classify(Input{MessageID: 7, Signals: Signals{ReplyNeeded: 1}, Thread: thread}); got.Primary != Reply ||
		got.Waiting != WaitingOwner {
		t.Fatalf("reply target = %+v", got)
	}
	thread.ReplyToMessageID = 0
	thread.LastMessageID = 8
	if got := Classify(Input{MessageID: 7, Signals: Signals{ReplyNeeded: 1}, Thread: thread}); got.Primary != Read ||
		!slices.Contains(got.Reasons, "thread_history") {
		t.Fatalf("history row = %+v", got)
	}
}

func TestUnknownVisibilityKeepsTheActionButNotTheDirection(t *testing.T) {
	got := Classify(Input{Signals: Signals{ReplyNeeded: 1}, Thread: threadintel.Assessment{State: threadintel.Unknown}})
	if got.Primary != Reply || got.Waiting != WaitingUnknown {
		t.Fatalf("class = %+v", got)
	}
}

func TestDueSourcesAreNeverConfused(t *testing.T) {
	stored := Classify(Input{Signals: Signals{Category: "work"}, StoredDue: true})
	if stored.Primary != FollowUp || stored.Waiting != WaitingOwner || stored.DueSource != DueStored || stored.DueLevel != 0 {
		t.Fatalf("stored due = %+v", stored)
	}
	reply := Classify(Input{Signals: Signals{ReplyNeeded: 1}, StoredDue: true})
	if reply.Primary != Reply || reply.DueSource != DueStored || reply.Waiting != WaitingOwner {
		t.Fatalf("reply with due = %+v", reply)
	}
	classified := Classify(Input{Signals: Signals{Category: "finance", Deadline: 4}})
	if classified.Primary != FollowUp || classified.DueSource != DueClassifier || classified.DueLevel != 4 {
		t.Fatalf("classifier due = %+v", classified)
	}
	plain := Classify(Input{Signals: Signals{Category: "work", Deadline: 2}})
	if plain.Primary != Read || plain.DueSource != DueNone || plain.DueLevel != 0 {
		t.Fatalf("distant date = %+v", plain)
	}
}

func TestOwnerActionPredicate(t *testing.T) {
	for _, p := range []Primary{Reply, Decide, Pay, Schedule, Send, Do, Verify, FollowUp} {
		if !p.OwnerAction() {
			t.Errorf("%s must be an owner action", p)
		}
	}
	for _, p := range []Primary{Read, Wait} {
		if p.OwnerAction() {
			t.Errorf("%s must not be an owner action", p)
		}
	}
}
