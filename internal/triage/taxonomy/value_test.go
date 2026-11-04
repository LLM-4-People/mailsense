package taxonomy

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// The triage wire carries only the answer the UI reads. Model, Prior and
// Probabilities stay in Go (Answer, priors, feedback, evaluation) but are
// never encoded; this table covers every answer shape the app produces.
func TestValueJSONOmitsDiagnostics(t *testing.T) {
	prior, confidence := 0.35, 0.8
	cases := []struct {
		name  string
		value Value
	}{
		{"choice", Value{Type: engine.Choice, Choice: "work", Confidence: &confidence, Model: 0.61,
			Prior: &prior, Probabilities: map[string]float64{"work": 0.8, "finance": 0.2}}},
		{"score", Value{Type: engine.Score, Number: 2.4, Confidence: &confidence, Model: 2.4,
			Prior: &prior, Probabilities: map[string]float64{"0": 0.1, "1": 0.2, "2": 0.3, "3": 0.4}}},
		{"noul", Value{Type: engine.Noul, Number: 0.72, Model: 0.72}},
		{"blended prior", Value{Type: engine.Noul, Number: 0.51, Model: 0.72, Prior: &prior}},
		{"abstained with probabilities", Value{Type: engine.Choice, Abstain: engine.AbstainBelowTarget,
			Probabilities: map[string]float64{"work": 0.55, "finance": 0.45}}},
		{"refused", Value{Type: engine.Noul, Refused: true}},
	}
	wire := map[string]bool{"type": true, "refused": true, "choice": true, "number": true, "confidence": true, "abstain": true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("not a JSON object: %v: %s", err, raw)
			}
			for key := range got {
				if !wire[key] {
					t.Fatalf("the triage wire carries %q: %s", key, raw)
				}
			}
			for _, key := range []string{"model", "prior", "probabilities"} {
				if _, ok := got[key]; ok {
					t.Fatalf("diagnostic %q leaked on the wire: %s", key, raw)
				}
			}
			// The UI fields survive byte for byte.
			var kind engine.QuestionType
			if err := json.Unmarshal(got["type"], &kind); err != nil || kind != tc.value.Type {
				t.Fatalf("type %s: %v", got["type"], err)
			}
			var number float64
			if err := json.Unmarshal(got["number"], &number); err != nil || number != tc.value.Number {
				t.Fatalf("number %s, want %v: %v", got["number"], tc.value.Number, err)
			}
			var choice string
			if raw, ok := got["choice"]; ok {
				if err := json.Unmarshal(raw, &choice); err != nil || choice != tc.value.Choice {
					t.Fatalf("choice %s, want %q: %v", raw, tc.value.Choice, err)
				}
			} else if tc.value.Choice != "" {
				t.Fatalf("choice %q was dropped", tc.value.Choice)
			}
			var conf *float64
			if raw, ok := got["confidence"]; ok {
				if err := json.Unmarshal(raw, &conf); err != nil || conf == nil || *conf != *tc.value.Confidence {
					t.Fatalf("confidence %s, want %v: %v", raw, *tc.value.Confidence, err)
				}
			} else if tc.value.Confidence != nil {
				t.Fatalf("confidence %v was dropped", *tc.value.Confidence)
			}
			var abstain string
			if raw, ok := got["abstain"]; ok {
				if err := json.Unmarshal(raw, &abstain); err != nil || abstain != tc.value.Abstain {
					t.Fatalf("abstain %s, want %q: %v", raw, tc.value.Abstain, err)
				}
			} else if tc.value.Abstain != "" {
				t.Fatalf("abstain %q was dropped", tc.value.Abstain)
			}
			var refused bool
			if raw, ok := got["refused"]; ok {
				if err := json.Unmarshal(raw, &refused); err != nil || refused != tc.value.Refused {
					t.Fatalf("refused %s, want %v: %v", raw, tc.value.Refused, err)
				}
			} else if tc.value.Refused {
				t.Fatal("refused was dropped")
			}

			// The Go fields are alive: Answer() is the inverse of
			// FromAnswer and still carries Model and Probabilities.
			answer := tc.value.Answer()
			if answer.Type != tc.value.Type || answer.Refused != tc.value.Refused || answer.Choice != tc.value.Choice ||
				answer.Value != tc.value.Model || !reflect.DeepEqual(answer.Confidence, tc.value.Confidence) ||
				!reflect.DeepEqual(answer.Probabilities, tc.value.Probabilities) || answer.Abstain != tc.value.Abstain {
				t.Fatalf("Answer() = %+v, value %+v", answer, tc.value)
			}
		})
	}
}

// The wire value's answer-state methods delegate to the owner
// (engine.Answer), so a consumer of a Value cannot rebuild the check
// differently. The route reason strings are its wire values.
func TestValueAnswerState(t *testing.T) {
	confidence := 0.9
	cases := []struct {
		name      string
		value     Value
		answered  bool
		uncertain bool
	}{
		{"missing type", Value{Model: 0.9}, false, false},
		{"answered noul", Value{Type: engine.Noul, Model: 0.9}, true, false},
		{"answered choice", Value{Type: engine.Choice, Choice: "work", Confidence: &confidence}, true, false},
		{"answered score", Value{Type: engine.Score, Model: 2, Confidence: &confidence}, true, false},
		{"refused", Value{Type: engine.Noul, Refused: true, Model: 0.9}, false, true},
		{"below target", Value{Type: engine.Noul, Abstain: engine.AbstainBelowTarget, Model: 0.9}, false, true},
		{"not certified", Value{Type: engine.Noul, Abstain: "not_certified", Model: 0.9}, false, false},
		{"below coverage", Value{Type: engine.Noul, Abstain: "below_coverage", Model: 0.9}, false, false},
		{"unknown abstention", Value{Type: engine.Noul, Abstain: "future_reason", Model: 0.9}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.value.Answered(); got != tc.answered {
				t.Fatalf("Answered() = %v, want %v (%+v)", got, tc.answered, tc.value)
			}
			if got := tc.value.Uncertain(); got != tc.uncertain {
				t.Fatalf("Uncertain() = %v, want %v (%+v)", got, tc.uncertain, tc.value)
			}
		})
	}
}
