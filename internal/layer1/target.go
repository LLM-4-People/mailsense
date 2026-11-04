package layer1

import (
	"errors"
	"fmt"
	"math"
)

// Part is one component of a target: weight W on the classes Lo..Hi (a
// single class when Lo == Hi; a score's level range from a rule).
type Part struct {
	W      float64 `json:"w"`
	Lo, Hi int
}

// Target is a soft target over a question's classes (choice: options in
// order; yes/no: 0 = no, 1 = yes; score: levels): the loss is
// −Σ W·log P(Lo..Hi). Parts' weights sum to 1.
type Target []Part

// Errors building targets.
var (
	ErrBadTarget = errors.New("layer1: invalid target")
)

// OneHot is the target of one class.
func OneHot(k int) Target { return Target{{W: 1, Lo: k, Hi: k}} }

// Soft is the target of a distribution over classes (zero classes left
// out). The distribution must be finite, non-negative and sum to 1
// within tolerance (it is renormalised).
func Soft(p []float64, tolerance float64) (Target, error) {
	var sum float64
	for _, x := range p {
		if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
			return nil, fmt.Errorf("%w: probability %v", ErrBadTarget, x)
		}
		sum += x
	}
	if math.Abs(sum-1) > tolerance || sum == 0 {
		return nil, fmt.Errorf("%w: probabilities sum to %v", ErrBadTarget, sum)
	}
	var t Target
	for k, x := range p {
		if x > 0 {
			t = append(t, Part{W: x / sum, Lo: k, Hi: k})
		}
	}
	return t, nil
}

// Sharpen raises a soft target to the power 1/temperature and
// renormalises (temperature 1: unchanged; → 0: the argmax). Ranges are
// kept as they are.
func (t Target) Sharpen(temperature float64) Target {
	if temperature == 1 || len(t) < 2 {
		return t
	}
	out := make(Target, len(t))
	var sum float64
	for i, p := range t {
		out[i] = p
		out[i].W = math.Pow(p.W, 1/temperature)
		sum += out[i].W
	}
	for i := range out {
		out[i].W /= sum
	}
	return out
}

// Check validates the target for k classes.
func (t Target) Check(k int) error {
	if len(t) == 0 {
		return fmt.Errorf("%w: empty", ErrBadTarget)
	}
	var sum float64
	for _, p := range t {
		if p.Lo < 0 || p.Hi < p.Lo || p.Hi >= k || !(p.W >= 0) || math.IsInf(p.W, 0) {
			return fmt.Errorf("%w: part %+v of %d classes", ErrBadTarget, p, k)
		}
		sum += p.W
	}
	if math.Abs(sum-1) > 1e-9 {
		return fmt.Errorf("%w: weights sum to %v", ErrBadTarget, sum)
	}
	return nil
}

// Mass spreads the target over classes (a range evenly): the class
// distribution it stands for (priors, counts).
func (t Target) Mass(k int) []float64 {
	out := make([]float64, k)
	for _, p := range t {
		n := float64(p.Hi - p.Lo + 1)
		for c := p.Lo; c <= p.Hi; c++ {
			out[c] += p.W / n
		}
	}
	return out
}

// Class is the target's most likely class (ties: the lower).
func (t Target) Class(k int) int {
	m := t.Mass(k)
	best := 0
	for c := range m {
		if m[c] > m[best] {
			best = c
		}
	}
	return best
}

// exactSum is the tolerance of a two-class target built as 1 − p and p
// (rounding only).
const exactSum = 1e-9
