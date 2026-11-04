package layer1

import (
	"math"
	"time"

	"github.com/LLM-4-People/mailsense/internal/vector"
)

// KNNConfig are the kNN distribution's settings (layer1.knn.*).
type KNNConfig struct {
	K           int
	Temperature float64
	Prior       float64 // α, the class prior's pseudo-weight
}

// knnMaxMessages bounds the population where the kNN pool is served: its
// scan is O(population) per message and question, so a larger mailbox
// would spend hours in the commit. The setting stays for small mailboxes
// and experiments; above this bound the pass trains without it.
const knnMaxMessages = 2000

// neighbour is one labelled message the kNN may use.
type neighbour struct {
	mass    []float64 // its target's class distribution
	weight  float64
	instant time.Time
	group   string
}

// knnIndex is the causal kNN of one question: for a message m, the k most
// similar labelled messages dated strictly before m, never m itself nor
// its conversation, give
//
//	p_c = (Σ_j w_j·e^{(s_j−1)/τ}·t_jc + α·π_c) / (Σ_j w_j·e^{(s_j−1)/τ} + α)
//
// with π the question's class prior.
type knnIndex struct {
	set    *vector.Set
	cfg    KNNConfig
	prior  []float64
	labels map[int64]neighbour
}

func newKNN(set *vector.Set, cfg KNNConfig, prior []float64) *knnIndex {
	return &knnIndex{set: set, cfg: cfg, prior: prior, labels: map[int64]neighbour{}}
}

// add makes a message a neighbour.
func (x *knnIndex) add(id int64, t Target, weight float64, instant time.Time, group string) {
	x.labels[id] = neighbour{mass: t.Mass(len(x.prior)), weight: weight, instant: instant, group: group}
}

// logProbs writes the kNN log-distribution of message id (its own vector,
// its instant, its conversation); hidden neighbours (a fold's held-out
// evaluation labels) do not count. A message without a vector gets the
// prior.
func (x *knnIndex) logProbs(id int64, instant time.Time, group string, hidden map[int64]bool, out []float64) {
	k := len(x.prior)
	num := make([]float64, k)
	den := x.cfg.Prior
	for c := range k {
		num[c] = x.cfg.Prior * x.prior[c]
	}
	if row := x.set.Row(id); row != nil && !instant.IsZero() {
		q := row.Floats()
		if norm := vector.Norm(q); norm > 0 {
			for i := range q {
				q[i] /= float32(norm)
			}
			keep := func(j int64) bool {
				n, ok := x.labels[j]
				return ok && j != id && !hidden[j] && n.group != group && !n.instant.IsZero() && n.instant.Before(instant)
			}
			for _, s := range x.set.TopFunc(q, x.cfg.K, keep) {
				n := x.labels[s.ID]
				w := n.weight * math.Exp((float64(s.Sim)-1)/x.cfg.Temperature)
				for c := range k {
					num[c] += w * n.mass[c]
				}
				den += w
			}
		}
	}
	for c := range k {
		if den > 0 {
			out[c] = math.Max(math.Log(num[c]/den), minLogP)
		} else {
			out[c] = math.Log(1 / float64(k))
		}
	}
}
