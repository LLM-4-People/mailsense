// Package triage decides every message with the configured Decider: it
// composes each state from the profile's template (observed facts, and
// a causal history: standings, reply examples and labels dated before the
// message), caches RAW answers by message, state hash and spec fingerprint
// (partial answers too: only missing questions are asked again), blends
// learned priors at read time, and runs one job at a time with a budget
// guard, per-message failure records and coverage counts.
package triage

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

//go:embed rules.json
var rulesJSON []byte

// Bands is an ordered scale: len(Labels) == len(Bounds)+1, bounds ascending.
type Bands struct {
	Bounds []float64 `json:"bounds"`
	Labels []string  `json:"labels"`
}

// Rules is the state-context data (rules.json + triage.rules.file).
type Rules struct {
	Doc     string `json:"_doc"`
	Volume  Bands  `json:"volume_bands"`
	Reply   Bands  `json:"reply_bands"`
	Urgency struct {
		BoundsMinutes []int     `json:"bounds_minutes"`
		Levels        []float64 `json:"levels"`
		// ResolutionMinutes is the precision the median is compared at
		// (legacy: rounded to 0.1 minute, then ≤ a bound).
		ResolutionMinutes float64 `json:"resolution_minutes"`
	} `json:"urgency_prior"`
	// Facts are the data of the observed facts: when a text counts as
	// spaceless (no word count) or uncased (no all-caps words), and the
	// shortest word counted as all caps (the question marks of every
	// script are Unicode data: mail.QuestionMarks).
	Facts struct {
		Doc            string  `json:"_doc"`
		SpacelessShare float64 `json:"spaceless_share"`
		CasedShare     float64 `json:"cased_share"`
		CapsMinLetters int     `json:"caps_min_letters"`
	} `json:"facts"`
	Phrases map[string]string `json:"phrases"`
	tmpl    map[string]*taxonomy.Template
}

// phraseNames are the phrases code renders.
var phraseNames = []string{"history", "history_sender", "history_domain", "history_overall", "history_speed", "examples",
	"example_reply", "example_reply_preview", "example_label", "facts", "fact_thread", "fact_recipients", "fact_attachments",
	"fact_questions", "fact_links", "fact_caps", "fact_length", "fact_length_chars", "fact_shape", "fact_shape_sentences", "fact_arrival",
	"unknown_sender"}

// LoadRules reads the embedded rules merged with triage.rules.file
// (config.Overlay: the one reader of shipped data + the user's file).
func LoadRules(cfg *config.Config) (*Rules, error) {
	merged, err := cfg.Overlay(RulesFileSetting, rulesJSON, rulesDeep...)
	if err != nil {
		return nil, fmt.Errorf("triage: rules: %w", err)
	}
	return loadRules(merged)
}

// rulesDeep are the sections a rules file overrides per key.
var rulesDeep = []string{"volume_bands", "reply_bands", "urgency_prior", "facts", "phrases"}

// loadRules parses and validates the rules (every problem at once).
func loadRules(data []byte) (*Rules, error) {
	var r Rules
	if err := jsonx.DecodeStrict(data, &r); err != nil {
		return nil, fmt.Errorf("triage: rules: %w", err)
	}
	var problems []string
	problems = append(problems, r.Volume.check("volume_bands")...)
	problems = append(problems, r.Reply.check("reply_bands")...)
	u := Bands{Labels: make([]string, len(r.Urgency.Levels))}
	for _, b := range r.Urgency.BoundsMinutes {
		u.Bounds = append(u.Bounds, float64(b))
	}
	problems = append(problems, u.check("urgency_prior")...)
	if r.Urgency.ResolutionMinutes < 0 {
		problems = append(problems, "urgency_prior/resolution_minutes: must be ≥ 0")
	}
	if r.Facts.SpacelessShare <= 0 || r.Facts.SpacelessShare > 1 || r.Facts.CasedShare <= 0 || r.Facts.CasedShare > 1 || r.Facts.CapsMinLetters < 1 {
		problems = append(problems, "facts: the shares must be in (0, 1] and caps_min_letters ≥ 1")
	}
	r.tmpl = map[string]*taxonomy.Template{}
	for _, name := range phraseNames {
		t, err := taxonomy.ParseTemplate(r.Phrases[name])
		if err != nil || r.Phrases[name] == "" {
			problems = append(problems, fmt.Sprintf("phrases/%s: missing or invalid (%v)", name, err))
			continue
		}
		r.tmpl[name] = t
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("triage: invalid rules:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return &r, nil
}

func (b Bands) check(name string) []string {
	if len(b.Labels) != len(b.Bounds)+1 || len(b.Bounds) == 0 || !slices.IsSorted(b.Bounds) {
		return []string{fmt.Sprintf("%s: needs ascending bounds and one more label than bounds", name)}
	}
	return nil
}

// phrase renders a named phrase.
func (r *Rules) phrase(name string, values map[string]string) string {
	return r.tmpl[name].Render(values)
}

// Band is the band of v (a bound belongs to the band above it).
func (b Bands) Band(v float64) int {
	i := 0
	for i < len(b.Bounds) && v >= b.Bounds[i] {
		i++
	}
	return i
}

// Label is the label of v's band.
func (b Bands) Label(v float64) string { return b.Labels[b.Band(v)] }
