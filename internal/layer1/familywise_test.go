package layer1

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
)

// The correction is the identity for one question and the two-sided
// Bonferroni quantile for the profile's question count.
func TestFamilyWiseCorrectionScalesWithQuestions(t *testing.T) {
	t.Parallel()
	data := []byte(`{"defaults":{"min_effective_train":1,"min_eval_labels":2,"target_accuracy":0.95,"min_coverage":0.5,"z":1.96,"family_wise":true,"certify_sources":["manual"]},"report_targets":[0.95],"questions":{}}`)
	one, err := parseRequirements(data, map[string]bool{"q0": true})
	if err != nil {
		t.Fatal(err)
	}
	if got := one.For("q0").EffectiveZ(); math.Abs(got-1.96) > 1e-3 {
		t.Fatalf("one question effective z = %v, want 1.96", got)
	}
	names := map[string]bool{}
	for i := range 17 {
		names[fmt.Sprintf("q%d", i)] = true
	}
	many, err := parseRequirements(data, names)
	if err != nil {
		t.Fatal(err)
	}
	if got := many.For("q0").EffectiveZ(); math.Abs(got-2.9738) > 1e-3 {
		t.Fatalf("17 questions effective z = %v, want 2.9738", got)
	}
	offData := []byte(strings.Replace(string(data), `"family_wise":true`, `"family_wise":false`, 1))
	off, err := parseRequirements(offData, names)
	if err != nil {
		t.Fatal(err)
	}
	if got := off.For("q0").EffectiveZ(); math.Abs(got-1.96) > 1e-3 {
		t.Fatalf("family_wise off effective z = %v, want 1.96", got)
	}
}

// A precision just above a per-question 95% bound falls below the corrected
// family-wise bound: the correction changes the verdict, not only a label.
func TestFamilyWiseBoundMovesCertification(t *testing.T) {
	t.Parallel()
	rows := make([]selective.Ranked, 982)
	for i := range rows {
		rows[i] = selective.Ranked{ID: int64(i), Correct: i < 953}
	}
	base := selective.CertifyFixed(rows, len(rows), 0.95, 1.96)
	wide := selective.CertifyFixed(rows, len(rows), 0.95, 2.9738)
	if !base.Certified || wide.Certified {
		t.Fatalf("953/982: per-question certified=%v lower=%.4f; family-wise certified=%v lower=%.4f",
			base.Certified, base.Lower, wide.Certified, wide.Lower)
	}
}

// The shipped document enables the correction.
func TestShippedRequirementsEnableFamilyWise(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for i := range 17 {
		names[fmt.Sprintf("q%d", i)] = true
	}
	req, err := parseRequirements(requirementsJSON, names)
	if err != nil {
		t.Fatal(err)
	}
	def := req.For("q0")
	if !def.FamilyWise || def.EffectiveZ() <= def.Z {
		t.Fatalf("shipped requirements do not correct z: family_wise=%v z=%v effective=%v",
			def.FamilyWise, def.Z, def.EffectiveZ())
	}
}
