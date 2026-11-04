//go:build fixture

package layer1

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// experiment is one harness configuration (docs/LAYER1.md, experiments):
// production settings changed through the configuration and the teacher
// file only, plus the harness's own choices (vectors, population,
// exclusions).
type experiment struct {
	settings map[string]any // configuration overrides
	teachers string         // a layer1.teachers.file override (JSON), "" = the shipped sources
	purpose  string         // the vectors' purpose ("" = document)
	sample   bool           // E1: only the experiment sample (gold + the stratified draw)
	mates    bool           // E6: also exclude the dev and test messages' conversation-mates from every target
	plan     func(*Plan)    // a pass-level choice under test (E2 heads)
}

// noSubQ is the cold start: no decider answers, live or legacy.
const noSubQ = `{"sources": {"decider": null, "legacy": null}}`

// experiments by name; "e1-<purpose>-<dims>" names are built in experimentNamed.
var experiments = map[string]experiment{
	"default": {},
	// E1 (on the experiment sample)
	"e1-noemb":      {sample: true, settings: map[string]any{SettingUseEmbedding: false}},
	"e1-noevidence": {sample: true, settings: map[string]any{SettingUseEvidence: false}},
	// E2
	"e2-l2-wide":  {settings: map[string]any{SettingGrid: []any{-5, -4, -3, -2, -1, 0}}},
	"e2-l2-fixed": {settings: map[string]any{SettingGrid: []any{-3}}},
	"e2-l2-low":   {settings: map[string]any{SettingGrid: []any{-6, -5, -4}}},
	"e2-tol4":     {settings: map[string]any{SettingTolerance: 1e-4}},
	"e2-iter100":  {settings: map[string]any{SettingMaxIterations: 100}},
	// E3
	"e3-knn":     {settings: map[string]any{SettingKNN: true}},
	"e3-knn-k10": {settings: map[string]any{SettingKNN: true, SettingKNNK: 10}},
	"e3-knn-k50": {settings: map[string]any{SettingKNN: true, SettingKNNK: 50}},
	"e3-knn-t02": {settings: map[string]any{SettingKNN: true, SettingKNNTemperature: 0.02}},
	"e3-knn-t10": {settings: map[string]any{SettingKNN: true, SettingKNNTemperature: 0.1}},
	// E4
	"e4-coldstart":        {teachers: noSubQ},
	"e4-coldstart-knn":    {teachers: noSubQ, settings: map[string]any{SettingKNN: true}},
	"e4-legacy-only":      {teachers: `{"sources": {"judge": {"from": "labels", "label_source": "judge", "precedence": 2, "weight": 2, "eval": true}, "rules": null}}`},
	"e4-rules-unmeasured": {teachers: `{"sources": {"rules": {"from": "rules", "precedence": 5, "weight": 0.3, "min_lower_bound": 0.7, "unmeasured_precision": 0.6}}}`},
	"e4-hard":             {teachers: `{"sources": {"legacy": {"from": "decisions", "decider": "legacy:subq-snap-preview", "provider": "subq", "precedence": 4, "weight": 1, "temperature": 0.05}}}`},
	"e4-T2":               {teachers: `{"sources": {"legacy": {"from": "decisions", "decider": "legacy:subq-snap-preview", "provider": "subq", "precedence": 4, "weight": 1, "temperature": 2}}}`},
	"e4-noleak":           {teachers: `{"questions": {"reply_needed": {"legacy": null}, "call_to_action": {"legacy": null}, "decision_needed": {"legacy": null}, "urgency": {"legacy": null}}}`},
	"e4-judge-heavy":      {teachers: `{"sources": {"judge": {"from": "labels", "label_source": "judge", "precedence": 2, "weight": 50, "eval": true}}}`},
	// E5
	"e5-nocal": {settings: map[string]any{SettingCalibration: "none"}},
	// E6
	"e6-mates":     {mates: true},
	"e6-date-past": {}, // the past-date/overdue signal and the deadline rule, production settings
	"e2-balance":   {plan: func(p *Plan) { p.Balance = true }},
	// E2 heads
	"e2-score-multinomial": {settings: map[string]any{SettingScoreHead: "multinomial"}},
	"e2-score-ordinal":     {settings: map[string]any{SettingScoreHead: "ordinal"}},
}

func experimentNamed(t *testing.T, name string) experiment {
	t.Helper()
	if e, ok := experiments[name]; ok {
		return e
	}
	// e1-<purpose>[-<dims>]: the purpose's vectors on the experiment sample
	parts := strings.Split(name, "-")
	if len(parts) >= 2 && parts[0] == "e1" {
		p := purposeNamed(t, parts[1])
		e := experiment{sample: true, purpose: p.name}
		if len(parts) == 3 {
			d, err := strconv.Atoi(parts[2])
			if err != nil {
				t.Fatalf("experiment %s: dimensions %q", name, parts[2])
			}
			e.settings = map[string]any{SettingDimensions: d}
		}
		return e
	}
	t.Fatalf("unknown experiment %q (known: %v, e1-<purpose>[-<dims>])", name, slices.Sorted(maps.Keys(experiments)))
	return experiment{}
}
