package config

import (
	"errors"
	"fmt"
	"strings"
)

// Problem is one validation failure, naming the setting and where the value came from.
type Problem struct {
	Key     string `json:"key"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	return fmt.Sprintf("%s (%s): %s", p.Key, p.Source, p.Message)
}

// Problems collects every failure of one validation pass.
type Problems []Problem

func (ps *Problems) add(key, source, msg string) {
	*ps = append(*ps, Problem{Key: key, Source: source, Message: msg})
}

func (ps Problems) err() error {
	if len(ps) == 0 {
		return nil
	}
	return &ValidationError{Problems: ps}
}

// ValidationError reports all problems found at once.
type ValidationError struct{ Problems Problems }

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return fmt.Sprintf("config: %d problem(s): %s", len(lines), strings.Join(lines, "; "))
}

// ProblemsOf extracts the problems from an error returned by this package.
func ProblemsOf(err error) Problems {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return nil
}
