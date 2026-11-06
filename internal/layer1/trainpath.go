package layer1

import (
	"fmt"
	"math"

	"github.com/LLM-4-People/mailsense/internal/optim"
)

// lambdaPath chooses a question's λ on the selection labels and returns
// their out-of-fold predictions at it.
type lambdaPath struct {
	qs   *questionSet
	d    *Design
	tc   TrainConfig
	sel  []EvalItem
	fold []int
	base []Example
	fit  func([]Example, float64, *Params) (Params, optim.Result, error)
	rep  *QuestionReport
	step func(Step)
}

// report is one step of the path (nil step: nothing to tell).
func (p *lambdaPath) report(stage string, done, total int) {
	if p.step != nil {
		p.step(Step{Stage: stage, Question: p.qs.q.Name, StepDone: done, StepTotal: total})
	}
}

// nll is the mean NLL of the scored selection labels (+Inf: none scored).
func (p *lambdaPath) nll(lp [][]float64) float64 {
	sum, n := 0.0, 0
	for i, e := range p.sel {
		if lp[i] != nil {
			sum -= lp[i][e.Class]
			n++
		}
	}
	if n == 0 {
		return math.Inf(1)
	}
	return sum / float64(n)
}

func (p *lambdaPath) record(lambda, nll float64) {
	// A path step with no scored selection label (no out-of-fold
	// prediction, e.g. a single-class question) has an infinite NLL: it is
	// a diagnostic that carries no information, and an infinite float
	// cannot be stored as JSON at all (it would fail the run's commit).
	// Omit it; the λ choice below falls back to the default.
	if math.IsInf(nll, 0) || math.IsNaN(nll) {
		return
	}
	if p.rep.PathNLL == nil {
		p.rep.PathNLL = map[string]float64{}
	}
	p.rep.PathNLL[fmt.Sprintf("%g", lambda)] = nll
}

// withTeachers fits the λ path on plain (every target but the evaluation
// labels' messages), from nil and each λ warm from the previous one,
// scores each fit on the selection labels (out of sample: one fit per
// λ), then cross-fits the selection labels at the chosen λ from that λ's
// path fit (it never saw an evaluation label). It returns the out-of-fold
// predictions and the path fit (the final fit's start).
func (p *lambdaPath) withTeachers(plain []Example) ([][]float64, *Params, error) {
	best := math.Inf(1)
	var bestP Params
	var prev *Params
	for i, lambda := range p.tc.Lambdas {
		p.report(StageFit, i, len(p.tc.Lambdas))
		par, _, err := p.fit(plain, lambda, prev)
		if err != nil {
			return nil, nil, err
		}
		nll := p.nll(predictItems(p.d, p.qs, par, p.sel, standardizerOf(p.d, plain)))
		p.record(lambda, nll)
		if nll < best {
			best, bestP, p.rep.Lambda = nll, par, lambda
		}
		prev = &par
	}
	p.report(StageFit, len(p.tc.Lambdas), len(p.tc.Lambdas))
	var start *Params
	if !math.IsInf(best, 1) {
		start = &bestP
	} else {
		// No λ could be scored on the selection labels: keep the default λ
		// and warm the final fit from nothing. A zero Params here would
		// silently warm-start the final fit from a model nobody chose.
		p.rep.Lambda = p.tc.DefaultLambda
	}
	oof, err := crossFit(p.qs, p.sel, p.fold, p.tc.Folds, p.base, func(ex []Example, f int) (Params, error) {
		p.report(StageFold, f+1, p.tc.Folds)
		par, _, err := p.fit(ex, p.rep.Lambda, start)
		return par, err
	}, p.d)
	return oof, start, err
}

// coldStart cross-fits every λ over the selection labels (no target but
// the evaluation labels), each fold warm from its own previous-λ fit,
// and keeps the λ of the lowest out-of-fold NLL (the default λ when no
// fold could be scored).
func (p *lambdaPath) coldStart() ([][]float64, error) {
	prev := make([]*Params, p.tc.Folds)
	best := math.Inf(1)
	var bestOOF [][]float64
	p.rep.Lambda = p.tc.DefaultLambda
	fits, total := 0, len(p.tc.Lambdas)*p.tc.Folds
	for _, lambda := range p.tc.Lambdas {
		oof, err := crossFit(p.qs, p.sel, p.fold, p.tc.Folds, p.base, func(ex []Example, f int) (Params, error) {
			fits++
			p.report(StageFold, fits, total)
			par, _, err := p.fit(ex, lambda, prev[f])
			if err == nil {
				prev[f] = &par
			}
			return par, err
		}, p.d)
		if err != nil {
			return nil, err
		}
		nll := p.nll(oof)
		p.record(lambda, nll)
		if nll < best {
			best, bestOOF, p.rep.Lambda = nll, oof, lambda
		}
	}
	if bestOOF == nil {
		bestOOF = make([][]float64, len(p.sel))
	}
	return bestOOF, nil
}
