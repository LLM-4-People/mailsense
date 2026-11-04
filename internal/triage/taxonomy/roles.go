// Package taxonomy is the triage profile: the decision questions (the
// embedded default_profile.json is the committed source of truth),
// Mailsense's additions
// (mailsense_profile.json: escalation, insight vocabulary, extra
// thresholds), the roles questions play, thresholds, prior blending, how
// states are composed, user overrides merged per key, strict all-errors
// validation (the porting record) and the question-spec fingerprint caches key on.
package taxonomy

import "github.com/LLM-4-People/mailsense/internal/engine"

// Role is what a question means to the app (reports, priors, insights).
type Role string

// Roles.
const (
	RoleCategory     Role = "category"
	RoleTopic        Role = "topic"
	RoleReply        Role = "reply"
	RoleUrgency      Role = "urgency"
	RoleSuspicious   Role = "suspicious"
	RoleQuestion     Role = "question"
	RoleCTA          Role = "cta"
	RoleDecision     Role = "decision"
	RoleAsksFor      Role = "asks_for"
	RoleDeadline     Role = "deadline"
	RoleRelationship Role = "relationship"
	RoleEffort       Role = "effort"
	RoleSentiment    Role = "sentiment"
	RoleStance       Role = "stance"
	RoleInfo         Role = "info"
	RoleRequests     Role = "requests"
	RoleSignature    Role = "signature"
)

// roleTypes is the question type each role requires (taxonomy.py:405-423,
// profile.py:26-44).
var roleTypes = map[Role]engine.QuestionType{
	RoleCategory: engine.Choice, RoleTopic: engine.Choice, RoleReply: engine.Noul, RoleUrgency: engine.Score,
	RoleSuspicious: engine.Noul, RoleQuestion: engine.Noul, RoleCTA: engine.Noul, RoleDecision: engine.Noul,
	RoleAsksFor: engine.Choice, RoleDeadline: engine.Score, RoleRelationship: engine.Choice, RoleEffort: engine.Score,
	RoleSentiment: engine.Score, RoleStance: engine.Choice, RoleInfo: engine.Noul, RoleRequests: engine.Score,
	RoleSignature: engine.Noul,
}

// BlendRoles are the roles whose answers blend with a learned prior.
var BlendRoles = []Role{RoleReply, RoleUrgency, RoleSuspicious}

// Threshold is a threshold key of the profile.
type Threshold string

// Threshold keys.
const (
	ThresholdReply             Threshold = "reply"
	ThresholdSuspicious        Threshold = "suspicious"
	ThresholdLowConfidence     Threshold = "low_confidence"
	ThresholdQuestion          Threshold = "question"
	ThresholdCTA               Threshold = "cta"
	ThresholdDecision          Threshold = "decision"
	ThresholdInfoGap           Threshold = "info_gap"
	ThresholdNoulDefault       Threshold = "noul_default"
	ThresholdUrgentFromLevel   Threshold = "urgent_from_level"
	ThresholdDeadlineSoon      Threshold = "deadline_soon_level"
	ThresholdEffortHeavy       Threshold = "effort_heavy_level"
	ThresholdMultiRequest      Threshold = "multi_request"
	ThresholdSentimentNegative Threshold = "sentiment_negative_max"
	ThresholdSentimentPositive Threshold = "sentiment_positive_min"
)

// Threshold kinds: probabilities, level indexes into a role's scale, and
// values on a role's scale.
var (
	unitThresholds  = []Threshold{ThresholdReply, ThresholdSuspicious, ThresholdLowConfidence, ThresholdQuestion, ThresholdCTA, ThresholdDecision, ThresholdInfoGap, ThresholdNoulDefault}
	levelThresholds = map[Threshold]Role{ThresholdUrgentFromLevel: RoleUrgency, ThresholdDeadlineSoon: RoleDeadline,
		ThresholdEffortHeavy: RoleEffort, ThresholdMultiRequest: RoleRequests}
	rangeThresholds = map[Threshold]Role{ThresholdSentimentNegative: RoleSentiment, ThresholdSentimentPositive: RoleSentiment}
)

// RoleCutoffs is the one role → yes/no threshold table (every noul role
// with its own cutoff; other noul questions use noul_default).
var RoleCutoffs = []struct {
	Role Role
	Key  Threshold
}{{RoleReply, ThresholdReply}, {RoleSuspicious, ThresholdSuspicious}, {RoleQuestion, ThresholdQuestion},
	{RoleCTA, ThresholdCTA}, {RoleDecision, ThresholdDecision}, {RoleInfo, ThresholdInfoGap}}

// TemplateFields are the placeholders a state template may use.
var TemplateFields = []string{"sender", "sender_name", "sender_address", "received_at", "subject", "body_preview",
	"folder", "to_addresses", "cc_addresses", "observed_facts", "history_context", "examples"}

// Document limits (legacy profile.py / config.py).
const (
	Version          = 1
	MaxBodyChars     = 20000
	MaxQuestionName  = 100
	topLevelDocument = "_doc"
)

// mergedSections merge per key over the built-ins; every other section
// replaces wholesale (profile.py:267-286, plus Mailsense's sections).
var mergedSections = []string{"questions", "roles", "thresholds", "prior_blend", "context", "escalation", "insights"}

// knownKeys are the top-level keys of a profile document.
var knownKeys = []string{"version", "name", topLevelDocument, "body_chars", "state_template", "questions", "roles",
	"thresholds", "prior_blend", "prior_min_samples", "context", "escalation", "insights"}

var (
	contextFlags = []string{"include_history", "include_examples", "include_facts"}
	contextKeys  = append(append([]string(nil), contextFlags...), "max_examples")
	insightKeys  = []string{"suspicious_categories", "known_relationships"}
)
