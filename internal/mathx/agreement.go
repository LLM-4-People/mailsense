package mathx

import "slices"

// AUC is the area under the ROC curve of scores against binary outcomes
// (the Mann-Whitney probability that a positive outranks a negative; ties
// count one half). ok is false when the lengths differ or a class is empty.
func AUC(scores []float64, positive []bool) (float64, bool) {
	if len(scores) != len(positive) {
		return 0, false
	}
	var pos, neg []float64
	for i, s := range scores {
		if positive[i] {
			pos = append(pos, s)
		} else {
			neg = append(neg, s)
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return 0, false
	}
	slices.Sort(neg)
	wins := 0.0
	for _, p := range pos {
		below, _ := slices.BinarySearch(neg, p) // first index ≥ p
		upTo := below
		for upTo < len(neg) && neg[upTo] == p {
			upTo++
		}
		wins += float64(below) + float64(upTo-below)/2
	}
	return wins / float64(len(pos)*len(neg)), true
}

// Brier is the mean squared difference between probabilities and binary
// outcomes (0 is perfect, 0.25 is a constant 0.5). ok is false when the
// lengths differ or there is no pair.
func Brier(probabilities []float64, outcome []bool) (float64, bool) {
	if len(probabilities) != len(outcome) || len(probabilities) == 0 {
		return 0, false
	}
	sum := 0.0
	for i, p := range probabilities {
		o := 0.0
		if outcome[i] {
			o = 1
		}
		sum += (p - o) * (p - o)
	}
	return sum / float64(len(probabilities)), true
}

// Kappa is Cohen's kappa of two raters' categorical labels: agreement
// beyond what their own label frequencies give by chance. ok is false
// when the lengths differ, there is no pair, or chance agreement is total
// (both always give the same one label: kappa is undefined).
func Kappa(a, b []string) (float64, bool) {
	n := len(a)
	if n != len(b) || n == 0 {
		return 0, false
	}
	agree := 0
	ca, cb := map[string]int{}, map[string]int{}
	for i := range a {
		if a[i] == b[i] {
			agree++
		}
		ca[a[i]]++
		cb[b[i]]++
	}
	chance := 0.0
	for label, k := range ca {
		chance += float64(k) * float64(cb[label]) / float64(n*n)
	}
	if chance == 1 {
		return 0, false
	}
	return (float64(agree)/float64(n) - chance) / (1 - chance), true
}
