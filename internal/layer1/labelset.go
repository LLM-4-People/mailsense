package layer1

import (
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// SourceCount is what one teacher source gave a question.
type SourceCount struct {
	Targets   int `json:"targets"`   // messages it gave a valid target
	Used      int `json:"used"`      // of them, the target trained on (no source before it had one)
	Dropped   int `json:"dropped"`   // answers refused as invalid (probabilities off, unknown option or level)
	Refused   int `json:"refused"`   // the decider declined the question
	Abstained int `json:"abstained"` // abstained answers (never a target)
	Excluded  int `json:"excluded"`  // rules not measured well enough to teach
}

// EvalItem is one evaluation label: the message's row, its class, its
// conversation (the folds' and the split's group), the teacher source
// that gave it and its part (Certify: the certification part, held out
// of every fit; else the selection part).
type EvalItem struct {
	Row     int
	ID      int64
	Class   int
	Group   string
	Source  string
	Certify bool
}

// questionSet is one question's training and evaluation data.
type questionSet struct {
	q       *engine.Question
	kind    Kind
	classes int
	cutoff  float64
	train   []Example
	eval    []EvalItem
	counts  map[string]*SourceCount
	neff    float64
}

// gathered is the run's teacher data, read once.
type gathered struct {
	labels    map[string]map[int64]map[string]string // label source → message → question → value (JSON)
	decisions map[string]map[string]*compactAnswers  // decisions source → question → answers by design row
	keys      map[string]string                      // decisions source → decider key
	subq      map[string]*compactAnswers             // the configured decider (the route's SubQ participant) → answers by design row
	rules     []RuleInfo
	fires     map[int64][]int
}

// classesOf is the number of classes of a question.
func classesOf(q *engine.Question) int {
	switch q.Type {
	case engine.Choice:
		return len(q.Options)
	case engine.Noul:
		return 2
	}
	return len(q.Levels)
}

// buildSet gathers a question's targets: per member the first source (in
// precedence order) with a valid target trains; evaluation sources also
// give the evaluation labels.
func buildSet(p *taxonomy.Profile, q *engine.Question, kind Kind, sources []Source, g *gathered, d *Design, groupOf func(int64) string, fraction float64) (*questionSet, error) {
	if _, err := KindOf(q.Type); err != nil {
		return nil, err
	}
	qs := &questionSet{q: q, kind: kind, classes: classesOf(q), cutoff: p.Cutoff(q.Name),
		counts: map[string]*SourceCount{}}
	for _, s := range sources {
		qs.counts[s.Name] = &SourceCount{}
	}
	for row, id := range d.IDs {
		var chosen *Example
		for _, s := range sources {
			t, _, ok := qs.target(s, g, id, row)
			if !ok {
				continue
			}
			qs.counts[s.Name].Targets++
			if chosen == nil {
				chosen = &Example{Row: row, Target: t, Weight: s.Weight}
				qs.counts[s.Name].Used++
			}
		}
		if l, ok := evalOf(q, sources, g.labels, id, qs.cutoff); ok {
			group := groupOf(id)
			qs.eval = append(qs.eval, EvalItem{Row: row, ID: id, Class: l.class, Group: group, Source: l.source,
				Certify: selective.InCertifyPart(group, fraction)})
		}
		if chosen != nil {
			qs.train = append(qs.train, *chosen)
		}
	}
	var sw, sw2 float64
	for _, e := range qs.train {
		sw += e.Weight
		sw2 += e.Weight * e.Weight
	}
	if sw2 > 0 {
		qs.neff = sw * sw / sw2
	}
	return qs, nil
}

// evalOf is a message's evaluation label of a question at the profile's
// cutoff: the first evaluation source (precedence order) with a valid label
// (labels: label source → message → question → value).
func evalOf(q *engine.Question, sources []Source, labels map[string]map[int64]map[string]string, id int64, cutoff float64) (evalLabel, bool) {
	for _, s := range sources {
		if !s.Eval {
			continue
		}
		raw, ok := labels[s.LabelSource][id][q.Name]
		if !ok {
			continue
		}
		if _, class, err := labelTarget(q, raw, cutoff); err == nil {
			return evalLabel{class: class, source: s.Name}, true
		}
	}
	return evalLabel{}, false
}

// parts splits the evaluation labels into the selection and the
// certification part.
func (qs *questionSet) parts() (sel, cert []EvalItem) {
	for _, e := range qs.eval {
		if e.Certify {
			cert = append(cert, e)
		} else {
			sel = append(sel, e)
		}
	}
	return sel, cert
}

// labels are the evaluation labels by message.
func (qs *questionSet) labels() map[int64]evalLabel {
	out := make(map[int64]evalLabel, len(qs.eval))
	for _, e := range qs.eval {
		out[e.ID] = evalLabel{class: e.Class, source: e.Source}
	}
	return out
}

// target is one source's target of a message (ok false: none), and the
// class a label stands for.
func (qs *questionSet) target(s Source, g *gathered, id int64, row int) (Target, int, bool) {
	c := qs.counts[s.Name]
	switch s.From {
	case FromLabels:
		raw, ok := g.labels[s.LabelSource][id][qs.q.Name]
		if !ok {
			return nil, 0, false
		}
		t, class, err := labelTarget(qs.q, raw, qs.cutoff)
		if err != nil {
			c.Dropped++
			return nil, 0, false
		}
		return t, class, true
	case FromDecisions:
		t, st := g.decisions[s.Name][qs.q.Name].target(row)
		switch st {
		case answerRefused:
			c.Refused++
		case answerDropped:
			c.Dropped++
		case answerAbstained:
			c.Abstained++
		}
		if st != answerOK {
			return nil, 0, false
		}
		return t.Sharpen(s.Temperature), 0, true
	case FromRules:
		t, ok := qs.ruleTarget(s, g, id)
		return t, 0, ok
	}
	return nil, 0, false
}
