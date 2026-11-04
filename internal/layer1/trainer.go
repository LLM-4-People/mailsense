package layer1

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// Trained is one training pass over every question.
type Trained struct {
	Models   map[string]*Model // trained questions (a blocked one may be absent)
	Reports  []QuestionReport  // in question order
	Design   *Design
	Space    string
	Layout   Layout
	Members  map[int64]Member
	NoVector int // members without a vector of the space (pending: never imputed)
	Wall     time.Duration
	sets     map[string]*questionSet
	knn      map[string]*knnIndex
	g        *gathered
}

// Train runs one pass: the design over the members (a member without a
// vector of the space is pending, never imputed), the teachers' targets,
// then every question (warm: the active models, nil for none; one only
// starts a question's unmeasured fit, never a fit that scores a label).
func Train(ctx context.Context, plan *Plan, in Inputs, warm map[string]*Model) (*Trained, error) {
	start := time.Now()
	members, err := in.Members(ctx)
	if err != nil {
		return nil, err
	}
	// kNN pools each answer with the labelled population's nearest
	// neighbours, and its scan is O(population) per message and question:
	// at 35k messages the commit would hold the writer for hours. The
	// setting is for small mailboxes and experiments; above this bound the
	// pass trains without it and says so.
	if plan.Config.KNN != nil && len(members) > knnMaxMessages {
		log.Warn("layer1 kNN disabled: the mailbox exceeds the serving bound",
			"messages", len(members), "bound", knnMaxMessages)
		plan.Config.KNN = nil
	}
	out := &Trained{Models: map[string]*Model{}, Members: map[int64]Member{}, sets: map[string]*questionSet{}, knn: map[string]*knnIndex{}}
	for _, m := range members {
		out.Members[m.ID] = m
	}
	// The conversation of a member is read from the one member map the pass
	// already keeps; a second id → group map of the whole mailbox duplicated
	// every string header for no extra information.
	groupOf := func(id int64) string { return out.Members[id].Group }
	total, done := len(plan.Profile.Questions), 0
	step := func(s Step) {
		if plan.Step == nil {
			return
		}
		s.Done, s.Total = done, total
		plan.Step(s)
	}
	step(Step{Stage: StageDesign})
	design, set, err := buildDesign(ctx, plan, in, members, out)
	if err != nil {
		return nil, err
	}
	out.Design = design
	step(Step{Stage: StageGather, StepTotal: total})
	g, err := gather(ctx, plan, in, design, groupOf)
	if err != nil {
		return nil, err
	}
	out.g = g
	if _, err := extendDesignWithDecider(plan, g, &out.Layout, design); err != nil {
		return nil, err
	}
	for i, q := range plan.Profile.Questions {
		step(Step{Stage: StageGather, Question: q.Name, StepDone: i, StepTotal: total})
		qs, err := buildSet(plan.Profile, &q, plan.headOf(q.Type), plan.Teachers.For(q.Name), g, design, groupOf, plan.Config.CertifyFraction)
		if err != nil {
			return nil, err
		}
		if plan.Balance && qs.kind == Binary {
			balance(qs)
		}
		out.sets[q.Name] = qs
	}
	if plan.BeforeFit != nil {
		if err := plan.BeforeFit(out); err != nil {
			return nil, err
		}
	}
	for _, q := range plan.Profile.Questions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		qs := out.sets[q.Name]
		tc := plan.Config
		tc.Req = plan.Requirements.For(q.Name)
		var knn *knnIndex
		if tc.KNN != nil && set != nil {
			_, cert := qs.parts()
			knn = newKNN(set, *tc.KNN, Prior(trainingSet(qs, rowsOf(cert)), qs.classes)) // the certification part never shapes the prior
			for _, e := range qs.train {
				id := design.IDs[e.Row]
				knn.add(id, e.Target, e.Weight, out.Members[id].Instant, groupOf(id))
			}
			out.knn[q.Name] = knn
		}
		spec := SpecOf(&q)
		step(Step{Stage: StageFit, Question: q.Name})
		model, rep, err := trainQuestion(ctx, design, qs, tc, warmStart(warm[q.Name], qs, design, out, spec), knn, out.Members, step)
		if err != nil {
			return nil, fmt.Errorf("layer1: %s: %w", q.Name, err)
		}
		if model != nil {
			model.Space, model.Layout, model.Spec = out.Space, out.Layout.ID, spec
			out.Models[q.Name] = model
		}
		log.Info("layer1 question trained", "question_key", q.Name, "kind", rep.Kind, "train", rep.Train, "eval", rep.Eval,
			"lambda", rep.Lambda, "iterations", rep.Iterations, "converged", rep.Converged, "code", rep.Code, "wall", rep.Wall)
		out.Reports = append(out.Reports, rep)
		done = len(out.Reports)
		if plan.Progress != nil {
			plan.Progress(done, total, rep)
		}
	}
	step(Step{Stage: StageFit}) // every question is done
	out.Wall = time.Since(start)
	return out, nil
}

// gather reads every source's data once: the labels, each decisions
// source's answers as compact per-question distributions (streamed), the
// configured decider's answers for the route (the Ondelle participant, read
// even when no teacher source names that decider; a teacher source with
// the same key reuses the one read) and the rules measured on the
// selection part's evaluation labels only (the certification part never
// shapes a target). The rules are always measured: the route's key
// participant needs them even when no rules teacher source exists.
func gather(ctx context.Context, plan *Plan, in Inputs, d *Design, groupOf func(int64) string) (*gathered, error) {
	labels, err := in.Labels(ctx)
	if err != nil {
		return nil, err
	}
	g := &gathered{labels: labelIndex(labels), decisions: map[string]map[string]*compactAnswers{}, keys: map[string]string{}}
	read := map[string]map[string]*compactAnswers{} // decider key → question → answers (one read, one validation)
	readDecider := func(key store.TriageKey, tolerance float64) (map[string]*compactAnswers, error) {
		if c, ok := read[key.Decider]; ok {
			return c, nil
		}
		c, err := readDecisions(ctx, in, key, plan.Profile, d, tolerance)
		if err != nil {
			return nil, err
		}
		read[key.Decider] = c
		return c, nil
	}
	ondelleKey := store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: plan.DeciderKey}
	if g.ondelle, err = readDecider(ondelleKey, plan.DeciderTolerance); err != nil {
		return nil, err
	}
	for _, s := range plan.Teachers.Sources {
		if s.From != FromDecisions {
			continue
		}
		key := plan.keyOf(s)
		g.keys[s.Name] = key.Decider
		if g.decisions[s.Name], err = readDecider(key, plan.Tolerance[s.Name]); err != nil {
			return nil, err
		}
	}
	eval := evalLabels(plan.Teachers.Sources, g)
	for id := range eval {
		if selective.InCertifyPart(groupOf(id), plan.Config.CertifyFraction) {
			delete(eval, id)
		}
	}
	if g.rules, g.fires, err = in.Rules(ctx, eval); err != nil {
		return nil, err
	}
	return g, nil
}

// warmStart is the active model's parameters when they may start the
// question's unmeasured fit (the only fit that scores nothing): the same
// head, classes, question spec, vector space, evidence layout and shape;
// nil otherwise.
func warmStart(m *Model, qs *questionSet, d *Design, tr *Trained, spec string) *Params {
	if m == nil || m.Kind != qs.kind || m.Classes != qs.classes || m.Spec != spec || m.Space != tr.Space || m.Layout != tr.Layout.ID ||
		m.Dims != d.Dims || m.Cells != d.Cells {
		return nil
	}
	return &m.Params
}

// buildDesign builds the rows of the members: their vectors of the space
// (cut to layer1.embedding.dimensions) and their evidence cells.
func buildDesign(ctx context.Context, plan *Plan, in Inputs, members []Member, out *Trained) (*Design, *vector.Set, error) {
	var set *vector.Set
	dims := 0
	if plan.UseEmbedding {
		space, s, err := in.Vectors(ctx)
		if err != nil {
			return nil, nil, err
		}
		if s == nil || s.Len() == 0 {
			return nil, nil, ErrNoVectors
		}
		set, out.Space, dims = s, space, s.Dims()
		if plan.Dimensions != 0 {
			if plan.Dimensions > dims {
				return nil, nil, fmt.Errorf("layer1: %s %d exceeds the stored vectors' %d dimensions", SettingDimensions, plan.Dimensions, dims)
			}
			dims = plan.Dimensions
		}
	}
	var cells map[int64][]int32
	if plan.UseEvidence {
		layout, c, err := in.Cells(ctx)
		if err != nil {
			return nil, nil, err
		}
		out.Layout, cells = layout, c
	}
	// The lexical/sender/metadata block is appended after the evidence
	// cells. Its version is part of the layout id, so a feature change
	// invalidates a warm start instead of silently reusing old weights.
	if out.Layout.ID == "" {
		out.Layout.ID = "features-" + featureVersion
	} else {
		out.Layout.ID += "+features-" + featureVersion
	}
	evidenceCells := out.Layout.Cells
	b := NewDesign(dims, evidenceCells+featureCellsCount())
	ids := make([]int64, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	for _, id := range ids {
		var v *vector.Quantized
		if set != nil {
			if v = set.Row(id); v == nil {
				out.NoVector++
				continue
			}
		}
		rowCells := make([]int32, 0, len(cells[id])+featureCellsCount())
		rowCells = append(rowCells, cells[id]...)
		rowCells = append(rowCells, featureCells(out.Members[id], evidenceCells)...)
		if err := b.Add(id, v, rowCells); err != nil {
			return nil, nil, err
		}
	}
	d, err := b.Build()
	return d, set, err
}

// ErrNoVectors: layer1.features.embedding is on and the space holds no
// vector yet (the embeddings stage comes first).
var ErrNoVectors = errors.New("layer1: no vector of the current space yet")

// TargetIDs are the messages that have a training target or an
// evaluation label for some question in this pass.
func (t *Trained) TargetIDs() map[int64]bool {
	out := map[int64]bool{}
	for _, qs := range t.sets {
		for _, e := range qs.train {
			out[t.Design.IDs[e.Row]] = true
		}
		for _, e := range qs.eval {
			out[e.ID] = true
		}
	}
	return out
}

// balance reweights a set's training examples by the inverse prior of
// their target's classes, keeping the total weight.
func balance(qs *questionSet) {
	prior := Prior(qs.train, qs.classes)
	var before, after float64
	for i := range qs.train {
		e := &qs.train[i]
		before += e.Weight
		var f float64
		for c, m := range e.Target.Mass(qs.classes) {
			if prior[c] > 0 {
				f += m / (prior[c] * float64(qs.classes))
			}
		}
		e.Weight *= f
		after += e.Weight
	}
	for i := range qs.train {
		qs.train[i].Weight *= before / after
	}
}
