package layer1

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed teachers.json
var teachersJSON []byte

// Teacher source kinds.
const (
	FromLabels    = "labels"
	FromDecisions = "decisions"
	FromRules     = "rules"
)

// DeciderTriage names the configured triage decider in a decisions source.
const DeciderTriage = "triage"

// SourceDecider is the shipped decisions source that follows
// triage.decider (the only decisions source choose_teacher configures).
const SourceDecider = "decider"

// Source is one teacher source (teachers.json).
type Source struct {
	Name                string   `json:"-"`
	From                string   `json:"from"`
	LabelSource         string   `json:"label_source,omitempty"`
	Decider             string   `json:"decider,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	Precedence          int      `json:"precedence"`
	Weight              float64  `json:"weight"`
	Eval                bool     `json:"eval,omitempty"`
	Temperature         float64  `json:"temperature,omitempty"`
	MinLowerBound       float64  `json:"min_lower_bound,omitempty"`
	UnmeasuredPrecision *float64 `json:"unmeasured_precision,omitempty"`
}

// Teachers is the effective teacher configuration: the sources in
// precedence order, and per question the sources it uses (overrides
// applied).
type Teachers struct {
	Sources []Source
	// Digest is the merged document's digest (every source and every
	// per-question override): a run's inputs.
	Digest    string
	questions map[string]map[string]*Source // question → source name → override (nil: removed)
}

// For lists the sources of one question in precedence order.
func (t *Teachers) For(question string) []Source {
	over := t.questions[question]
	var out []Source
	for _, s := range t.Sources {
		o, has := over[s.Name]
		switch {
		case !has:
			out = append(out, s)
		case o != nil:
			out = append(out, *o)
		}
	}
	return out
}

// LoadTeachers reads teachers.json merged with layer1.teachers.file and
// checks it against the question names (every problem at once).
func LoadTeachers(cfg *config.Config, questions map[string]bool) (*Teachers, error) {
	data, err := cfg.Overlay(SettingTeachersFile, teachersJSON, "sources", "questions")
	if err == nil {
		var t *Teachers
		if t, err = parseTeachers(data, questions); err == nil {
			return t, nil
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrTeachers, err)
}

func parseTeachers(data []byte, questions map[string]bool) (*Teachers, error) {
	var doc struct {
		Doc       string                     `json:"_doc"`
		Sources   jsonx.Object               `json:"-"`
		RawSrc    json.RawMessage            `json:"sources"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("layer1: teachers: %w", err)
	}
	ordered, err := jsonx.DecodeOrdered(doc.RawSrc)
	src, ok := ordered.(jsonx.Object)
	if err != nil || !ok || len(src) == 0 {
		return nil, errors.New("layer1: teachers: sources must be a non-empty object")
	}
	var errs []string
	t := &Teachers{questions: map[string]map[string]*Source{}, Digest: digest.Hex(data, 32)}
	seen := map[int]string{}
	byName := map[string]Source{}
	for _, m := range src {
		raw, _ := json.Marshal(m.Value)
		s, problems := decodeSource(m.Key, raw, nil)
		errs = append(errs, problems...)
		if other, dup := seen[s.Precedence]; dup && s.Precedence != 0 {
			errs = append(errs, fmt.Sprintf("sources %s and %s share precedence %d", other, s.Name, s.Precedence))
		}
		seen[s.Precedence] = s.Name
		t.Sources = append(t.Sources, s)
		byName[s.Name] = s
	}
	slices.SortStableFunc(t.Sources, func(a, b Source) int { return a.Precedence - b.Precedence })
	for _, q := range sortedKeys(doc.Questions) {
		if !questions[q] {
			errs = append(errs, fmt.Sprintf("questions: unknown question %q", q))
			continue
		}
		var over map[string]json.RawMessage
		if err := json.Unmarshal(doc.Questions[q], &over); err != nil {
			errs = append(errs, fmt.Sprintf("questions.%s: must be an object of sources", q))
			continue
		}
		t.questions[q] = map[string]*Source{}
		for _, name := range sortedKeys(over) {
			base, known := byName[name]
			switch {
			case !known:
				errs = append(errs, fmt.Sprintf("questions.%s: unknown source %q", q, name))
			case string(over[name]) == "null":
				t.questions[q][name] = nil
			default:
				s, problems := decodeSource(name, over[name], &base)
				if s.Precedence != base.Precedence {
					problems = append(problems, fmt.Sprintf("questions.%s.%s: precedence is the source's own", q, name))
				}
				errs = append(errs, problems...)
				t.questions[q][name] = &s
			}
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("layer1: teachers: %s", strings.Join(errs, "; "))
	}
	return t, nil
}

// decodeSource decodes and checks one source (over base's fields when
// given: a per-question override).
func decodeSource(name string, raw []byte, base *Source) (Source, []string) {
	var s Source
	if base != nil {
		s = *base
	}
	if err := jsonx.DecodeStrict(raw, &s); err != nil {
		return s, []string{fmt.Sprintf("source %s: %v", name, err)}
	}
	s.Name = name
	var errs []string
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Sprintf("source %s: ", name)+fmt.Sprintf(format, a...))
	}
	switch s.From {
	case FromLabels:
		if s.LabelSource == "" {
			bad("labels need label_source")
		}
	case FromDecisions:
		if s.Decider == "" {
			bad("decisions need a decider (%q or a stored decider key)", DeciderTriage)
		}
		if s.Decider == Decider {
			bad("layer 1 cannot teach itself (decider %q)", Decider)
		}
		if s.Decider != DeciderTriage && s.Provider == "" {
			bad("a stored decider key needs the provider whose probability_tolerance applies")
		}
		if !(s.Temperature > 0) || math.IsInf(s.Temperature, 0) {
			bad("temperature must be > 0")
		}
	case FromRules:
		if s.MinLowerBound < 0 || s.MinLowerBound > 1 {
			bad("min_lower_bound must be in [0, 1]")
		}
		if p := s.UnmeasuredPrecision; p != nil && (!(*p > 0) || *p > 1) {
			bad("unmeasured_precision must be null or in (0, 1]")
		}
	default:
		bad("from must be %s, %s or %s, not %q", FromLabels, FromDecisions, FromRules, s.From)
	}
	if s.Eval && s.From != FromLabels {
		bad("only labels can evaluate (eval)")
	}
	if s.Precedence < 1 {
		bad("precedence must be ≥ 1")
	}
	if !(s.Weight > 0) || math.IsInf(s.Weight, 0) {
		bad("weight must be > 0")
	}
	return s, errs
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
