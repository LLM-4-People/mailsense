// Package route is the certified answer route for one question: an
// escalation cascade of the layer-0 rule key, then layer 1, then the
// stored Ondelle answer. Each layer answers only above the gate chosen for
// it on the selection labels; a higher layer that offered below its gate
// falls through to the next, and the cascade answers only when its
// decisions, fixed by those gates, were certified once on the disjoint
// certification labels.
//
// It is pure: only internal/engine and internal/selective, no I/O, clock,
// store or configuration. Build chooses the gates and certifies the
// cascade, State.Decide applies it, and both are deterministic: items are
// processed in id order, an item has at most one offer per layer, the
// layers have one fixed order, and no map iteration decides anything.
package route

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// Layer identifies one participant of the route.
type Layer string

// The participants, in the lead's escalation order.
//
// An answer stored before this value was renamed keeps its former layer
// string; every reader decodes the layer without comparing it, so no
// migration is needed.
const (
	LayerKey     Layer = "key"     // a layer-0 rule key (the evidence rule's answer)
	Layer1       Layer = "layer1"  // the active local head's calibrated distribution
	LayerOndelle Layer = "ondelle" // the configured triage decider's stored answer
)

// The abstain reasons the route adds to the engine's vocabulary
// (engine.AbstainBelowTarget is the third form: a member offered but did
// not reach its own gate, and no lower member answered either).
const (
	// AbstainNotCertified: the question's route is not certified, so no
	// participant may answer (no distribution is carried).
	AbstainNotCertified = "not_certified"
	// AbstainBelowCoverage: the route is certified at the target, but its
	// coverage lower bound misses the requirement's minimum. It is not an
	// uncertified route; naming the two apart keeps preview and the UI from
	// treating a certified route as a trust failure.
	AbstainBelowCoverage = "below_coverage"
	// AbstainNoAnswer: no member of the route offered an answer for this
	// message (no distribution is carried).
	AbstainNoAnswer = "no_answer"
)

// layers are the participants in the lead's escalation order, highest
// priority first. The cascade walks this order: the first member whose
// offer reaches its gate answers, and a higher member below its gate does
// not veto the item (it falls through).
var layers = [...]Layer{LayerKey, Layer1, LayerOndelle}

// probsTolerance is how far an offer's probabilities may sum from 1
// (rounding only: the float32 storage bound); a malformed offer is
// refused, never renormalised.
const probsTolerance = 1e-5

// ErrInvalid reports a malformed question, item or offer. Every Build and
// Decide error wraps it, so errors.Is(err, ErrInvalid) identifies misuse.
var ErrInvalid = errors.New("route: invalid input")

// Offer is one participant's class distribution for one message/question.
// Rule is the layer-0 rule id when Layer is LayerKey (empty otherwise).
type Offer struct {
	Layer Layer     `json:"layer"`
	Probs []float64 `json:"probs"`
	Rule  string    `json:"rule,omitempty"`
}

// Item is one message/question: its label and the offers of its
// participants. Class is the labelled class (a choice option's index, 0
// for no and 1 for yes, a score level) or -1 when unlabelled. Certify
// marks the disjoint certification part; a certification item must carry
// a label and counts in the coverage denominator. IDs identify items and
// must be unique.
type Item struct {
	ID      int64   `json:"id"`
	Class   int     `json:"class"`
	Certify bool    `json:"certify,omitempty"`
	Offers  []Offer `json:"offers,omitempty"`
}

// Member is one gate-qualified participant. Gate is the confidence
// threshold chosen for it on the selection items where it offered (never
// selective.NoThreshold: that is not a member). Band is its priority band
// in the lead's order (key 2, layer1 1, Ondelle 0); it orders and reports the
// members and is never compared with a confidence: the cascade answers at
// each layer's own gate, not at a shared score.
type Member struct {
	Layer Layer   `json:"layer"`
	Band  float64 `json:"band"`
	Gate  float64 `json:"gate"`
}

// Answer is a route answer: the engine's triage answer plus the
// participant that gave it. Layer and Rule are empty on every
// abstention. An abstained answer carries no Choice, Value or
// Confidence; only the reason and (below target) the highest-priority
// member's distribution for diagnostics.
type Answer struct {
	engine.Answer
	Layer Layer  `json:"layer,omitempty"`
	Rule  string `json:"rule,omitempty"`
}

// State is one question's certified route: the members, their gates and
// the member set chosen on the selection part, and the one certification
// test of that set on the disjoint certification part. No certification
// label chooses a member, a gate or a threshold: a certification-only
// difference cannot change the route, only whether it certifies. A cascade
// has no single cut: Certification.Threshold is 0, so every consumer reads
// Certification.Certified (and Threshold(layer) for a layer's own gate).
// Build produces the state; answering never changes it.
type State struct {
	Question      string                  `json:"question"`
	Type          engine.QuestionType     `json:"type"`
	Cutoff        float64                 `json:"cutoff"`
	Target        float64                 `json:"target"`
	MinCoverage   float64                 `json:"min_coverage"`
	Certification selective.Certification `json:"certification"`
	Members       []Member                `json:"members"`
}

// Build chooses the question's per-layer gates and member set on the
// selection items, then certifies that one set once on the certification
// items. A subset can beat the full cascade on the selection part when a
// permissive higher gate adds wrong answers; the choice is still made only
// on selection labels. q must satisfy engine.Question.Validate; target and
// z are the question's requirement (validated at its owner).
//
// Items: Class is the labelled class or -1 when unlabelled. Certify marks
// the certification part. Every certification item must be labelled and
// counts in Certification.Eligible whether or not a member answered it:
// the caller decides which labels are eligible by choosing the Certify
// set, and a message whose label must not count is not marked Certify.
// Duplicate IDs are refused, and items are processed in ascending ID
// order.
//
// Gates (selection part only): every labelled selection item on which a
// layer offered feeds that layer's own gate. selective.Decide turns the
// offer into its answered class and confidence, the class is compared
// with the item's Class, and selective.Targets(selective.Rank(rows),
// target, z) picks the gate. The gates are independent of one another: a
// higher layer offering on an item does not hide a lower layer's offer. A
// layer is a member iff its gate is not selective.NoThreshold.
//
// Cascade: on an item the members are walked in escalation order. The
// first member that offered and whose offer confidence (selective.Decide's)
// reaches its gate answers. A higher member that offered below its gate
// falls through to the next member — this is the escalation. A
// non-member's offer is ignored. No member answering abstains:
// below_target when at least one member offered (carrying the
// highest-priority member offer's distribution for diagnostics), no_answer
// when none did.
//
// Certification: the cascade's decisions are fixed by the selection-part
// gates, so the certification part chooses no threshold. It builds one
// row per certification item the cascade answers (the item's correctness
// from the same cascade) and tests them once with
// selective.CertifyFixed(rows, every labelled certification item, target,
// z). Certification.Threshold is 0; no gate, member or decision is ever
// chosen on a certification label, and every certification item counts in
// the coverage denominator.
//
// An uncertified state answers nothing: Decide returns
// AbstainNotCertified for every offer.
func Build(q *engine.Question, cutoff, target, z, minCoverage float64, items []Item) (State, error) {
	if q == nil {
		return State{}, fmt.Errorf("%w: nil question", ErrInvalid)
	}
	if err := q.Validate(); err != nil {
		return State{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	classes := classCount(q)
	ordered := slices.Clone(items)
	slices.SortFunc(ordered, func(a, b Item) int { return cmp.Compare(a.ID, b.ID) })
	for i := range ordered {
		if i > 0 && ordered[i].ID == ordered[i-1].ID {
			return State{}, fmt.Errorf("%w: duplicate item id %d", ErrInvalid, ordered[i].ID)
		}
		if err := checkItem(&ordered[i], classes); err != nil {
			return State{}, fmt.Errorf("%w: item %d: %v", ErrInvalid, ordered[i].ID, err)
		}
	}

	s := State{Question: q.Name, Type: q.Type, Cutoff: cutoff, Target: target, MinCoverage: minCoverage}

	// Gates: every labelled selection item's offer of a layer feeds that
	// layer's gate. Offers of the same layer never compete here.
	var gateRows [len(layers)][]selective.Ranked
	for i := range ordered {
		it := &ordered[i]
		if it.Certify || it.Class < 0 {
			continue
		}
		for _, o := range it.Offers {
			rank, _ := layerRank(o.Layer)
			class, conf := selective.Decide(q.Type, o.Probs, cutoff)
			gateRows[rank] = append(gateRows[rank], selective.Ranked{ID: it.ID, Correct: class == it.Class, Confidence: conf})
		}
	}
	for rank, layer := range layers {
		gate := selective.Targets(selective.Rank(gateRows[rank]), []float64{target}, z)[0].Threshold
		if gate != selective.NoThreshold {
			band, _ := layerBand(layer)
			s.Members = append(s.Members, Member{Layer: layer, Band: band, Gate: gate})
		}
	}

	// The gates are fixed on the selection part. The member set is chosen
	// there too: a permissive higher gate can add wrong answers and leave
	// the full cascade below the target while a subset reaches it. The
	// certification part then tests that one set once, so no certification
	// label ever chooses a member, a gate or a threshold.
	full := slices.Clone(s.Members)
	bestMembers := full
	bestScore := scoreMembers(q, cutoff, full, ordered)
	for mask := 1; mask < 1<<len(full); mask++ {
		subset := make([]Member, 0, len(full))
		for i, m := range full {
			if mask&(1<<i) != 0 {
				subset = append(subset, m)
			}
		}
		score := scoreMembers(q, cutoff, subset, ordered)
		if betterSelected(score, bestScore, len(subset), len(bestMembers), target) {
			bestScore, bestMembers = score, subset
		}
	}
	s.Members = bestMembers
	s.Certification = certifyMembers(q, cutoff, bestMembers, ordered, target, z)
	return s, nil
}

// certifyMembers certifies one member set's cascade once on the
// certification items: every labelled certification item is eligible, and
// one row per item carries whether the cascade answered it. An unanswered
// item stays in the class report (Abstained), so a class the cascade never
// reaches surfaces as collapsed instead of vanishing from the recall. The
// gates are the selection-part gates already stored in members. Each
// answered row carries the item's label and the decided class.
func certifyMembers(q *engine.Question, cutoff float64, members []Member, ordered []Item, target, z float64) selective.Certification {
	s := State{Question: q.Name, Type: q.Type, Cutoff: cutoff, Members: members}
	var cert []selective.Ranked
	eligible := 0
	for i := range ordered {
		it := &ordered[i]
		if !it.Certify {
			continue
		}
		eligible++
		o, answered, _ := s.cascade(it.Offers)
		if !answered {
			cert = append(cert, selective.Ranked{ID: it.ID, HasClass: true, Label: it.Class, Abstained: true})
			continue
		}
		class, conf := selective.Decide(q.Type, o.Probs, cutoff)
		cert = append(cert, selective.Ranked{ID: it.ID, Correct: class == it.Class, Confidence: conf,
			HasClass: true, Label: it.Class, Predicted: class})
	}
	return selective.CertifyFixed(cert, eligible, target, z)
}

// selectionScore is a member set's cascade on the selection part: the only
// data Build may use to choose a member set.
type selectionScore struct {
	eligible int
	answered int
	correct  int
}

// scoreMembers applies one member set's cascade to the selection items.
func scoreMembers(q *engine.Question, cutoff float64, members []Member, ordered []Item) selectionScore {
	s := State{Question: q.Name, Type: q.Type, Cutoff: cutoff, Members: members}
	var score selectionScore
	for i := range ordered {
		it := &ordered[i]
		if it.Certify || it.Class < 0 {
			continue
		}
		score.eligible++
		o, answered, _ := s.cascade(it.Offers)
		if !answered {
			continue
		}
		class, _ := selective.Decide(q.Type, o.Probs, cutoff)
		score.answered++
		if class == it.Class {
			score.correct++
		}
	}
	return score
}

// qualifies reports whether the set's selection precision reaches target.
func (s selectionScore) qualifies(target float64) bool {
	return s.answered > 0 && float64(s.correct)/float64(s.answered) >= target
}

// coverage is the answered share of the labelled selection items.
func (s selectionScore) coverage() float64 {
	if s.eligible == 0 {
		return 0
	}
	return float64(s.answered) / float64(s.eligible)
}

// betterSelected reports whether candidate a beats the current best b on
// the selection part: a set that reaches the target beats one that does
// not, then higher selection coverage wins, then more members (a richer
// cascade on a tie). Certification labels never enter this comparison.
func betterSelected(a, b selectionScore, aMembers, bMembers int, target float64) bool {
	aOK, bOK := a.qualifies(target), b.qualifies(target)
	switch {
	case aOK != bOK:
		return aOK
	case !aOK:
		return false
	case a.coverage() != b.coverage():
		return a.coverage() > b.coverage()
	}
	return aMembers > bMembers
}

// Answering is the one answer gate of a route: certification at the target
// is necessary but not sufficient. The requirement's minimum coverage is a
// documented precondition to answering, and it is tested on the coverage
// lower bound: a route certified on a non-representative slice must abstain
// rather than answer. The predicate itself lives in selective so the
// verdict and the learning stage cannot disagree with this gate.
func (s State) Answering() bool {
	return selective.Answering(s.Certification, s.MinCoverage)
}

// Threshold is the confidence above which a member layer answers: its own
// selection-part gate. It is selective.NoThreshold for a layer that is
// not a member, for an unknown layer, and for a route that is not
// answerable (which abstains). A cascade has no route-wide cut.
func (s State) Threshold(l Layer) float64 {
	if !s.Answering() {
		return selective.NoThreshold
	}
	m, ok := s.member(l)
	if !ok {
		return selective.NoThreshold
	}
	return m.Gate
}

// Decide answers one message from the offers of its participants. q must
// be the question Build validated: its name and type must match the
// state.
//
// The offers are validated first (known layer, one offer per layer, a
// distribution of the question's classes summing to 1 within
// probsTolerance). An uncertified state abstains AbstainNotCertified and
// carries no distribution: Certification.Certified is the whole test (a
// cascade has no threshold to compare). Otherwise the cascade runs in the
// state's layer order: the first member that offered at or above its gate
// answers with selective.Given plus Layer and Rule; a higher member below
// its gate falls through. If no member answers, the result is
// engine.AbstainBelowTarget carrying the highest-priority member offer's
// distribution when some member offered, and AbstainNoAnswer when none
// did. A non-member's offer is ignored.
//
// A Gate is not re-chosen here: it is the selection-part gate Build
// stored. The state's Cutoff shapes the answer. The order is fixed by the
// layer list, so shuffled offers cannot change the answer; two offers of
// one layer are refused.
func (s State) Decide(q *engine.Question, offers []Offer) (Answer, error) {
	if q == nil {
		return Answer{}, fmt.Errorf("%w: nil question", ErrInvalid)
	}
	if q.Name != s.Question || q.Type != s.Type {
		return Answer{}, fmt.Errorf("%w: question %q (%s) is not the route's %q (%s)", ErrInvalid, q.Name, q.Type, s.Question, s.Type)
	}
	classes := classCount(q)
	if classes < 2 {
		return Answer{}, fmt.Errorf("%w: question %q has %d classes", ErrInvalid, q.Name, classes)
	}
	if err := checkOffers(offers, classes); err != nil {
		return Answer{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !s.Answering() {
		reason := AbstainNotCertified
		if s.Certification.Certified {
			reason = AbstainBelowCoverage
		}
		return Answer{Answer: engine.Answer{Type: q.Type, Abstain: reason}}, nil
	}
	o, answered, offered := s.cascade(offers)
	if !answered {
		if !offered {
			return Answer{Answer: engine.Answer{Type: q.Type, Abstain: AbstainNoAnswer}}, nil
		}
		return Answer{Answer: selective.Abstained(q, o.Probs, engine.AbstainBelowTarget)}, nil
	}
	return Answer{Answer: selective.Given(q, o.Probs, s.Cutoff), Layer: o.Layer, Rule: o.Rule}, nil
}

// cascade runs the escalation cascade over one message's offers. It
// returns the first member offer that reaches its gate. When none answers,
// offered reports whether any member offered and offer is then the
// highest-priority member offer (the below-target diagnostic).
func (s State) cascade(offers []Offer) (offer Offer, answered, offered bool) {
	for _, l := range layers {
		m, ok := s.member(l)
		if !ok {
			continue
		}
		o, ok := offerOf(offers, l)
		if !ok {
			continue
		}
		if !offered {
			offer, offered = o, true
		}
		if _, conf := selective.Decide(s.Type, o.Probs, s.Cutoff); conf >= m.Gate {
			return o, true, true
		}
	}
	return offer, false, offered
}

// layerRank is a layer's position in the escalation order (0: key); ok
// false when it is not a route layer.
func layerRank(l Layer) (int, bool) {
	for i, x := range layers {
		if x == l {
			return i, true
		}
	}
	return 0, false
}

// layerBand is a layer's priority band in the report: the number of
// layers below it (key 2, layer1 1, Ondelle 0). It orders and reports the
// members; it is never a score.
func layerBand(l Layer) (float64, bool) {
	rank, ok := layerRank(l)
	if !ok {
		return 0, false
	}
	return float64(len(layers) - 1 - rank), true
}

// offerOf is one layer's offer of a message (ok false: it did not offer).
func offerOf(offers []Offer, l Layer) (Offer, bool) {
	for _, o := range offers {
		if o.Layer == l {
			return o, true
		}
	}
	return Offer{}, false
}

// classCount is the question's class count (a yes/no question has two);
// 0 for an unknown type.
func classCount(q *engine.Question) int {
	switch q.Type {
	case engine.Noul:
		return 2
	case engine.Choice:
		return len(q.Options)
	case engine.Score:
		return len(q.Levels)
	}
	return 0
}

// member is a layer's membership entry, only when it is gate-qualified.
func (s State) member(l Layer) (Member, bool) {
	for _, m := range s.Members {
		if m.Layer == l && m.Gate != selective.NoThreshold {
			return m, true
		}
	}
	return Member{}, false
}

// checkItem validates one item against the question's classes.
func checkItem(it *Item, classes int) error {
	if it.Class < -1 || it.Class >= classes {
		return fmt.Errorf("class %d is not -1 or 0..%d", it.Class, classes-1)
	}
	if it.Certify && it.Class < 0 {
		return errors.New("a certification item must be labelled")
	}
	return checkOffers(it.Offers, classes)
}

// checkOffers validates one offer list against the question's classes: a known
// layer, one offer per layer, and a distribution that sums to 1 within
// probsTolerance. Decide and the item validation apply the same trust-boundary
// rule; only their wrapping of the error differs.
func checkOffers(offers []Offer, classes int) error {
	var seen [len(layers)]bool
	for _, o := range offers {
		rank, ok := layerRank(o.Layer)
		if !ok {
			return fmt.Errorf("unknown layer %q", o.Layer)
		}
		if seen[rank] {
			return fmt.Errorf("layer %q offered twice", o.Layer)
		}
		seen[rank] = true
		if err := checkProbs(o.Probs, classes); err != nil {
			return fmt.Errorf("layer %q: %v", o.Layer, err)
		}
	}
	return nil
}

// checkProbs validates one class distribution.
func checkProbs(p []float64, classes int) error {
	if len(p) != classes {
		return fmt.Errorf("%d probabilities for %d classes", len(p), classes)
	}
	var sum float64
	for _, x := range p {
		if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
			return fmt.Errorf("probability %v", x)
		}
		sum += x
	}
	if math.Abs(sum-1) > probsTolerance {
		return fmt.Errorf("probabilities sum to %v", sum)
	}
	return nil
}
