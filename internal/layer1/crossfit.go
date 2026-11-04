package layer1

import (
	"errors"
)

// crossFit returns the out-of-fold log-probabilities of items (the
// selection labels): fold f's model trains on base (every target but the
// certification part's messages) without fold f's messages — a held-out
// message has no target of any source, as a message the model never saw
// (its teacher's own answer would let the model repeat it: the
// measurement would be the teacher's). fit gets the fold's index (a cold
// start warm-starts each fold from its own previous-λ fit). A fold whose
// training lacks a class (ErrSingleClass) is skipped: its items stay nil
// (unscored), the question is still trained.
func crossFit(qs *questionSet, items []EvalItem, fold []int, folds int, base []Example, fit func(ex []Example, f int) (Params, error), d *Design) ([][]float64, error) {
	out := make([][]float64, len(items))
	for f := range folds {
		held := map[int]bool{}
		for i, e := range items {
			if fold[i] == f {
				held[e.Row] = true
			}
		}
		if len(held) == 0 {
			continue
		}
		ex := withoutRows(base, held)
		p, err := fit(ex, f)
		if errors.Is(err, ErrSingleClass) {
			log.Debug("layer1 fold skipped: its training lacks a class", "question_key", qs.q.Name, "fold", f, "items", len(held))
			continue
		}
		if err != nil {
			return nil, err
		}
		m := &Model{Kind: qs.kind, Classes: qs.classes, Params: p}
		m.fold(standardizerOf(d, ex))
		for i, e := range items {
			if fold[i] == f {
				out[i] = make([]float64, qs.classes)
				m.logProbsRow(d, e.Row, out[i])
			}
		}
	}
	return out, nil
}

// trainingSet is the question's training examples without the held-out
// rows (no target of any source for them).
func trainingSet(qs *questionSet, held map[int]bool) []Example { return withoutRows(qs.train, held) }

func withoutRows(ex []Example, held map[int]bool) []Example {
	var out []Example
	for _, e := range ex {
		if !held[e.Row] {
			out = append(out, e)
		}
	}
	return out
}

// predictItems is items' log-probabilities under params.
func predictItems(d *Design, qs *questionSet, p Params, items []EvalItem, std standardizer) [][]float64 {
	m := &Model{Kind: qs.kind, Classes: qs.classes, Params: p}
	m.fold(std)
	out := make([][]float64, len(items))
	for i, e := range items {
		out[i] = make([]float64, qs.classes)
		m.logProbsRow(d, e.Row, out[i])
	}
	return out
}

// knnOutOfFold is each item's kNN distribution with its own fold's items
// and the hidden messages (the certification part) left out.
func knnOutOfFold(items []EvalItem, knn *knnIndex, fold []int, members map[int64]Member, hidden map[int64]bool) [][]float64 {
	byFold := map[int][]int64{}
	for i, e := range items {
		byFold[fold[i]] = append(byFold[fold[i]], e.ID)
	}
	out := make([][]float64, len(items))
	for i, e := range items {
		h := map[int64]bool{}
		for id := range hidden {
			h[id] = true
		}
		for _, id := range byFold[fold[i]] {
			h[id] = true
		}
		out[i] = make([]float64, len(knn.prior))
		m := members[e.ID]
		knn.logProbs(e.ID, m.Instant, m.Group, h, out[i])
	}
	return out
}

// rowsOf, groupsOf and idsOf are items' design rows, conversations and
// message ids.
func rowsOf(items []EvalItem) map[int]bool {
	out := make(map[int]bool, len(items))
	for _, e := range items {
		out[e.Row] = true
	}
	return out
}

func groupsOf(items []EvalItem) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.Group
	}
	return out
}

func idsOf(items []EvalItem) map[int64]bool {
	out := make(map[int64]bool, len(items))
	for _, e := range items {
		out[e.ID] = true
	}
	return out
}
