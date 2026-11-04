package taxonomy

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Value is one judged answer of a message under a profile: the model's
// raw answer and the value after prior blending (blended at read time;
// the cache keeps raw answers only). The diagnostic fields (Model, Prior,
// Probabilities) stay in Go for evaluation, feedback and priors, but are
// never encoded on the triage wire: the UI reads only the answer itself.
type Value struct {
	Type    engine.QuestionType `json:"type"`
	Refused bool                `json:"refused,omitempty"`
	Choice  string              `json:"choice,omitempty"`
	Number  float64             `json:"number"` // model value of noul/score: P(yes) or expected level
	// Decided is the certified class of a noul or score answer: "yes"/"no"
	// for noul, the level key for score. selective.Decide owns it at the
	// profile cutoff; the UI renders it and never re-thresholds Number.
	Decided string `json:"decided,omitempty"`
	// Model is an internal diagnostic: the model's own value before the
	// blend (evaluation compares this), never on the triage wire.
	Model float64 `json:"-"`
	// Prior is an internal diagnostic: the learned prior blended into
	// Number, never on the triage wire.
	Prior *float64 `json:"-"`
	// Confidence is the answer's stated confidence (a UI field).
	Confidence *float64 `json:"confidence,omitempty"`
	// Probabilities is an internal diagnostic distribution: the stored form
	// of engine.Answer in route_answers/triage_decisions, never on the
	// triage wire.
	Probabilities map[string]float64 `json:"-"`
	Abstain       string             `json:"abstain,omitempty"` // the answer abstained (engine.Answer.Abstain): never used on its own
}

// FromAnswer is the unblended value of a raw answer.
func FromAnswer(a engine.Answer) Value {
	return Value{Type: a.Type, Refused: a.Refused, Choice: a.Choice, Number: a.Value, Model: a.Value,
		Confidence: a.Confidence, Probabilities: a.Probabilities, Abstain: a.Abstain}
}

// Answer is the model's own answer behind the value (FromAnswer's
// inverse: the blend is not part of it).
func (v Value) Answer() engine.Answer {
	return engine.Answer{Type: v.Type, Refused: v.Refused, Choice: v.Choice, Value: v.Model, Confidence: v.Confidence,
		Probabilities: v.Probabilities, Abstain: v.Abstain}
}

// Answered reports a usable value: a typed answer that was neither
// refused nor abstained (engine.Answer.Answered).
func (v Value) Answered() bool { return v.Answer().Answered() }

// Uncertain reports the model's own uncertainty (engine.Answer.Uncertain):
// a refusal or a below-target abstention. A route-level abstention is a
// deliberate non-answer, not an uncertainty.
func (v Value) Uncertain() bool { return v.Answer().Uncertain() }

// Label is a user's answer to one question (choice option or number).
type Label struct {
	Choice string  `json:"choice,omitempty"`
	Number float64 `json:"number"`
}

// JSON is the label's stored form (a string for choice, a number else).
func (l Label) JSON(t engine.QuestionType) string {
	var b []byte
	if t == engine.Choice {
		b, _ = json.Marshal(l.Choice)
	} else {
		b, _ = json.Marshal(l.Number)
	}
	return string(b)
}

// ParseLabel validates a label value for q (cli.py _parse_label_value):
// choice = one of the options; noul = true/false, yes/no, 1/0 or a number
// in [0, 1]; score = a number in [0, top]. A numeric value may be a JSON
// number or a string holding exactly one (see numberLabel). raw is a
// decoded JSON value.
func ParseLabel(q *engine.Question, raw any) (Label, error) {
	switch q.Type {
	case engine.Choice:
		s, ok := raw.(string)
		if !ok || !q.HasOption(s) {
			return Label{}, fmt.Errorf("%s: value must be one of %v, got %s", q.Name, optionNames(q), show(raw))
		}
		return Label{Choice: s}, nil
	case engine.Noul:
		if b, ok := raw.(bool); ok {
			return Label{Number: map[bool]float64{true: 1, false: 0}[b]}, nil
		}
		if s, ok := raw.(string); ok {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "yes", "1":
				return Label{Number: 1}, nil
			case "false", "no", "0":
				return Label{Number: 0}, nil
			}
		}
		return numberLabel(q, raw, 1)
	default:
		return numberLabel(q, raw, float64(q.Top()))
	}
}

func numberLabel(q *engine.Question, raw any, top float64) (Label, error) {
	f, ok := jsonx.Finite(raw)
	if !ok {
		f, ok = quotedNumber(raw)
	}
	if !ok || f < 0 || f > top {
		return Label{}, fmt.Errorf("%s: value must be a number in [0, %v], got %s", q.Name, top, show(raw))
	}
	return Label{Number: f}, nil
}

// quotedNumber reads the string form of a numeric label: the content must
// be exactly a JSON number literal after trimming whitespace (a model
// sometimes quotes an unambiguous score: "3", " 3.0 "). Words, units,
// extra punctuation, hex or underscored literals, NaN/Inf and values
// ParseFloat cannot represent stay rejected; range is checked by the caller.
// Only numeric question types reach it, so a choice option name is never a
// number.
func quotedNumber(raw any) (float64, bool) {
	s, ok := raw.(string)
	if !ok {
		return 0, false
	}
	s = strings.TrimSpace(s)
	if s == "" || (s[0] != '-' && (s[0] < '0' || s[0] > '9')) || !json.Valid([]byte(s)) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return jsonx.Finite(n)
}

// DecodeLabel reads a stored label (its JSON form) back.
func DecodeLabel(q *engine.Question, stored string) (Label, error) {
	v, err := jsonx.DecodeOne([]byte(stored))
	if err != nil {
		return Label{}, err
	}
	return ParseLabel(q, v)
}

func optionNames(q *engine.Question) []string {
	out := make([]string, len(q.Options))
	for i, o := range q.Options {
		out[i] = o.Name
	}
	return out
}
