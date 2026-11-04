package engine

// Answer is one typed answer, as the model gave it (raw: no prior blended).
type Answer struct {
	Type QuestionType `json:"type"`
	// Refused is true when the model declined the question (OpenAI
	// refusal); no value is present then.
	Refused bool `json:"refused,omitempty"`
	// Value is the noul probability or the score's expected level.
	Value float64 `json:"value,omitempty"`
	// Choice is the chosen option of a choice question.
	Choice string `json:"choice,omitempty"`
	// Probabilities: per option (choice) or per level key "0".."n-1"
	// (score); an abstained answer of any type carries its distribution
	// here only (yes/no: per NoulNo and NoulYes).
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Confidence of choice and score answers (noul answers carry none).
	Confidence *float64 `json:"confidence,omitempty"`
	// Abstain names why the answer must not be used on its own; "" when
	// it may. An abstained answer carries no Value, Choice or Confidence
	// — nothing a consumer could use by mistake — only its distribution
	// in Probabilities for diagnostics and the next layer
	// (internal/selective). Codes: AbstainBelowTarget, plus the route's
	// non-answer reasons (internal/route: not_certified, below_coverage,
	// no_answer).
	Abstain string `json:"abstain,omitempty"`
}

// The distribution keys of an abstained yes/no answer.
const (
	NoulNo  = "no"
	NoulYes = "yes"
)

// AbstainBelowTarget: the answer's confidence is below its question's
// certified operating threshold (the precision target is not reached).
const AbstainBelowTarget = "below_target"

// Answered reports a usable answer: a typed value that was neither
// refused nor abstained.
func (a Answer) Answered() bool { return a.Type != "" && !a.Refused && a.Abstain == "" }

// Uncertain reports the model's own uncertainty: a refusal or a
// below-target abstention. Any other abstention is a deliberate
// non-answer (the route did not certify the question, its coverage fell
// below the minimum, or no member answered), not something to escalate.
func (a Answer) Uncertain() bool { return a.Refused || a.Abstain == AbstainBelowTarget }
