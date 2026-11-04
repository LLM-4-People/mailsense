package feedback

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Every item rule lives in the labeler. A batch that mixes a valid item with
// any rejected item stores nothing, and the refusal is the one typed
// ValidationError the API maps to 422: no partial write, no bare error.
func TestPutBatchMixedValidAndInvalidWritesNothing(t *testing.T) {
	st, p, ids, _, lb := batchWorld(t, 2)
	valid := PutItem{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)}
	cases := []struct {
		name  string
		items []PutItem
	}{
		{"unknown question", []PutItem{valid, {MessageID: ids[1], Question: "not_a_question", Value: json.RawMessage(`1`)}}},
		{"invalid option", []PutItem{valid, {MessageID: ids[1], Question: "category", Value: json.RawMessage(`"not_an_option"`)}}},
		{"invalid JSON value", []PutItem{valid, {MessageID: ids[1], Question: "category", Value: json.RawMessage(`{bad`)}}},
		{"invisible message", []PutItem{valid, {MessageID: 999999, Question: "category", Value: json.RawMessage(`"work"`)}}},
		{"duplicate pair", []PutItem{valid, valid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lb.PutBatch(t.Context(), p, tc.items)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("want *ValidationError, got %T: %v", err, err)
			}
			if len(validation.Problems) == 0 {
				t.Fatal("a typed refusal carries no problem")
			}
			rows, err := st.ListLabels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("a rejected batch wrote %d labels", len(rows))
			}
		})
	}
	got, err := lb.PutBatch(t.Context(), p, []PutItem{valid})
	if err != nil || len(got.Stored) != 1 {
		t.Fatalf("the control batch %+v %v", got, err)
	}
}

// The batch's own bounds are the labeler's rule too, and their refusal is
// the same typed ValidationError rather than a bare error the API would turn
// into an internal one.
func TestPutBatchBoundsAreTyped(t *testing.T) {
	st, p, ids, _, lb := batchWorld(t, 1)
	oversized := make([]PutItem, MaxLabelBatch+1)
	for i := range oversized {
		oversized[i] = PutItem{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)}
	}
	cases := []struct {
		name  string
		items []PutItem
		want  string
	}{
		{"empty", nil, "cannot be empty"},
		{"oversized", oversized, "at most " + strconv.Itoa(MaxLabelBatch)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lb.PutBatch(t.Context(), p, tc.items)
			var validation *ValidationError
			if !errors.As(err, &validation) || len(validation.Problems) != 1 {
				t.Fatalf("want one typed problem, got %T: %v", err, err)
			}
			if problem := strings.Join(validation.Problems, " "); !strings.Contains(problem, tc.want) {
				t.Fatalf("the refusal does not name %q: %q", tc.want, problem)
			}
			rows, err := st.ListLabels(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("a refused batch wrote %d labels", len(rows))
			}
		})
	}
}
