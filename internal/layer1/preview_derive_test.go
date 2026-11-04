package layer1

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// The commit derives a message's threshold-free preview from its stored
// decision. That is exact only when the decision's answer round-trips through
// JSON and selective.Probs returns the same distribution the inference pass
// had: this pins the identity for every head kind, answered and abstained,
// including a yes/no answer (whose stored form is the probability of "yes",
// not the two-value distribution).
func TestPreviewDerivesExactlyFromTheDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		q         *engine.Question
		probs     []float64
		cutoff    float64
		threshold float64
	}{
		{"choice answered", q(engine.Choice), []float64{0.1, 0.7, 0.2}, 0.5, 0.5},
		{"choice abstained", q(engine.Choice), []float64{0.1, 0.5, 0.4}, 0.5, 0.99},
		{"noul answered", q(engine.Noul), []float64{0.3, 0.7}, 0.5, 0.5},
		{"noul abstained", q(engine.Noul), []float64{0.3, 0.7}, 0.5, 0.99},
		{"score answered", q(engine.Score), []float64{0.1, 0.2, 0.3, 0.4}, 0.5, 0.1},
		{"score abstained", q(engine.Score), []float64{0.1, 0.2, 0.3, 0.4}, 0.5, 0.99},
		{"unnormalised", q(engine.Choice), []float64{0.05, 0.45, 0.2}, 0.5, 0.1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decided := selective.Answer(tc.q, tc.probs, tc.cutoff, tc.threshold)
			raw, err := json.Marshal(map[string]engine.Answer{tc.q.Name: decided})
			if err != nil {
				t.Fatal(err)
			}
			var stored map[string]engine.Answer
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(selective.Given(tc.q, tc.probs, tc.cutoff))
			if err != nil {
				t.Fatal(err)
			}
			probs, ok := selective.Probs(tc.q, stored[tc.q.Name])
			if !ok {
				t.Fatalf("the stored decision holds no distribution: %s", raw)
			}
			got, err := json.Marshal(selective.Given(tc.q, probs, tc.cutoff))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("preview from the decision = %s, want %s", got, want)
			}
			// The route's layer-1 offer reads the same distribution: it must
			// see the same bits after the decision's JSON round-trip.
			before, _ := selective.Probs(tc.q, decided)
			for i := range probs {
				if probs[i] != before[i] {
					t.Fatalf("probability %d changed: %v → %v", i, before[i], probs[i])
				}
			}
		})
	}
}
