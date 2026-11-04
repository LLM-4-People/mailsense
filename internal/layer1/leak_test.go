package layer1

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/optim"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// noiseSet is a yes/no question whose labels are pure noise: n evaluation
// labels (and teachers more messages with noise teacher targets) over
// random dims-dimensional embeddings, more columns than rows.
func noiseSet(t *testing.T, n, teachers, dims int) (*Design, *questionSet) {
	t.Helper()
	r := rand.New(rand.NewPCG(11, 13))
	b := NewDesign(dims, 0)
	for i := range n + teachers {
		v := make([]float32, dims)
		for j := range v {
			v[j] = float32(r.NormFloat64())
		}
		u, _ := vector.Truncate(v, dims)
		q := vector.Quantize(u)
		if err := b.Add(int64(i+1), &q, nil); err != nil {
			t.Fatal(err)
		}
	}
	d, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	qs := &questionSet{q: &engine.Question{Name: "n", Type: engine.Noul}, kind: Binary, classes: 2, cutoff: 0.5, counts: map[string]*SourceCount{}}
	for i := range n + teachers {
		c := r.IntN(2)
		id := int64(i + 1)
		if i < n {
			g := "m" + strconv.Itoa(i)
			qs.train = append(qs.train, Example{Row: i, Target: OneHot(c), Weight: 2})
			qs.eval = append(qs.eval, EvalItem{Row: i, ID: id, Class: c, Group: g, Source: "judge", Certify: selective.InCertifyPart(g, 0.5)})
			continue
		}
		p := 0.2 + 0.6*r.Float64()
		tg, _ := Soft([]float64{1 - p, p}, exactSum)
		qs.train = append(qs.train, Example{Row: i, Target: tg, Weight: 1})
	}
	qs.neff = float64(len(qs.train))
	return d, qs
}

func noiseConfig() TrainConfig {
	return TrainConfig{Lambdas: []float64{1e-1, 1e-2, 1e-3, 1e-4}, DefaultLambda: 1e-3, Folds: 5, Calibrate: true, CalibrationPrior: 1,
		Fit:             FitOptions{Optim: optim.Options{Memory: 10, MaxIterations: 300, Tolerance: 1e-4}, Workers: 4, ShardRows: 64},
		CertifyFraction: 0.5, Report: []float64{0.99},
		Req: Requirement{MinEffectiveTrain: 1, MinEvalLabels: 50, TargetAccuracy: 0.99, MinCoverage: 0.5, Z: 1.96, CertifySources: []string{"judge"}}}
}

// Finding (bugs review #1, critical): the folds warm-started from the fit
// on everything (the held-out labels included) and the λ path from the
// active model: in directions only held-out rows span, the memorised
// weights survived, and on pure-noise labels with more columns than rows
// the out-of-fold accuracy was 1.000 and 99 % was certified on every
// answer. No fit that scores a row may start from parameters fitted on
// that row's target: noise must measure as noise and certify nothing.
func TestNoiseLabelsMeasureAsNoise(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		teachers int
		lambdas  []float64
	}{{"cold start", 0, nil}, {"with teacher targets", 300, nil}, {"cold start, λ 1e-4 only", 0, []float64{1e-4}},
		{"teacher targets, λ 1e-4 only", 300, []float64{1e-4}}} {
		t.Run(c.name, func(t *testing.T) {
			d, qs := noiseSet(t, 450, c.teachers, 768)
			tc := noiseConfig()
			if c.lambdas != nil {
				tc.Lambdas = c.lambdas
			}
			// a model fitted on every label, as the active model of an earlier run
			leaky, _, err := Fit(t.Context(), d, logistic{}, qs.train, nil, FitOptions{Lambda: 1e-4, Optim: noiseConfig().Fit.Optim, Workers: 4, ShardRows: 64})
			if err != nil {
				t.Fatal(err)
			}
			m, rep, err := trainQuestion(t.Context(), d, qs, tc, &leaky, nil, nil, nil)
			if err != nil || m == nil || rep.OOF == nil {
				t.Fatalf("%+v %v", rep, err)
			}
			t.Logf("noise %s: out-of-fold accuracy %.3f on %d selection labels (λ %g, %d fits, train %d), certification %+v", c.name, rep.OOF.Accuracy, rep.Select,
				rep.Lambda, rep.Fits, rep.Train, *rep.Certification)
			if math.Abs(rep.OOF.Accuracy-0.5) > 0.12 {
				t.Fatalf("out-of-fold accuracy %.3f on noise", rep.OOF.Accuracy)
			}
			if rep.Certification.Certified || m.Threshold != selective.NoThreshold {
				t.Fatalf("noise certified: %+v", *rep.Certification)
			}
			// the active model never starts a fit that scores: same measurement without it
			_, cold, err := trainQuestion(t.Context(), d, qs, tc, nil, nil, nil, nil)
			if err != nil || cold.OOF.NLL != rep.OOF.NLL || cold.Lambda != rep.Lambda {
				t.Fatalf("the active model changed the measurement: NLL %v vs %v, λ %v vs %v (%v)", cold.OOF.NLL, rep.OOF.NLL, cold.Lambda, rep.Lambda, err)
			}
		})
	}
}

// The certification labels never shape the model that is certified on
// them (rules review A): flipping every one of them leaves the model's
// parameters bitwise the same; only the certification changes.
func TestCertificationLabelsNeverTrainTheCertifiedModel(t *testing.T) {
	t.Parallel()
	d, qs := noiseSet(t, 200, 200, 32)
	m, rep, err := trainQuestion(t.Context(), d, qs, noiseConfig(), nil, nil, nil, nil)
	if err != nil || rep.Certify == 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	for i := range qs.train {
		for _, e := range qs.eval {
			if e.Certify && e.Row == qs.train[i].Row {
				qs.train[i].Target = OneHot(1 - qs.train[i].Target.Class(2))
			}
		}
	}
	for i := range qs.eval {
		if qs.eval[i].Certify {
			qs.eval[i].Class = 1 - qs.eval[i].Class
		}
	}
	m2, _, err := trainQuestion(t.Context(), d, qs, noiseConfig(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Params.W {
		if m.Params.W[i] != m2.Params.W[i] {
			t.Fatalf("weight %d moved with the certification labels", i)
		}
	}
	for i := range m.Holdout.Certify {
		if m.Holdout.Certify[i].Probs[1] != m2.Holdout.Certify[i].Probs[1] {
			t.Fatal("a certification prediction moved with its own label")
		}
	}
}

// Finding (bugs review #6): one fold whose training lacked a class made
// the whole question layer1_single_class. Now that fold is skipped
// (its labels unscored, counted) and the question is trained.
func TestAFoldMissingAClassIsSkippedNotTheQuestion(t *testing.T) {
	t.Parallel()
	d, qs := noiseSet(t, 120, 0, 8)
	// the only three "yes" labels, in one conversation of the selection part
	yes := 0
	for i := range qs.eval {
		e := &qs.eval[i]
		e.Class, e.Group, e.Certify = 0, "g"+strconv.Itoa(i), i%2 == 1
		if !e.Certify && yes < 3 {
			e.Class, e.Group = 1, "rare"
			yes++
		}
		qs.train[i].Target = OneHot(e.Class)
	}
	// the certification part holds yes labels too (the full data has both classes)
	for i := range qs.eval {
		if qs.eval[i].Certify && i%10 == 1 {
			qs.eval[i].Class, qs.train[i].Target = 1, OneHot(1)
		}
	}
	tc := noiseConfig()
	tc.Req.MinEvalLabels = 10
	m, rep, err := trainQuestion(t.Context(), d, qs, tc, nil, nil, nil, nil)
	if err != nil || m == nil || rep.Code == BlockSingleClass {
		t.Fatalf("the question failed: %+v %v", rep, err)
	}
	if rep.Unscored < 3 || rep.Unscored >= rep.Select {
		t.Fatalf("only the skipped fold's labels (the rare conversation's among them) are unscored: %d of %d", rep.Unscored, rep.Select)
	}
}

// The short-evaluation branch must not train on the certification rows
// either: even without a threshold, a head that memorised its own
// certification rows could certify on them.
func TestShortEvaluationBranchNeverTrainsOnCertificationLabels(t *testing.T) {
	t.Parallel()
	d, qs := noiseSet(t, 40, 0, 32)
	m, rep, err := trainQuestion(t.Context(), d, qs, noiseConfig(), nil, nil, nil, nil)
	if err != nil || m == nil {
		t.Fatalf("short branch: %+v %v", rep, err)
	}
	if rep.Code != BlockTooFewEval {
		t.Fatalf("the short branch was not taken: %+v", rep)
	}
	for i := range qs.train {
		for _, e := range qs.eval {
			if e.Certify && e.Row == qs.train[i].Row {
				qs.train[i].Target = OneHot(1 - qs.train[i].Target.Class(2))
			}
		}
	}
	for i := range qs.eval {
		if qs.eval[i].Certify {
			qs.eval[i].Class = 1 - qs.eval[i].Class
		}
	}
	m2, _, err := trainQuestion(t.Context(), d, qs, noiseConfig(), nil, nil, nil, nil)
	if err != nil || m2 == nil {
		t.Fatal(err)
	}
	for i := range m.Params.W {
		if m.Params.W[i] != m2.Params.W[i] {
			t.Fatalf("weight %d moved with the certification labels", i)
		}
	}
}
