package layer1

import (
	"reflect"
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// layerOffer is one layer's offer of a message (ok false: it offered
// nothing).
func layerOffer(offers []route.Offer, layer route.Layer) (route.Offer, bool) {
	for _, o := range offers {
		if o.Layer == layer {
			return o, true
		}
	}
	return route.Offer{}, false
}

// memberOfState is one layer's gate-qualified member (ok false: the layer
// is not a member).
func memberOfState(st route.State, layer route.Layer) (route.Member, bool) {
	for _, m := range st.Members {
		if m.Layer == layer {
			return m, true
		}
	}
	return route.Member{}, false
}

// The layer-1 offer follows the item kind, never the data: a selection item
// carries its model's out-of-fold distribution, a certification item the
// final model's, and a selection label without an out-of-fold prediction
// offers no layer 1 at all (the key and Ondelle offers stay).
func TestSelectionOffersUseOutOfFoldPredictions(t *testing.T) {
	t.Parallel()
	truth := true
	q := &engine.Question{Name: "yesno", Type: engine.Noul, Instructions: "is it so?"}
	rule := RuleInfo{ID: "r1", Question: q.Name, Answer: evidence.RuleAnswer{Truth: &truth},
		Stat: evidence.RuleStat{Key: true, WilsonLow: 0.9}}
	final := []float64{0.9, 0.1}
	oof := []float64{0.25, 0.75}
	p := routeParticipants{
		rules: []RuleInfo{rule},
		fires: map[int64][]int{1: {0}, 2: {0}},
		ondelle: map[string]*compactAnswers{q.Name: {classes: 2,
			probs: []float32{0.75, 0.25, 0.75, 0.25}, state: []answerState{answerOK, answerOK}}},
		rows: membership{1: 0, 2: 1},
		layer1: func(*settled, int64) (engine.Answer, bool) {
			return selective.Given(q, final, 0.5), true
		},
	}
	s := &settled{q: q, model: &Model{Holdout: &Holdout{Select: []HeldItem{{ID: 1, Probs: oof}}}}}
	oofByMessage := oofByID(s.model)
	if len(oofByMessage) != 1 || !slices.Equal(oofByMessage[1], oof) {
		t.Fatalf("the out-of-fold map is %v, want message 1 only", oofByMessage)
	}

	// A selection item carries the out-of-fold distribution, not the final
	// model's.
	selection := p.offers(nil, s, 1, layer1Select, oofByMessage)
	got, ok := layerOffer(selection, route.Layer1)
	if !ok || !slices.Equal(got.Probs, oof) {
		t.Fatalf("selection item's layer-1 offer is %v, want the out-of-fold %v", got.Probs, oof)
	}
	for _, layer := range []route.Layer{route.LayerKey, route.LayerOndelle} {
		if _, ok := layerOffer(selection, layer); !ok {
			t.Fatalf("selection item lost its %s offer: %+v", layer, selection)
		}
	}

	// A certification item keeps the final model's distribution.
	certification := p.offers(nil, s, 1, layer1Final, oofByMessage)
	got, ok = layerOffer(certification, route.Layer1)
	if !ok || !slices.Equal(got.Probs, final) {
		t.Fatalf("certification item's layer-1 offer is %v, want the final model's %v", got.Probs, final)
	}
	for _, layer := range []route.Layer{route.LayerKey, route.LayerOndelle} {
		if _, ok := layerOffer(certification, layer); !ok {
			t.Fatalf("certification item lost its %s offer: %+v", layer, certification)
		}
	}

	// A selection label with no out-of-fold prediction offers no layer 1:
	// no in-sample fallback.
	missing := p.offers(nil, s, 2, layer1Select, oofByMessage)
	if o, ok := layerOffer(missing, route.Layer1); ok {
		t.Fatalf("a selection label with no out-of-fold prediction offered the final model: %v", o.Probs)
	}
	for _, layer := range []route.Layer{route.LayerKey, route.LayerOndelle} {
		if _, ok := layerOffer(missing, layer); !ok {
			t.Fatalf("a selection label without an out-of-fold prediction lost its %s offer: %+v", layer, missing)
		}
	}
	// The same message is still a certification item on the final model.
	certification = p.offers(nil, s, 2, layer1Final, oofByMessage)
	if got, ok := layerOffer(certification, route.Layer1); !ok || !slices.Equal(got.Probs, final) {
		t.Fatalf("certification item without a selection holdout offered %v, want the final model's %v", got.Probs, final)
	}
}

// The gate-selection path feeds route.Build the out-of-fold distribution,
// not the final model's in-sample one: the regression fails when
// routeItem's selection source falls back to the final model.
func TestRouteItemsSelectLayer1OnOutOfFoldPredictions(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Train(t.Context(), plan, &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := w.p.Question("category")
	s := &settled{q: cq, model: tr.Models["category"]}
	if s.model == nil || s.model.Holdout == nil || len(s.model.Holdout.Select) == 0 {
		t.Fatalf("category has no out-of-fold holdout: %+v", s.model)
	}
	byID := map[int64]route.Item{}
	for _, it := range routeItems(plan, trainedParticipants(plan, tr), tr.sets[cq.Name], s) {
		byID[it.ID] = it
	}
	oof := oofByID(s.model)
	selection, scored, different := 0, 0, 0
	for _, e := range tr.sets[cq.Name].eval {
		if e.Certify {
			continue
		}
		selection++
		it, ok := byID[e.ID]
		if !ok {
			t.Fatalf("selection label %d has no route item", e.ID)
		}
		got, offered := layerOffer(it.Offers, route.Layer1)
		want, hasOOF := oof[e.ID]
		if !hasOOF {
			if offered {
				t.Fatalf("selection label %d has no out-of-fold prediction but offered %v", e.ID, got.Probs)
			}
			continue
		}
		if !offered || !slices.Equal(got.Probs, want) {
			t.Fatalf("selection label %d offered %v, want the out-of-fold %v", e.ID, got.Probs, want)
		}
		final, ok := tr.predictWith(s.model, e.ID)
		if !ok {
			t.Fatalf("the final model has no prediction for selection label %d", e.ID)
		}
		if !slices.Equal(want, final) {
			different++
		}
		scored++
	}
	if selection < 2 || scored == 0 || different == 0 {
		t.Fatalf("category has %d selection labels, %d scored out of fold and %d where OOF differs from the final model (the fixture must exercise the distinction)", selection, scored, different)
	}
}

// The route's gates and members are chosen on the out-of-fold selection
// offers. An artificially overconfident final model on the selection rows
// (every answer correct at 0.99) would keep a layer-1 member under the old
// final-model offers; the out-of-fold offers (wrong at 0.6) do not.
func TestRouteGatesFollowOutOfFoldNotAnOverconfidentFinalModel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := Train(t.Context(), plan, &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cq, _ := w.p.Question("category")
	qs := tr.sets[cq.Name]
	if qs == nil {
		t.Fatal("category has no question set")
	}
	classes := classesOf(cq)
	classOf := make(map[int64]int, len(qs.eval))
	for _, e := range qs.eval {
		classOf[e.ID] = e.Class
	}

	// The final model: overconfident and correct on every evaluation row.
	p := trainedParticipants(plan, tr)
	p.layer1 = func(s *settled, id int64) (engine.Answer, bool) {
		class, ok := classOf[id]
		if !ok {
			return engine.Answer{}, false
		}
		probs := make([]float64, classes)
		probs[class] = 0.99
		for k := range probs {
			if k != class {
				probs[k] = 0.01 / float64(classes-1)
			}
		}
		return selective.Given(s.q, probs, plan.Profile.Cutoff(s.q.Name)), true
	}

	// The out-of-fold selection predictions: wrong, not overconfident.
	holdout := &Holdout{}
	for _, e := range qs.eval {
		if e.Certify {
			continue
		}
		probs := make([]float64, classes)
		wrong := (e.Class + 1) % classes
		probs[wrong] = 0.6
		for k := range probs {
			if k != wrong {
				probs[k] = 0.4 / float64(classes-1)
			}
		}
		holdout.Select = append(holdout.Select, HeldItem{ID: e.ID, Probs: probs})
	}
	s := &settled{q: cq, model: &Model{Holdout: holdout}}
	if len(holdout.Select) < 2 {
		t.Fatalf("category has %d out-of-fold selection labels", len(holdout.Select))
	}

	// The production items use the out-of-fold offers for selection and
	// the forced final model for certification. The baseline is the old
	// behavior: every labelled item offers the final model.
	oofItems := routeItems(plan, p, qs, s)
	finalItems := make([]route.Item, 0, len(qs.eval))
	for _, e := range qs.eval {
		if e.Certify && !slices.Contains(plan.Requirements.For(cq.Name).CertifySources, e.Source) {
			continue
		}
		finalItems = append(finalItems, route.Item{ID: e.ID, Class: e.Class, Certify: e.Certify,
			Offers: p.offers(plan, s, e.ID, layer1Final, nil)})
	}
	states, err := routeStatesFrom(t.Context(), plan, []*settled{s}, func(*settled) []route.Item { return oofItems })
	if err != nil {
		t.Fatal(err)
	}
	old, err := routeStatesFrom(t.Context(), plan, []*settled{s}, func(*settled) []route.Item { return finalItems })
	if err != nil {
		t.Fatal(err)
	}
	oofState, oldState := states[cq.Name], old[cq.Name]
	oofMember, oofIsMember := memberOfState(oofState, route.Layer1)
	_, oldIsMember := memberOfState(oldState, route.Layer1)
	if !oldIsMember {
		t.Fatalf("the overconfident final model did not produce a layer-1 member: %+v", oldState.Members)
	}
	if oofIsMember {
		t.Fatalf("the out-of-fold selection offers produced layer-1 gate %v; the gates were not chosen on them: %+v", oofMember.Gate, oofState.Members)
	}
	if reflect.DeepEqual(oofState.Members, oldState.Members) {
		t.Fatalf("the out-of-fold and final-model selection offers chose the same members: %+v", oofState.Members)
	}
	// The key and Ondelle offers are unchanged: both states still see them.
	for _, layer := range []route.Layer{route.LayerKey, route.LayerOndelle} {
		if (memberOf(oldState, layer)) != (memberOf(oofState, layer)) {
			t.Fatalf("%s membership changed with the layer-1 offer source: old %+v, out-of-fold %+v", layer, oldState.Members, oofState.Members)
		}
	}
}

// memberOf reports whether a layer is a member of a state.
func memberOf(st route.State, layer route.Layer) bool {
	_, ok := memberOfState(st, layer)
	return ok
}
