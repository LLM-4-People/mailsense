package layer1

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// profileWithCutoff is a profile whose yes/no questions without a role
// cutoff of their own use cutoff.
func profileWithCutoff(t *testing.T, cutoff float64) (*taxonomy.Profile, *engine.Question) {
	t.Helper()
	p, err := taxonomy.Parse([]byte(`{"thresholds": {"noul_default": `+
		strconv.FormatFloat(cutoff, 'f', -1, 64)+`}}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	q, ok := p.Question("has_signature") // its role has no cutoff: noul_default
	if !ok || q.Type != engine.Noul {
		t.Fatalf("has_signature is not a yes/no question: %+v", q)
	}
	if got := p.Cutoff(q.Name); got != cutoff {
		t.Fatalf("cutoff %v, want %v", got, cutoff)
	}
	return p, q
}

// assertNoulCutoff exercises the one stored-label class rule end to end: the
// training/evaluation class, the review suggestion and the certification of
// the route prediction must all agree at the profile's cutoff. wantClass is
// the golden side (yes = 1), independent of selective.Decide.
func assertNoulCutoff(t *testing.T, cutoff, value float64, wantClass int, source string) {
	t.Helper()
	_, q := profileWithCutoff(t, cutoff)
	raw := strconv.FormatFloat(value, 'f', -1, 64)
	labels := map[string]map[int64]map[string]string{
		source: {1: {q.Name: raw}},
	}

	target, class, err := labelTarget(q, raw, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	probs := []float64{1 - value, value}
	if decided, _ := selective.Decide(engine.Noul, probs, cutoff); decided != wantClass {
		t.Fatalf("cutoff %v value %v: selective.Decide says %d, golden class is %d", cutoff, value, decided, wantClass)
	}
	if class != wantClass {
		t.Fatalf("cutoff %v value %v: label class %d, want %d", cutoff, value, class, wantClass)
	}
	if mass := target.Mass(2)[1]; math.Abs(mass-value) > 1e-12 {
		t.Fatalf("the soft target moved: P(yes)=%v, want %v", mass, value)
	}

	eval, ok := evalOf(q, []Source{{Name: source, From: FromLabels, LabelSource: source, Eval: true}}, labels, 1, cutoff)
	if !ok || eval.class != wantClass || eval.source != source {
		t.Fatalf("cutoff %v value %v: eval %+v (%v), want class %d from %s", cutoff, value, eval, ok, wantClass, source)
	}

	// The review suggestion reads a judge label under the same cutoff: it
	// must be the side the certified route answer takes.
	judge := map[string]map[int64]map[string]string{store.LabelJudge: {1: {q.Name: raw}}}
	suggestion, hasSuggestion, offer := reviewCandidate(q, judge, 1, cutoff)
	if !offer || !hasSuggestion || suggestion != (wantClass == 1) {
		t.Fatalf("cutoff %v value %v: suggestion %v (%v/%v), want %v", cutoff, value, suggestion, hasSuggestion, offer, wantClass == 1)
	}

	// The route predicts the stored value's distribution; the evaluation
	// label's class makes that prediction correct and certifiable.
	holdout := &Holdout{
		Select:  []HeldItem{{ID: 1, Probs: probs}},
		Certify: []HeldItem{{ID: 1, Probs: probs}},
	}
	req := Requirement{TargetAccuracy: 0.5, Z: 1, CertifySources: []string{source}}
	cert := holdout.certify(engine.Noul, map[int64]evalLabel{1: {class: eval.class, source: eval.source}}, cutoff, req)
	if !cert.Certified || cert.Answered != 1 || cert.Errors != 0 {
		t.Fatalf("cutoff %v value %v: certification %+v", cutoff, value, cert)
	}
}

// A stored yes/no value's class is selective.Decide's side at the profile's
// cutoff, not a hardcoded 0.5: at cutoff 0.3 a stored 0.4 is yes for the
// training target, the evaluation label, the review suggestion and the
// certification of the route prediction.
func TestStoredNoulClassFollowsTheProfileCutoff(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		cutoff, value float64
		want          int
	}{
		{0.3, 0.4, 1}, // the defect's golden case
		{0.2, 0.19, 0}, {0.2, 0.2, 1}, {0.2, 0.21, 1},
		{0.5, 0.49, 0}, {0.5, 0.5, 1}, {0.5, 0.51, 1},
		{0.8, 0.79, 0}, {0.8, 0.8, 1}, {0.8, 0.81, 1},
	} {
		c := c
		t.Run(strconv.FormatFloat(c.cutoff, 'f', -1, 64)+"/"+strconv.FormatFloat(c.value, 'f', -1, 64), func(t *testing.T) {
			assertNoulCutoff(t, c.cutoff, c.value, c.want, "judge")
		})
	}
}

// A manual label is the ground truth: it wins precedence and its class uses
// the same cutoff rule.
func TestStoredNoulClassOfAManualGroundTruth(t *testing.T) {
	t.Parallel()
	assertNoulCutoff(t, 0.3, 0.4, 1, store.LabelManual)
}

// writeThresholdProfile replaces the profile file with a yes/no cutoff.
func writeThresholdProfile(t *testing.T, w *world, cutoff float64) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"thresholds": map[string]any{"noul_default": cutoff}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.cfg.Paths().Resolve(w.cfg.String(taxonomy.ProfileFileSetting)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The review queue must pass the profile's cutoff into the decoded
// suggestion: a stored 0.4 under a 0.3 cutoff is "yes".
func TestReviewSuggestionUsesTheProfileCutoff(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	writeThresholdProfile(t, w, 0.3)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Cutoff("has_signature"); got != 0.3 {
		t.Fatalf("the profile cutoff is %v, want 0.3", got)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], "has_signature", "0.4", store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "has_signature", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Tasks) != 1 {
		t.Fatalf("review offers %d tasks, want the one stored label", len(queue.Tasks))
	}
	task := queue.Tasks[0]
	if task.MessageID != w.ids[0] || task.SuggestionSource != store.LabelJudge {
		t.Fatalf("review task %+v", task)
	}
	if suggestion, ok := task.Suggestion.(bool); !ok || !suggestion {
		t.Fatalf("a stored 0.4 at cutoff 0.3 suggests %T %v, want true", task.Suggestion, task.Suggestion)
	}
}
