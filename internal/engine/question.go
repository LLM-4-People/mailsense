package engine

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// QuestionType is the answer shape of a question.
type QuestionType string

// Question types (the systemone vocabulary; OpenAI's "predicate" is noul).
const (
	Noul   QuestionType = "noul"   // probability of yes
	Choice QuestionType = "choice" // one option of 2–255, with probabilities
	Score  QuestionType = "score"  // expected level on an ordered scale of 2–10
)

// Option is one choice option; Description may be absent (null).
type Option struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// Question is one typed question. Options keep their order (the order is
// sent to the model; some models favour the first option).
type Question struct {
	Name         string       `json:"name"`
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	// Noul criteria (optional): what true and false mean.
	True  string `json:"true,omitempty"`
	False string `json:"false,omitempty"`
	// Options of a choice question, in order.
	Options []Option `json:"options,omitempty"`
	// Levels of a score question, lowest first (index = level).
	Levels []string `json:"levels,omitempty"`
}

// Contract limits of the question types.
const (
	MinOptions = 2
	MaxOptions = 255
	MinLevels  = 2
	MaxLevels  = 10
)

// Validate checks a question against the shared contract.
func (q *Question) Validate() error {
	var errs []error
	if q.Name == "" {
		errs = append(errs, errors.New("name is empty"))
	}
	if mail.Strip(q.Instructions) == "" {
		errs = append(errs, errors.New("instructions must be a non-empty string"))
	}
	switch q.Type {
	case Noul:
	case Choice:
		if n := len(q.Options); n < MinOptions || n > MaxOptions {
			errs = append(errs, fmt.Errorf("choice needs %d-%d options, has %d", MinOptions, MaxOptions, n))
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			if o.Name == "" || seen[o.Name] {
				errs = append(errs, fmt.Errorf("option names must be unique and non-empty (%q)", o.Name))
			}
			seen[o.Name] = true
		}
	case Score:
		if n := len(q.Levels); n < MinLevels || n > MaxLevels {
			errs = append(errs, fmt.Errorf("score needs %d-%d levels, has %d", MinLevels, MaxLevels, n))
		}
		if slices.Contains(q.Levels, "") {
			errs = append(errs, errors.New("score level labels must be non-empty strings"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown type %q", q.Type))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("question %q: %w", q.Name, err)
	}
	return nil
}

// HasOption reports whether name is one of the choice options.
func (q *Question) HasOption(name string) bool {
	for _, o := range q.Options {
		if o.Name == name {
			return true
		}
	}
	return false
}

// Top is the highest level of a score question.
func (q *Question) Top() int { return len(q.Levels) - 1 }

// LevelKey is the probability key of level i ("0".."n-1").
func LevelKey(i int) string { return strconv.Itoa(i) }
