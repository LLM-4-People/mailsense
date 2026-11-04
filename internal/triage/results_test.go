package triage

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// encodeAnswers is the persistence boundary of the paid triage cache: a
// choice distribution must survive both a real run and a direct store round
// trip, so trimming the triage wire can never trim triage_decisions.
func TestTriageDecisionCacheKeepsChoiceProbabilities(t *testing.T) {
	w := newWorld(t, nil)
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	rep := w.run(Options{})
	if rep.Plan == nil || rep.Decided != 1 {
		t.Fatalf("run: %+v", rep)
	}

	stored, err := w.st.LoadDecisions(t.Context(), rep.Plan.Key)
	if err != nil {
		t.Fatal(err)
	}
	decision, ok := stored[id]
	if !ok || decision.Answers == "" {
		t.Fatalf("the run stored no decision for %d: %+v", id, stored)
	}
	if !strings.Contains(decision.Answers, `"probabilities"`) {
		t.Fatalf("the run's decision dropped its distribution: %s", decision.Answers)
	}
	var ran map[string]engine.Answer
	if err := json.Unmarshal([]byte(decision.Answers), &ran); err != nil {
		t.Fatal(err)
	}
	category, ok := ran["category"]
	if !ok || category.Type != engine.Choice || category.Choice == "" || len(category.Probabilities) == 0 {
		t.Fatalf("the run's stored choice lost its distribution: %+v", ran)
	}

	// The encoder with a full synthetic distribution, through the store's
	// decision writer and reader: a future trim of encodeAnswers fails here.
	confidence := 0.82
	full := engine.Answer{Type: engine.Choice, Choice: "work", Confidence: &confidence,
		Probabilities: map[string]float64{"work": 0.62, "finance": 0.38}}
	encoded, err := encodeAnswers(map[string]engine.Answer{"category": full})
	if err != nil {
		t.Fatal(err)
	}
	key := store.TriageKey{Fingerprint: "round21-persistence", Decider: "synthetic"}
	if err := w.st.SaveDecisions(t.Context(), key, []store.Decision{{MessageID: id, Revision: w.revision(t, id), Answers: encoded}}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := w.st.LoadDecisions(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]engine.Answer
	if err := json.Unmarshal([]byte(reloaded[id].Answers), &got); err != nil {
		t.Fatalf("the stored decision is not an answer map: %v: %s", err, reloaded[id].Answers)
	}
	if !reflect.DeepEqual(got, map[string]engine.Answer{"category": full}) {
		t.Fatalf("decision cache round trip:\n got %+v\nwant %+v", got, map[string]engine.Answer{"category": full})
	}
}
