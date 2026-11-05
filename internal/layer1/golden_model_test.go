package layer1

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// The golden fixture is the shared test world at one fixed size: its
// messages, vectors, decider answers and judge labels are all seeded, so
// the trained head and every answer it commits must be byte-identical
// forever. The hashes are the parity proof for representation changes
// that must not move a single weight; they were captured on the code before
// the packed design and the compact answer commit (2026-11-03).
const (
	goldenWorldSize = 60
	// 2026-11-05: the λ path now omits an unmeasurable (infinite) NLL and
	// falls back to the default λ instead of λ 0; the golden world's
	// category has a step no fold could score, so its final fit changed.
	goldenCategoryParams = "ed6fee01bd62e02ae372b3d263ddfa7c"
	goldenPredictions    = "9d0a68e0cae188c4d5e232f91a8eade5"
	goldenAnswers        = "72ab0e141b0b9d48bce7bafbd9a9da2e"
	goldenRoutes         = "f2d54cd96dd3accbc7c2823ef5c588bf"
	goldenPreviews       = "490bcfa485909283e73b0356a7d38594"
)

// goldenPredictionHash hashes every member's calibrated distribution
// (float64 bits, in member order) of one question.
func goldenPredictionHash(t *testing.T, tr *Trained, question string, ids []int64) string {
	t.Helper()
	var raw []byte
	for _, id := range ids {
		p, ok := tr.Predict(question, id)
		if !ok {
			t.Fatalf("message %d has no %s prediction", id, question)
		}
		for _, x := range p {
			raw = append(raw, fmt.Sprint(math.Float64bits(x))...)
			raw = append(raw, ',')
		}
		raw = append(raw, ';')
	}
	return digest.Hex(raw, 32)
}

// goldenAnswerHash hashes every committed decision of one question
// (message id and its decoded answer, encoded without map ordering).
func goldenAnswerHash(t *testing.T, answers map[int64]map[string]engine.Answer, ids []int64, question string) string {
	t.Helper()
	var raw []byte
	for _, id := range ids {
		a, ok := answers[id][question]
		if !ok {
			t.Fatalf("message %d has no %s answer", id, question)
		}
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, fmt.Sprint(id)...)
		raw = append(raw, ':')
		raw = append(raw, b...)
		raw = append(raw, ';')
	}
	return digest.Hex(raw, 32)
}

// TestGoldenTrainingOutputIsStable trains the fixed world and pins the
// parameters, the calibrated verdicts and the committed answers. It is the
// proof that the packed design and the answer commit keep every result.
func TestGoldenTrainingOutputIsStable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, goldenWorldSize, map[string]any{"learning.test_mode": true})
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Train(t.Context(), plan, w.r.in, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := tr.Models["category"]
	if m == nil {
		t.Fatal("the category question did not train")
	}
	if got := digest.Hex(m.Encode(), 32); got != goldenCategoryParams {
		t.Errorf("category parameters = %s, want %s", got, goldenCategoryParams)
	}
	if got := goldenPredictionHash(t, tr, "category", w.ids); got != goldenPredictions {
		t.Errorf("category verdicts = %s, want %s", got, goldenPredictions)
	}

	rep := w.run()
	if rep.State != runstate.Succeeded {
		t.Fatalf("run %+v", rep)
	}
	if got := goldenAnswerHash(t, w.answers(), w.ids, "category"); got != goldenAnswers {
		t.Errorf("committed category answers = %s, want %s", got, goldenAnswers)
	}
	routes, err := w.st.LoadRouteAnswers(t.Context(), rep.RouteKey, w.ids)
	if err != nil {
		t.Fatal(err)
	}
	previews, err := w.st.LoadPreviewRecords(t.Context(), rep.RouteKey, w.ids)
	if err != nil {
		t.Fatal(err)
	}
	answers := w.answers()
	var decisionBytes, routeBytes, previewBytes int
	var routeRaw, previewRaw []byte
	for _, id := range w.ids {
		d, err := json.Marshal(answers[id])
		if err != nil {
			t.Fatal(err)
		}
		decisionBytes += len(d)
		b, err := json.Marshal(routes[id])
		if err != nil {
			t.Fatal(err)
		}
		routeBytes += len(b)
		routeRaw = append(append(routeRaw, b...), ';')
		b, err = json.Marshal(previews[id].Answers)
		if err != nil {
			t.Fatal(err)
		}
		previewBytes += len(b)
		previewRaw = append(append(previewRaw, b...), ';')
	}
	if got := digest.Hex(routeRaw, 32); got != goldenRoutes {
		t.Errorf("committed route answers = %s, want %s", got, goldenRoutes)
	}
	if got := digest.Hex(previewRaw, 32); got != goldenPreviews {
		t.Errorf("committed preview answers = %s, want %s", got, goldenPreviews)
	}
	if decisionBytes+routeBytes+previewBytes > 0 {
		t.Logf("staged payload: decision=%d route=%d preview=%d bytes (compact keeps %d of %d, %.0f%%)",
			decisionBytes, routeBytes, previewBytes, decisionBytes, decisionBytes+routeBytes+previewBytes,
			100*float64(decisionBytes)/float64(decisionBytes+routeBytes+previewBytes))
	}
}
