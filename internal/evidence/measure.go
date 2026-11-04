package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// answer is a rule's answer: an option, a truth value or a level range.
type answer struct {
	option   string
	truth    *bool
	min, max float64
	isRange  bool
}

func parseAnswer(raw json.RawMessage) (answer, error) {
	var a answer
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return a, errors.New("needs an answer")
	case raw[0] == '"':
		return a, json.Unmarshal(raw, &a.option)
	case raw[0] == '{':
		var r struct {
			Min *float64 `json:"min"`
			Max *float64 `json:"max"`
		}
		if err := json.Unmarshal(raw, &r); err != nil || r.Min == nil || r.Max == nil || *r.Min > *r.Max {
			return a, fmt.Errorf("a level answer is {\"min\": a, \"max\": b} with a ≤ b: %s", raw)
		}
		a.min, a.max, a.isRange = *r.Min, *r.Max, true
		return a, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return a, fmt.Errorf("an answer is an option, true/false or a level range: %s", raw)
	}
	a.truth = &b
	return a, nil
}

// fits checks the answer against its question: an option of a choice, a
// truth value of a yes/no question, whole levels within a score's scale.
func (a answer) fits(q *engine.Question) error {
	switch {
	case a.option != "":
		if q.Type != engine.Choice || !slices.ContainsFunc(q.Options, func(o engine.Option) bool { return o.Name == a.option }) {
			return fmt.Errorf("answer %q is not an option of %s (%s)", a.option, q.Name, q.Type)
		}
	case a.truth != nil:
		if q.Type != engine.Noul {
			return fmt.Errorf("a true/false answer needs a yes/no question; %s is %s", q.Name, q.Type)
		}
	case a.isRange:
		if q.Type != engine.Score || a.min < 0 || a.max > float64(len(q.Levels)-1) || a.min != math.Trunc(a.min) || a.max != math.Trunc(a.max) {
			return fmt.Errorf("levels %v-%v are not whole levels of %s (%s, 0-%d)", a.min, a.max, q.Name, q.Type, len(q.Levels)-1)
		}
	default:
		return errors.New("no answer")
	}
	return nil
}

// matches reports whether a label value (an option name, true/false or
// 0/1, a level) agrees with the answer; ok is false for a label of
// another kind (it does not count).
func (a answer) matches(label json.RawMessage) (match, ok bool) {
	var v any
	if json.Unmarshal(label, &v) != nil {
		return false, false
	}
	switch x := v.(type) {
	case string:
		return x == a.option, a.option != ""
	case bool:
		return a.truth != nil && x == *a.truth, a.truth != nil
	case float64:
		switch {
		case a.isRange:
			return x >= a.min && x <= a.max, true
		case a.truth != nil && (x == 0 || x == 1):
			return (x == 1) == *a.truth, true
		}
	}
	return false, false
}

// RuleStat is one rule's measurement against labels.
type RuleStat struct {
	ID        string  `json:"id"`
	Question  string  `json:"question"`
	Answer    string  `json:"answer"`
	Labelled  int     `json:"labelled"` // messages with a label for the question (and a record)
	Fired     int     `json:"fired"`    // of them, where the rule fires
	Correct   int     `json:"correct"`  // of those, where the label agrees
	Precision float64 `json:"precision"`
	Coverage  float64 `json:"coverage"` // fired / labelled
	WilsonLow float64 `json:"wilson_low"`
	WilsonHi  float64 `json:"wilson_high"`
	Key       bool    `json:"key"` // the lower bound reaches the minimum: the rule may answer
}

// Measure measures every rule on labelled messages: labels are message →
// question → value, records the evidence of those messages. A rule is a
// key when it fired and the Wilson lower bound of its precision at z is
// at least minLow.
func (s *RuleSet) Measure(labels map[int64]map[string]json.RawMessage, records map[int64]Record, z, minLow float64) []RuleStat {
	var out []RuleStat
	for i := range s.Rules {
		r := &s.Rules[i]
		st := RuleStat{ID: r.ID, Question: r.Question, Answer: string(r.Answer)}
		for id, qs := range labels {
			label, ok := qs[r.Question]
			rec, has := records[id]
			if !ok || !has {
				continue
			}
			match, kind := r.answer.matches(label)
			if !kind {
				continue
			}
			st.Labelled++
			if r.Fires(rec) {
				st.Fired++
				if match {
					st.Correct++
				}
			}
		}
		if st.Labelled > 0 {
			st.Coverage = float64(st.Fired) / float64(st.Labelled)
		}
		if lo, hi, ok := mathx.Wilson(st.Correct, st.Fired, z); ok {
			st.Precision, st.WilsonLow, st.WilsonHi = float64(st.Correct)/float64(st.Fired), lo, hi
			st.Key = lo >= minLow && !math.IsNaN(lo)
		}
		out = append(out, st)
	}
	return out
}

// ErrNoJudge: this install has no judge labels yet (the judge layer is
// not built): no rule is measured, so none is an answer key.
var ErrNoJudge = errors.New("evidence: no judge labels on this install yet: rules are evidence only")

// Judge supplies this install's judge labels (message → question →
// value), the ground the rules are measured on per install.
type Judge interface {
	Labels(ctx context.Context) (map[int64]map[string]json.RawMessage, error)
}

type noJudge struct{}

func (noJudge) Labels(context.Context) (map[int64]map[string]json.RawMessage, error) {
	return nil, ErrNoJudge
}

// UseJudge sets the judge whose labels the rules are measured against.
func (e *Engine) UseJudge(j Judge) {
	if j == nil {
		j = noJudge{}
	}
	e.judge = j
}

// RuleTable measures the rules on this install's judge labels
// (evidence.rules.z, evidence.rules.min_precision); ErrNoJudge until a
// judge exists.
func (e *Engine) RuleTable(ctx context.Context) ([]RuleStat, error) {
	labels, err := e.judge.Labels(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(labels))
	for id := range labels {
		ids = append(ids, id)
	}
	recs, err := e.Records(ctx, ids)
	if err != nil {
		return nil, err
	}
	snap := e.cfg.Snapshot()
	return e.rules.Measure(labels, recs, snap.Float(SettingRulesZ), snap.Float(SettingRulesMinPrecision)), nil
}

// Rules is the loaded rule set.
func (e *Engine) Rules() *RuleSet { return e.rules }
