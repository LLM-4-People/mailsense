package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// engine.Answer is the persistence format of triage_decisions and
// route_answers: trimming a field from the triage wire must never touch
// its JSON tags. A choice distribution must survive the round trip.
func TestChoiceAnswerJSONKeepsProbabilities(t *testing.T) {
	confidence := 0.82
	cases := []struct {
		name   string
		answer Answer
	}{
		{"answered", Answer{Type: Choice, Choice: "work", Confidence: &confidence,
			Probabilities: map[string]float64{"work": 0.82, "finance": 0.18}}},
		{"abstained with probabilities", Answer{Type: Choice, Abstain: AbstainBelowTarget,
			Probabilities: map[string]float64{"work": 0.55, "finance": 0.45}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.answer)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"probabilities"`) {
				t.Fatalf("the stored answer dropped its distribution: %s", raw)
			}
			var got Answer
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.answer) {
				t.Fatalf("round trip:\n got %+v\nwant %+v", got, tc.answer)
			}
		})
	}
}

// Answered and Uncertain are the one answer-state predicate every
// consumer shares. A route-level abstention is a deliberate non-answer:
// its reason names why the route did not answer, so it is unusable but
// never an uncertainty. The reason strings are route's wire values; engine
// names them only here, without importing the route package.
func TestAnswerState(t *testing.T) {
	confidence := 0.9
	cases := []struct {
		name      string
		answer    Answer
		answered  bool
		uncertain bool
	}{
		{"missing type", Answer{Value: 0.9}, false, false},
		{"answered noul", Answer{Type: Noul, Value: 0.9}, true, false},
		{"answered choice", Answer{Type: Choice, Choice: "work", Confidence: &confidence}, true, false},
		{"answered score", Answer{Type: Score, Value: 2, Confidence: &confidence}, true, false},
		{"refused", Answer{Type: Noul, Refused: true, Value: 0.9}, false, true},
		{"below target", Answer{Type: Noul, Abstain: AbstainBelowTarget, Value: 0.9}, false, true},
		{"not certified", Answer{Type: Noul, Abstain: "not_certified", Value: 0.9}, false, false},
		{"below coverage", Answer{Type: Noul, Abstain: "below_coverage", Value: 0.9}, false, false},
		{"unknown abstention", Answer{Type: Noul, Abstain: "future_reason", Value: 0.9}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.answer.Answered(); got != tc.answered {
				t.Fatalf("Answered() = %v, want %v (%+v)", got, tc.answered, tc.answer)
			}
			if got := tc.answer.Uncertain(); got != tc.uncertain {
				t.Fatalf("Uncertain() = %v, want %v (%+v)", got, tc.uncertain, tc.answer)
			}
		})
	}
}
