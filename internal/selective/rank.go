package selective

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// Ranked is one answer of a selective evaluation: whether it is right and
// how confident it was. HasClass says Label/Predicted are meaningful (the
// class it was labelled with and the class the rule answered). Class 0 is a
// real class, so the flag is explicit rather than a zero sentinel; a caller
// that supplies no classes leaves it false and gets no per-class report.
// Abstained marks a certification row the rule did not answer (a fixed
// cascade below every gate). It is meaningful only to CertifyFixed: such a
// row stays eligible for the per-class report but is not an answer, so a
// class the rule never answered can surface as collapsed. The zero value is
// an answer, so a caller that does not model abstention keeps the old
// behavior.
type Ranked struct {
	ID         int64
	Correct    bool
	Confidence float64
	HasClass   bool
	Label      int
	Predicted  int
	Abstained  bool
}

// Rank sorts answers most confident first; ties by a digest of the id
// (deterministic and unbiased).
func Rank(rows []Ranked) []Ranked {
	out := slices.Clone(rows)
	tie := func(id int64) string { return digest.Hex([]byte(strconv.FormatInt(id, 10)), 16) }
	slices.SortFunc(out, func(a, b Ranked) int {
		if c := cmp.Compare(b.Confidence, a.Confidence); c != 0 {
			return c
		}
		return cmp.Compare(tie(a.ID), tie(b.ID))
	})
	return out
}

// TargetPoint is the largest most-confident share answered at a
// precision target: by the point estimate (its precision ≥ the target)
// and by the Wilson lower bound at z (the lower bound ≥ the target), with
// the lowest confidence each answers (an answer below it abstains;
// NoThreshold when no share qualifies). Both are chosen by scanning every
// cut of the same answers, so neither is a certification on its own
// (Certify is): the scan picks the best of many cuts.
type TargetPoint struct {
	Target      float64 `json:"target"`
	Coverage    float64 `json:"coverage"`
	Threshold   float64 `json:"threshold"`
	CoverageLB  float64 `json:"coverage_lb"`
	ThresholdLB float64 `json:"threshold_lb"`
	// Covered and Errors are the counts of the point-estimate share.
	Covered int `json:"covered"`
	Errors  int `json:"errors"`
}

// NoThreshold is the threshold of a target no share reaches: above every
// value its callers rank — confidences are probabilities, at most 1, and
// a route gate is such a confidence — so every answer abstains. It must
// stay above every real cut: a one-hot answer is a real gate at 1.0.
const NoThreshold = 4.0

// Targets measures ranked answers at each precision target. A share is
// only cut between two different confidences (an answer can never be
// told apart from another of the same confidence).
func Targets(ranked []Ranked, targets []float64, z float64) []TargetPoint {
	out := make([]TargetPoint, len(targets))
	n := float64(len(ranked))
	for i, t := range targets {
		out[i] = TargetPoint{Target: t, Threshold: NoThreshold, ThresholdLB: NoThreshold}
	}
	correct := 0
	for k, r := range ranked {
		if r.Correct {
			correct++
		}
		m := k + 1
		if m < len(ranked) && ranked[m].Confidence == r.Confidence {
			continue // not a cut point
		}
		prec := float64(correct) / float64(m)
		lo, _, _ := mathx.Wilson(correct, m, z)
		for i, t := range targets {
			if prec >= t {
				out[i].Coverage, out[i].Threshold, out[i].Covered, out[i].Errors = float64(m)/n, r.Confidence, m, m-correct
			}
			if lo >= t {
				out[i].CoverageLB, out[i].ThresholdLB = float64(m)/n, r.Confidence
			}
		}
	}
	return out
}

// Answered is the answers at or above a confidence threshold: how many
// and how many of them are wrong (an operating point applied to other
// data).
func Answered(ranked []Ranked, threshold float64) (covered, errors int) {
	for _, r := range ranked {
		if r.Confidence >= threshold {
			covered++
			if !r.Correct {
				errors++
			}
		}
	}
	return covered, errors
}
