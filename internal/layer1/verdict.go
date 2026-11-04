package layer1

import (
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// Verdict is one question's requirement verdict (the questions stage),
// from the model that answers it after the run.
type Verdict struct {
	Question  string  `json:"question"`
	Meets     bool    `json:"meets"`
	Code      string  `json:"code,omitempty"` // the blocker when it does not
	Eval      int     `json:"eval"`           // evaluation labels now
	Effective float64 `json:"effective_train"`
	// RequiredTrain is the requirement's min_effective_train.
	RequiredTrain float64 `json:"min_effective_train"`
	Coverage      float64 `json:"coverage_at_target"` // the certified coverage (0: not certified)
	Target        float64 `json:"target_accuracy"`
	Z             float64 `json:"z"`           // the effective Wilson z (the certification's)
	ZBase         float64 `json:"z_base"`      // the uncorrected requirement z
	FamilyWise    bool    `json:"family_wise"` // Z was corrected across questions
	Required      float64 `json:"min_coverage"`
	Requested     int     `json:"request_labels"` // labels-stage plan: the static design request when more labels cannot help
	Remaining     int     `json:"remaining"`      // of them, still to label (requested − eval; 0 when labels cannot help)
	// CertifiedOn are the label sources the certification tested against:
	// the coverage is agreement with them.
	CertifiedOn   []string                `json:"certified_on"`
	Certification selective.Certification `json:"certification"`
}

// verdicts measures every question against its requirement: a model that
// answers, enough effective training, enough evaluation labels, and the
// certified coverage at the target accuracy.
func verdicts(plan *Plan, qs []*settled) []Verdict {
	var out []Verdict
	for _, s := range qs {
		qr := s.report
		req := plan.Requirements.For(s.q.Name)
		need := req.LabelNeed(plan.Config.CertifyFraction, s.cert)
		v := Verdict{Question: s.q.Name, Eval: qr.Eval, Effective: qr.Effective, Target: req.TargetAccuracy,
			Z: req.EffectiveZ(), ZBase: req.Z, FamilyWise: req.FamilyWise, Required: req.MinCoverage,
			RequiredTrain: req.MinEffectiveTrain, Requested: need.Need, CertifiedOn: req.CertifySources, Certification: s.cert, Coverage: s.cert.Coverage}
		if need.Fixable {
			v.Remaining = max(0, v.Requested-v.Eval)
		}
		switch {
		case qr.Code != "" && qr.Code != BlockTooFewEval:
			v.Code = qr.Code
		case s.model == nil:
			v.Code = BlockNoTeacher
		case qr.Effective < req.MinEffectiveTrain:
			v.Code = BlockTooFewLabels
		case qr.Eval < req.MinEvalLabels:
			v.Code = BlockTooFewEval
		// The same predicate the route answers by: the point coverage can
		// clear the minimum while its lower bound does not, and a verdict
		// that says "meets" would disagree with a route that abstains.
		case !selective.Answering(s.cert, req.MinCoverage):
			v.Code = BlockRequirement
		default:
			v.Meets = true
		}
		out = append(out, v)
	}
	return out
}
