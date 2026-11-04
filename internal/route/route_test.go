package route

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

func question(t engine.QuestionType) *engine.Question {
	switch t {
	case engine.Noul:
		return &engine.Question{Name: "noul", Type: engine.Noul, Instructions: "Is it?"}
	case engine.Choice:
		return &engine.Question{Name: "choice", Type: engine.Choice, Instructions: "Which?",
			Options: []engine.Option{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	case engine.Score:
		return &engine.Question{Name: "score", Type: engine.Score, Instructions: "How much?",
			Levels: []string{"low", "mid", "high"}}
	}
	return nil
}

// peak is a distribution with confidence on class and the rest even.
func peak(q *engine.Question, class int, confidence float64) []float64 {
	n := classCount(q)
	rest := (1 - confidence) / float64(n-1)
	p := make([]float64, n)
	for i := range p {
		p[i] = rest
	}
	p[class] = confidence
	return p
}

func offer(l Layer, q *engine.Question, class int, confidence float64) Offer {
	return Offer{Layer: l, Probs: peak(q, class, confidence)}
}

func wrongClass(q *engine.Question, class int) int { return (class + 1) % classCount(q) }

func cloneItems(items []Item) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = it
		out[i].Offers = slices.Clone(it.Offers)
	}
	return out
}

func mustBuild(t *testing.T, q *engine.Question, cutoff, target, z float64, items []Item) State {
	t.Helper()
	s, err := Build(q, cutoff, target, z, 0, items)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

func mustDecide(t *testing.T, s State, q *engine.Question, offers []Offer) Answer {
	t.Helper()
	a, err := s.Decide(q, offers)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	return a
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	return b
}

// certifiedState is a key-only route certified at target 0.5: n labelled
// selection items and n labelled certification items, all correct at
// confidence 0.9.
func certifiedState(t *testing.T, q *engine.Question, n int) State {
	t.Helper()
	var items []Item
	for i := range n {
		items = append(items, Item{ID: int64(i + 1), Class: 0,
			Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
	}
	for i := range n {
		items = append(items, Item{ID: int64(1000 + i), Class: 0, Certify: true,
			Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !s.Certification.Certified {
		t.Fatalf("fixture does not certify: %+v", s.Certification)
	}
	return s
}

// TestRouteCertificationReportsClassRecall: the cascade's certification
// carries every labelled certification item, answered or not, so the route
// report can see both classes and a class the cascade never answered is
// reported as collapsed instead of vanishing from the recall.
func TestRouteCertificationReportsClassRecall(t *testing.T) {
	q := question(engine.Choice)
	var items []Item
	for i := range 40 {
		items = append(items, Item{ID: int64(i + 1), Class: 0,
			Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
	}
	for i := range 17 {
		items = append(items, Item{ID: int64(1000 + i), Class: 0, Certify: true,
			Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
	}
	for i := range 3 {
		items = append(items, Item{ID: int64(2000 + i), Class: 1, Certify: true,
			Offers: []Offer{offer(LayerKey, q, 1, 0.9)}})
	}
	// A third class whose only offer is below every gate: the cascade
	// abstains, and the class report must say so.
	for i := range 3 {
		items = append(items, Item{ID: int64(3000 + i), Class: 2, Certify: true,
			Offers: []Offer{offer(LayerKey, q, 2, 0.1)}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	want := []selective.ClassRecall{
		{Class: 0, Eligible: 17, Answered: 17, Correct: 17},
		{Class: 1, Eligible: 3, Answered: 3, Correct: 3},
		{Class: 2, Eligible: 3, Answered: 0, Correct: 0},
	}
	if !slices.Equal(s.Certification.ClassRecall, want) {
		t.Fatalf("class recall %+v, want %+v", s.Certification.ClassRecall, want)
	}
	if !s.Certification.ClassCollapsed {
		t.Fatalf("a class with 3 eligible rows and no answer is collapsed: %+v", s.Certification)
	}
	if !s.Certification.Certified || s.Certification.Answered != 20 {
		t.Fatalf("abstentions must not change the pooled verdict: %+v", s.Certification)
	}
}

// TestCascadeCertificationChoosesNoThreshold: the members and their gates
// are functions of the selection part only, and a cascade has no route
// threshold at all (Certification.Threshold is 0). Flipping every
// certification label, or unlinking every certification item, moves only
// the tested counts and the verdict.
func TestCascadeCertificationChoosesNoThreshold(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	for i := range n {
		items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	certStart := len(items)
	for i := range n {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	base := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	wantMembers := []Member{
		{Layer: LayerKey, Band: 2, Gate: 0.9},
		{Layer: Layer1, Band: 1, Gate: 0.8},
	}
	if !reflect.DeepEqual(base.Members, wantMembers) {
		t.Fatalf("members %+v, want %+v", base.Members, wantMembers)
	}
	if !base.Certification.Certified || base.Certification.Answered != n || base.Certification.Errors != 0 {
		t.Fatalf("fixture: %+v", base.Certification)
	}
	if base.Certification.Threshold != 0 || base.Certification.Selected != 0 {
		t.Fatalf("a cascade chose a threshold or a selection scan: %+v", base.Certification)
	}
	if got, want := base.Threshold(LayerKey), 0.9; got != want {
		t.Fatalf("key threshold %v, want its gate %v", got, want)
	}
	if got, want := base.Threshold(Layer1), 0.8; got != want {
		t.Fatalf("layer1 threshold %v, want its gate %v", got, want)
	}

	flipped := cloneItems(items)
	for i := certStart; i < len(flipped); i++ {
		flipped[i].Class = wrongClass(q, flipped[i].Class)
	}
	f := mustBuild(t, q, 0.5, 0.5, 1.96, flipped)
	if !reflect.DeepEqual(f.Members, wantMembers) {
		t.Fatalf("the certification labels moved the members: %+v", f.Members)
	}
	if f.Certification.Threshold != 0 || f.Certification.Selected != 0 {
		t.Fatalf("the certification labels moved a chosen field: %+v", f.Certification)
	}
	if f.Certification.Certified || f.Certification.Answered != n || f.Certification.Errors != n {
		t.Fatalf("every flipped label was still certified: %+v", f.Certification)
	}
	if f.Certification.Lower >= base.Certification.Lower {
		t.Fatalf("the lower bound did not move: %v -> %v", base.Certification.Lower, f.Certification.Lower)
	}

	// Unlabel all but one certification item: the chosen fields stay; the
	// tested counts move with the labels.
	unlabelled := cloneItems(items)
	for i := certStart + 1; i < len(unlabelled); i++ {
		unlabelled[i].Class = -1
		unlabelled[i].Certify = false
	}
	u := mustBuild(t, q, 0.5, 0.5, 1.96, unlabelled)
	if !reflect.DeepEqual(u.Members, wantMembers) || u.Certification.Threshold != 0 {
		t.Fatalf("unlabelling moved a chosen field: %+v", u)
	}
	if u.Certification.N != 1 || u.Certification.Eligible != 1 || u.Certification.Certified {
		t.Fatalf("one perfect certification label: %+v", u.Certification)
	}

	// No certification label at all: the members chosen on the selection
	// part are unchanged, and no threshold is invented to test.
	none := cloneItems(items)
	for i := certStart; i < len(none); i++ {
		none[i].Class = -1
		none[i].Certify = false
	}
	z := mustBuild(t, q, 0.5, 0.5, 1.96, none)
	if !reflect.DeepEqual(z.Members, wantMembers) {
		t.Fatalf("members moved with the certification labels: %+v", z)
	}
	if z.Certification.Certified || z.Certification.Threshold != 0 ||
		z.Certification.N != 0 || z.Certification.Eligible != 0 {
		t.Fatalf("no certification labels: %+v", z.Certification)
	}
	for _, l := range []Layer{LayerKey, Layer1} {
		if got := z.Threshold(l); got != selective.NoThreshold {
			t.Fatalf("an uncertified %q threshold is %v", l, got)
		}
	}
}

// TestGatesMeasureEveryOfferOfTheirLayer: each layer's gate is chosen on
// every labelled selection item where that layer offered, independently of
// the other layers. A higher layer offering on every item does not hide
// the lower layer's offers: layer1 gets its own gate and answers where the
// key is below its gate. A layer whose own offers never reach the target
// (SubQ here, always wrong) gets no gate and is not a member.
func TestGatesMeasureEveryOfferOfTheirLayer(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	add := func(id int64, certify bool) {
		items = append(items, Item{ID: id, Class: 0, Certify: certify, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),                 // every item, always right
			offer(Layer1, q, 0, 0.4),                   // every item, right at 0.4
			offer(LayerSubQ, q, wrongClass(q, 0), 0.7), // every item, always wrong
		}})
	}
	for i := range n {
		add(int64(i+1), false)
	}
	for i := range n {
		add(int64(1001+i), true)
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	want := []Member{
		{Layer: LayerKey, Band: 2, Gate: 0.9},
		{Layer: Layer1, Band: 1, Gate: 0.4},
	}
	if !reflect.DeepEqual(s.Members, want) {
		t.Fatalf("members %+v, want %+v", s.Members, want)
	}
	if !s.Certification.Certified || s.Certification.Answered != n {
		t.Fatalf("fixture: %+v", s.Certification)
	}
	a := mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.5), offer(Layer1, q, 0, 0.4)})
	if a.Abstain != "" || a.Layer != Layer1 || a.Choice != "a" {
		t.Fatalf("the lower layer did not answer below the higher gate: %+v", a)
	}
}

// TestTheCascadeUsesALowerLayerWhenAHigherMemberIsBelowItsGate is the
// regression of the single-stack-threshold bug: the key and layer1 are
// both members, the key offers below its gate and layer1 reaches its own.
// Before the cascade the item abstained (the key's score could not reach
// the one stack threshold); now layer1 answers. When no member reaches its
// gate the item still abstains below target, carrying the highest-priority
// member's distribution.
func TestTheCascadeUsesALowerLayerWhenAHigherMemberIsBelowItsGate(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	for i := range n {
		items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	for i := range n {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !s.Certification.Certified {
		t.Fatalf("fixture does not certify: %+v", s.Certification)
	}

	a := mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.9), offer(Layer1, q, 0, 0.8)})
	if a.Abstain != "" || a.Layer != LayerKey {
		t.Fatalf("the key at its gate did not answer first: %+v", a)
	}
	a = mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.5), offer(Layer1, q, 0, 0.8)})
	if a.Abstain != "" || a.Layer != Layer1 || a.Choice != "a" {
		t.Fatalf("the lower member did not answer below the higher gate: %+v", a)
	}
	below := mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.5), offer(Layer1, q, 0, 0.7)})
	if below.Abstain != engine.AbstainBelowTarget || below.Probabilities["a"] != 0.5 {
		t.Fatalf("no member reached its gate: %+v (want the key's distribution)", below)
	}
	if empty := mustDecide(t, s, q, nil); empty.Abstain != AbstainNoAnswer || empty.Probabilities != nil {
		t.Fatalf("no member offered: %+v", empty)
	}
}

// TestTheRouteIsNotJustLayer1: layer 1 offers on every item but only
// reaches the target on 60 of 80 certification labels, so its own gate
// covers 75%; the cascade falls through to the key on the 20 items where
// layer 1 is below its gate and covers all 80, still correct. Before the
// cascade the key's gate was measured on no item (layer 1 was the
// top-priority offer everywhere) and the route covered exactly layer 1.
func TestTheRouteIsNotJustLayer1(t *testing.T) {
	q := question(engine.Choice)
	const (
		correct = 60
		below   = 20
		target  = 0.9
	)
	var items []Item
	var l1Cert []selective.Ranked
	add := func(id int64, certify bool, l1Class int, l1Conf float64, withKey bool) {
		it := Item{ID: id, Class: 0, Certify: certify,
			Offers: []Offer{offer(Layer1, q, l1Class, l1Conf)}}
		if withKey {
			it.Offers = append(it.Offers, offer(LayerKey, q, 0, 0.7))
		}
		items = append(items, it)
		if certify && l1Conf >= target {
			l1Cert = append(l1Cert, selective.Ranked{ID: id, Correct: l1Class == 0, Confidence: l1Conf})
		}
	}
	var id int64 = 1
	for range correct {
		add(id, false, 0, 0.9, false)
		id++
	}
	for range below {
		add(id, false, wrongClass(q, 0), 0.8, true)
		id++
	}
	id = 1001
	for range correct {
		add(id, true, 0, 0.9, false)
		id++
	}
	for range below {
		add(id, true, wrongClass(q, 0), 0.8, true)
		id++
	}
	s := mustBuild(t, q, 0.5, target, 1.96, items)
	if !s.Certification.Certified {
		t.Fatalf("fixture does not certify: %+v", s.Certification)
	}
	// Layer 1 alone, certified on the same 80 eligible labels: its fixed
	// gate answers its 60 labels, coverage 0.75.
	l1 := selective.CertifyFixed(l1Cert, correct+below, target, 1.96)
	if !l1.Certified || math.Abs(l1.Coverage-0.75) > 1e-12 {
		t.Fatalf("layer 1 alone: %+v", l1)
	}
	if s.Certification.Coverage <= l1.Coverage || math.Abs(s.Certification.Coverage-1) > 1e-12 {
		t.Fatalf("the cascade covers %v, layer 1 alone %v", s.Certification.Coverage, l1.Coverage)
	}
	if want := []Member{{Layer: LayerKey, Band: 2, Gate: 0.7}, {Layer: Layer1, Band: 1, Gate: 0.9}}; !reflect.DeepEqual(s.Members, want) {
		t.Fatalf("members %+v, want %+v", s.Members, want)
	}
	answered := 0
	for _, it := range items {
		if !it.Certify {
			continue
		}
		a := mustDecide(t, s, q, it.Offers)
		if a.Abstain != "" {
			t.Fatalf("certification item %d abstained: %+v", it.ID, a)
		}
		answered++
		o, ok := offerOf(it.Offers, a.Layer)
		if !ok {
			t.Fatalf("item %d answered by %q without that offer", it.ID, a.Layer)
		}
		if class, _ := selective.Decide(q.Type, o.Probs, s.Cutoff); class != it.Class {
			t.Fatalf("item %d answered wrongly by %q", it.ID, a.Layer)
		}
	}
	if answered != s.Certification.Answered || answered != correct+below {
		t.Fatalf("Decide answered %d, certification counted %d of %d", answered, s.Certification.Answered, correct+below)
	}
}

// TestANonMemberNeverAnswers: a gate-less layer's offer is ignored even
// when it is perfect; a member below it answers. An uncertified route
// abstains not_certified whatever the offers are, and its layer
// thresholds are all NoThreshold.
func TestANonMemberNeverAnswers(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	for i := range n { // key offers on its own and is always wrong
		items = append(items, Item{ID: int64(i + 1), Class: 0,
			Offers: []Offer{offer(LayerKey, q, wrongClass(q, 0), 0.9)}})
	}
	for i := range n { // layer1 offers on its own and is always right
		items = append(items, Item{ID: int64(101 + i), Class: 0,
			Offers: []Offer{offer(Layer1, q, 0, 0.9)}})
	}
	for i := range n {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true,
			Offers: []Offer{offer(Layer1, q, 0, 0.9)}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !s.Certification.Certified {
		t.Fatalf("fixture does not certify: %+v", s.Certification)
	}
	if want := []Member{{Layer: Layer1, Band: 1, Gate: 0.9}}; !reflect.DeepEqual(s.Members, want) {
		t.Fatalf("members %+v, want only the gate-qualified layer1", s.Members)
	}

	perfectKey := []Offer{offer(LayerKey, q, 0, 1)}
	a := mustDecide(t, s, q, perfectKey)
	if a.Abstain != AbstainNoAnswer || a.Layer != "" || a.Probabilities != nil {
		t.Fatalf("a non-member's perfect offer answered: %+v", a)
	}
	a = mustDecide(t, s, q, append(perfectKey, offer(Layer1, q, 0, 0.9)))
	if a.Abstain != "" || a.Layer != Layer1 || a.Choice != "a" {
		t.Fatalf("the lower member did not answer: %+v", a)
	}
	if got, want := s.Threshold(Layer1), 0.9; got != want {
		t.Fatalf("layer1 threshold %v, want its gate %v", got, want)
	}
	if got := s.Threshold(LayerKey); got != selective.NoThreshold {
		t.Fatalf("a non-member threshold %v", got)
	}
	if got := s.Threshold(Layer("other")); got != selective.NoThreshold {
		t.Fatalf("an unknown layer threshold %v", got)
	}

	// The same members with too few certification labels: nothing answers.
	unc := mustBuild(t, q, 0.5, 0.99, 1.96, items)
	if unc.Certification.Certified || unc.Certification.Threshold != 0 {
		t.Fatalf("fixture certified at 0.99: %+v", unc.Certification)
	}
	a = mustDecide(t, unc, q, perfectKey)
	if a.Abstain != AbstainNotCertified || a.Layer != "" || a.Probabilities != nil {
		t.Fatalf("an uncertified route answered: %+v", a)
	}
	for _, l := range []Layer{LayerKey, Layer1, LayerSubQ} {
		if got := unc.Threshold(l); got != selective.NoThreshold {
			t.Fatalf("uncertified threshold of %q is %v", l, got)
		}
	}
}

// TestAnAbstainedAnswerHasNoValue: every reason and every question type
// leaves no Choice, Value or Confidence (selective.Confidence says so
// too), and the JSON round-trip preserves it.
func TestAnAbstainedAnswerHasNoValue(t *testing.T) {
	for _, q := range []*engine.Question{question(engine.Noul), question(engine.Choice), question(engine.Score)} {
		t.Run(string(q.Type), func(t *testing.T) {
			state := func(certified bool) State {
				return State{
					Question: q.Name, Type: q.Type, Cutoff: 0.5,
					Certification: selective.Certification{Certified: certified},
					Members:       []Member{{Layer: LayerKey, Band: 2, Gate: 0.6}},
				}
			}
			cases := []struct {
				name    string
				state   State
				offers  []Offer
				reason  string
				carries bool
			}{
				{"not certified", state(false), []Offer{offer(LayerKey, q, 0, 0.9)}, AbstainNotCertified, false},
				{"no answer", state(true), nil, AbstainNoAnswer, false},
				{"below target", state(true), []Offer{offer(LayerKey, q, 0, 0.5)}, engine.AbstainBelowTarget, true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					a := mustDecide(t, tc.state, q, tc.offers)
					if a.Abstain != tc.reason {
						t.Fatalf("reason %q, want %q: %+v", a.Abstain, tc.reason, a)
					}
					if a.Choice != "" || a.Value != 0 || a.Confidence != nil {
						t.Fatalf("an abstained answer carries a value: %+v", a)
					}
					if a.Layer != "" || a.Rule != "" {
						t.Fatalf("an abstained answer names a participant: %+v", a)
					}
					if tc.carries != (a.Probabilities != nil) {
						t.Fatalf("distribution presence %v, want %v: %+v", a.Probabilities != nil, tc.carries, a)
					}
					if _, ok := selective.Confidence(a.Answer, 0.5); ok {
						t.Fatal("selective.Confidence found a confidence in an abstention")
					}
					b := mustJSON(t, a)
					for _, forbidden := range []string{`"choice":`, `"value":`, `"confidence":`, `"layer":`, `"rule":`} {
						if strings.Contains(string(b), forbidden) {
							t.Fatalf("abstention JSON contains %s: %s", forbidden, b)
						}
					}
					var back Answer
					if err := json.Unmarshal(b, &back); err != nil {
						t.Fatalf("unmarshal: %v", err)
					}
					if !reflect.DeepEqual(back, a) {
						t.Fatalf("round trip:\n got %+v\nwant %+v", back, a)
					}
				})
			}
		})
	}
}

// TestRouteIsDeterministic: the same inputs give an equal state and
// byte-identical JSON; shuffled items and offers give the same; answers
// do not depend on offer order.
func TestRouteIsDeterministic(t *testing.T) {
	q := question(engine.Choice)
	var items []Item
	id := int64(1)
	add := func(certify bool, offers ...Offer) {
		items = append(items, Item{ID: id, Class: 0, Certify: certify, Offers: offers})
		id++
	}
	for range 10 {
		add(false, offer(LayerKey, q, 0, 0.9))
	}
	for range 10 {
		add(false, offer(Layer1, q, 0, 0.8))
	}
	for range 5 {
		add(false, offer(LayerKey, q, 0, 0.9), offer(Layer1, q, 0, 0.8), offer(LayerSubQ, q, 0, 0.7))
	}
	for range 5 {
		add(false, offer(LayerSubQ, q, 0, 0.7))
	}
	for range 5 {
		add(false)
	}
	for range 10 {
		add(true, offer(LayerKey, q, 0, 0.9))
	}
	for range 10 {
		add(true, offer(Layer1, q, 0, 0.8))
	}
	for range 5 {
		add(true)
	}
	base := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	again := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !reflect.DeepEqual(base, again) {
		t.Fatal("Build twice differs")
	}
	if b, c := mustJSON(t, base), mustJSON(t, again); !bytes.Equal(b, c) {
		t.Fatalf("Build twice differs in JSON:\n%s\n%s", b, c)
	}
	if !base.Certification.Certified || len(base.Members) != 3 {
		t.Fatalf("fixture: %+v %+v", base.Certification, base.Members)
	}

	r := rand.New(rand.NewPCG(1, 2))
	var sample []Offer
	for _, it := range items {
		if len(it.Offers) == 3 {
			sample = it.Offers
			break
		}
	}
	if sample == nil {
		t.Fatal("fixture has no item with all three offers")
	}
	want := mustJSON(t, mustDecide(t, base, q, sample))
	for trial := range 10 {
		shuffled := cloneItems(items)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		for i := range shuffled {
			r.Shuffle(len(shuffled[i].Offers), func(a, b int) {
				shuffled[i].Offers[a], shuffled[i].Offers[b] = shuffled[i].Offers[b], shuffled[i].Offers[a]
			})
		}
		s := mustBuild(t, q, 0.5, 0.5, 1.96, shuffled)
		if !reflect.DeepEqual(s, base) {
			t.Fatalf("trial %d: a shuffled build differs", trial)
		}
		if b, c := mustJSON(t, s), mustJSON(t, base); !bytes.Equal(b, c) {
			t.Fatalf("trial %d: state JSON differs:\n%s\n%s", trial, b, c)
		}
		offers := slices.Clone(sample)
		r.Shuffle(len(offers), func(a, b int) { offers[a], offers[b] = offers[b], offers[a] })
		if got := mustJSON(t, mustDecide(t, s, q, offers)); !bytes.Equal(got, want) {
			t.Fatalf("trial %d: answer JSON differs:\n%s\n%s", trial, got, want)
		}
	}
}

// TestCoverageCannotHideUnansweredLabels: 100 eligible certification
// labels, 3 answerable and all right: at a permissive target coverage is
// 3/100, never 1; below the bound nothing is certified and coverage is 0.
func TestCoverageCannotHideUnansweredLabels(t *testing.T) {
	q := question(engine.Choice)
	var items []Item
	for i := range 30 {
		items = append(items, Item{ID: int64(i + 1), Class: 0,
			Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
	}
	const eligible = 100
	for i := range eligible {
		it := Item{ID: int64(1001 + i), Class: 0, Certify: true}
		if i < 3 {
			it.Offers = []Offer{offer(LayerKey, q, 0, 0.9)}
		}
		items = append(items, it)
	}
	cases := []struct {
		target    float64
		certified bool
		coverage  float64
	}{
		{0.4, true, 0.03},
		{0.5, false, 0},
	}
	for _, tc := range cases {
		s := mustBuild(t, q, 0.5, tc.target, 1.96, items)
		if s.Certification.N != eligible || s.Certification.Answered != 3 ||
			s.Certification.Eligible != eligible {
			t.Fatalf("target %v: counts %+v", tc.target, s.Certification)
		}
		if s.Certification.Certified != tc.certified {
			t.Fatalf("target %v: certified %v (%+v)", tc.target, s.Certification.Certified, s.Certification)
		}
		if math.Abs(s.Certification.Coverage-tc.coverage) > 1e-12 {
			t.Fatalf("target %v: coverage %v, want %v", tc.target, s.Certification.Coverage, tc.coverage)
		}
		if s.Certification.Coverage > 1 {
			t.Fatalf("target %v: coverage above 1: %v", tc.target, s.Certification.Coverage)
		}
	}
}

// TestCertifiedCountsEqualDecidedAnswers: the certification counts the
// cascade's own answers, while N is the tested certification population.
// For every certification item, the cascade decides an answer iff the item
// contributed an answered row, and the row's correctness is the answer's
// correctness. A member below its gate and a message with no member offer
// abstain without an answer but stay in the tested population.
func TestCertifiedCountsEqualDecidedAnswers(t *testing.T) {
	q := question(engine.Choice)
	var items []Item
	for i := range 20 {
		items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9), offer(Layer1, q, 0, 0.8)}})
	}
	for i := range 20 {
		items = append(items, Item{ID: int64(101 + i), Class: 0,
			Offers: []Offer{offer(Layer1, q, 0, 0.8)}})
	}
	for i := range 30 { // the key answers first
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9), offer(Layer1, q, 0, 0.8)}})
	}
	for i := range 20 { // only layer1 offers
		items = append(items, Item{ID: int64(1101 + i), Class: 0, Certify: true,
			Offers: []Offer{offer(Layer1, q, 0, 0.8)}})
	}
	for i := range 5 { // the key is below its gate: layer1 answers
		items = append(items, Item{ID: int64(1201 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, 0, 0.5), offer(Layer1, q, 0, 0.8)}})
	}
	for i := range 5 { // every member is below its gate
		items = append(items, Item{ID: int64(1301 + i), Class: 0, Certify: true,
			Offers: []Offer{offer(Layer1, q, 0, 0.4)}})
	}
	for i := range 5 { // no member offers
		items = append(items, Item{ID: int64(1401 + i), Class: 0, Certify: true})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !s.Certification.Certified || s.Certification.Answered != 55 || s.Certification.N != 65 ||
		s.Certification.Errors != 0 || s.Certification.Eligible != 65 {
		t.Fatalf("fixture: %+v", s.Certification)
	}
	answered, errors := 0, 0
	for _, it := range items {
		if !it.Certify {
			continue
		}
		a := mustDecide(t, s, q, it.Offers)
		if a.Abstain != "" {
			want := engine.AbstainBelowTarget
			if len(it.Offers) == 0 {
				want = AbstainNoAnswer
			}
			if a.Abstain != want {
				t.Fatalf("item %d: abstain %q, want %q", it.ID, a.Abstain, want)
			}
			continue
		}
		o, ok := offerOf(it.Offers, a.Layer)
		if !ok {
			t.Fatalf("item %d: answered by %q without that offer", it.ID, a.Layer)
		}
		class, _ := selective.Decide(q.Type, o.Probs, s.Cutoff)
		answered++
		if class != it.Class {
			errors++
		}
	}
	if answered != s.Certification.Answered || s.Certification.N != 65 || errors != s.Certification.Errors {
		t.Fatalf("Decide answered %d with %d wrong; certification %+v", answered, errors, s.Certification)
	}
}

// TestAddingALowerMemberNeverLosesAnswers: adding SubQ below a certified
// key route cannot lower the answers the higher members give; a correct
// SubQ adds answers where no higher member answers, and a SubQ that never
// reaches the target is not a member at all.
func TestAddingALowerMemberNeverLosesAnswers(t *testing.T) {
	q := question(engine.Choice)
	baseItems := func() []Item {
		var items []Item
		for i := range 20 {
			items = append(items, Item{ID: int64(i + 1), Class: 0,
				Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
		}
		for i := range 20 {
			items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true,
				Offers: []Offer{offer(LayerKey, q, 0, 0.9)}})
		}
		return items
	}
	base := mustBuild(t, q, 0.5, 0.5, 1.96, baseItems())
	if !base.Certification.Certified || len(base.Members) != 1 {
		t.Fatalf("base: %+v %+v", base.Certification, base.Members)
	}
	for _, tc := range []struct {
		name    string
		class   int
		members int
		answers int
	}{
		{"subq correct", 0, 2, 60},
		{"subq below the target", wrongClass(q, 0), 1, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := baseItems()
			for i := range 10 {
				items = append(items, Item{ID: int64(201 + i), Class: 0,
					Offers: []Offer{offer(LayerSubQ, q, tc.class, 0.9)}})
			}
			for i := range 10 {
				items = append(items, Item{ID: int64(1201 + i), Class: 0, Certify: true,
					Offers: []Offer{offer(LayerSubQ, q, tc.class, 0.9)}})
			}
			s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
			if !s.Certification.Certified || len(s.Members) != tc.members {
				t.Fatalf("extended: %+v %+v", s.Certification, s.Members)
			}
			answers, subq := 0, 0
			for _, it := range items {
				a := mustDecide(t, s, q, it.Offers)
				if a.Abstain == "" {
					answers++
					if a.Layer == LayerSubQ {
						subq++
					}
				}
			}
			if answers != tc.answers {
				t.Fatalf("%d answers, want %d", answers, tc.answers)
			}
			if (tc.members == 2) != (subq > 0) {
				t.Fatalf("%d SubQ answers with %d members", subq, tc.members)
			}
			for _, it := range baseItems() {
				before := mustDecide(t, base, q, it.Offers)
				if before.Abstain != "" {
					continue
				}
				after := mustDecide(t, s, q, it.Offers)
				if after.Abstain != "" || after.Layer != before.Layer || !reflect.DeepEqual(after.Answer, before.Answer) {
					t.Fatalf("item %d: %+v became %+v", it.ID, before, after)
				}
			}
		})
	}
}

// TestAGateAtOneIsARealCut: a layer whose selection offers are certain (a
// one-hot distribution) gets the real gate 1.0, never the NoThreshold
// sentinel; it certifies and answers.
func TestAGateAtOneIsARealCut(t *testing.T) {
	q := question(engine.Choice)
	oneHot := peak(q, 0, 1)
	var items []Item
	for i := range 20 {
		items = append(items, Item{ID: int64(i + 1), Class: 0,
			Offers: []Offer{{Layer: Layer1, Probs: oneHot}}})
	}
	for i := range 20 {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true,
			Offers: []Offer{{Layer: Layer1, Probs: oneHot}}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if !s.Certification.Certified || s.Certification.Answered != 20 {
		t.Fatalf("a gate of 1.0 was mistaken for no member: %+v", s.Certification)
	}
	if got := s.Threshold(Layer1); got != 1 {
		t.Fatalf("layer1 threshold %v, want its gate 1", got)
	}
	a := mustDecide(t, s, q, []Offer{{Layer: Layer1, Probs: oneHot}})
	if a.Abstain != "" || a.Layer != Layer1 || a.Choice != "a" {
		t.Fatalf("a gate of 1.0 did not answer: %+v", a)
	}
}

// TestEmptyRouteIsNotCertified: no labels means no member, no gate and no
// cut (a cascade never invents one); the route exists but answers nothing.
func TestEmptyRouteIsNotCertified(t *testing.T) {
	q := question(engine.Choice)
	s := mustBuild(t, q, 0.5, 0.5, 1.96, nil)
	if s.Certification.Certified || len(s.Members) != 0 || s.Certification.Threshold != 0 {
		t.Fatalf("an empty route chose something: %+v", s)
	}
	for _, l := range []Layer{LayerKey, Layer1, LayerSubQ} {
		if got := s.Threshold(l); got != selective.NoThreshold {
			t.Fatalf("an empty route's %q threshold is %v", l, got)
		}
	}
	a := mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.9)})
	if a.Abstain != AbstainNotCertified || a.Probabilities != nil {
		t.Fatalf("an empty route answered: %+v", a)
	}
}

// TestMalformedInputIsRefused: malformed questions, items and offers are
// refused with ErrInvalid, not silently repaired.
func TestMalformedInputIsRefused(t *testing.T) {
	q := question(engine.Choice)
	valid := []Item{{ID: 1, Class: 0, Offers: []Offer{offer(LayerKey, q, 0, 0.9)}}}
	if _, err := Build(q, 0.5, 0.5, 1.96, 0, valid); err != nil {
		t.Fatalf("valid input refused: %v", err)
	}
	// The documented tolerance: rounding drift is accepted, a real
	// disagreement is refused, and neither is silently renormalised.
	near := []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{0.3, 0.3, 0.400005}}}}}
	if _, err := Build(q, 0.5, 0.5, 1.96, 0, near); err != nil {
		t.Fatalf("a distribution within probsTolerance was refused: %v", err)
	}
	far := []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{0.3, 0.3, 0.40002}}}}}
	if _, err := Build(q, 0.5, 0.5, 1.96, 0, far); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a distribution outside probsTolerance: %v", err)
	}

	for _, tc := range []struct {
		name string
		q    *engine.Question
	}{
		{"nil question", nil},
		{"unknown type", &engine.Question{Name: "x", Type: "other", Instructions: "?"}},
		{"missing question contract", &engine.Question{Name: "x", Type: engine.Choice, Options: []engine.Option{{Name: "a"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Build(tc.q, 0.5, 0.5, 1.96, 0, valid); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v is not ErrInvalid", err)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		items []Item
	}{
		{"duplicate id", []Item{
			{ID: 1, Class: 0, Offers: []Offer{offer(LayerKey, q, 0, 0.9)}},
			{ID: 1, Class: 0, Offers: []Offer{offer(Layer1, q, 0, 0.9)}},
		}},
		{"class below -1", []Item{{ID: 1, Class: -2}}},
		{"class above the range", []Item{{ID: 1, Class: 3}}},
		{"unlabelled certification item", []Item{{ID: 1, Class: -1, Certify: true}}},
		{"unknown layer", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: "other", Probs: peak(q, 0, 0.9)}}}}},
		{"duplicate layer", []Item{{ID: 1, Class: 0, Offers: []Offer{offer(LayerKey, q, 0, 0.9), offer(LayerKey, q, 0, 0.9)}}}},
		{"wrong probability count", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{0.5, 0.5}}}}}},
		{"negative probability", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{-0.1, 0.6, 0.5}}}}}},
		{"NaN probability", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{math.NaN(), 0.5, 0.5}}}}}},
		{"infinite probability", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{math.Inf(1), 0, 0}}}}}},
		{"probabilities do not sum to 1", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey, Probs: []float64{0.5, 0.5, 0.5}}}}}},
		{"empty probabilities", []Item{{ID: 1, Class: 0, Offers: []Offer{{Layer: LayerKey}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Build(q, 0.5, 0.5, 1.96, 0, tc.items); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v is not ErrInvalid", err)
			}
		})
	}

	s := certifiedState(t, q, 20)
	other := question(engine.Score)
	for _, tc := range []struct {
		name   string
		q      *engine.Question
		offers []Offer
	}{
		{"nil question", nil, valid[0].Offers},
		{"another question", other, []Offer{offer(LayerKey, other, 0, 0.9)}},
		{"unknown layer", q, []Offer{{Layer: "other", Probs: peak(q, 0, 0.9)}}},
		{"duplicate layer", q, []Offer{offer(LayerKey, q, 0, 0.9), offer(LayerKey, q, 0, 0.9)}},
		{"wrong probability count", q, []Offer{{Layer: LayerKey, Probs: []float64{0.5, 0.5}}}},
		{"negative probability", q, []Offer{{Layer: LayerKey, Probs: []float64{-0.1, 0.6, 0.5}}}},
		{"NaN probability", q, []Offer{{Layer: LayerKey, Probs: []float64{math.NaN(), 0.5, 0.5}}}},
		{"probabilities do not sum to 1", q, []Offer{{Layer: LayerKey, Probs: []float64{0.5, 0.5, 0.5}}}},
	} {
		t.Run("decide/"+tc.name, func(t *testing.T) {
			if _, err := s.Decide(tc.q, tc.offers); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v is not ErrInvalid", err)
			}
		})
	}

	// Validation comes before the uncertified short-circuit.
	unc := State{Question: q.Name, Type: q.Type, Cutoff: 0.5}
	if _, err := unc.Decide(q, []Offer{{Layer: LayerKey, Probs: []float64{1}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed offer on an uncertified state: %v", err)
	}
}

// TestQuestionTypesShapeLikeSelective: every question type and class
// answers through selective.Given, plus the route's Layer and Rule.
func TestQuestionTypesShapeLikeSelective(t *testing.T) {
	for _, q := range []*engine.Question{question(engine.Noul), question(engine.Choice), question(engine.Score)} {
		t.Run(string(q.Type), func(t *testing.T) {
			s := certifiedState(t, q, 20)
			classes := []int{0, 1}
			if q.Type != engine.Noul {
				classes = []int{0, 1, 2}
			}
			for _, class := range classes {
				probs := peak(q, class, 0.9)
				offers := []Offer{{Layer: LayerKey, Probs: probs, Rule: "rule-1"}}
				a := mustDecide(t, s, q, offers)
				if a.Abstain != "" || a.Layer != LayerKey || a.Rule != "rule-1" {
					t.Fatalf("class %d: %+v", class, a)
				}
				if want := selective.Given(q, probs, s.Cutoff); !reflect.DeepEqual(a.Answer, want) {
					t.Fatalf("class %d: answer\n got %+v\nwant %+v", class, a.Answer, want)
				}
				// The route gates with selective.Decide, the one
				// confidence definition; it must equal the value
				// selective.Confidence derives from the shaped answer.
				_, byDecide := selective.Decide(q.Type, probs, s.Cutoff)
				byConfidence, ok := selective.Confidence(selective.Given(q, probs, s.Cutoff), s.Cutoff)
				if !ok || byConfidence != byDecide {
					t.Fatalf("class %d: confidence %v (ok %v) != selective.Decide's %v", class, byConfidence, ok, byDecide)
				}
				if _, ok := selective.Confidence(a.Answer, s.Cutoff); !ok || byDecide < s.Threshold(LayerKey) {
					t.Fatalf("class %d: confidence %v (ok %v), layer gate %v", class, byDecide, ok, s.Threshold(LayerKey))
				}
			}
		})
	}
}

// Certifying a member set is not choosing one: when the key and layer 1
// are indistinguishable on the selection part, the richer cascade stays
// even though the key is wrong on every certification label. The route is
// then uncertified, and answers abstain.
func TestCertificationLabelsDoNotChooseMembers(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	for i := range n {
		items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	for i := range n {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, wrongClass(q, 0), 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if len(s.Members) != 2 || s.Members[0].Layer != LayerKey || s.Members[1].Layer != Layer1 {
		t.Fatalf("certification labels changed the member set: %+v", s.Members)
	}
	if s.Certification.Certified || s.Answering() {
		t.Fatalf("the full cascade certified on a certification-only subset: %+v", s.Certification)
	}
	a := mustDecide(t, s, q, []Offer{offer(LayerKey, q, 0, 0.9), offer(Layer1, q, 0, 0.8)})
	if a.Abstain != AbstainNotCertified {
		t.Fatalf("answer %+v, want not_certified", a)
	}
}

// One certification label cannot change the member set: the selection part
// is the only data that chooses it.
func TestOneCertificationLabelCannotChangeMembers(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	build := func(flipped bool) State {
		var items []Item
		for i := range n {
			items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
				offer(LayerKey, q, 0, 0.9),
				offer(Layer1, q, 0, 0.8),
			}})
		}
		for i := range n {
			class := 0
			if flipped && i == 0 {
				class = wrongClass(q, 0)
			}
			items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
				offer(LayerKey, q, class, 0.9),
				offer(Layer1, q, 0, 0.8),
			}})
		}
		return mustBuild(t, q, 0.5, 0.5, 1.96, items)
	}
	base, flipped := build(false), build(true)
	if !reflect.DeepEqual(base.Members, flipped.Members) {
		t.Fatalf("one certification label changed members: %+v vs %+v", base.Members, flipped.Members)
	}
}

// TestBuildKeepsTheFullCascadeOnATie: when the full cascade and a single
// member certify the same coverage, the richer cascade stays.
func TestBuildKeepsTheFullCascadeOnATie(t *testing.T) {
	q := question(engine.Choice)
	const n = 20
	var items []Item
	for i := range n {
		items = append(items, Item{ID: int64(i + 1), Class: 0, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	for i := range n {
		items = append(items, Item{ID: int64(1001 + i), Class: 0, Certify: true, Offers: []Offer{
			offer(LayerKey, q, 0, 0.9),
			offer(Layer1, q, 0, 0.8),
		}})
	}
	s := mustBuild(t, q, 0.5, 0.5, 1.96, items)
	if len(s.Members) != 2 || s.Members[0].Layer != LayerKey || s.Members[1].Layer != Layer1 {
		t.Fatalf("members %+v, want the full cascade", s.Members)
	}
}

// Certification at the target is necessary but not sufficient: a route whose
// certified coverage is below the requirement's minimum must abstain, and the
// stored minimum must travel with the state.
func TestAnsweringRequiresCoverage(t *testing.T) {
	q := question(engine.Choice)
	s := State{
		Question:    q.Name,
		Type:        q.Type,
		Cutoff:      0.5,
		MinCoverage: 0.6,
		Certification: selective.Certification{
			Certified:     true,
			Coverage:      0.5,
			CoverageLower: 0.5,
		},
	}
	if s.Answering() {
		t.Fatal("a certified route below min_coverage may answer")
	}
	if got := s.Threshold(LayerKey); got != selective.NoThreshold {
		t.Fatalf("below-coverage threshold %v", got)
	}
	a, err := s.Decide(q, nil)
	if err != nil || a.Abstain != AbstainBelowCoverage {
		t.Fatalf("below-coverage decision %+v %v", a, err)
	}
	s.MinCoverage = 0.5
	if !s.Answering() {
		t.Fatal("a certified route at min_coverage was refused")
	}
	// 40 answered of 73 eligible is point coverage 0.548, above the 0.5
	// minimum, but its lower bound is 0.434: the route must abstain.
	s.Certification.Coverage, s.Certification.CoverageLower = 40.0/73, 0.434
	if s.Answering() {
		t.Fatal("a point coverage above the minimum with a lower bound below it answered")
	}

	built := mustBuildAtCoverage(t, q, 0.7, []Item{{ID: 1, Class: 0, Offers: []Offer{offer(LayerKey, q, 0, 0.9)}}})
	if built.MinCoverage != 0.7 {
		t.Fatalf("Build lost the coverage requirement: %+v", built)
	}
}

func mustBuildAtCoverage(t *testing.T, q *engine.Question, minCoverage float64, items []Item) State {
	t.Helper()
	s, err := Build(q, 0.5, 0.5, 1.96, minCoverage, items)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}
