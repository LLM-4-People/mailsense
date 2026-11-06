//go:build fixture

package layer1

// E8: zero-shot semantics from the taxonomy itself. No rule and no wordlist:
// each question's own instructions and class descriptions are embedded with
// the same local model as the mail, and a message is classified by the
// nearest class description. It is the language-independent alternative to
// hand-written cue phrases; the gold DEV split measures whether it is worth
// wiring in.

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// zeroClass is one class description of a question and its query vector.
type zeroClass struct {
	class int
	vec   []float32
}

// zeroQuestion is one question's zero-shot result on the dev split.
type zeroQuestion struct {
	Question string  `json:"question"`
	Type     string  `json:"type"`
	N        int     `json:"n"`
	Pending  int     `json:"pending"`
	Accuracy float64 `json:"accuracy"`
	Majority float64 `json:"majority"`
	MAE      float64 `json:"mae,omitempty"`
}

// zeroShotRun embeds the taxonomy's own descriptions and measures the
// nearest-description classifier on the gold dev split. Test stays sealed.
func zeroShotRun(t *testing.T, ctx context.Context, profile *taxonomy.Profile, e engine.Embedder,
	set *vector.Set, gold *goldset.Set, rows map[string]int64, run string) {
	t.Helper()
	trainMajority := map[string]int{}
	counts := map[string]map[int]int{}
	for _, labels := range gold.Of(goldset.Train) {
		for i := range profile.Questions {
			q := &profile.Questions[i]
			raw, ok := labels[q.Name]
			if !ok {
				continue
			}
			_, class, err := labelTarget(q, string(raw), profile.Cutoff(q.Name))
			if err != nil {
				t.Fatalf("train label %s: %v", q.Name, err)
			}
			if counts[q.Name] == nil {
				counts[q.Name] = map[int]int{}
			}
			counts[q.Name][class]++
		}
	}
	for name, c := range counts {
		best, n := 0, -1
		for class, k := range c {
			if k > n {
				best, n = class, k
			}
		}
		trainMajority[name] = best
	}

	var out []zeroQuestion
	for i := range profile.Questions {
		q := &profile.Questions[i]
		classes, err := zeroClasses(ctx, e, q)
		if err != nil {
			t.Fatal(err)
		}
		zq := zeroQuestion{Question: q.Name, Type: string(q.Type)}
		hits, major, n, pending := 0, 0, 0, 0
		var absErr float64
		for id, labels := range gold.Of(goldset.Dev) {
			raw, ok := labels[q.Name]
			if !ok {
				continue
			}
			_, want, err := labelTarget(q, string(raw), profile.Cutoff(q.Name))
			if err != nil {
				t.Fatalf("dev label %s: %v", q.Name, err)
			}
			row, ok := rows[id]
			if !ok {
				t.Fatalf("dev id %s names no owner message", id)
			}
			qv := set.Row(row)
			if qv == nil {
				pending++
				n++
				continue
			}
			got, scores := zeroPredict(qv.Floats(), classes)
			n++
			if got == want {
				hits++
			}
			if got == trainMajority[q.Name] {
				major++
			}
			if q.Type == engine.Score {
				absErr += math.Abs(float64(zeroExpect(scores)) - float64(want))
			}
		}
		zq.N, zq.Pending = n, pending
		if n > 0 {
			zq.Accuracy = float64(hits) / float64(n)
			zq.Majority = float64(major) / float64(n)
			zq.MAE = absErr / float64(n)
		}
		t.Logf("LAYER1 zero %-18s n=%3d pending=%d accuracy %.3f majority %.3f mae %.2f",
			q.Name, zq.N, zq.Pending, zq.Accuracy, zq.Majority, zq.MAE)
		out = append(out, zq)
	}
	mean, meanMajority := 0.0, 0.0
	for _, z := range out {
		mean += z.Accuracy
		meanMajority += z.Majority
	}
	if len(out) > 0 {
		mean /= float64(len(out))
		meanMajority /= float64(len(out))
	}
	t.Logf("LAYER1 zero mean accuracy over %d questions: zero-shot %.4f majority %.4f", len(out), mean, meanMajority)
	writeJSON(t, filepath.Join(os.Getenv("LAYER1_REPORT_DIR"), "layer1-"+run+".json"), map[string]any{
		"experiment": "e8-definitions", "run": run, "mean_accuracy": mean, "mean_majority": meanMajority, "questions": out})
}

// zeroClasses embeds one question's class descriptions as retrieval queries:
// the question's instructions plus what the class means, exactly as the
// taxonomy states it. No hand-written word list enters the classifier.
func zeroClasses(ctx context.Context, e engine.Embedder, q *engine.Question) ([]zeroClass, error) {
	var texts []engine.Text
	switch q.Type {
	case engine.Noul:
		texts = []engine.Text{
			{Purpose: engine.PurposeQuery, Body: q.Instructions + "\nThis is false: " + q.False},
			{Purpose: engine.PurposeQuery, Body: q.Instructions + "\nThis is true: " + q.True},
		}
	case engine.Choice:
		for _, o := range q.Options {
			d := ""
			if o.Description != nil {
				d = *o.Description
			}
			texts = append(texts, engine.Text{Purpose: engine.PurposeQuery, Body: q.Instructions + "\n" + o.Name + ": " + d})
		}
	default:
		for i, level := range q.Levels {
			texts = append(texts, engine.Text{Purpose: engine.PurposeQuery,
				Body: q.Instructions + "\nLevel " + strconv.Itoa(i) + ": " + level})
		}
	}
	emb, err := e.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("zero-shot %s: %w", q.Name, err)
	}
	if len(emb.Vectors) != len(texts) {
		return nil, fmt.Errorf("zero-shot %s: %d vectors for %d texts", q.Name, len(emb.Vectors), len(texts))
	}
	out := make([]zeroClass, len(texts))
	for i := range texts {
		out[i] = zeroClass{class: i, vec: emb.Vectors[i]}
	}
	return out, nil
}

// zeroPredict returns the nearest class (cosine, both vectors unit length)
// and every class's score.
func zeroPredict(msg []float32, classes []zeroClass) (int, []float64) {
	scores := make([]float64, len(classes))
	best, bestScore := 0, math.Inf(-1)
	for i, c := range classes {
		s := dot(msg, c.vec)
		scores[i] = s
		if s > bestScore {
			best, bestScore = i, s
		}
	}
	return best, scores
}

// zeroExpect is the softmax expectation of a score question's level, with a
// fixed temperature (0.1: cosine scores of short descriptions are tightly
// packed; the ranking, and therefore the accuracy, does not depend on it).
func zeroExpect(scores []float64) int {
	const temperature = 0.1
	maxScore := math.Inf(-1)
	for _, s := range scores {
		maxScore = math.Max(maxScore, s)
	}
	var sum, weighted float64
	for i, s := range scores {
		p := math.Exp((s - maxScore) / temperature)
		sum += p
		weighted += p * float64(i)
	}
	if sum == 0 {
		return 0
	}
	return int(math.Round(weighted / sum))
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}
