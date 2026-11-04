package layer1

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

//go:embed requirements.json
var requirementsJSON []byte

// Requirement is what one question's answers must reach.
type Requirement struct {
	MinEffectiveTrain float64 `json:"min_effective_train"`
	MinEvalLabels     int     `json:"min_eval_labels"`
	TargetAccuracy    float64 `json:"target_accuracy"`
	MinCoverage       float64 `json:"min_coverage"`
	Z                 float64 `json:"z"`
	// FamilyWise corrects Z for the number of questions tested, so the
	// target is a family-wise statement rather than a per-question one.
	FamilyWise bool `json:"family_wise"`
	// ZEffective is the corrected quantile; it is derived from Z and the
	// question count at parse time and never decoded from JSON.
	ZEffective    float64 `json:"-"`
	RequestLabels *int    `json:"request_labels"`
	// CertifySources are the teacher sources (with eval) whose labels the
	// certification part is tested against: the verdict is "agreement with"
	// them (a judge's labels certify agreement with the judge).
	CertifySources []string `json:"certify_sources"`
}

// EffectiveZ is the quantile every bound of this requirement uses: the
// family-wise corrected value when the document enables it, else Z. A
// Requirement built directly (tests) has no derived value and falls back to
// Z.
func (r Requirement) EffectiveZ() float64 {
	if r.ZEffective > 0 {
		return r.ZEffective
	}
	return r.Z
}

// effectiveZ applies a two-sided Bonferroni correction across questions:
// alpha = 2·(1−Φ(z)); z_eff = Φ⁻¹(1 − alpha/(2m)). With one question the
// correction is the identity.
func (r Requirement) effectiveZ(questions int) float64 {
	if !r.FamilyWise || questions <= 1 {
		return r.Z
	}
	alpha := 2 * (1 - mathx.NormalCDF(r.Z))
	z, ok := mathx.NormalQuantile(1 - alpha/(2*float64(questions)))
	if !ok {
		return r.Z
	}
	return z
}

// Requested is the static design request: the setting, else the fewest
// evaluation labels that can open the gate at all — the certification
// part (fraction of the labels) must answer selective.Needed labels
// without an error on at least min_coverage of itself:
// ⌈Needed / (min_coverage · fraction)⌉, at least min_eval_labels. Without
// observed certification this is what the labels stage asks for.
func (r Requirement) Requested(fraction float64) int {
	if r.RequestLabels != nil {
		return *r.RequestLabels
	}
	need := float64(selective.Needed(r.TargetAccuracy, r.EffectiveZ())) / (r.MinCoverage * fraction)
	// Do not cap an honest request: the shipped family-wise target needs
	// more than the old 2000-label ceiling, and a capped request would say
	// "nothing remains" while the gate still needs hundreds of answers. The
	// automatic spend is bounded by labels.auto.max and the budget, not here.
	return max(r.MinEvalLabels, int(math.Ceil(need-1e-9)))
}

// maxLabelSearch bounds the honest search for the labels a measured
// precision needs. It is not a cap on a request: a need at or above it
// means no practical label count can raise the lower bound to the target,
// so the labels stage treats the question as unfixable and names the model
// instead of buying labels that cannot change the verdict.
const maxLabelSearch = 1_000_000

// LabelNeed is the labels-stage plan for one question: the evaluation
// labels to ask for, and whether any label count can certify the model.
// Fixable is false when the measured certification precision is below the
// target (more labels cannot raise it) or the honest request would exceed
// maxLabelSearch. Need stays the static design request in that case, so a
// report still shows the requirement's measure instead of zero.
type LabelNeed struct {
	Need    int
	Fixable bool
}

// LabelNeed follows the model's observed certification: the certification
// split must answer enough labels at the measured error rate, and the
// labels are scaled by the observed certified coverage (at least
// min_coverage). A 99 % model asks for the zero-error minimum; a 97 % model
// asks for the larger split its error rate needs, so the request rises
// with the quality it has to prove, not with a fixed assumption of
// perfection. An explicit request_labels setting is always honored. Without
// an answered certification the static zero-error request stands, uncapped.
func (r Requirement) LabelNeed(fraction float64, cert selective.Certification) LabelNeed {
	if r.RequestLabels != nil {
		return LabelNeed{Need: *r.RequestLabels, Fixable: true}
	}
	static := r.Requested(fraction)
	if cert.Answered <= 0 || fraction <= 0 {
		return LabelNeed{Need: static, Fixable: true}
	}
	precision := cert.AnsweredPrecision()
	if precision < r.TargetAccuracy {
		return LabelNeed{Need: static, Fixable: false}
	}
	needed := neededAt(precision, r.TargetAccuracy, r.EffectiveZ(), maxLabelSearch)
	if needed >= maxLabelSearch {
		return LabelNeed{Need: static, Fixable: false}
	}
	cover := max(cert.Coverage, r.MinCoverage)
	if cover <= 0 {
		return LabelNeed{Need: static, Fixable: true}
	}
	need := max(r.MinEvalLabels, int(math.Ceil(float64(needed)/(cover*fraction)-1e-9)))
	return LabelNeed{Need: need, Fixable: true}
}

// neededAt is the fewest answers whose Wilson lower bound at the observed
// precision reaches target; searchBound when even that many cannot (the
// precision is below the target, or too close to it for a practical sample
// to distinguish).
func neededAt(precision, target, z float64, searchBound int) int {
	if precision < target {
		return searchBound
	}
	lo, hi := 1, searchBound
	for lo < hi {
		mid := (lo + hi) / 2
		successes := int(math.Round(precision * float64(mid)))
		successes = max(0, min(mid, successes))
		if lower, _, ok := mathx.Wilson(successes, mid, z); ok && lower >= target {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// Requirements are the defaults and the per-question overrides.
type Requirements struct {
	Defaults Requirement
	// Digest is the merged document's digest (defaults, every
	// per-question override, report_targets): a run's inputs.
	Digest string
	// Report are the precision targets every report measures coverage at.
	Report    []float64
	questions map[string]Requirement
}

// For is a question's requirement.
func (r *Requirements) For(question string) Requirement {
	if q, ok := r.questions[question]; ok {
		return q
	}
	return r.Defaults
}

// LoadRequirements reads requirements.json merged with
// layer1.requirements.file, checked against the question names.
func LoadRequirements(cfg *config.Config, questions map[string]bool) (*Requirements, error) {
	data, err := cfg.Overlay(SettingRequirementsFile, requirementsJSON, "defaults", "questions")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequirements, err)
	}
	r, err := parseRequirements(data, questions)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequirements, err)
	}
	return r, nil
}

// ErrRequirements and ErrTeachers mark a broken requirements or teachers
// document (the stage names the remedy).
var (
	ErrRequirements = errors.New("layer1: the requirements file has problems")
	ErrTeachers     = errors.New("layer1: the teachers file has problems")
)

func parseRequirements(data []byte, questions map[string]bool) (*Requirements, error) {
	var doc struct {
		Doc       string                     `json:"_doc"`
		Defaults  json.RawMessage            `json:"defaults"`
		Report    []float64                  `json:"report_targets"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("layer1: requirements: %w", err)
	}
	var errs []string
	out := &Requirements{questions: map[string]Requirement{}, Report: doc.Report, Digest: digest.Hex(data, 32)}
	for _, t := range doc.Report {
		if !(t > 0 && t <= 1) {
			errs = append(errs, fmt.Sprintf("report_targets: %v is not in (0, 1]", t))
		}
	}
	if err := jsonx.DecodeStrict(doc.Defaults, &out.Defaults); err != nil {
		errs = append(errs, "defaults: "+err.Error())
	}
	errs = append(errs, out.Defaults.problems("defaults")...)
	for _, q := range sortedKeys(doc.Questions) {
		if !questions[q] {
			errs = append(errs, fmt.Sprintf("questions: unknown question %q", q))
			continue
		}
		r := out.Defaults
		if err := jsonx.DecodeStrict(doc.Questions[q], &r); err != nil {
			errs = append(errs, fmt.Sprintf("questions.%s: %v", q, err))
			continue
		}
		errs = append(errs, r.problems("questions."+q)...)
		out.questions[q] = r
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("layer1: requirements: %s", strings.Join(errs, "; "))
	}
	m := len(questions)
	out.Defaults.ZEffective = out.Defaults.effectiveZ(m)
	for q, r := range out.questions {
		r.ZEffective = r.effectiveZ(m)
		out.questions[q] = r
	}
	return out, nil
}

func (r Requirement) problems(where string) []string {
	var out []string
	bad := func(s string) { out = append(out, where+": "+s) }
	if !(r.MinEffectiveTrain >= 1) || math.IsInf(r.MinEffectiveTrain, 0) {
		bad("min_effective_train must be ≥ 1")
	}
	if r.MinEvalLabels < 2 {
		bad("min_eval_labels must be ≥ 2")
	}
	if !(r.TargetAccuracy > 0 && r.TargetAccuracy < 1) {
		bad("target_accuracy must be in (0, 1)")
	}
	if !(r.MinCoverage > 0 && r.MinCoverage <= 1) {
		bad("min_coverage must be in (0, 1]")
	}
	if !(r.Z > 0 && r.Z <= 10) {
		bad("z must be in (0, 10]")
	}
	if r.RequestLabels != nil && *r.RequestLabels < 1 {
		bad("request_labels must be null or ≥ 1")
	}
	if len(r.CertifySources) == 0 {
		bad("certify_sources must name at least one evaluation source")
	}
	return out
}
