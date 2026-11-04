package layer1

import (
	"fmt"
	"math"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Kind is a head's model family.
type Kind string

// Head kinds: one per question type.
const (
	Multinomial Kind = "multinomial" // choice: softmax over the options
	Binary      Kind = "binary"      // yes/no: logistic
	Ordinal     Kind = "ordinal"     // score: cumulative logit (proportional odds, McCullagh 1980)
)

// KindOf is the head of a question type.
func KindOf(t engine.QuestionType) (Kind, error) {
	switch t {
	case engine.Choice:
		return Multinomial, nil
	case engine.Noul:
		return Binary, nil
	case engine.Score:
		return Ordinal, nil
	}
	return "", fmt.Errorf("layer1: no head for question type %q", t)
}

// head is one family's loss over linear outputs z (and its own extra
// parameters, the ordinal thresholds).
type head interface {
	kind() Kind
	classes() int
	outputs() int // linear outputs per row
	extra() int   // extra parameters
	// init sets the bias and extra parameters from the class prior.
	init(prior []float64, bias, extra []float64)
	// loss is −Σ W·log P(Lo..Hi) at z; it writes dℓ/dz into dz and adds
	// w·dℓ/dθ into dextra.
	loss(z, extra []float64, t Target, dz, dextra []float64, w float64) float64
	// logProbs writes the class log-probabilities.
	logProbs(z, extra []float64, out []float64)
}

func newHead(k Kind, classes int) (head, error) {
	if classes < 2 {
		return nil, fmt.Errorf("layer1: a head needs 2 classes or more, not %d", classes)
	}
	switch k {
	case Multinomial:
		return multinomial{k: classes}, nil
	case Binary:
		if classes != 2 {
			return nil, fmt.Errorf("layer1: a binary head has 2 classes, not %d", classes)
		}
		return logistic{}, nil
	case Ordinal:
		return ordinal{k: classes}, nil
	}
	return nil, fmt.Errorf("layer1: unknown head kind %q", k)
}

// Numerical helpers shared by the heads.

// softplus is log(1 + eˣ), stable.
func softplus(x float64) float64 {
	if x > 30 {
		return x + math.Exp(-x)
	}
	if x < -30 {
		return math.Exp(x)
	}
	return math.Log1p(math.Exp(x))
}

// logSigmoid is log σ(x) = −softplus(−x).
func logSigmoid(x float64) float64 { return -softplus(-x) }

func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

// logSumExp of v[lo..hi].
func logSumExp(v []float64, lo, hi int) float64 {
	m := math.Inf(-1)
	for i := lo; i <= hi; i++ {
		m = math.Max(m, v[i])
	}
	if math.IsInf(m, -1) {
		return m
	}
	var s float64
	for i := lo; i <= hi; i++ {
		s += math.Exp(v[i] - m)
	}
	return m + math.Log(s)
}

// minLogP keeps a class probability's logarithm finite.
const minLogP = -700
