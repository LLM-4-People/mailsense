package route

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// TestOfferValidationIsOneRule: the trust-boundary rule of an offer list (known
// layer, one per layer, a distribution summing to 1) is read by the item
// validator and by Decide. Both used to spell it, so a rule added to one could
// pass the other.
func TestOfferValidationIsOneRule(t *testing.T) {
	q := question(engine.Choice)
	classes := classCount(q)
	valid := peak(q, 0, 0.6)
	for _, tc := range []struct {
		name   string
		offers []Offer
		want   string
	}{
		{"unknown layer", []Offer{{Layer: "nope", Probs: valid}}, "unknown layer"},
		{"duplicate layer", []Offer{{Layer: LayerKey, Probs: valid}, {Layer: LayerKey, Probs: valid}}, "offered twice"},
		{"wrong count", []Offer{{Layer: LayerKey, Probs: []float64{1}}}, "probabilities"},
		{"NaN", []Offer{{Layer: LayerKey, Probs: []float64{math.NaN(), 0.5, 0.5}}}, "probability"},
		{"negative", []Offer{{Layer: LayerKey, Probs: []float64{-0.1, 0.55, 0.55}}}, "probability"},
		{"bad sum", []Offer{{Layer: LayerKey, Probs: []float64{0.2, 0.2, 0.2}}}, "sum"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkOffers(tc.offers, classes)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkOffers = %v, want %q", err, tc.want)
			}
			if itemErr := checkItem(&Item{Class: 0, Offers: tc.offers}, classes); itemErr == nil || itemErr.Error() != err.Error() {
				t.Fatalf("checkItem = %v, the shared rule answers %v", itemErr, err)
			}
			state := State{Question: q.Name, Type: q.Type}
			if _, err := state.Decide(q, tc.offers); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Decide = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}

	// A valid list passes all three.
	good := []Offer{{Layer: LayerKey, Probs: valid}}
	if err := checkOffers(good, classes); err != nil {
		t.Fatalf("checkOffers(valid) = %v", err)
	}
	if err := checkItem(&Item{Class: 0, Offers: good}, classes); err != nil {
		t.Fatalf("checkItem(valid) = %v", err)
	}
	if _, err := (State{Question: q.Name, Type: q.Type}).Decide(q, good); err != nil {
		t.Fatalf("Decide(valid) = %v", err)
	}
}
