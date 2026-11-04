// Package subject is the canonical subject hierarchy of Mailsense: the
// classifier's flat topic plus one evidence-backed subtopic derived from the
// other answers the same message already carries. It is pure and owns no
// storage, model call or display copy.
//
// The topic is the model's single choice. The subtopic is not a second guess
// at the same question: it is the dominant ask (asks_for), the verdict
// request (decision), the message's stance, or the mail's category applied
// inside that topic. Every result names the evidence it used, so a surface can
// explain the path instead of printing a label nobody can justify.
//
// A topic outside the built-in catalogue (a user profile can add one) keeps
// its model label and has no subtopic. The package never invents a parent or
// a child for a label it does not know.
package subject

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Subject is one message's place in the hierarchy. Topic, Subtopic and Path
// are stable identifiers, never display copy. Path is "topic/subtopic", or
// just the topic when no subtopic applies. Basis names the one answer that
// selected the subtopic ("ask:payment_money", "decision", "category:finance",
// "reply", "default").
type Subject struct {
	Topic    string `json:"topic,omitempty"`
	Subtopic string `json:"subtopic,omitempty"`
	Path     string `json:"path,omitempty"`
	Basis    string `json:"basis,omitempty"`
}

// Empty reports whether the classifier produced no subject at all.
func (s Subject) Empty() bool { return s.Topic == "" }

// Input is the classified evidence a subject is derived from. The booleans
// are the thresholded yes/no answers; the strings are the chosen options.
type Input struct {
	Category     string
	Topic        string
	AsksFor      string
	Stance       string
	Relationship string
	Decision     bool
	Reply        bool
	CTA          bool
}

// topicSpec is the one table of subtopic rules for a topic. The first
// matching rule wins in this order: ByAsk, Decision, ByCategory, Reply,
// Default. A rule that is empty is skipped.
type topicSpec struct {
	Default    string
	ByAsk      map[string]string
	Decision   string
	ByCategory map[string]string
	Reply      string
}

// suboptions is the closed set of subtopics each topic may produce. It is the
// validation oracle for topicSpec and the catalogue surfaces use to render a
// complete hierarchy.
var suboptions = map[string][]string{
	"project_work":          {"status_update", "deliverable", "coordination", "approval", "execution"},
	"support_issue":         {"incident", "troubleshooting", "fix", "escalation"},
	"sales_inquiry":         {"lead", "quote_pricing", "proposal", "demo", "terms"},
	"billing_account":       {"invoice", "payment", "approval", "account_change", "contract"},
	"meeting_scheduling":    {"invitation", "availability", "confirmation", "reschedule"},
	"technical_integration": {"setup", "access_credentials", "troubleshooting", "configuration", "approval"},
	"hiring":                {"application", "interview", "assessment", "offer"},
	"partnership":           {"introduction", "meeting", "proposal", "commercial_terms", "agreement"},
	"internal_ops":          {"policy", "approval", "admin_task", "coordination"},
	"news_updates":          {"newsletter", "product_update", "announcement", "digest"},
	"other":                 {"question", "request", "information", "general"},
}

// specs is the one rule table. Every subtopic it names is validated against
// suboptions by the package test.
var specs = map[string]topicSpec{
	"project_work": {
		Default:  "status_update",
		Decision: "approval",
		ByAsk: map[string]string{
			"answer_question":      "status_update",
			"approval_decision":    "approval",
			"information_document": "deliverable",
			"meeting_availability": "coordination",
			"task_work":            "execution",
		},
		Reply: "status_update",
	},
	"support_issue": {
		Default:  "incident",
		Decision: "troubleshooting",
		ByAsk: map[string]string{
			"answer_question":      "troubleshooting",
			"task_work":            "fix",
			"meeting_availability": "troubleshooting",
			"approval_decision":    "escalation",
		},
		Reply: "troubleshooting",
	},
	"sales_inquiry": {
		Default:  "lead",
		Decision: "terms",
		ByAsk: map[string]string{
			"payment_money":        "quote_pricing",
			"information_document": "proposal",
			"meeting_availability": "demo",
			"approval_decision":    "terms",
			"answer_question":      "lead",
		},
	},
	"billing_account": {
		Default:  "contract",
		Decision: "approval",
		ByAsk: map[string]string{
			"payment_money":        "payment",
			"information_document": "invoice",
			"task_work":            "account_change",
			"approval_decision":    "approval",
		},
		ByCategory: map[string]string{"finance": "invoice"},
	},
	"meeting_scheduling": {
		Default:  "invitation",
		Decision: "confirmation",
		ByAsk: map[string]string{
			"meeting_availability": "availability",
			"approval_decision":    "confirmation",
		},
	},
	"technical_integration": {
		Default:  "configuration",
		Decision: "approval",
		ByAsk: map[string]string{
			"information_document": "access_credentials",
			"task_work":            "setup",
			"answer_question":      "troubleshooting",
			"approval_decision":    "approval",
		},
	},
	"hiring": {
		Default:  "application",
		Decision: "offer",
		ByAsk: map[string]string{
			"meeting_availability": "interview",
			"information_document": "application",
			"task_work":            "assessment",
			"approval_decision":    "offer",
		},
	},
	"partnership": {
		Default:  "introduction",
		Decision: "agreement",
		ByAsk: map[string]string{
			"payment_money":        "commercial_terms",
			"information_document": "proposal",
			"meeting_availability": "meeting",
			"approval_decision":    "agreement",
		},
	},
	"internal_ops": {
		Default:  "admin_task",
		Decision: "approval",
		ByAsk: map[string]string{
			"approval_decision":    "approval",
			"task_work":            "admin_task",
			"information_document": "policy",
			"meeting_availability": "coordination",
		},
	},
	"news_updates": {
		Default: "announcement",
		ByAsk: map[string]string{
			"information_document": "digest",
			"answer_question":      "product_update",
			"nothing_fyi":          "newsletter",
		},
	},
	"other": {
		Default: "general",
		ByAsk: map[string]string{
			"answer_question":      "question",
			"task_work":            "request",
			"information_document": "information",
		},
	},
}

// Classify derives the subject of one message. A blank topic returns the zero
// Subject: an unclassified message has no honest parent.
func Classify(in Input) Subject {
	topic := normalize(in.Topic)
	if topic == "" {
		return Subject{}
	}
	out := Subject{Topic: topic, Path: topic}
	spec, known := specs[topic]
	if !known {
		return out
	}
	if sub, basis := choose(spec, in); sub != "" {
		out.Subtopic, out.Path, out.Basis = sub, topic+"/"+sub, basis
	}
	return out
}

// FromAnswers derives the subject from the stored route answers under a
// profile. It is the one adapter every surface uses, so the detail screen and
// the ranking cannot disagree about a message: both read the same answers with
// the profile's own yes/no cutoff. An abstained, refused, missing or
// type-mismatched answer is absent, never a zero-valued yes.
func FromAnswers(p *taxonomy.Profile, answers map[string]engine.Answer) Subject {
	if p == nil {
		return Subject{}
	}
	name := func(role taxonomy.Role) string { return p.QuestionName(role) }
	choice := func(name string) string {
		q, ok := p.Question(name)
		if !ok || q.Type != engine.Choice {
			return ""
		}
		a, ok := thanAnswer(answers, name)
		if !ok || a.Type != engine.Choice || !q.HasOption(a.Choice) {
			return ""
		}
		return a.Choice
	}
	yes := func(name string) bool {
		q, ok := p.Question(name)
		if !ok || q.Type != engine.Noul {
			return false
		}
		a, ok := thanAnswer(answers, name)
		if !ok || a.Type != engine.Noul {
			return false
		}
		return a.Value >= p.Cutoff(name)
	}
	return Classify(Input{
		Category: choice(name(taxonomy.RoleCategory)), Topic: choice(name(taxonomy.RoleTopic)),
		AsksFor: choice(name(taxonomy.RoleAsksFor)), Stance: choice(name(taxonomy.RoleStance)),
		Relationship: choice(name(taxonomy.RoleRelationship)),
		Decision:     yes(name(taxonomy.RoleDecision)), Reply: yes(name(taxonomy.RoleReply)),
		CTA: yes(name(taxonomy.RoleCTA)),
	})
}

// thanAnswer returns a usable answer: a typed answer that was neither
// refused nor abstained.
func thanAnswer(answers map[string]engine.Answer, name string) (engine.Answer, bool) {
	a, ok := answers[name]
	if !ok || !a.Answered() {
		return engine.Answer{}, false
	}
	return a, true
}

// choose applies one topic's rule order and returns the subtopic and the
// evidence code. It never returns a subtopic outside suboptions.
func choose(spec topicSpec, in Input) (string, string) {
	ask := normalize(in.AsksFor)
	if ask != "" {
		if sub := spec.ByAsk[ask]; sub != "" {
			return sub, "ask:" + ask
		}
	}
	if in.Decision && spec.Decision != "" {
		return spec.Decision, "decision"
	}
	category := normalize(in.Category)
	if category != "" {
		if sub := spec.ByCategory[category]; sub != "" {
			return sub, "category:" + category
		}
	}
	if in.Reply && spec.Reply != "" {
		return spec.Reply, "reply"
	}
	return spec.Default, "default"
}

// Topics returns every catalogue topic in display order. The order is stable
// so a surface can render a complete hierarchy without sorting map keys.
func Topics() []string {
	return []string{
		"project_work", "support_issue", "sales_inquiry", "billing_account",
		"meeting_scheduling", "technical_integration", "hiring", "partnership",
		"internal_ops", "news_updates", "other",
	}
}

// Subtopics returns the closed subtopic set of one topic, in display order.
// An unknown topic returns nil.
func Subtopics(topic string) []string {
	topic = normalize(topic)
	out := suboptions[topic]
	return append([]string(nil), out...)
}

// HasSubtopic reports whether topic/subtopic is a catalogue path.
func HasSubtopic(topic, subtopic string) bool {
	topic, subtopic = normalize(topic), normalize(subtopic)
	for _, candidate := range suboptions[topic] {
		if candidate == subtopic {
			return true
		}
	}
	return false
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
