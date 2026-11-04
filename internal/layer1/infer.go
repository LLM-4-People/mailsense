package layer1

// Predict is the calibrated class distribution of a member for a
// question by this pass's model (false: no model, or the member has no
// row — no vector of the space yet: pending, never imputed).
func (t *Trained) Predict(question string, id int64) ([]float64, bool) {
	m := t.Models[question]
	if m == nil {
		return nil, false
	}
	return t.predictWith(m, id)
}

// predictWith is Predict by any model that reads this pass's rows (a
// model pooled with kNN reads it only when the pass has a kNN index of
// its question: fits).
func (t *Trained) predictWith(m *Model, id int64) ([]float64, bool) {
	row, ok := t.Design.Row(id)
	if !ok {
		return nil, false
	}
	lr := make([]float64, m.Classes)
	m.logProbsRow(t.Design, row, lr)
	var kn []float64
	if x := t.knn[m.Question]; m.Combine.KNN && x != nil {
		kn = make([]float64, m.Classes)
		mem := t.Members[id]
		x.logProbs(id, mem.Instant, mem.Group, nil, kn)
	}
	return calibrated(m.Combine, lr, kn), true
}
