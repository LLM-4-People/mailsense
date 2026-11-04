package search

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/triage"
)

// TriageFilter keeps messages whose triage answer to a question matches:
// a choice among Choices, or a yes/no or score value within [Min, Max].
// A message without a usable answer (not triaged, refused, abstained)
// never matches: an abstention is not a zero.
type TriageFilter struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}

// TriageReader reads the triage of several messages (triage.Triager).
type TriageReader interface {
	Results(ctx context.Context, rows []int64) (map[int64]triage.Result, error)
}

// ErrNoTriage is returned for triage filters when no triage is wired.
var ErrNoTriage = errors.New("search: triage filters need the triage results")

func (f *TriageFilter) validate() error {
	if f.Question == "" || (len(f.Choices) == 0 && f.Min == nil && f.Max == nil) {
		return fmt.Errorf("search: a triage filter needs a question and choices or a min/max (%+v)", *f)
	}
	return nil
}

func (f *TriageFilter) matches(r triage.Result) bool {
	v, ok := r.Values[f.Question]
	switch {
	case !ok || !v.Answered():
		return false
	case len(f.Choices) > 0:
		return slices.Contains(f.Choices, v.Choice)
	}
	return (f.Min == nil || v.Number >= *f.Min) && (f.Max == nil || v.Number <= *f.Max)
}

// keepTriaged drops the candidates whose triage fails any filter.
func keepTriaged(ctx context.Context, tr TriageReader, filters []TriageFilter, cands []candidate) ([]candidate, error) {
	if len(filters) == 0 {
		return cands, nil
	}
	if tr == nil {
		return nil, ErrNoTriage
	}
	ids := make([]int64, len(cands))
	for i, c := range cands {
		ids[i] = c.id
	}
	results, err := tr.Results(ctx, ids)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(cands, func(c candidate) bool {
		r, ok := results[c.id]
		for i := range filters {
			if !ok || !filters[i].matches(r) {
				return true
			}
		}
		return false
	}), nil
}
