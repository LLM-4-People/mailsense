package layer1

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
)

// TestLabelNeedFollowsTheMeasuredQuality: the label request is not a fixed
// zero-error minimum. A 99 % model asks for the minimum; a 97 % model asks
// for the larger split its error rate needs; a model below the target gets
// the static design count but is marked unfixable, because no label count
// can raise its precision. The static request itself is never capped.
func TestLabelNeedFollowsTheMeasuredQuality(t *testing.T) {
	t.Parallel()
	req := Requirement{MinEvalLabels: 50, TargetAccuracy: 0.95, MinCoverage: 0.5, Z: 1.96}
	base := req.Requested(0.5)
	if got := req.LabelNeed(0.5, selective.Certification{}); got != (LabelNeed{Need: base, Fixable: true}) {
		t.Fatalf("no evidence: %+v, want the static %d fixable", got, base)
	}
	perfect := selective.Certification{Answered: 100, Errors: 0, Coverage: 0.5}
	if got := req.LabelNeed(0.5, perfect); got != (LabelNeed{Need: base, Fixable: true}) {
		t.Fatalf("perfect model: %+v, want the static %d", got, base)
	}
	good := selective.Certification{Answered: 100, Errors: 3, Coverage: 0.5}
	if got := req.LabelNeed(0.5, good); !got.Fixable || got.Need <= base {
		t.Fatalf("97%% model asked for %+v, want more than the static %d, fixable", got, base)
	}
	weak := selective.Certification{Answered: 894, Errors: 126, Coverage: 0.5}
	if got := req.LabelNeed(0.5, weak); got != (LabelNeed{Need: base, Fixable: false}) {
		t.Fatalf("below-target model asked for %+v, want the static measure %d marked unfixable", got, base)
	}
	wide := selective.Certification{Answered: 100, Errors: 0, Coverage: 0.8}
	if got := req.LabelNeed(0.5, wide); !got.Fixable || got.Need >= base {
		t.Fatalf("80%% coverage asked for %+v, want less than the 50%% static %d", got, base)
	}

	// An explicit request_labels setting is the owner's decision and is
	// honored even when the measured precision is below the target.
	explicit := 123
	req.RequestLabels = &explicit
	if got := req.LabelNeed(0.5, weak); got != (LabelNeed{Need: explicit, Fixable: true}) {
		t.Fatalf("explicit request: %+v, want %d fixable", got, explicit)
	}

	// A precision at the target but too close to distinguish within the
	// honest search bound is unfixable, not silently capped.
	unreachable := Requirement{MinEvalLabels: 50, TargetAccuracy: 0.999999, MinCoverage: 0.5, Z: 1.96}
	if got := unreachable.LabelNeed(0.5, selective.Certification{Answered: 100, Errors: 0, Coverage: 0.5}); got != (LabelNeed{Need: unreachable.Requested(0.5), Fixable: false}) {
		t.Fatalf("an unreachable honest request: %+v, want the static measure marked unfixable", got)
	}
}

// The shipped family-wise requirements ask for 3,504 labels: uncapped, and
// still uncapped when a model has no certification yet (the static design
// request). Capping it made Remaining zero while the gate still needed
// hundreds of answers.
func TestTheRequestedLabelsCanOpenTheShippedGate(t *testing.T) {
	t.Parallel()
	req := Requirement{MinEvalLabels: 50, TargetAccuracy: 0.99, MinCoverage: 0.5, Z: 1.96, FamilyWise: true}
	req.ZEffective = req.effectiveZ(17)
	const fraction = 0.5
	const want = 3504
	needed := selective.Needed(req.TargetAccuracy, req.EffectiveZ())
	requested := req.Requested(fraction)
	certifiable := int(float64(requested) * fraction * req.MinCoverage)
	if certifiable < needed {
		t.Fatalf("requested %d labels buy %d certification labels, want at least %d", requested, certifiable, needed)
	}
	if requested != want {
		t.Fatalf("the shipped gate asked for %d labels, want the honest %d", requested, want)
	}
	if got := req.LabelNeed(fraction, selective.Certification{}); got != (LabelNeed{Need: want, Fixable: true}) {
		t.Fatalf("the static labels-stage plan: %+v, want %d fixable", got, want)
	}
}

// TestLabelNeedReadsALegacyCertification: the same stored counts must produce
// the same label request whether or not the report carries its precision
// field. The requirement used to recompute the ratio itself, which made a
// legacy row and a fresh certificate pass through two formulas.
func TestLabelNeedReadsALegacyCertification(t *testing.T) {
	t.Parallel()
	req := Requirement{MinEvalLabels: 50, TargetAccuracy: 0.95, MinCoverage: 0.5, Z: 1.96}
	legacy := selective.Certification{Answered: 100, Errors: 3, Coverage: 0.5}
	fresh := legacy
	fresh.Precision = 0.97
	if got, want := req.LabelNeed(0.5, legacy), req.LabelNeed(0.5, fresh); got != want {
		t.Fatalf("legacy %+v differs from a report carrying its precision %+v", got, want)
	}
}
