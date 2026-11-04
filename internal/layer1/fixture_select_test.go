//go:build fixture

package layer1

// Selective evaluation on dev (lead directive "1.0 accuracy": an answer
// given on its own must be right; below the target the route abstains
// and escalates), for layer 1, legacy SubQ (its own probabilities as
// confidence) and the production route of layer-0 keys + layer 1 + SubQ:
//   - the honest dev certification (selective.Certify for a single layer,
//     selective.CertifyFixed for the route's fixed cascade): dev split by
//     conversation (layer1.certify.fraction), the cuts chosen on the dev
//     selection part and tested once on the dev certification part — what
//     dev can certify;
//   - the dev curves (the largest most-confident dev share at each target,
//     scanned on dev itself: diagnostics, optimistic by construction);
//   - the operating points on dev: layer 1's own thresholds (the point
//     one chosen on its selection labels and the certified one) and the
//     route's (the per-layer gates chosen on its selection part and the
//     answers the app gives when the fixed cascade certified);
//   - the answers each layer gives at the route's operating point;
//   - the errors at the 0.99 point operating point by how their gold label
//     came about (counts here; the per-item list only in the private runs
//     directory, never in out/).

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/goldset"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// devAnswer is one dev item's answers to one question (class −1: none).
// The distributions are what the participants offer the route; the class
// and confidence are the diagnostics (accuracy, error analysis).
type devAnswer struct {
	item      devItem
	class     int // the gold label's class
	l1        int
	l1Conf    float64
	l1Probs   []float64
	key       int // a layer-0 key's answer
	keyLB     float64
	keyProbs  []float64
	keyRule   string
	subq      int
	subqConf  float64
	subqProbs []float64
}

// Selective is one question's selective measurement on dev.
type Selective struct {
	// Certified is the honest dev certification at each report target.
	Certified map[string][]selective.Certification `json:"certified"` // layer1, subq, stack (the route)
	// dev curves (scanned on dev: diagnostics)
	Layer1 []selective.TargetPoint `json:"layer1"`
	SubQ   []selective.TargetPoint `json:"subq"`
	Stack  []selective.TargetPoint `json:"stack"`
	// the operating points on dev: layer 1's own thresholds and the
	// route's (its per-layer gates, and the answers the app gives when
	// the cascade certified)
	Layer1At []Operating `json:"layer1_at"`
	StackAt  []Operating `json:"stack_at"`
	// how many answers the route's gates give by layer on the dev items,
	// per report target: the point operating point. CertifiedGiven is the
	// same count for the app (zero when the route is uncertified).
	Given          []map[route.Layer]int `json:"given"`
	CertifiedGiven []map[route.Layer]int `json:"certified_given"`
	Keys           int                   `json:"keys"` // dev items a layer-0 key answers
	// how the answered items' gold labels came about, at the 0.99 point
	// operating point (counts only)
	Answered map[string]int `json:"answered_by_agreement"`
	Wrong    map[string]int `json:"errors_by_agreement"`
	errors   []ErrorItem    // private: the runs directory
}

// Operating is a reader's operating point applied to dev: for layer 1 the
// point threshold chosen on its selection labels and the certified one the
// app answers with. Cascade marks the route's operating point: a cascade
// has no single cut, so Threshold and Certified are NoThreshold and the
// counts are its per-layer gates' answers (Covered) and the answers the
// app gives when the certification passed (CoveredCertified).
type Operating struct {
	Target    float64 `json:"target"`
	Threshold float64 `json:"threshold"` // chosen on the model's selection labels (point estimate)
	Covered   int     `json:"covered"`   // dev answers at or above it
	Errors    int     `json:"errors"`    // of them, wrong
	// Certified is the app's threshold: the point one when the model's
	// certification labels certified it, NoThreshold otherwise.
	Certified        float64 `json:"certified_threshold"`
	CoveredCertified int     `json:"covered_certified"`
	ErrorsCertified  int     `json:"errors_certified"`
	Cascade          bool    `json:"cascade,omitempty"`
	N                int     `json:"n"`
}

// ErrorItem is one wrong answer given on its own (no text; private).
type ErrorItem struct {
	Gold      string  `json:"gold_id"`
	Agreement string  `json:"agreement"` // single, agreed, adjudicated
	Label     string  `json:"label"`
	Answer    string  `json:"answer"`
	By        string  `json:"by"` // key, layer1 or subq
	Conf      float64 `json:"confidence"`
}

// selectiveOf measures one question (targets: the report targets; 0.99
// must be one for the agreement counts). model is the trained model (nil:
// none), labels its own evaluation labels (its holdout's re-score), z
// and fraction the requirement's and the split's. The route is the
// production escalation cascade (route.Build with the key, layer 1 and
// legacy SubQ offers) on the dev split: the honest split certification of
// its fixed decisions, the per-layer gates chosen on its selection part,
// the answers those gates give and, when it certified, the answers the app
// gives. The layer-1 and SubQ solo curves stay selective.Targets scans
// (diagnostics, optimistic by construction).
func selectiveOf(q *engine.Question, dq *DevQuestion, model *Model, labels map[int64]evalLabel, cutoff float64, req Requirement,
	targets []float64, fraction float64, kinds map[string]string) (*Selective, error) {
	var l1, sq []selective.Ranked
	part := map[int64]bool{} // dev certification part
	sel := &Selective{Answered: map[string]int{}, Wrong: map[string]int{}, Certified: map[string][]selective.Certification{}}
	var items []route.Item
	byID := make(map[int64]devAnswer, len(dq.answers))
	for _, a := range dq.answers {
		byID[a.item.id] = a
		part[a.item.id] = selective.InCertifyPart(a.item.group, fraction)
		if a.l1 >= 0 {
			l1 = append(l1, selective.Ranked{ID: a.item.id, Correct: a.l1 == a.class, Confidence: a.l1Conf})
		}
		if a.subq >= 0 {
			sq = append(sq, selective.Ranked{ID: a.item.id, Correct: a.subq == a.class, Confidence: a.subqConf})
		}
		var offers []route.Offer
		if a.keyProbs != nil {
			sel.Keys++
			offers = append(offers, route.Offer{Layer: route.LayerKey, Probs: a.keyProbs, Rule: a.keyRule})
		}
		if a.l1Probs != nil {
			offers = append(offers, route.Offer{Layer: route.Layer1, Probs: a.l1Probs})
		}
		if a.subqProbs != nil {
			offers = append(offers, route.Offer{Layer: route.LayerSubQ, Probs: a.subqProbs})
		}
		items = append(items, route.Item{ID: a.item.id, Class: a.class, Certify: part[a.item.id], Offers: offers})
	}
	l1, sq = selective.Rank(l1), selective.Rank(sq)
	sel.Layer1, sel.SubQ = selective.Targets(l1, targets, req.Z), selective.Targets(sq, targets, req.Z)
	split := func(rows []selective.Ranked) (s, c []selective.Ranked) {
		for _, r := range rows {
			if part[r.ID] {
				c = append(c, r)
			} else {
				s = append(s, r)
			}
		}
		return s, c
	}
	sl1, cl1 := split(l1)
	ssq, csq := split(sq)
	for _, t := range targets {
		sel.Certified["layer1"] = append(sel.Certified["layer1"], selective.Certify(sl1, cl1, t, req.Z))
		sel.Certified["subq"] = append(sel.Certified["subq"], selective.Certify(ssq, csq, t, req.Z))
	}
	states := make(map[float64]route.State, len(targets))
	cascade := make(map[float64][]routeRow, len(targets))
	for _, t := range targets {
		st, err := route.Build(q, cutoff, t, req.Z, req.MinCoverage, items)
		if err != nil {
			return nil, fmt.Errorf("dev %s route at %.2f: %w", q.Name, t, err)
		}
		states[t] = st
		sel.Certified["stack"] = append(sel.Certified["stack"], st.Certification)
		rows, given, err := devCascadeRows(q, cutoff, items, st, byID)
		if err != nil {
			return nil, err
		}
		cascade[t] = rows
		sel.Given = append(sel.Given, given)
		certifiedGiven := map[route.Layer]int{}
		if st.Certification.Certified {
			for l, n := range given {
				certifiedGiven[l] = n
			}
		}
		sel.CertifiedGiven = append(sel.CertifiedGiven, certifiedGiven)
		curve := selective.Targets(selective.Rank(rankedRows(rows)), []float64{t}, req.Z)[0]
		sel.Stack = append(sel.Stack, curve)
	}
	for _, t := range targets {
		th, thc := selective.NoThreshold, selective.NoThreshold
		if model != nil && model.Holdout != nil {
			r := req
			r.TargetAccuracy = t
			c := model.Holdout.certify(q.Type, labels, cutoff, r)
			th, thc = c.Threshold, threshold(c)
		}
		sel.Layer1At = append(sel.Layer1At, operating(l1, len(dq.answers), t, th, thc))
		st := states[t]
		o := Operating{Target: t, Threshold: selective.NoThreshold, Certified: selective.NoThreshold,
			Cascade: true, N: len(dq.answers), Covered: len(cascade[t])}
		o.Errors = wrongRows(cascade[t])
		if st.Certification.Certified {
			o.CoveredCertified, o.ErrorsCertified = o.Covered, o.Errors
		}
		sel.StackAt = append(sel.StackAt, o)
		if t != 0.99 {
			continue
		}
		// The errors at the route's point operating point, by how their
		// gold label came about: the cascade's gate-qualified answers. The
		// app gives the same answers only when the route certified.
		for _, row := range cascade[t] {
			kind := kinds[row.info.item.gold]
			if kind == "" {
				kind = "unknown"
			}
			sel.Answered[kind]++
			if !row.Correct {
				sel.Wrong[kind]++
				sel.errors = append(sel.errors, ErrorItem{Gold: row.info.item.gold, Agreement: kind, Label: className(q, row.info.class),
					Answer: className(q, row.Class), By: string(row.Layer), Conf: row.Confidence})
			}
		}
	}
	slices.SortFunc(sel.errors, func(a, b ErrorItem) int {
		if c := cmp.Compare(b.Conf, a.Conf); c != 0 {
			return c
		}
		return cmp.Compare(a.Gold, b.Gold)
	})
	return sel, nil
}

// routeRow is one dev item's route answer: the participant that gave it,
// the class it answered and the item.
type routeRow struct {
	selective.Ranked
	Layer route.Layer
	Class int
	info  devAnswer
}

// devCascadeRows are the route's decisions on the dev items: exactly
// route.State.Decide's escalation cascade (the gates chosen on the
// selection part), run on a copy whose certification verdict is ignored,
// because this diagnostic measures the gates before the one honest test on
// the certification part. An item the cascade abstains on has no row.
func devCascadeRows(q *engine.Question, cutoff float64, items []route.Item, st route.State,
	info map[int64]devAnswer) ([]routeRow, map[route.Layer]int, error) {
	diag := st
	diag.Certification.Certified = true // the gates' decisions, before the certification test
	rows := make([]routeRow, 0, len(items))
	given := map[route.Layer]int{}
	for _, it := range items {
		a, err := diag.Decide(q, it.Offers)
		if err != nil {
			return nil, nil, fmt.Errorf("dev %s cascade: %w", q.Name, err)
		}
		if a.Abstain != "" {
			continue
		}
		class, ok := devClass(a, info[it.ID])
		if !ok {
			return nil, nil, fmt.Errorf("dev %s cascade: layer %q answered item %d without that offer", q.Name, a.Layer, it.ID)
		}
		conf, ok := selective.Confidence(a.Answer, cutoff)
		if !ok {
			return nil, nil, fmt.Errorf("dev %s cascade: layer %q answered item %d without a confidence", q.Name, a.Layer, it.ID)
		}
		rows = append(rows, routeRow{Ranked: selective.Ranked{ID: it.ID, Correct: class == it.Class, Confidence: conf},
			Layer: a.Layer, Class: class, info: info[it.ID]})
		given[a.Layer]++
	}
	return rows, given, nil
}

// devClass is the class a dev participant's offer answers (the same
// selective.Decide the gate rows used), false when it offered nothing.
func devClass(a route.Answer, d devAnswer) (int, bool) {
	switch a.Layer {
	case route.LayerKey:
		return d.key, d.keyProbs != nil
	case route.Layer1:
		return d.l1, d.l1Probs != nil
	case route.LayerSubQ:
		return d.subq, d.subqProbs != nil
	}
	return 0, false
}

// wrongRows counts the wrong answers of a cascade's rows.
func wrongRows(rows []routeRow) int {
	n := 0
	for _, r := range rows {
		if !r.Correct {
			n++
		}
	}
	return n
}

func rankedRows(rows []routeRow) []selective.Ranked {
	out := make([]selective.Ranked, len(rows))
	for i, r := range rows {
		out[i] = r.Ranked
	}
	return out
}

// operating applies a point threshold (chosen on the selection labels) and
// its certified threshold (the point one when certified; NoThreshold, which
// answers nothing, otherwise) to dev rows.
func operating(rows []selective.Ranked, n int, target, th, certified float64) Operating {
	o := Operating{Target: target, Threshold: th, Certified: certified, N: n}
	o.Covered, o.Errors = selective.Answered(rows, th)
	o.CoveredCertified, o.ErrorsCertified = selective.Answered(rows, certified)
	return o
}

// agreementKinds is the gold sample's agreement per question and id.
func agreementKinds(t interface{ Fatal(...any) }, gold *goldset.Set) map[string]map[string]string {
	k, err := gold.Agreement()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// stackSummary is the mean over questions at each target: the honest dev
// certification (coverage, and how many questions are certified), the
// dev-curve coverage (layer 1, SubQ, the route), the route's operating
// points on dev (its per-layer gates: coverage, answers, errors,
// precision; the app's answers when it certified), and the answers each
// layer gives.
func stackSummary(qs []DevQuestion, targets []float64) []map[string]float64 {
	var out []map[string]float64
	for i, t := range targets {
		m := map[string]float64{"target": t}
		var cov, covErr, covC, covCErr int
		for _, q := range qs {
			if q.Select == nil {
				continue
			}
			s := q.Select
			for _, name := range []string{"layer1", "subq", "stack"} {
				c := s.Certified[name][i]
				m[name+"_certified_coverage"] += c.Coverage
				if c.Certified {
					m[name+"_certified_questions"]++
				}
			}
			m["layer1_coverage"] += s.Layer1[i].Coverage
			m["layer1_coverage_lb"] += s.Layer1[i].CoverageLB
			m["subq_coverage"] += s.SubQ[i].Coverage
			m["subq_coverage_lb"] += s.SubQ[i].CoverageLB
			m["stack_coverage"] += s.Stack[i].Coverage
			m["stack_coverage_lb"] += s.Stack[i].CoverageLB
			if s.StackAt[i].N > 0 {
				m["stack_point_coverage"] += float64(s.StackAt[i].Covered) / float64(s.StackAt[i].N)
				m["stack_certified_op_coverage"] += float64(s.StackAt[i].CoveredCertified) / float64(s.StackAt[i].N)
			}
			for _, l := range []route.Layer{route.LayerKey, route.Layer1, route.LayerSubQ} {
				m["given_"+string(l)] += float64(s.Given[i][l])
				m["certified_given_"+string(l)] += float64(s.CertifiedGiven[i][l])
			}
			cov, covErr = cov+s.StackAt[i].Covered, covErr+s.StackAt[i].Errors
			covC, covCErr = covC+s.StackAt[i].CoveredCertified, covCErr+s.StackAt[i].ErrorsCertified
		}
		n := float64(len(qs))
		for k := range m {
			if k != "target" && !slices.Contains([]string{"layer1_certified_questions", "subq_certified_questions", "stack_certified_questions"}, k) {
				m[k] /= n
			}
		}
		if cov > 0 {
			m["stack_point_precision"] = 1 - float64(covErr)/float64(cov)
		}
		if covC > 0 {
			m["stack_certified_op_precision"] = 1 - float64(covCErr)/float64(covC)
		}
		m["stack_point_answers"], m["stack_point_errors"] = float64(cov), float64(covErr)
		m["stack_certified_op_answers"], m["stack_certified_op_errors"] = float64(covC), float64(covCErr)
		out = append(out, m)
	}
	return out
}
