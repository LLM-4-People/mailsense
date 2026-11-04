package judge

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// MaxAnswerBytes bounds a model's answer before it is decoded: the
// question set is small, so anything larger is a broken or abusive reply,
// not a label.
const MaxAnswerBytes = 64 << 10

// answerDoc is the only accepted answer shape: one object with one
// "answers" array, whose entries carry exactly "name" and "value"
// (DecodeStrict refuses unknown fields at every level).
type answerDoc struct {
	Answers []answer `json:"answers"`
}

type answer struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// parseAnswer validates a model answer against the requested questions in
// their canonical (profile) order. It returns one entry per question
// answered validly (nil = the model refused) and joins a contract error
// for every question that failed; a structurally broken document (not
// JSON, an unknown field, an unknown or duplicate question) returns no
// labels at all, because attribution is unsound.
func parseAnswer(content string, qs []engine.Question) (map[string]*taxonomy.Label, error) {
	if len(content) > MaxAnswerBytes {
		return nil, fmt.Errorf("%w: %d bytes, more than %d", ErrContract, len(content), MaxAnswerBytes)
	}
	var doc answerDoc
	if err := jsonx.DecodeStrict([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContract, err)
	}
	byName := make(map[string]*engine.Question, len(qs))
	for i := range qs {
		byName[qs[i].Name] = &qs[i]
	}
	labels := make(map[string]*taxonomy.Label, len(qs))
	seen := make(map[string]bool, len(qs))
	var errs []error
	for i, a := range doc.Answers {
		switch {
		case a.Name == "":
			return nil, fmt.Errorf("%w: answer %d has no name", ErrContract, i+1)
		case byName[a.Name] == nil:
			return nil, fmt.Errorf("%w: answer %d names %q, which was not asked", ErrContract, i+1, a.Name)
		case seen[a.Name]:
			return nil, fmt.Errorf("%w: question %q is answered twice", ErrContract, a.Name)
		}
		seen[a.Name] = true
		q := byName[a.Name]
		if a.Value == nil {
			errs = append(errs, fmt.Errorf("%w: %s: no value", ErrContract, q.Name))
			continue
		}
		raw, err := jsonx.DecodeOne(a.Value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: %s: %v", ErrContract, q.Name, err))
			continue
		}
		if raw == nil {
			labels[q.Name] = nil // refused: no label, never a default
			continue
		}
		label, err := taxonomy.ParseLabel(q, raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: %s", ErrContract, err))
			continue
		}
		labels[q.Name] = &label
	}
	for _, q := range qs {
		if !seen[q.Name] {
			errs = append(errs, fmt.Errorf("%w: %s: no answer", ErrContract, q.Name))
		}
	}
	if len(errs) > 0 {
		return labels, errors.Join(errs...)
	}
	return labels, nil
}
