package layer1

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// TestHoldoutCertificationReportsClassRecall: the layer-1 holdout carries
// every certification label, so a class whose predictions all fall below
// the chosen threshold is reported as collapsed while the pooled verdict
// follows the majority.
func TestHoldoutCertificationReportsClassRecall(t *testing.T) {
	t.Parallel()
	const n = 40
	labels := map[int64]evalLabel{}
	var sel, cert []HeldItem
	sure := []float64{0.9, 0.05, 0.05}
	unsure := []float64{0.5, 0.5, 0}
	for i := range n {
		id := int64(i + 1)
		labels[id] = evalLabel{class: 0, source: "manual"}
		sel = append(sel, HeldItem{ID: id, Probs: sure})
	}
	for i := range 97 {
		id := int64(1000 + i)
		labels[id] = evalLabel{class: 0, source: "manual"}
		cert = append(cert, HeldItem{ID: id, Probs: sure})
	}
	for i := range 3 {
		id := int64(2000 + i)
		labels[id] = evalLabel{class: 1, source: "manual"}
		cert = append(cert, HeldItem{ID: id, Probs: unsure})
	}
	req := Requirement{TargetAccuracy: 0.9, Z: 1.96, CertifySources: []string{"manual"}}
	h := &Holdout{Select: sel, Certify: cert}
	c := h.certify(engine.Choice, labels, 0.5, req)
	if !c.Certified || c.Answered != 97 {
		t.Fatalf("the pooled rule did not certify its majority answers: %+v", c)
	}
	want := []selective.ClassRecall{
		{Class: 0, Eligible: 97, Answered: 97, Correct: 97},
		{Class: 1, Eligible: 3, Answered: 0, Correct: 0},
	}
	if !slices.Equal(c.ClassRecall, want) {
		t.Fatalf("class recall %+v, want %+v", c.ClassRecall, want)
	}
	if !c.ClassCollapsed {
		t.Fatalf("the unmeasured minority class did not collapse: %+v", c)
	}

	// The same minority predictions at the majority's confidence are
	// answered, correct, and do not collapse.
	for i := range cert {
		if labels[cert[i].ID].class == 1 {
			cert[i].Probs = []float64{0.05, 0.9, 0.05}
		}
	}
	b := h.certify(engine.Choice, labels, 0.5, req)
	if !b.Certified || b.ClassCollapsed {
		t.Fatalf("the balanced fixture collapsed: %+v", b)
	}
	if !slices.Equal(b.ClassRecall, []selective.ClassRecall{
		{Class: 0, Eligible: 97, Answered: 97, Correct: 97},
		{Class: 1, Eligible: 3, Answered: 3, Correct: 3},
	}) {
		t.Fatalf("balanced class recall %+v", b.ClassRecall)
	}
}
