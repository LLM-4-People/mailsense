package layer1

import (
	"cmp"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/optim"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Plan is what a pass trains with: the profile, the teachers, the
// requirements and the settings, read once.
type Plan struct {
	Profile      *taxonomy.Profile
	Teachers     *Teachers
	Requirements *Requirements
	Config       TrainConfig
	UseEmbedding bool
	UseEvidence  bool
	Dimensions   int
	DeciderKey   string // the configured triage decider's cache key (id/model/fingerprint)
	// DeciderTolerance is the configured decider's probability tolerance:
	// the route's Ondelle read validates its stored distributions with it.
	DeciderTolerance float64
	// Heads overrides the head of a question type (layer1.heads.score;
	// absent: KindOf).
	Heads map[engine.QuestionType]Kind
	// Balance reweights the training examples of yes/no questions by the
	// inverse of their class's prior (the dev harness's E2 measures it;
	// off in the app).
	Balance bool
	// BeforeFit, when set, sees the pass once every question's targets are
	// built and before any fit (the harness's guard fails fast there).
	BeforeFit func(*Trained) error
	// Progress, when set, is told about every question trained (job
	// progress, the harness's log).
	Progress func(done, total int, r QuestionReport)
	// Step, when set, is told about the work inside a run: the design, the
	// targets, and each question's λ path, folds, measurement and
	// certification. It is the job's fine-grained progress.
	Step      func(Step)
	Tolerance map[string]float64 // decisions source → probability tolerance
	// routeKey is the identity of every configuration input that can change
	// a route answer (routeKeyOf). Route rows are stored under it.
	routeKey string
}

// NewPlan reads the profile, teachers, requirements and settings once.
func NewPlan(cfg *config.Config) (*Plan, error) {
	p, err := taxonomy.Load(cfg)
	if err != nil {
		return nil, err
	}
	names := p.QuestionNames()
	t, err := LoadTeachers(cfg, names)
	if err != nil {
		return nil, err
	}
	r, err := LoadRequirements(cfg, names)
	if err != nil {
		return nil, err
	}
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, err
	}
	s := cfg.Snapshot()
	plan := &Plan{Profile: p, Teachers: t, Requirements: r, UseEmbedding: s.Bool(SettingUseEmbedding),
		UseEvidence: s.Bool(SettingUseEvidence), Dimensions: s.Int(SettingDimensions), Tolerance: map[string]float64{},
		Heads: map[engine.QuestionType]Kind{engine.Score: Kind(s.String(SettingScoreHead))}}
	deciderID := s.String(triage.DeciderSetting)
	decider, err := cat.Select(deciderID, engine.KindDecider)
	if err != nil {
		return nil, fmt.Errorf("layer1: triage.decider: %w", err)
	}
	plan.DeciderKey, plan.DeciderTolerance = decider.Key(), decider.ProbabilityTolerance
	for _, src := range t.Sources {
		if src.From != FromDecisions {
			continue
		}
		prov := decider
		if src.Decider != DeciderTriage {
			var err error
			if prov, err = cat.Select(src.Provider, engine.KindDecider); err != nil {
				return nil, fmt.Errorf("%w: teacher %s: %v", ErrTeachers, src.Name, err)
			}
		}
		if prov.Runtime != "" {
			// a local decider is not a teacher: its answers are as local as
			// layer 1's own and would not add what a paid decider knows
			return nil, fmt.Errorf("%w: teacher %s is the local decider %s (a local model never teaches layer 1)", ErrTeachers, src.Name, prov.ID)
		}
		plan.Tolerance[src.Name] = prov.ProbabilityTolerance
	}
	if err := checkCertifySources(t, r, names); err != nil {
		return nil, err
	}
	workers := s.Int(SettingWorkers)
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	var lambdas []float64
	for _, g := range s.Ints(SettingGrid) {
		lambdas = append(lambdas, math.Pow(10, float64(g)))
	}
	slices.SortFunc(lambdas, func(a, b float64) int { return cmp.Compare(b, a) }) // largest first: warm starts down the path
	plan.Config = TrainConfig{Lambdas: lambdas, DefaultLambda: math.Pow(10, float64(s.Int(SettingDefaultL2))),
		Folds: s.Int(SettingFolds), Calibrate: s.String(SettingCalibration) == "temperature", CalibrationPrior: s.Float(SettingCalibrationPrior),
		Fit: FitOptions{Workers: workers, ShardRows: s.Int(SettingShardRows),
			Optim: optim.Options{Memory: s.Int(SettingMemory), MaxIterations: s.Int(SettingMaxIterations), Tolerance: s.Float(SettingTolerance)}},
		CertifyFraction: s.Float(SettingCertifyFraction), Report: r.Report, Req: r.Defaults}
	if len(lambdas) == 0 {
		return nil, fmt.Errorf("layer1: %s is empty", SettingGrid)
	}
	if s.Bool(SettingKNN) {
		plan.Config.KNN = &KNNConfig{K: s.Int(SettingKNNK), Temperature: s.Float(SettingKNNTemperature), Prior: s.Float(SettingKNNPrior)}
	}
	if !plan.UseEmbedding && !plan.UseEvidence {
		return nil, fmt.Errorf("layer1: %s and %s are both off: the heads have no input", SettingUseEmbedding, SettingUseEvidence)
	}
	if plan.routeKey, err = routeKeyOf(plan.Profile, plan.Teachers, plan.Requirements, plan.DeciderKey, plan.DeciderTolerance, cfg); err != nil {
		return nil, fmt.Errorf("layer1: route key: %w", err)
	}
	return plan, nil
}

// checkCertifySources refuses a requirement certifying against a name
// that is no evaluation source of the teachers (a typo would certify
// nothing, silently). A question whose own teachers drop that source is
// not refused: it has no such labels, so nothing of it is certified.
func checkCertifySources(t *Teachers, r *Requirements, names map[string]bool) error {
	eval := map[string]bool{}
	for _, s := range t.Sources {
		if s.Eval {
			eval[s.Name] = true
		}
	}
	var errs []string
	for _, q := range sortedKeys(names) {
		for _, c := range r.For(q).CertifySources {
			if !eval[c] {
				errs = append(errs, fmt.Sprintf("%s: certify_sources names %q, not an evaluation source of the teachers", q, c))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrRequirements, strings.Join(errs, "; "))
	}
	return nil
}

// headOf is the head of a question type.
func (p *Plan) headOf(t engine.QuestionType) Kind {
	if k, ok := p.Heads[t]; ok {
		return k
	}
	k, _ := KindOf(t)
	return k
}

// keyOf is a decisions source's decider key.
func (p *Plan) keyOf(s Source) store.TriageKey {
	d := s.Decider
	if d == DeciderTriage {
		d = p.DeciderKey
	}
	return store.TriageKey{Fingerprint: p.Profile.Fingerprint, Decider: d}
}
