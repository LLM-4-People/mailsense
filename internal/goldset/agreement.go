package goldset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Agreement kinds of a gold label (per question and message).
const (
	Single      = "single"      // one labeller
	Agreed      = "agreed"      // two labellers gave the same answer
	Adjudicated = "adjudicated" // two labellers disagreed; an adjudicator decided
)

// Judge files the agreement is read from (the second labeller's batches,
// the disagreements per question): ids only, never an answer.
const (
	SecondGlob    = "batches/labels-B-*.jsonl"
	Disagreements = "batches/disagreements-AB.json"
)

// Agreement is how each train and dev label came about, per question and
// id. A directory without the judges' files gives none (an error says
// which file is unreadable).
func (s *Set) Agreement() (map[string]map[string]string, error) {
	second := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(s.Dir, SecondGlob))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := eachLine(f, func(n int, line []byte) error {
			id, err := lineID(line)
			if err != nil {
				return fmt.Errorf("%s:%d: %w", f, n, err)
			}
			second[id] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	disputed := map[string][]string{}
	if raw, err := os.ReadFile(filepath.Join(s.Dir, Disagreements)); err == nil {
		if err := json.Unmarshal(raw, &disputed); err != nil {
			return nil, fmt.Errorf("%s: %w", Disagreements, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	out := map[string]map[string]string{}
	for id, answers := range s.Labels {
		for q := range answers {
			if out[q] == nil {
				out[q] = map[string]string{}
			}
			kind := Single
			if second[id] {
				kind = Agreed
			}
			out[q][id] = kind
		}
	}
	for q, ids := range disputed {
		for _, id := range ids {
			if _, ok := out[q][id]; ok {
				out[q][id] = Adjudicated
			}
		}
	}
	return out, nil
}
