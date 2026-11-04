package layer1

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// RouteReport is one question's route after a pass: the certification of
// the fixed cascade (with its Eligible denominator), the gate-qualified
// members and how many answers each layer gave. An abstention names no
// layer and is not counted in Given.
type RouteReport struct {
	Question      string                  `json:"question"`
	Certification selective.Certification `json:"certification"`
	// Answering is the route's one answer gate: certified at the target and
	// covering at least the requirement's minimum coverage.
	Answering bool                `json:"answering"`
	Members   []route.Member      `json:"members"`
	Given     map[route.Layer]int `json:"given"`
}

// routeCertifications is every question's route certification in a stored
// report, keyed by question. The route is what answers, so it is the one
// certification the labels and questions stages follow; the layer-1 verdict
// certification is the training-stage diagnostic.
func routeCertifications(last *Report) map[string]selective.Certification {
	out := make(map[string]selective.Certification, len(last.Routes))
	for _, r := range last.Routes {
		out[r.Question] = r.Certification
	}
	return out
}

// routeParticipants are one pass's sources of route offers: the rules
// measured on the selection labels, the configured decider's stored
// answers, and the layer-1 distribution the item's part allows (the
// selection part's out-of-fold prediction, the certification part's final
// model). The re-score feeds the same three from the store only, so it
// offers exactly what the run wrote.
type routeParticipants struct {
	rules  []RuleInfo
	fires  map[int64][]int
	subq   map[string]*compactAnswers
	rows   rowIndex
	layer1 func(*settled, int64) (engine.Answer, bool)
}

// oofProbs are a model's out-of-fold predictions of its selection labels,
// keyed by message: the only layer-1 distribution route gate selection may
// see. The final model trained on those labels, so its predictions there
// are in-sample and optimistic.
type oofProbs map[int64][]float64

// oofByID indexes a model's selection-part holdout once per route build. A
// selection label with no entry (its fold could not score it, or the
// active model never scored it) has no out-of-fold prediction: the route
// offers no layer-1 distribution for it and never falls back to the final
// model.
func oofByID(m *Model) oofProbs {
	if m == nil || m.Holdout == nil || len(m.Holdout.Select) == 0 {
		return nil
	}
	out := make(oofProbs, len(m.Holdout.Select))
	for _, it := range m.Holdout.Select {
		out[it.ID] = it.Probs
	}
	return out
}

// layer1Source names the layer-1 distribution a route item offers. Gate
// selection (a selection item) may use only the out-of-fold prediction; a
// certification item (the one honest test of the cascade) and answering
// use the final model.
type layer1Source uint8

const (
	layer1Final  layer1Source = iota // the final model (or the caller's stored answer)
	layer1Select                     // the selection part's out-of-fold prediction
)

// offers are one message's offers for a question: the best layer-0 key
// (its soft distribution), the layer-1 distribution of the item's part
// (see layer1Source) and the configured decider's stored distribution. A
// refused, abstained or invalid stored answer, a message without a model
// row, a selection label without an out-of-fold prediction and a question
// no key fires on simply offer nothing.
func (p routeParticipants) offers(plan *Plan, s *settled, id int64, source layer1Source, oof oofProbs) []route.Offer {
	return p.offersWith(plan, s, id, engine.Answer{}, false, source, oof)
}

// offersWith is offers with the layer-1 answer the caller already holds:
// the commit computes it once for the decision and the preview, so the
// route must not infer it again. hasStored false falls back to p.layer1
// for a certification item and to the out-of-fold map for a selection item
// (no fallback between them).
func (p routeParticipants) offersWith(plan *Plan, s *settled, id int64, stored engine.Answer, hasStored bool, source layer1Source, oof oofProbs) []route.Offer {
	q := s.q
	var offers []route.Offer
	if r, probs, ok := keyAnswer(q, p.rules, p.fires, id); ok {
		offers = append(offers, route.Offer{Layer: route.LayerKey, Probs: probs, Rule: r.ID})
	}
	if hasStored {
		if probs, ok := selective.Probs(q, stored); ok {
			offers = append(offers, route.Offer{Layer: route.Layer1, Probs: probs})
		}
	} else if source == layer1Select {
		if probs, ok := oof[id]; ok {
			offers = append(offers, route.Offer{Layer: route.Layer1, Probs: probs})
		}
	} else if p.layer1 != nil {
		if a, ok := p.layer1(s, id); ok {
			// the stored answer's own form, so the run and a re-score
			// (which reads it back) offer the same distribution
			if probs, ok := selective.Probs(q, a); ok {
				offers = append(offers, route.Offer{Layer: route.Layer1, Probs: probs})
			}
		}
	}
	if p.rows != nil {
		if c := p.subq[q.Name]; c != nil {
			if row, ok := p.rows.Row(id); ok {
				if probs, ok := c.distribution(row); ok {
					offers = append(offers, route.Offer{Layer: route.LayerSubQ, Probs: probs})
				}
			}
		}
	}
	return offers
}

// trainedParticipants are a training pass's route offers: the rules and
// stored SubQ answers it gathered, and the final model's layer-1 answer
// (selective.Answer with the answering model's threshold). A selection
// item's layer-1 offer never comes from this final model: routeItems uses
// the model's out-of-fold holdout, because the final model trained on the
// selection labels.
func trainedParticipants(plan *Plan, tr *Trained) routeParticipants {
	if tr == nil {
		return routeParticipants{}
	}
	p := routeParticipants{layer1: func(s *settled, id int64) (engine.Answer, bool) {
		if s.model == nil {
			return engine.Answer{}, false
		}
		probs, ok := tr.predictWith(s.model, id)
		if !ok {
			return engine.Answer{}, false
		}
		return selective.Answer(s.q, probs, plan.Profile.Cutoff(s.q.Name), s.model.Threshold), true
	}}
	if tr.Design != nil {
		p.rows = tr.Design
	}
	if tr.g != nil {
		p.rules, p.fires, p.subq = tr.g.rules, tr.g.fires, tr.g.subq
	}
	return p
}

// routeStates builds every question's route after the pass settles: the
// items are the evaluation labels (the certification part filtered to the
// requirement's certify sources) with each message's offers. route.Build
// chooses the per-layer gates on the selection part, whose layer-1 offers
// are the model's out-of-fold predictions, and certifies the cascade once
// on the certification part, whose layer-1 offers are the final model's.
func routeStates(ctx context.Context, plan *Plan, tr *Trained, questions []*settled) (map[string]route.State, error) {
	p := trainedParticipants(plan, tr)
	return routeStatesFrom(ctx, plan, questions, func(s *settled) []route.Item {
		return routeItems(plan, p, tr.sets[s.q.Name], s)
	})
}

// routeStatesFrom builds every question's route from its items.
func routeStatesFrom(ctx context.Context, plan *Plan, questions []*settled, items func(*settled) []route.Item) (map[string]route.State, error) {
	out := make(map[string]route.State, len(questions))
	for _, s := range questions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req := plan.Requirements.For(s.q.Name)
		st, err := route.Build(s.q, plan.Profile.Cutoff(s.q.Name), req.TargetAccuracy, req.EffectiveZ(), req.MinCoverage, items(s))
		if err != nil {
			return nil, fmt.Errorf("layer1: %s route: %w", s.q.Name, err)
		}
		out[s.q.Name] = st
	}
	return out, nil
}

// routeItems are one question's labelled items from a training pass:
// every evaluation label is a selection item, and a certification-part
// label is an item only when its source is one the requirement certifies
// against (as the model's own holdout does).
func routeItems(plan *Plan, p routeParticipants, qs *questionSet, s *settled) []route.Item {
	if qs == nil {
		return nil
	}
	oof := oofByID(s.model)
	items := make([]route.Item, 0, len(qs.eval))
	for _, e := range qs.eval {
		if it, ok := routeItem(plan, p, s, e.ID, e.Class, e.Source, e.Certify, oof); ok {
			items = append(items, it)
		}
	}
	return items
}

// routeItemsNow are one question's labelled items from the current
// evaluation labels (a re-score): the split is the layer-1 one (the
// message's conversation), and a certification label counts only when
// the requirement certifies against its source. The items are the stored
// population's messages (the design rows the pass answered): a label on
// a pending message is not a certification item, exactly as in a pass.
func routeItemsNow(plan *Plan, p routeParticipants, s *settled, labels map[int64]evalLabel, groups map[int64]string) []route.Item {
	if len(labels) == 0 || p.rows == nil {
		return nil
	}
	oof := oofByID(s.model)
	ids := make([]int64, 0, len(labels))
	for id := range labels {
		if _, ok := p.rows.Row(id); ok {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	items := make([]route.Item, 0, len(ids))
	for _, id := range ids {
		group, ok := groups[id]
		if !ok {
			continue // not a member of the current mailbox
		}
		l := labels[id]
		certify := selective.InCertifyPart(group, plan.Config.CertifyFraction)
		if it, ok := routeItem(plan, p, s, id, l.class, l.source, certify, oof); ok {
			items = append(items, it)
		}
	}
	return items
}

// routeItem keeps one labelled evaluation label as a route item: a
// certification label is an item only when the requirement certifies
// against its source; any source's selection label counts. The item's
// certify kind chooses its layer-1 offer: a selection item offers the
// out-of-fold prediction (nothing when it has none), a certification item
// the final model's distribution.
func routeItem(plan *Plan, p routeParticipants, s *settled, id int64, class int, source string, certify bool, oof oofProbs) (route.Item, bool) {
	if certify && !slices.Contains(plan.Requirements.For(s.q.Name).CertifySources, source) {
		return route.Item{}, false
	}
	offerSource := layer1Select
	if certify {
		offerSource = layer1Final
	}
	return route.Item{ID: id, Class: class, Certify: certify, Offers: p.offers(plan, s, id, offerSource, oof)}, true
}

// routeOffers are one message's route offers of a training pass on the
// final model (the tests read them); the commit and the re-score build
// them from the stored answers with routeParticipants. Gate selection uses
// routeItems, never this.
func routeOffers(plan *Plan, tr *Trained, s *settled, id int64) []route.Offer {
	return trainedParticipants(plan, tr).offers(plan, s, id, layer1Final, nil)
}

// routeReports turns the settled states into the run report, in profile
// order.
func routeReports(plan *Plan, states map[string]route.State) []RouteReport {
	out := make([]RouteReport, 0, len(plan.Profile.Questions))
	for i := range plan.Profile.Questions {
		q := &plan.Profile.Questions[i]
		st, ok := states[q.Name]
		if !ok {
			continue
		}
		out = append(out, RouteReport{Question: q.Name, Certification: st.Certification, Answering: st.Answering(),
			Members: st.Members, Given: map[route.Layer]int{}})
	}
	return out
}

// routeOfferer gives one message's route offers for a question, reusing the
// layer-1 answer the commit already computed when hasStored is true.
type routeOfferer func(*settled, int64, engine.Answer, bool) []route.Offer

// routeAnswerer is the route half of a commit: one route.Answer for every
// question of the profile (an abstention included, so the app escalates),
// or "" when no settled model answers the message (the layer-1 key has no
// row: pending, never imputed). answer is the commit's layer-1 answer
// source, so the route's population is exactly the stored answers'
// population. It counts what it gives in the report.
func routeAnswerer(o *outcome, offers routeOfferer, rep *Report) func(int64, map[string]engine.Answer) (string, error) {
	index := make(map[string]int, len(rep.Routes))
	for i := range rep.Routes {
		index[rep.Routes[i].Question] = i
	}
	return func(id int64, stored map[string]engine.Answer) (string, error) {
		// The commit calls this only for a message whose decision has at
		// least one answer; the map is exactly that population.
		if len(stored) == 0 {
			return "", nil
		}
		answers := make(map[string]route.Answer, len(o.questions))
		for _, s := range o.questions {
			st, ok := o.routes[s.q.Name]
			if !ok {
				return "", fmt.Errorf("layer1: %s: no route state", s.q.Name)
			}
			// An uncertified route answers not_certified whatever the
			// offers: do not compute them (the app escalates).
			var itemOffers []route.Offer
			if st.Answering() {
				item, has := stored[s.q.Name]
				itemOffers = offers(s, id, item, has)
			}
			a, err := st.Decide(s.q, itemOffers)
			if err != nil {
				return "", fmt.Errorf("layer1: %s: route answer of message %d: %w", s.q.Name, id, err)
			}
			answers[s.q.Name] = a
			if a.Abstain == "" {
				rep.Given++
				if i, ok := index[s.q.Name]; ok {
					rep.Routes[i].Given[a.Layer]++
				}
			}
		}
		raw, err := json.Marshal(answers)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}
