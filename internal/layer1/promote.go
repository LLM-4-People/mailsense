package layer1

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// modelMeta is what a stored model was trained on and measured with
// (layer1_models.meta).
type modelMeta struct {
	Space     string                  `json:"space"`
	Layout    string                  `json:"layout"`
	Spec      string                  `json:"spec"` // SpecOf its question
	Dims      int                     `json:"dims"`
	Cells     int                     `json:"cells"`
	Lambda    float64                 `json:"lambda"`
	Combine   Combiner                `json:"combine"`
	Threshold float64                 `json:"threshold"`
	Train     int                     `json:"train"`
	Effective float64                 `json:"effective_train"`
	Eval      int                     `json:"eval"`
	Sources   map[string]*SourceCount `json:"sources"`
	Algorithm int                     `json:"algorithm"`
	Holdout   *Holdout                `json:"holdout,omitempty"` // the predictions it is (re-)scored on
}

// activeModel is a question's active stored model, decoded.
type activeModel struct {
	id      int64
	model   *Model
	meta    modelMeta
	metrics *Metrics
}

// metaOf is a trained model's meta.
func metaOf(m *Model, qr QuestionReport) modelMeta {
	return modelMeta{Space: m.Space, Layout: m.Layout, Spec: m.Spec, Dims: m.Dims, Cells: m.Cells, Lambda: qr.Lambda, Combine: m.Combine,
		Threshold: m.Threshold, Train: qr.Train, Effective: qr.Effective, Eval: qr.Eval, Sources: qr.Sources, Algorithm: algorithmVersion,
		Holdout: m.Holdout}
}

// active decodes every question's active model; one that does not decode
// (a corrupt blob, a question of another shape) is logged and treated as
// absent (it never answers).
func (r *Runner) active(ctx context.Context) (map[string]*activeModel, error) {
	rows, err := r.st.ActiveLayer1(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*activeModel{}
	for _, row := range rows {
		a, err := decodeStored(row)
		if err != nil {
			r.log.Warn("layer1 active model ignored", "question_key", row.Question, "id", row.ID, "err", err)
			continue
		}
		out[row.Question] = a
	}
	return out, nil
}

func decodeStored(row store.Layer1Model) (*activeModel, error) {
	a := &activeModel{id: row.ID, model: &Model{Question: row.Question, Kind: Kind(row.Kind), Classes: row.Classes}}
	if err := json.Unmarshal([]byte(row.Meta), &a.meta); err != nil {
		return nil, fmt.Errorf("%w: meta: %v", ErrBadBlob, err)
	}
	if row.Metrics != "" && row.Metrics != "null" {
		a.metrics = &Metrics{}
		if err := json.Unmarshal([]byte(row.Metrics), a.metrics); err != nil {
			return nil, fmt.Errorf("%w: metrics: %v", ErrBadBlob, err)
		}
	}
	if err := a.model.Decode(row.Params); err != nil {
		return nil, err
	}
	m := a.model
	m.Combine, m.Threshold, m.Space, m.Layout, m.Spec, m.Holdout = a.meta.Combine, a.meta.Threshold, a.meta.Space, a.meta.Layout, a.meta.Spec, a.meta.Holdout
	return a, nil
}

// fits reports whether a stored model can answer this pass's rows for its
// question as specified now: the same vector space, evidence layout and
// shape, the same question spec (an option added, renamed or reordered
// makes an old model unusable: bugs review #2), the same class count, and
// a kNN index when it pools one (a kept kNN model never answers without
// its kNN term).
func (a *activeModel) fits(tr *Trained, spec string, classes int) bool {
	m := a.model
	return a.meta.Space == tr.Space && a.meta.Layout == tr.Layout.ID && a.meta.Dims == tr.Design.Dims && a.meta.Cells == tr.Design.Cells &&
		a.meta.Spec == spec && m.Classes == classes && (!m.Combine.KNN || tr.knn[m.Question] != nil)
}

// keepActive is why the active model stays ("" = promote the candidate):
// it fits the pass and the candidate is unmeasured while it is measured,
// or the candidate's out-of-fold NLL is worse by more than maxReg.
func keepActive(cur *activeModel, fits bool, oof *Metrics, maxReg float64) string {
	switch {
	case cur == nil || !fits || cur.meta.Algorithm != algorithmVersion:
		return ""
	case cur.metrics != nil && oof == nil:
		return "the candidate is not measured (too few evaluation labels) and the active model is"
	case cur.metrics != nil && oof != nil && oof.NLL > cur.metrics.NLL+maxReg:
		return fmt.Sprintf("out-of-fold NLL %.4f is worse than the active model's %.4f by more than %g", oof.NLL, cur.metrics.NLL, maxReg)
	}
	return ""
}
