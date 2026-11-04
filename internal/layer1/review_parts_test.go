package layer1

import (
	"errors"
	"testing"
)

// A certify_sources name that is no evaluation source would certify
// nothing, silently: refused.
func TestCertifySourcesMustBeEvaluationSources(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, nil)
	if err := writeRequirements(cfg, `{"defaults": {"certify_sources": ["jduge"]}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(cfg); !errors.Is(err, ErrRequirements) {
		t.Fatalf("a typo accepted: %v", err)
	}
}

// Lead decision: a local decider (a model served by a local runtime) is
// never a teacher, nor is layer 1 itself (parseTeachers).
func TestALocalDeciderNeverTeaches(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, nil)
	doc := `{"sources": {"local": {"from": "decisions", "decider": "x/y", "provider": "embeddinggemma", "temperature": 1, "precedence": 9, "weight": 1}}}`
	if err := writeTeachers(cfg, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlan(cfg); !errors.Is(err, ErrTeachers) {
		t.Fatalf("a local decider teaches: %v", err)
	}
}

// Findings (bugs review, plausible): the warm start ignored the vector
// space and the layout id; a kept kNN model answered without its kNN
// term once kNN was off. A model starts a fit or answers only where it
// reads the same rows the same way.
func TestModelsOnlyServeTheRowsTheyRead(t *testing.T) {
	t.Parallel()
	d, ex := synthetic(t, 30, 4, 2)
	qs := &questionSet{kind: Binary, classes: 2, train: ex}
	tr := &Trained{Space: "s1", Layout: Layout{ID: "l1"}, Design: d, knn: map[string]*knnIndex{}}
	m := &Model{Question: "q", Kind: Binary, Classes: 2, Dims: d.Dims, Cells: d.Cells, Space: "s1", Layout: "l1", Spec: "x", Params: Params{W: []float64{1}}}
	if warmStart(m, qs, d, tr, "x") == nil {
		t.Fatal("a compatible model does not warm-start")
	}
	for name, change := range map[string]func(*Model){
		"another space":  func(m *Model) { m.Space = "s2" },
		"another layout": func(m *Model) { m.Layout = "l2" },
		"another spec":   func(m *Model) { m.Spec = "y" },
	} {
		c := *m
		change(&c)
		if warmStart(&c, qs, d, tr, "x") != nil {
			t.Errorf("%s warm-starts", name)
		}
	}
	a := &activeModel{model: m, meta: modelMeta{Space: "s1", Layout: "l1", Dims: d.Dims, Cells: d.Cells, Spec: "x"}}
	if !a.fits(tr, "x", 2) || a.fits(tr, "y", 2) || a.fits(tr, "x", 3) {
		t.Fatal("fits: spec and classes")
	}
	m.Combine.KNN = true
	if a.fits(tr, "x", 2) {
		t.Fatal("a kNN model fits a pass without its kNN index")
	}
}
