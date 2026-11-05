//go:build fixture

package layer1

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// A fresh install has the label sources defined but no label rows. Removing
// the sources would be a different, invalid configuration (the requirements
// certify on manual and judge), and keeping gold labels would measure
// "labels + rules" while claiming zero teaching. Both mistakes happened in
// this experiment's history, so the composition and the empty label store
// are pinned here.
func TestRulesOnlyExperimentIsAZeroLabelInstall(t *testing.T) {
	t.Parallel()
	exp := experiments["e4-rules-unmeasured"]
	if !exp.noLabels {
		t.Fatal("the rules-only experiment stores labels")
	}
	file, err := harnessTeachers(exp)
	if err != nil {
		t.Fatal(err)
	}
	// The real load: the file over the shipped document (what NewPlan does).
	merged, err := jsonx.Overlay(teachersJSON, file, "sources", "questions")
	if err != nil {
		t.Fatal(err)
	}
	teachers, err := parseTeachers(merged, map[string]bool{"is_question": true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range teachers.Sources {
		seen[s.Name] = true
		if s.From == FromDecisions {
			t.Fatalf("the zero-label experiment still has decisions teacher %q", s.Name)
		}
	}
	for _, want := range []string{"manual", "judge", "rules"} {
		if !seen[want] {
			t.Fatalf("teacher %q is missing: the install must define the label sources it certifies on", want)
		}
	}
}
