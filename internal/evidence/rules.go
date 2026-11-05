package evidence

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

//go:embed rules.json
var rulesJSON []byte

// The evidence rule settings layer 1's route key and the measurement read.
const (
	SettingRulesFile         = "evidence.rules.file"
	SettingRulesZ            = "evidence.rules.z"
	SettingRulesMinPrecision = "evidence.rules.min_precision"
)

// Rule maps conditions on causal signals to one answer of a question.
type Rule struct {
	ID       string          `json:"-"`
	Question string          `json:"question"`
	Answer   json.RawMessage `json:"answer"` // a choice option, true/false, or {"min": a, "max": b} levels
	When     []Cond          `json:"when"`
	Doc      string          `json:"doc"`
	answer   answer
}

// Cond is one condition on one signal's value.
type Cond struct {
	Signal string          `json:"signal"`
	Is     json.RawMessage `json:"is,omitempty"`  // equal (bool, enum or category value, number)
	In     []string        `json:"in,omitempty"`  // one of (enum or category)
	Min    *float64        `json:"min,omitempty"` // at least (numeric)
	Max    *float64        `json:"max,omitempty"` // at most (numeric)
	Has    string          `json:"has,omitempty"` // holds (set)
	sig    Signal          // the signal, resolved at load
	is     any             // Is decoded at load (bool, string or float64)
}

// RuleAnswer is a rule's parsed answer: an option, a truth value, or a
// range of whole levels.
type RuleAnswer struct {
	Option   string
	Truth    *bool
	Min, Max int
	Range    bool
}

// Parsed is the rule's answer as checked at load.
func (r *Rule) Parsed() RuleAnswer {
	return RuleAnswer{Option: r.answer.option, Truth: r.answer.truth, Min: int(r.answer.min), Max: int(r.answer.max), Range: r.answer.isRange}
}

// Classes are the classes the rule's answer names: the index of a choice
// option (lo −1 when it is not one of the question's), 1 or 0 for a
// true/false answer, and the level range of a score answer.
func (r RuleAnswer) Classes(q *engine.Question) (lo, hi int) {
	switch {
	case r.Option != "":
		k := slices.IndexFunc(q.Options, func(o engine.Option) bool { return o.Name == r.Option })
		return k, k
	case r.Truth != nil:
		if *r.Truth {
			return 1, 1
		}
		return 0, 0
	}
	return r.Min, r.Max
}

// RuleSet is the loaded rules, by id order. Digest is the identity of the
// merged document (the shipped rules plus evidence.rules.file): it is part
// of the layer-1 route key, so a rule change can never read a route answer
// computed under another rule set.
type RuleSet struct {
	Digest string
	Rules  []Rule

	// profile is the typed question catalogue the rules were checked
	// against. A rule answer is built from it, so a rule can never be served
	// with a type or option from another profile.
	profile *taxonomy.Profile
}

// Question returns the typed question a rule names.
func (s *RuleSet) Question(name string) (*engine.Question, bool) {
	if s == nil || s.profile == nil {
		return nil, false
	}
	return s.profile.Question(name)
}

// RulesDigest is the identity of the merged evidence-rules document. It is
// parsed and checked exactly like the engine's own load, so an invalid
// document fails here instead of producing a route under a key no run can
// reproduce.
func RulesDigest(cfg *config.Config) (string, error) {
	set, err := LoadRules(cfg, Signals())
	if err != nil {
		return "", err
	}
	return set.Digest, nil
}

// LoadRules reads the shipped rules merged with evidence.rules.file (per
// rule id; null removes) and checks them against the registry and the
// triage taxonomy (taxonomy.Load: its questions, options, answer kinds and
// levels): a rule that could never match a question is refused.
func LoadRules(cfg *config.Config, signals []Signal) (*RuleSet, error) {
	profile, err := taxonomy.Load(cfg)
	if err != nil {
		return nil, fmt.Errorf("evidence: rules: %w", err)
	}
	set, err := readRules(cfg, signals)
	if err != nil {
		return nil, err
	}
	if err := set.checkTaxonomy(profile); err != nil {
		return nil, err
	}
	set.profile = profile
	return set, nil
}

func readRules(cfg *config.Config, signals []Signal) (*RuleSet, error) {
	data, err := withOverrides(cfg, SettingRulesFile, rulesJSON, "rules")
	if err != nil {
		return nil, err
	}
	return parseRules(data, signals)
}

func parseRules(data []byte, signals []Signal) (*RuleSet, error) {
	var doc struct {
		Doc   string          `json:"_doc"`
		Rules map[string]Rule `json:"rules"`
	}
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("evidence: rules: %w", err)
	}
	byName := map[string]Signal{}
	for _, s := range signals {
		byName[s.Name] = s
	}
	set := &RuleSet{Digest: digest.Hex(data, 32)}
	var errs []error
	for id, r := range doc.Rules {
		r.ID = id
		if err := r.check(byName); err != nil {
			errs = append(errs, fmt.Errorf("evidence: rule %s: %w", id, err))
			continue
		}
		set.Rules = append(set.Rules, r)
	}
	sort.Slice(set.Rules, func(i, j int) bool { return set.Rules[i].ID < set.Rules[j].ID })
	return set, errors.Join(errs...)
}

func (r *Rule) check(signals map[string]Signal) error {
	var errs []error
	if r.Question == "" || len(r.When) == 0 || r.Doc == "" {
		errs = append(errs, errors.New("needs a question, conditions and a doc"))
	}
	var err error
	if r.answer, err = parseAnswer(r.Answer); err != nil {
		errs = append(errs, err)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, r.Answer) == nil {
		r.Answer = compact.Bytes() // one form in reports and tables
	}
	for i := range r.When {
		if err := r.When[i].check(signals); err != nil {
			errs = append(errs, fmt.Errorf("condition %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Cond) check(signals map[string]Signal) error {
	s, ok := signals[c.Signal]
	switch {
	case !ok:
		return fmt.Errorf("unknown signal %q", c.Signal)
	case !s.Causal:
		return fmt.Errorf("signal %s is not causal: it can never settle a question", c.Signal)
	}
	c.sig = s
	ops := 0
	for _, set := range []bool{c.Is != nil, c.In != nil, c.Min != nil || c.Max != nil, c.Has != ""} {
		if set {
			ops++
		}
	}
	if ops != 1 {
		return fmt.Errorf("%s: exactly one of is, in, min/max, has", c.Signal)
	}
	switch {
	case c.Is != nil:
		return c.checkIs()
	case c.In != nil && s.Type == TypeEnum:
		for _, v := range c.In {
			if !slices.Contains(s.Values, v) {
				return fmt.Errorf("%s: %q is not one of its values %v", c.Signal, v, s.Values)
			}
		}
	case c.In != nil && s.Type != TypeCategory:
		return fmt.Errorf("%s: in needs an enum or a category", c.Signal)
	case (c.Min != nil || c.Max != nil) && !numeric(s.Type):
		return fmt.Errorf("%s: min/max need a number", c.Signal)
	case c.Min != nil && c.Max != nil && *c.Min > *c.Max:
		return fmt.Errorf("%s: min %v above max %v", c.Signal, *c.Min, *c.Max)
	case c.Has != "" && s.Type != TypeSet:
		return fmt.Errorf("%s: has needs a set", c.Signal)
	}
	return nil
}

// checkIs decodes is and checks it against the signal's type and
// vocabulary: a bool for a bool, one of its values for an enum, a string
// for a category, a number for a number (an ints list: its length).
func (c *Cond) checkIs() error {
	if err := json.Unmarshal(c.Is, &c.is); err != nil {
		return fmt.Errorf("%s: is: %w", c.Signal, err)
	}
	s := c.sig
	switch v := c.is.(type) {
	case bool:
		if s.Type == TypeBool {
			return nil
		}
	case string:
		if s.Type == TypeEnum && !slices.Contains(s.Values, v) {
			return fmt.Errorf("%s: %q is not one of its values %v", c.Signal, v, s.Values)
		}
		if s.Type == TypeEnum || s.Type == TypeCategory {
			return nil
		}
	case float64:
		if numeric(s.Type) {
			return nil
		}
	}
	return fmt.Errorf("%s: is %s does not fit its type %s", c.Signal, c.Is, s.Type)
}

// checkTaxonomy checks every rule against the triage profile: its
// question exists, and its answer is of the question's kind — an option
// of a choice question, true/false of a yes/no one, a level range within
// a score question's levels. Every problem is reported.
func (s *RuleSet) checkTaxonomy(p *taxonomy.Profile) error {
	var errs []error
	for _, r := range s.Rules {
		q, ok := p.Question(r.Question)
		if !ok {
			errs = append(errs, fmt.Errorf("evidence: rule %s: no question %q in the triage profile", r.ID, r.Question))
			continue
		}
		if err := r.answer.fits(q); err != nil {
			errs = append(errs, fmt.Errorf("evidence: rule %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Fires reports whether every condition holds on the record.
func (r *Rule) Fires(rec Record) bool {
	for _, c := range r.When {
		if !c.holds(rec[c.Signal]) {
			return false
		}
	}
	return true
}

// holds tests a value; a missing value satisfies nothing, and a value
// read on a cut input only what it cannot overstate (Signal.Unsure,
// Signal.Bound).
func (c Cond) holds(v Value) bool {
	if v.State != OK {
		return false
	}
	switch {
	case c.Is != nil:
		switch w := c.is.(type) {
		case bool:
			return v.Bool == w && c.sure(strconv.FormatBool(v.Bool), v, []string{strconv.FormatBool(w)})
		case string:
			return v.Str == w && c.sure(v.Str, v, []string{w})
		case float64:
			return number(c.sig, v) == w && !v.Partial
		}
		return false
	case c.In != nil:
		return slices.Contains(c.In, v.Str) && c.sure(v.Str, v, c.In)
	case c.Has != "":
		return slices.Contains(v.List, c.Has) // a member read is a member
	}
	x := number(c.sig, v)
	lowOK := c.Min == nil || x >= *c.Min && (!v.Partial || c.sig.Bound == AtLeast)
	highOK := c.Max == nil || x <= *c.Max && (!v.Partial || c.sig.Bound == AtMost)
	return lowOK && highOK
}

// sure reports whether a (possibly partial) value settles a condition
// whose accepted values are accept: complete, or every value the whole
// input could give instead (Signal.Unsure) accepted too.
func (c Cond) sure(value string, v Value, accept []string) bool {
	could, unsure := c.sig.Unsure[value]
	if !v.Partial || !unsure {
		return true
	}
	if could == nil {
		return false // it could be any value
	}
	for _, x := range could {
		if !slices.Contains(accept, x) {
			return false
		}
	}
	return true
}

// number is a numeric value: the number, or an ints list's length (the
// one numeric reading of every numeric signal: rules, features, facts).
func number(s Signal, v Value) float64 {
	if s.Type == TypeInts {
		return float64(len(v.Ints))
	}
	return v.Num
}
