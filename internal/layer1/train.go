package layer1

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/LLM-4-People/mailsense/internal/optim"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// TrainConfig are a run's training settings, read once.
type TrainConfig struct {
	Lambdas          []float64 // the grid, largest first
	DefaultLambda    float64
	Folds            int
	Fit              FitOptions // Lambda set per fit
	Calibrate        bool
	CalibrationPrior float64    // the scales' prior, in labels (layer1.calibration.prior_labels)
	KNN              *KNNConfig // nil: off
	CertifyFraction  float64    // the share of conversations whose evaluation labels certify (layer1.certify.fraction)
	Report           []float64  // the precision targets reports measure coverage at
	Req              Requirement
}

// QuestionReport is what training one question did and measured.
type QuestionReport struct {
	Question    string                  `json:"question"`
	Kind        Kind                    `json:"kind"`
	Classes     int                     `json:"classes"`
	Train       int                     `json:"train"`
	Effective   float64                 `json:"effective_train"`
	Eval        int                     `json:"eval"`
	Select      int                     `json:"select"`   // evaluation labels of the selection part (λ, calibration, threshold)
	Certify     int                     `json:"certify"`  // of the certification part (held out of every fit)
	Unscored    int                     `json:"unscored"` // selection labels without an out-of-fold prediction (their fold's training lacked a class)
	Sources     map[string]*SourceCount `json:"sources"`
	Lambda      float64                 `json:"lambda"`
	PathNLL     map[string]float64      `json:"path_nll,omitempty"` // λ → NLL on the selection labels (uncalibrated)
	Iterations  int                     `json:"iterations"`         // of the final fit
	Fits        int                     `json:"fits"`               // every fit of the question (path, folds, final)
	Evaluations int                     `json:"evaluations"`        // objective evaluations over every fit
	Converged   bool                    `json:"converged"`
	Combine     Combiner                `json:"combine"`
	// OOF are the out-of-fold metrics on the selection labels (calibrated
	// with the same predictions: a small optimism, disclosed; the
	// certification is not affected).
	OOF *Metrics `json:"oof,omitempty"`
	// Certification is the operating threshold chosen on the selection
	// labels and tested on the certification labels by the model itself.
	Certification *selective.Certification `json:"certification,omitempty"`
	Wall          time.Duration            `json:"wall_ns"`
	Error         string                   `json:"error,omitempty"`
	Code          string                   `json:"code,omitempty"` // a blocker code when the question could not be trained or measured
}

// Blocker codes of questions (the layer1, calibration and questions stages).
const (
	BlockNoTeacher    = "layer1_no_teacher"
	BlockSingleClass  = "layer1_single_class"
	BlockTooFewLabels = "layer1_too_few_labels"
	BlockTooFewEval   = "layer1_too_few_eval_labels"
	BlockRunFailed    = "layer1_run_failed"
	BlockConfig       = "layer1_config_invalid"
	BlockRequirement  = "requirement_unmet"
	// BlockRouteNotRouted: the last pass has no route answer record (or
	// row) for every question and every message layer 1 answered.
	BlockRouteNotRouted = "route_not_routed"
)

// trainQuestion fits and measures one question's head
// (docs/LAYER1.md, "Training a question"). No fit that scores a row ever
// starts from parameters fitted on that row's target (a warm start would
// carry the memorised target into the score: bugs review #1):
//
//   - the evaluation labels split by conversation into a selection part
//     (λ, calibration, the threshold's choice) and a certification part
//     (held out of every fit: the final model is tested on it);
//   - with too few evaluation labels: one fit at the default λ (warm
//     from the active model: it scores nothing), unmeasured (every answer
//     abstains);
//   - with targets besides the evaluation labels: the λ path from nil on
//     them (each λ warm from the previous one: none of them saw an
//     evaluation label), scored on the selection labels; the folds of
//     the selection labels start from the chosen λ's path fit;
//   - in a cold start (no other target): every λ cross-fitted over the
//     selection labels, each fold warm from its own previous-λ fit;
//   - the final model trains on every target but the certification
//     part's messages, from the chosen path fit (or nil in a cold start).
//
// A fold whose training lacks a class is skipped (its labels unscored,
// counted), not the question.
func trainQuestion(ctx context.Context, d *Design, qs *questionSet, tc TrainConfig, warm *Params, knn *knnIndex, members map[int64]Member, step func(Step)) (_ *Model, rep QuestionReport, _ error) {
	start := time.Now()
	if step == nil {
		step = func(Step) {}
	}
	rep = QuestionReport{Question: qs.q.Name, Kind: qs.kind, Classes: qs.classes, Train: len(qs.train), Effective: qs.neff,
		Eval: len(qs.eval), Sources: qs.counts}
	defer func() { rep.Wall = time.Since(start) }() // the named result: the report returned carries it
	if len(qs.train) == 0 {
		rep.Code = BlockNoTeacher
		return nil, rep, nil
	}
	h, err := newHead(qs.kind, qs.classes)
	if err != nil {
		return nil, rep, err
	}
	fit := func(ex []Example, lambda float64, from *Params) (Params, optim.Result, error) {
		o := tc.Fit
		o.Lambda = lambda
		p, res, err := Fit(ctx, d, h, ex, from, o)
		rep.Fits++
		rep.Evaluations += res.Evaluations
		return p, res, err
	}
	model := &Model{Question: qs.q.Name, Kind: qs.kind, Classes: qs.classes, Dims: d.Dims, Cells: d.Cells, Combine: Identity,
		Threshold: selective.NoThreshold} // unmeasured: nothing certified, every answer abstains
	sel, cert := qs.parts()
	rep.Select, rep.Certify = len(sel), len(cert)
	if len(qs.eval) < tc.Req.MinEvalLabels || len(sel) < 2 || len(cert) == 0 {
		// The branch exists because there are too few evaluation labels to
		// measure, not because the certification rows may train. Fit only
		// the rows outside the certification part, exactly like the normal
		// path: a head that memorised its own certification rows could
		// otherwise certify on them even with no threshold.
		ex := trainingSet(qs, rowsOf(cert))
		if len(ex) == 0 {
			rep.Code = BlockTooFewEval
			return nil, rep, nil
		}
		step(Step{Stage: StageFit, Question: qs.q.Name, StepDone: 0, StepTotal: 1})
		p, res, err := fit(ex, tc.DefaultLambda, warm)
		if err != nil {
			return nil, rep, classify(&rep, err)
		}
		step(Step{Stage: StageFit, Question: qs.q.Name, StepDone: 1, StepTotal: 1})
		rep.Lambda, rep.Iterations, rep.Converged, rep.Code = tc.DefaultLambda, res.Iterations, res.Converged, BlockTooFewEval
		model.Params = p
		model.fold(standardizerOf(d, ex))
		return model, rep, nil
	}
	base := trainingSet(qs, rowsOf(cert))                  // every target but the certification part's messages
	plain := trainingSet(qs, rowsOf(append(sel, cert...))) // every target but the evaluation labels' messages
	fold := Folds(groupsOf(sel), tc.Folds)
	path := &lambdaPath{qs: qs, d: d, tc: tc, sel: sel, fold: fold, base: base, fit: fit, rep: &rep, step: step}
	var oof [][]float64
	var from *Params
	if len(plain) > 0 && !singleClass(plain, qs.classes) {
		oof, from, err = path.withTeachers(plain)
	} else {
		oof, err = path.coldStart()
	}
	if err != nil {
		return nil, rep, classify(&rep, err)
	}
	step(Step{Stage: StageFit, Question: qs.q.Name, StepDone: 0, StepTotal: 1})
	final, res, err := fit(base, rep.Lambda, from)
	if err != nil {
		return nil, rep, classify(&rep, err)
	}
	step(Step{Stage: StageFit, Question: qs.q.Name, StepDone: 1, StepTotal: 1})
	rep.Iterations, rep.Converged = res.Iterations, res.Converged
	model.Params = final
	model.fold(standardizerOf(d, base))
	if err := measure(ctx, d, qs, tc, model, &rep, sel, cert, fold, oof, knn, members, step); err != nil {
		return nil, rep, classify(&rep, err)
	}
	return model, rep, nil
}

// measure calibrates the model on the scored selection labels, measures
// them out of fold, and certifies the threshold chosen on them against
// the certification labels predicted by the model itself (kNN: the
// certification part never counts as a neighbour).
func measure(ctx context.Context, d *Design, qs *questionSet, tc TrainConfig, model *Model, rep *QuestionReport, sel, cert []EvalItem,
	fold []int, oof [][]float64, knn *knnIndex, members map[int64]Member, step func(Step)) error {
	step(Step{Stage: StageMeasure, Question: qs.q.Name})
	hideCert := idsOf(cert)
	var knnOOF [][]float64
	if knn != nil {
		knnOOF = knnOutOfFold(sel, knn, fold, members, hideCert)
	}
	var lr, kn [][]float64
	var labels []int
	var scored []EvalItem
	for i, e := range sel {
		if oof[i] == nil {
			rep.Unscored++
			continue
		}
		scored, lr, labels = append(scored, e), append(lr, oof[i]), append(labels, e.Class)
		if knnOOF != nil {
			kn = append(kn, knnOOF[i])
		}
	}
	if len(scored) < 2 {
		rep.Code, rep.Error = BlockTooFewEval, "fewer than 2 selection labels could be scored out of fold"
		return nil
	}
	if tc.Calibrate || knn != nil {
		c, err := FitCombiner(ctx, lr, kn, labels, tc.Calibrate, tc.CalibrationPrior)
		if err != nil {
			return err
		}
		model.Combine = c
	}
	rep.Combine = model.Combine
	h := &Holdout{}
	preds := make([]Prediction, len(scored))
	for i, e := range scored {
		var k []float64
		if kn != nil {
			k = kn[i]
		}
		p := calibrated(model.Combine, lr[i], k)
		preds[i] = Prediction{ID: e.ID, Label: e.Class, Probs: p}
		h.Select = append(h.Select, HeldItem{ID: e.ID, Probs: p})
	}
	m := Measure(qs.q.Type, preds, qs.cutoff, tc.Req.EffectiveZ(), tc.Report)
	rep.OOF = &m
	step(Step{Stage: StageCertify, Question: qs.q.Name, StepDone: 0, StepTotal: len(cert)})
	for i, e := range cert {
		lp := make([]float64, qs.classes)
		model.logProbsRow(d, e.Row, lp)
		var k []float64
		if knn != nil && model.Combine.KNN {
			k = make([]float64, qs.classes)
			mem := members[e.ID]
			knn.logProbs(e.ID, mem.Instant, mem.Group, hideCert, k)
		}
		h.Certify = append(h.Certify, HeldItem{ID: e.ID, Probs: calibrated(model.Combine, lp, k)})
		if len(cert) >= 200 && (i+1)%200 == 0 {
			step(Step{Stage: StageCertify, Question: qs.q.Name, StepDone: i + 1, StepTotal: len(cert)})
		}
	}
	step(Step{Stage: StageCertify, Question: qs.q.Name, StepDone: len(cert), StepTotal: len(cert)})
	c := h.certify(qs.q.Type, qs.labels(), qs.cutoff, tc.Req)
	rep.Certification = &c
	model.Holdout, model.Threshold = h, threshold(c)
	return nil
}

// calibrated is the combiner's probabilities of head log-probabilities
// (knn nil: none).
func calibrated(c Combiner, lr, knn []float64) []float64 {
	out := make([]float64, len(lr))
	c.Apply(lr, knn, out)
	for k := range out {
		out[k] = math.Exp(out[k])
	}
	return out
}

// classify names a training failure's blocker.
func classify(rep *QuestionReport, err error) error {
	rep.Error = err.Error()
	switch {
	case errors.Is(err, ErrSingleClass):
		rep.Code = BlockSingleClass
		return nil // a property of the data, not a failed run
	case errors.Is(err, optim.ErrCanceled), errors.Is(err, context.Canceled):
		return err
	}
	rep.Code = BlockRunFailed
	return err
}
