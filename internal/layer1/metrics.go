package layer1

import (
	"math"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// Prediction is one evaluated message: its labelled class and the
// predicted class probabilities.
type Prediction struct {
	ID    int64
	Label int
	Probs []float64
}

// Metrics measure a question's predictions against labels.
type Metrics struct {
	N           int     `json:"n"`
	Accuracy    float64 `json:"accuracy"`
	WithinOne   float64 `json:"within_one,omitempty"` // score: |predicted level − label| ≤ 1
	MAE         float64 `json:"mae,omitempty"`        // score: |expected level − label|
	NLL         float64 `json:"nll"`
	Brier       float64 `json:"brier"`
	ECE         float64 `json:"ece"`
	BalancedAcc float64 `json:"balanced_accuracy,omitempty"` // yes/no
	AUC         float64 `json:"auc,omitempty"`               // yes/no
	Kappa       float64 `json:"kappa"`
	Selective   []Point `json:"selective"` // accuracy of the most confident share, by coverage
	// Points are the coverage at each report target, point estimate and
	// Wilson lower bound, each chosen by scanning every cut of these same
	// predictions: diagnostics, never a certification (the model's
	// Certification is: chosen on the selection part, tested on the
	// certification part).
	Points []selective.TargetPoint `json:"points"`
	AURC   float64                 `json:"aurc"` // area under the risk–coverage curve
}

// Point is one point of the selective-accuracy curve.
type Point struct {
	Coverage float64 `json:"coverage"`
	Accuracy float64 `json:"accuracy"`
}

// eceBins is the number of equal-width confidence bins of the ECE: an
// algorithm constant of the metric (Guo et al. 2017 use 15, Naeini et al.
// 2015 10), not a setting — reports compare it across runs.
const eceBins = 10

// Measure computes the metrics of predictions (cutoff: a yes/no
// question's; z and report: the coverage at the report targets).
func Measure(t engine.QuestionType, preds []Prediction, cutoff, z float64, report []float64) Metrics {
	m := Metrics{N: len(preds)}
	if len(preds) == 0 {
		return m
	}
	rows := make([]selective.Ranked, len(preds))
	var pred, gold []string
	var scores []float64
	var positive []bool
	bins := make([][3]float64, eceBins) // count, Σ confidence, Σ correct
	perClass := map[int][2]int{}        // label → (n, correct)
	n := float64(len(preds))
	for i, p := range preds {
		c, conf := selective.Decide(t, p.Probs, cutoff)
		ok := c == p.Label
		rows[i] = selective.Ranked{ID: p.ID, Correct: ok, Confidence: conf}
		if ok {
			m.Accuracy++
		}
		m.NLL -= math.Log(math.Max(p.Probs[p.Label], 1e-300))
		for k, q := range p.Probs {
			y := 0.0
			if k == p.Label {
				y = 1
			}
			m.Brier += (q - y) * (q - y)
		}
		b := min(int(conf*eceBins), eceBins-1)
		bins[b][0]++
		bins[b][1] += conf
		if ok {
			bins[b][2]++
		}
		pred, gold = append(pred, strconv.Itoa(c)), append(gold, strconv.Itoa(p.Label))
		pc := perClass[p.Label]
		pc[0]++
		if ok {
			pc[1]++
		}
		perClass[p.Label] = pc
		switch t {
		case engine.Score:
			if abs(c-p.Label) <= 1 {
				m.WithinOne++
			}
			var e float64
			for k, q := range p.Probs {
				e += float64(k) * q
			}
			m.MAE += math.Abs(e - float64(p.Label))
		case engine.Noul:
			scores, positive = append(scores, p.Probs[1]), append(positive, p.Label == 1)
		}
	}
	m.Accuracy /= n
	m.NLL /= n
	m.Brier /= n
	m.WithinOne /= n
	m.MAE /= n
	for _, b := range bins {
		if b[0] > 0 {
			m.ECE += b[0] / n * math.Abs(b[2]/b[0]-b[1]/b[0])
		}
	}
	m.Kappa, _ = mathx.Kappa(pred, gold)
	if t == engine.Noul {
		m.AUC, _ = mathx.AUC(scores, positive)
		var sum float64
		for _, pc := range perClass {
			sum += float64(pc[1]) / float64(pc[0])
		}
		m.BalancedAcc = sum / float64(len(perClass))
	}
	rows = selective.Rank(rows)
	m.Points = selective.Targets(rows, report, z)
	correct := 0
	for i, r := range rows {
		if r.Correct {
			correct++
		}
		m.AURC += 1 - float64(correct)/float64(i+1)
	}
	m.AURC /= n
	for c := 10; c >= 1; c-- {
		k := max(1, int(math.Round(n*float64(c)/10)))
		hit := 0
		for _, r := range rows[:k] {
			if r.Correct {
				hit++
			}
		}
		m.Selective = append(m.Selective, Point{Coverage: float64(c) / 10, Accuracy: float64(hit) / float64(k)})
	}
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
