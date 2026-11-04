package subject

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// TestCatalogueIsCompleteAndConsistent is the one invariant that keeps the
// hierarchy honest: every topic has subtopics, every rule points at one of
// them, and every topic in the display order has a spec.
func TestCatalogueIsCompleteAndConsistent(t *testing.T) {
	topics := Topics()
	if len(topics) == 0 {
		t.Fatal("no topics")
	}
	seen := map[string]bool{}
	for _, topic := range topics {
		if seen[topic] {
			t.Fatalf("duplicate topic %q", topic)
		}
		seen[topic] = true
		subs := Subtopics(topic)
		if len(subs) < 2 {
			t.Fatalf("%s has %d subtopics", topic, len(subs))
		}
		spec, ok := specs[topic]
		if !ok {
			t.Fatalf("%s has no rule spec", topic)
		}
		if spec.Default != "" && !HasSubtopic(topic, spec.Default) {
			t.Fatalf("%s default %q is not a subtopic", topic, spec.Default)
		}
		for ask, sub := range spec.ByAsk {
			if !HasSubtopic(topic, sub) {
				t.Fatalf("%s ask %q -> unknown subtopic %q", topic, ask, sub)
			}
		}
		for category, sub := range spec.ByCategory {
			if !HasSubtopic(topic, sub) {
				t.Fatalf("%s category %q -> unknown subtopic %q", topic, category, sub)
			}
		}
		for _, sub := range []string{spec.Decision, spec.Reply} {
			if sub != "" && !HasSubtopic(topic, sub) {
				t.Fatalf("%s rule %q is not a subtopic", topic, sub)
			}
		}
	}
	for topic := range specs {
		if !seen[topic] {
			t.Fatalf("spec %q is not in Topics()", topic)
		}
	}
}

func TestClassifyUsesTheDominantAskFirst(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Subject
	}{
		{"payment inside billing", Input{Category: "finance", Topic: "billing_account", AsksFor: "payment_money", Decision: true},
			Subject{Topic: "billing_account", Subtopic: "payment", Path: "billing_account/payment", Basis: "ask:payment_money"}},
		{"decision when no ask", Input{Topic: "billing_account", Decision: true},
			Subject{Topic: "billing_account", Subtopic: "approval", Path: "billing_account/approval", Basis: "decision"}},
		{"category fallback", Input{Category: "finance", Topic: "billing_account"},
			Subject{Topic: "billing_account", Subtopic: "invoice", Path: "billing_account/invoice", Basis: "category:finance"}},
		{"reply fallback", Input{Topic: "support_issue", Reply: true},
			Subject{Topic: "support_issue", Subtopic: "troubleshooting", Path: "support_issue/troubleshooting", Basis: "reply"}},
		{"default", Input{Topic: "meeting_scheduling"},
			Subject{Topic: "meeting_scheduling", Subtopic: "invitation", Path: "meeting_scheduling/invitation", Basis: "default"}},
		{"unknown ask falls through", Input{Topic: "project_work", AsksFor: "other"},
			Subject{Topic: "project_work", Subtopic: "status_update", Path: "project_work/status_update", Basis: "default"}},
		{"blank topic", Input{Category: "work"},
			Subject{}},
		{"unknown topic keeps its label", Input{Topic: "Custom_Topic"},
			Subject{Topic: "custom_topic", Path: "custom_topic"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got != tc.want {
				t.Fatalf("Classify(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestEveryAskMapsToAClosedPath walks the classifier's actual input
// vocabulary, not only the catalogue. A new asks_for option that no topic
// handles still lands on that topic's default, never on an empty path.
func TestEveryAskMapsToAClosedPath(t *testing.T) {
	asks := []string{"answer_question", "approval_decision", "information_document", "payment_money",
		"meeting_availability", "task_work", "nothing_fyi", "other", "", "unknown_ask"}
	for _, topic := range Topics() {
		for _, ask := range asks {
			got := Classify(Input{Topic: topic, AsksFor: ask})
			if got.Topic != topic || got.Subtopic == "" || got.Path != topic+"/"+got.Subtopic {
				t.Fatalf("%s/%s: %+v", topic, ask, got)
			}
			if !HasSubtopic(topic, got.Subtopic) {
				t.Fatalf("%s/%s: %q is not a closed subtopic", topic, ask, got.Subtopic)
			}
		}
	}
}

// FromAnswers is the one adapter: it uses the profile's own yes/no cutoff,
// so a configured threshold moves the subject with the classifier instead
// of leaving a second 0.5 rule behind.
func TestFromAnswersUsesTheProfileCutoff(t *testing.T) {
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	p.Thresholds[taxonomy.ThresholdReply] = 0.6
	answers := map[string]engine.Answer{
		"category":     {Type: engine.Choice, Choice: "work"},
		"topic":        {Type: engine.Choice, Choice: "support_issue"},
		"reply_needed": {Type: engine.Noul, Value: 0.55},
	}
	if got := FromAnswers(p, answers); got.Subtopic != "incident" {
		t.Fatalf("0.55 under a 0.6 cutoff became %+v", got)
	}
	answers["reply_needed"] = engine.Answer{Type: engine.Noul, Value: 0.65}
	if got := FromAnswers(p, answers); got.Subtopic != "troubleshooting" || got.Basis != "reply" {
		t.Fatalf("0.65 above a 0.6 cutoff became %+v", got)
	}
	// A refused answer and every abstention are absent, even with a high
	// value: an abstention is a non-answer, not a yes.
	for _, a := range []engine.Answer{
		{Type: engine.Noul, Value: 0.95, Refused: true},
		{Type: engine.Noul, Value: 0.95, Abstain: engine.AbstainBelowTarget},
		{Type: engine.Noul, Value: 0.95, Abstain: route.AbstainNotCertified},
		{Type: engine.Noul, Value: 0.95, Abstain: route.AbstainBelowCoverage},
	} {
		answers["reply_needed"] = a
		if got := FromAnswers(p, answers); got.Subtopic != "incident" {
			t.Fatalf("unusable answer %+v became %+v", a, got)
		}
	}
}

// The subtopic must describe the message, not force an unrelated concept
// onto it. These are the mappings the audit found misleading.
func TestSemanticSubtopicMappings(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"support call is troubleshooting", Input{Topic: "support_issue", AsksFor: "meeting_availability"}, "troubleshooting"},
		{"support verdict is troubleshooting", Input{Topic: "support_issue", Decision: true}, "troubleshooting"},
		{"partner meeting is a meeting", Input{Topic: "partnership", AsksFor: "meeting_availability"}, "meeting"},
		{"integration verdict is an approval", Input{Topic: "technical_integration", Decision: true}, "approval"},
		{"integration approval ask is an approval", Input{Topic: "technical_integration", AsksFor: "approval_decision"}, "approval"},
		{"meeting prep is not a reschedule", Input{Topic: "meeting_scheduling", AsksFor: "task_work"}, "invitation"},
		{"press review is not a product update", Input{Topic: "news_updates", AsksFor: "task_work"}, "announcement"},
		{"a no-ask newsletter is a newsletter", Input{Topic: "news_updates", AsksFor: "nothing_fyi"}, "newsletter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got.Subtopic != tc.want {
				t.Fatalf("%+v -> %q, want %q", tc.in, got.Subtopic, tc.want)
			}
		})
	}
}

func TestEmptyAndNilSubtopics(t *testing.T) {
	if got := Subtopics("nope"); got != nil {
		t.Fatalf("unknown topic subtopics = %v", got)
	}
	if Subtopics("other") == nil || !HasSubtopic("other", "general") {
		t.Fatal("other/general is missing")
	}
	if HasSubtopic("other", "nope") {
		t.Fatal("unknown subtopic accepted")
	}
}

// A supported profile override can rename a question and point its role at
// it. The subject must read the role, not the default question name.
func TestFromAnswersFollowsRenamedRoleQuestions(t *testing.T) {
	p := &taxonomy.Profile{
		Questions: []engine.Question{
			{Name: "topic", Type: engine.Choice, Options: []engine.Option{{Name: "project_work"}}},
			{Name: "needs_reply", Type: engine.Noul},
		},
		Roles: map[taxonomy.Role]string{taxonomy.RoleTopic: "topic", taxonomy.RoleReply: "needs_reply"},
	}
	got := FromAnswers(p, map[string]engine.Answer{
		"topic":       {Type: engine.Choice, Choice: "project_work"},
		"needs_reply": {Type: engine.Noul, Value: 0.9},
	})
	if got.Basis != "reply" {
		t.Fatalf("renamed reply question was not read: %+v", got)
	}
}
