package layer1

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

func TestCombinerTemperatureRecoversTheScale(t *testing.T) {
	t.Parallel()
	// labels drawn from softmax(2·ℓ): the fitted A is near 2
	var lr [][]float64
	var labels []int
	for i := range 2000 {
		x := float64(i%40)/10 - 2
		l := []float64{-softplus(-x), -softplus(x)} // log σ(x), log σ(−x): a binary head
		p1 := 1 / (1 + math.Exp(-2*(l[1]-l[0])))
		y := 0
		if float64((i*7919)%1000)/1000 < p1 {
			y = 1
		}
		lr, labels = append(lr, l), append(labels, y)
	}
	c, err := FitCombiner(t.Context(), lr, nil, labels, true, 1)
	if err != nil || math.Abs(c.A-2) > 0.4 || c.KNN {
		t.Fatalf("%+v %v", c, err)
	}
	out := make([]float64, 2)
	c.Apply([]float64{math.Log(0.3), math.Log(0.7)}, nil, out)
	if s := math.Exp(out[0]) + math.Exp(out[1]); math.Abs(s-1) > 1e-12 {
		t.Fatalf("not normalised: %v", s)
	}
	if _, err := FitCombiner(t.Context(), nil, nil, nil, true, 1); err == nil {
		t.Error("no predictions")
	}
	if _, err := FitCombiner(t.Context(), lr, lr[:3], labels, true, 1); err == nil {
		t.Error("knn of another length")
	}
	kc, err := FitCombiner(t.Context(), lr, lr, labels, true, 1)
	if err != nil || !kc.KNN || !(kc.A > 0) || !(kc.B > 0) {
		t.Fatalf("with knn %+v %v", kc, err)
	}
	// finding (bugs review, plausible): calibration "none" with kNN on reset
	// A to 1 after fitting A and B together, so B was fitted for another A.
	// Now A stays 1 and B alone is fitted: the pooled NLL with B refitted
	// is no worse than with the B of the joint fit.
	only, err := FitCombiner(t.Context(), lr, lr, labels, false, 1)
	if err != nil || only.A != 1 || !(only.B > 0) || !only.KNN {
		t.Fatalf("kNN without calibration %+v %v", only, err)
	}
	stale := Combiner{A: 1, B: kc.B, KNN: true}
	if nll(only, lr, labels) > nll(stale, lr, labels)+1e-9 {
		t.Fatalf("B refitted at A = 1 (%v) is worse than the joint fit's B (%v)", nll(only, lr, labels), nll(stale, lr, labels))
	}
	if c, err := FitCombiner(t.Context(), lr, nil, labels, false, 1); err != nil || c != Identity {
		t.Fatalf("nothing to fit: %+v %v", c, err)
	}
}

// nll is a combiner's mean NLL of labels, the head and kNN terms both lr.
func nll(c Combiner, lr [][]float64, labels []int) float64 {
	sum := 0.0
	out := make([]float64, len(lr[0]))
	for i, l := range lr {
		c.Apply(l, l, out)
		sum -= out[labels[i]]
	}
	return sum / float64(len(lr))
}

func TestModelBlobRoundTripAndCorruption(t *testing.T) {
	t.Parallel()
	m := &Model{Kind: Ordinal, Classes: 4, Dims: 3, Cells: 2,
		Params: Params{W: []float64{1, 2, 3, 4, 5}, B: []float64{0}, Extra: []float64{-1, 0.5, 0.2}},
		U:      []float64{0.1, 0.2, 0.3, 0.4, 0.5}, Bias: []float64{0.7}}
	b := m.Encode()
	got := &Model{Kind: Ordinal, Classes: 4}
	if err := got.Decode(b); err != nil || got.Dims != 3 || got.Cells != 2 || math.Abs(got.U[2]-0.3) > 1e-6 || len(got.Params.Extra) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	for name, blob := range map[string][]byte{
		"truncated":     b[:len(b)-3],
		"trailing":      append(append([]byte{}, b...), 0),
		"empty":         nil,
		"other version": append([]byte{9, 0, 0, 0}, b[4:]...),
	} {
		if err := (&Model{Kind: Ordinal, Classes: 4}).Decode(blob); !errors.Is(err, ErrBadBlob) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (&Model{Kind: Ordinal, Classes: 5}).Decode(b); !errors.Is(err, ErrBadBlob) {
		t.Errorf("another class count: %v", err)
	}
	if err := (&Model{Kind: Multinomial, Classes: 4}).Decode(b); !errors.Is(err, ErrBadBlob) {
		t.Errorf("another head's shape: %v", err)
	}
	nan := &Model{Kind: Binary, Classes: 2, Params: Params{W: []float64{math.NaN()}, B: []float64{0}}, U: []float64{1}, Bias: []float64{0}}
	if err := (&Model{Kind: Binary, Classes: 2}).Decode(nan.Encode()); !errors.Is(err, ErrBadBlob) {
		t.Errorf("NaN: %v", err)
	}
}

func TestFoldsNeverSplitAGroup(t *testing.T) {
	t.Parallel()
	var groups []string
	for i := range 300 {
		groups = append(groups, string(rune('a'+i%37)))
	}
	f := Folds(groups, 5)
	of := map[string]int{}
	load := make([]int, 5)
	for i, g := range groups {
		if prev, ok := of[g]; ok && prev != f[i] {
			t.Fatalf("group %s in folds %d and %d", g, prev, f[i])
		}
		of[g] = f[i]
		load[f[i]]++
	}
	for _, n := range load {
		if n < 40 || n > 80 {
			t.Fatalf("unbalanced folds %v", load)
		}
	}
	again := Folds(groups, 5)
	for i := range f {
		if f[i] != again[i] {
			t.Fatal("folds are not deterministic")
		}
	}
}

func TestMeasure(t *testing.T) {
	t.Parallel()
	preds := []Prediction{
		{ID: 1, Label: 1, Probs: []float64{0.1, 0.9}},
		{ID: 2, Label: 1, Probs: []float64{0.4, 0.6}},
		{ID: 3, Label: 0, Probs: []float64{0.8, 0.2}},
		{ID: 4, Label: 0, Probs: []float64{0.45, 0.55}},
	}
	m := Measure(engine.Noul, preds, 0.5, 1.96, nil)
	if m.N != 4 || m.Accuracy != 0.75 || m.AUC != 1 || m.BalancedAcc != 0.75 || len(m.Selective) != 10 {
		t.Fatalf("%+v", m)
	}
	if m.Selective[len(m.Selective)-1].Accuracy != 1 { // the most confident one is right
		t.Errorf("selective %v", m.Selective)
	}
	// a yes/no cutoff decides
	if m := Measure(engine.Noul, preds, 0.58, 1.96, nil); m.Accuracy != 1 {
		t.Errorf("cutoff 0.58: %v", m.Accuracy)
	}
	score := []Prediction{{ID: 1, Label: 2, Probs: []float64{0, 0.5, 0.5}}, {ID: 2, Label: 0, Probs: []float64{0.1, 0.1, 0.8}}}
	sm := Measure(engine.Score, score, 0, 1.96, []float64{0.9})
	if sm.WithinOne != 0.5 || math.Abs(sm.MAE-(0.5+1.7)/2) > 1e-9 || sm.Points[0].CoverageLB != 0 {
		t.Fatalf("%+v", sm)
	}
	if e := Measure(engine.Choice, nil, 0, 1.96, nil); e.N != 0 {
		t.Error("empty")
	}
}

func q(t engine.QuestionType) *engine.Question {
	switch t {
	case engine.Choice:
		return &engine.Question{Name: "c", Type: t, Instructions: "x", Options: []engine.Option{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	case engine.Score:
		return &engine.Question{Name: "s", Type: t, Instructions: "x", Levels: []string{"0", "1", "2", "3"}}
	}
	return &engine.Question{Name: "n", Type: t, Instructions: "x"}
}

func TestAnswerTargets(t *testing.T) {
	t.Parallel()
	c := q(engine.Choice)
	tg, err := answerTarget(c, engine.Answer{Type: engine.Choice, Probabilities: map[string]float64{"a": 0.2, "c": 0.8}}, 0.02)
	if err != nil || tg.Class(3) != 2 || tg.Mass(3)[1] != 0 {
		t.Fatalf("missing option counts 0: %v %v", tg, err)
	}
	for name, a := range map[string]engine.Answer{
		"unknown option": {Type: engine.Choice, Probabilities: map[string]float64{"z": 1}},
		"off sum":        {Type: engine.Choice, Probabilities: map[string]float64{"a": 0.5, "b": 0.3}},
		"NaN":            {Type: engine.Choice, Probabilities: map[string]float64{"a": math.NaN(), "b": 1}},
		"another type":   {Type: engine.Noul, Value: 0.5},
	} {
		if _, err := answerTarget(c, a, 0.02); !errors.Is(err, ErrBadTarget) {
			t.Errorf("%s: %v", name, err)
		}
	}
	n := q(engine.Noul)
	if tg, err := answerTarget(n, engine.Answer{Type: engine.Noul, Value: 0.7}, 0.02); err != nil || math.Abs(tg.Mass(2)[1]-0.7) > 1e-12 {
		t.Fatalf("%v %v", tg, err)
	}
	if _, err := answerTarget(n, engine.Answer{Type: engine.Noul, Value: 1.2}, 0.02); err == nil {
		t.Error("value above 1")
	}
	s := q(engine.Score)
	if _, err := answerTarget(s, engine.Answer{Type: engine.Score, Probabilities: map[string]float64{"7": 1}}, 0.02); err == nil {
		t.Error("level out of range")
	}
	if tg, _ := answerTarget(s, engine.Answer{Type: engine.Score, Probabilities: map[string]float64{"1": 0.3, "3": 0.7}}, 0.02); tg.Class(4) != 3 {
		t.Error("score target")
	}
	// labels
	if _, k, err := labelTarget(c, `"b"`, 0.5); err != nil || k != 1 {
		t.Fatalf("choice label %d %v", k, err)
	}
	if _, _, err := labelTarget(c, `"z"`, 0.5); err == nil {
		t.Error("unknown option label")
	}
	if tg, k, err := labelTarget(n, `0.8`, 0.5); err != nil || k != 1 || math.Abs(tg.Mass(2)[1]-0.8) > 1e-12 {
		t.Errorf("probability label %v %d %v", tg, k, err)
	}
	if _, k, _ := labelTarget(s, `2.5`, 0.5); k != 3 {
		t.Errorf("a level label rounds half up: %d", k)
	}
}

func TestRuleTargets(t *testing.T) {
	t.Parallel()
	yes := true
	lb := 0.8
	rules := []RuleInfo{
		{ID: "r1", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}, Stat: evidence.RuleStat{Fired: 10, WilsonLow: 0.9}},
		{ID: "r2", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}, Stat: evidence.RuleStat{Fired: 10, WilsonLow: 0.5}}, // below min_lower_bound
		{ID: "r3", Question: "n", Answer: evidence.RuleAnswer{Truth: &yes}},                                                     // unmeasured
		{ID: "r4", Question: "s", Answer: evidence.RuleAnswer{Min: 1, Max: 2, Range: true}, Stat: evidence.RuleStat{Fired: 3, WilsonLow: 0.8}},
	}
	g := &gathered{rules: rules, fires: map[int64][]int{1: {0, 1, 2}, 2: {2}, 3: {3}}}
	src := Source{Name: "rules", From: FromRules, MinLowerBound: 0.7}
	qs := &questionSet{q: q(engine.Noul), classes: 2, counts: map[string]*SourceCount{"rules": {}}}
	tg, ok := qs.ruleTarget(src, g, 1)
	if !ok || tg.Check(2) != nil || math.Abs(tg.Mass(2)[1]-0.9) > 1e-12 {
		t.Fatalf("only the measured rule teaches: %v %v", tg, ok)
	}
	if _, ok := qs.ruleTarget(src, g, 2); ok {
		t.Error("an unmeasured rule taught without unmeasured_precision")
	}
	src.UnmeasuredPrecision = &lb
	if tg, ok := qs.ruleTarget(src, g, 1); !ok || math.Abs(tg.Mass(2)[1]-0.85) > 1e-12 {
		t.Errorf("linear pool of 0.9 and 0.8: %v", tg)
	}
	sq := &questionSet{q: q(engine.Score), classes: 4, counts: map[string]*SourceCount{"rules": {}}}
	tg, ok = sq.ruleTarget(src, g, 3)
	if m := tg.Mass(4); !ok || tg.Check(4) != nil || math.Abs(m[1]+m[2]-0.8) > 1e-12 || math.Abs(m[0]-0.1) > 1e-12 {
		t.Fatalf("a level range: %v", tg)
	}
	// finding (bugs review #10): a rule naming every level gave a target
	// summing to π < 1, and Fit refused it, failing the whole run; it is
	// uninformative: skipped
	g.rules = append(g.rules, RuleInfo{ID: "all", Question: "s", Answer: evidence.RuleAnswer{Min: 0, Max: 3, Range: true}, Stat: evidence.RuleStat{Fired: 9, WilsonLow: 0.9}})
	g.fires[5] = []int{4}
	if tg, ok := sq.ruleTarget(src, g, 5); ok {
		t.Fatalf("an uninformative rule taught: %v", tg)
	}
	g.fires[6] = []int{3, 4}
	if tg, ok := sq.ruleTarget(src, g, 6); !ok || tg.Check(4) != nil {
		t.Fatalf("with an informative rule beside it: %v %v", tg, ok)
	}
}

func TestTeachersAsData(t *testing.T) {
	t.Parallel()
	names := map[string]bool{"reply_needed": true, "category": true}
	tc, err := parseTeachers(teachersJSON, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Sources) != 4 || tc.Sources[0].Name != "manual" || tc.Sources[3].Name != "rules" {
		t.Fatalf("precedence order %+v", tc.Sources)
	}
	// a user file over the shipped sources (layer1.teachers.file)
	cfg := testConfig(t, nil)
	over := `{"sources": {"rules": null}, "questions": {"reply_needed": {"decider": null, "judge": {"weight": 9}}}}`
	if err := os.WriteFile(cfg.Paths().Resolve(cfg.String(SettingTeachersFile)), []byte(over), 0o600); err != nil {
		t.Fatal(err)
	}
	tc, err = LoadTeachers(cfg, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Sources) != 3 {
		t.Fatalf("null removes a source: %+v", tc.Sources)
	}
	got := tc.For("reply_needed")
	if len(got) != 2 || got[1].Name != "judge" || got[1].Weight != 9 || got[1].LabelSource != "judge" {
		t.Fatalf("per question: %+v", got)
	}
	if len(tc.For("category")) != 3 {
		t.Fatal("another question keeps every source")
	}
	for name, doc := range map[string]string{
		"unknown from":           `{"sources": {"x": {"from": "magic", "precedence": 1, "weight": 1}}}`,
		"shared precedence":      `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 1}, "b": {"from": "rules", "precedence": 1, "weight": 1}}}`,
		"zero weight":            `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 0}}}`,
		"eval decisions":         `{"sources": {"a": {"from": "decisions", "decider": "triage", "temperature": 1, "precedence": 1, "weight": 1, "eval": true}}}`,
		"no temperature":         `{"sources": {"a": {"from": "decisions", "decider": "triage", "precedence": 1, "weight": 1}}}`,
		"stored key no provider": `{"sources": {"a": {"from": "decisions", "decider": "x/y", "temperature": 1, "precedence": 1, "weight": 1}}}`,
		"labels no source":       `{"sources": {"a": {"from": "labels", "precedence": 1, "weight": 1}}}`,
		"unknown field":          `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 1, "colour": 1}}}`,
		"unknown question":       `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 1}}, "questions": {"zz": {}}}`,
		"unknown source of q":    `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 1}}, "questions": {"category": {"b": null}}}`,
		"bad unmeasured":         `{"sources": {"a": {"from": "rules", "precedence": 1, "weight": 1, "unmeasured_precision": 1.5}}}`,
		"empty sources":          `{"sources": {}}`,
		// rules review #8: layer 1 configured to train on its own answers
		"layer 1 teaching itself": `{"sources": {"a": {"from": "decisions", "decider": "layer1", "provider": "subq", "temperature": 1, "precedence": 1, "weight": 1}}}`,
	} {
		if _, err := parseTeachers([]byte(doc), names); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// every problem at once
	_, err = parseTeachers([]byte(`{"sources": {"a": {"from": "x", "precedence": 0, "weight": -1}}}`), names)
	if err == nil || strings.Count(err.Error(), "source a") < 3 {
		t.Errorf("all problems: %v", err)
	}
}

func TestRequirementsAsData(t *testing.T) {
	t.Parallel()
	names := map[string]bool{"effort": true}
	r, err := parseRequirements(requirementsJSON, names)
	// rules review #1: the gate needs 381 error-free certification labels
	// on at least min_coverage (0.5) of a certification part that is half
	// the labels (layer1.certify.fraction 0.5): 1,524, not 381
	if err != nil || r.For("effort").TargetAccuracy != 0.99 || r.For("effort").Requested(0.5) != 1524 || len(r.Report) != 4 {
		t.Fatalf("%+v %v", r, err)
	}
	if n := r.For("effort").Requested(1); n != 762 {
		t.Fatalf("every label certifying: %d", n)
	}
	r, err = parseRequirements([]byte(`{"defaults": {"min_effective_train": 200, "min_eval_labels": 50, "target_accuracy": 0.85, "min_coverage": 0.5, "z": 1.96, "request_labels": null, "certify_sources": ["manual"]},
		"questions": {"effort": {"target_accuracy": 0.7, "request_labels": 80}}}`), names)
	if err != nil || r.For("effort").TargetAccuracy != 0.7 || r.For("effort").MinCoverage != 0.5 || r.For("effort").Requested(0.5) != 80 ||
		r.For("effort").CertifySources[0] != "manual" {
		t.Fatalf("override %+v %v", r.For("effort"), err)
	}
	// the digest covers every per-question override (bugs review #3)
	r2, _ := parseRequirements([]byte(`{"defaults": {"min_effective_train": 200, "min_eval_labels": 50, "target_accuracy": 0.85, "min_coverage": 0.5, "z": 1.96, "request_labels": null, "certify_sources": ["manual"]},
		"questions": {"effort": {"target_accuracy": 0.71, "request_labels": 80}}}`), names)
	if r2 == nil || r2.Digest == r.Digest {
		t.Fatal("a per-question override does not change the digest")
	}
	for name, doc := range map[string]string{
		"accuracy 1":        `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 1, "min_coverage": 0.5, "z": 1}}`,
		"coverage 0":        `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 0.8, "min_coverage": 0, "z": 1}}`,
		"unknown question":  `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 0.8, "min_coverage": 0.5, "z": 1}, "questions": {"zz": {}}}`,
		"unknown field":     `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 0.8, "min_coverage": 0.5, "z": 1, "x": 1}}`,
		"request 0":         `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 0.8, "min_coverage": 0.5, "z": 1, "request_labels": 0, "certify_sources": ["judge"]}}`,
		"no certify source": `{"defaults": {"min_effective_train": 1, "min_eval_labels": 2, "target_accuracy": 0.8, "min_coverage": 0.5, "z": 1, "certify_sources": []}}`,
	} {
		if _, err := parseRequirements([]byte(doc), names); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// The kNN uses only labelled messages dated strictly before the message,
// never itself nor its conversation, and hidden ones never.
func TestKNNIsCausal(t *testing.T) {
	t.Parallel()
	set := &vector.Set{}
	put := func(id int64, x float32) {
		q := vector.Quantize([]float32{x, 1 - x})
		set.Put(id, q.Values, q.Scale)
	}
	for id := int64(1); id <= 4; id++ {
		put(id, 0.5)
	}
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	x := newKNN(set, KNNConfig{K: 10, Temperature: 0.05, Prior: 1e-6}, []float64{0.5, 0.5})
	x.add(1, OneHot(0), 1, day(1), "g1")
	x.add(2, OneHot(1), 1, day(5), "g2") // later than the query
	x.add(3, OneHot(1), 1, day(1), "g9") // the query's own conversation
	out := make([]float64, 2)
	x.logProbs(4, day(3), "g9", nil, out)
	if math.Exp(out[0]) < 0.99 {
		t.Fatalf("only the earlier, other-conversation neighbour counts: %v", out)
	}
	x.logProbs(4, day(3), "g9", map[int64]bool{1: true}, out)
	if math.Abs(math.Exp(out[0])-0.5) > 1e-6 {
		t.Fatalf("a hidden neighbour never counts (prior only): %v", out)
	}
	x.logProbs(99, day(3), "g", nil, out) // no vector
	if math.Abs(math.Exp(out[1])-0.5) > 1e-6 {
		t.Fatalf("no vector: the prior %v", out)
	}
}

// The first source with a valid target trains; invalid and refused
// answers are counted.
func TestBuildSetPrecedence(t *testing.T) {
	t.Parallel()
	cq := q(engine.Choice)
	b := NewDesign(0, 1)
	for id := int64(1); id <= 4; id++ {
		if err := b.Add(id, nil, []int32{0}); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := b.Build()
	soft := func(best string) engine.Answer {
		p := map[string]float64{"a": 0.1, "b": 0.1, "c": 0.1}
		p[best] = 0.8
		return engine.Answer{Type: engine.Choice, Choice: best, Probabilities: p}
	}
	p := &taxonomy.Profile{Questions: []engine.Question{*cq}}
	compact, err := readDecisions(t.Context(), answersOnly{map[int64]map[string]engine.Answer{
		1: {"c": soft("b")}, 2: {"c": soft("c")}, 3: {"c": {Type: engine.Choice, Refused: true}},
		4: {"c": {Type: engine.Choice, Probabilities: map[string]float64{"a": 0.2}}},
		5: {"c": soft("a")}, // no design row: skipped
	}}, store.TriageKey{}, p, d, 0.02)
	if err != nil {
		t.Fatal(err)
	}
	g := &gathered{
		labels:    map[string]map[int64]map[string]string{"judge": {1: {"c": `"a"`}, 4: {"c": `"zz"`}}},
		decisions: map[string]map[string]*compactAnswers{"decider": compact},
	}
	sources := []Source{
		{Name: "judge", From: FromLabels, LabelSource: "judge", Precedence: 2, Weight: 2, Eval: true},
		{Name: "decider", From: FromDecisions, Decider: "triage", Precedence: 3, Weight: 1, Temperature: 1},
	}
	qs, err := buildSet(p, cq, Multinomial, sources, g, d, func(id int64) string {
		return map[int64]string{1: "g1", 2: "g2"}[id]
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs.train) != 2 || qs.train[0].Weight != 2 || qs.train[0].Target.Class(3) != 0 || qs.train[1].Target.Class(3) != 2 {
		t.Fatalf("train %+v", qs.train)
	}
	if len(qs.eval) != 1 || qs.eval[0].ID != 1 || qs.eval[0].Class != 0 || qs.eval[0].Group != "g1" {
		t.Fatalf("eval %+v", qs.eval)
	}
	if ex := trainingSet(qs, map[int]bool{0: true}); len(ex) != 1 || ex[0].Row != 1 {
		t.Fatalf("a held-out evaluation message has no target of any source: %+v", ex)
	}
	c := qs.counts
	if c["judge"].Used != 1 || c["judge"].Dropped != 1 || c["decider"].Targets != 2 || c["decider"].Used != 1 ||
		c["decider"].Refused != 1 || c["decider"].Dropped != 1 {
		t.Fatalf("counts judge %+v decider %+v", *c["judge"], *c["decider"])
	}
	if math.Abs(qs.neff-1.8) > 1e-12 { // weights 2 and 1: 9/5
		t.Fatalf("effective n %v", qs.neff)
	}
}

// answersOnly is Inputs giving stored answers only (the rest unused).
type answersOnly struct {
	answers map[int64]map[string]engine.Answer
}

func (a answersOnly) Decisions(_ context.Context, _ store.TriageKey, fn func(int64, map[string]engine.Answer) error) error {
	for _, id := range slices.Sorted(maps.Keys(a.answers)) {
		if err := fn(id, a.answers[id]); err != nil {
			return err
		}
	}
	return nil
}
func (answersOnly) Members(context.Context) ([]Member, error)            { return nil, nil }
func (answersOnly) MemberIDs(context.Context) ([]int64, error)           { return nil, nil }
func (answersOnly) Vectors(context.Context) (string, *vector.Set, error) { return "", nil, nil }
func (answersOnly) Cells(context.Context) (Layout, map[int64][]int32, error) {
	return Layout{}, nil, nil
}
func (answersOnly) Labels(context.Context) ([]store.LabelRow, error) { return nil, nil }
func (answersOnly) Rules(context.Context, map[int64]map[string]json.RawMessage) ([]RuleInfo, map[int64][]int, error) {
	return nil, nil, nil
}

// Regression (round 1, e5-nocal): labels that never contradict the head
// let the unpenalised temperature grow without bound (A = 77 on 119
// one-sided labels); the prior keeps it moderate.
func TestCalibrationOfOneSidedLabelsStaysModerate(t *testing.T) {
	t.Parallel()
	var lr [][]float64
	var labels []int
	for range 119 {
		lr = append(lr, []float64{math.Log(0.9), math.Log(0.1)})
		labels = append(labels, 0)
	}
	c, err := FitCombiner(t.Context(), lr, nil, labels, true, 1)
	if err != nil || !(c.A > 1) || c.A > 12 {
		t.Fatalf("A = %v %v", c.A, err)
	}
}

func TestScoreHeadIsASetting(t *testing.T) {
	t.Parallel()
	for want, set := range map[Kind]map[string]any{Multinomial: nil, Ordinal: {SettingScoreHead: "ordinal"}} {
		plan, err := NewPlan(testConfig(t, set))
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.headOf(engine.Score); got != want {
			t.Errorf("score head %s, want %s", got, want)
		}
		if plan.headOf(engine.Noul) != Binary || plan.headOf(engine.Choice) != Multinomial {
			t.Error("other question types keep their heads")
		}
	}
}

// Finding (rules review #8): abstained answers became training targets.
// They never are: counted apart.
func TestAbstainedAnswersNeverTeach(t *testing.T) {
	t.Parallel()
	cq := q(engine.Choice)
	b := NewDesign(0, 1)
	_ = b.Add(1, nil, []int32{0})
	d, _ := b.Build()
	p := &taxonomy.Profile{Questions: []engine.Question{*cq}}
	compact, err := readDecisions(t.Context(), answersOnly{map[int64]map[string]engine.Answer{
		1: {"c": {Type: engine.Choice, Abstain: engine.AbstainBelowTarget, Probabilities: map[string]float64{"a": 0.5, "b": 0.3, "c": 0.2}}},
	}}, store.TriageKey{}, p, d, 0.02)
	if err != nil {
		t.Fatal(err)
	}
	g := &gathered{decisions: map[string]map[string]*compactAnswers{"decider": compact}}
	src := []Source{{Name: "decider", From: FromDecisions, Decider: "triage", Precedence: 1, Weight: 1, Temperature: 1}}
	qs, err := buildSet(p, cq, Multinomial, src, g, d, func(int64) string { return "" }, 0)
	if err != nil || len(qs.train) != 0 || qs.counts["decider"].Abstained != 1 {
		t.Fatalf("train %+v counts %+v %v", qs.train, qs.counts["decider"], err)
	}
}
