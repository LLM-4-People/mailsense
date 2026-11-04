package layer1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// routeKeyPrefix marks a route-key value and its composition version. Route
// and preview rows live under this key; the legacy 12-hex profile
// fingerprint stays the paid-decision and import identity.
const routeKeyPrefix = "rk1-"

// routeKeyParts is every configuration input that can change a route or
// preview answer. Store data (mail, labels, vectors, decisions) is
// deliberately absent: it is rewritten by the same commit, and including it
// would blank the route after every sync.
type routeKeyParts struct {
	Profile      string   `json:"profile"`      // the full profile, not only its questions
	Teachers     string   `json:"teachers"`     // the merged teachers document
	Requirements string   `json:"requirements"` // the merged requirements document
	Decider      string   `json:"decider"`      // id/model/provider fingerprint
	Tolerance    float64  `json:"tolerance"`    // the decider's probability tolerance
	Algorithm    int      `json:"algorithm"`    // training-code version
	Features     string   `json:"features"`     // feature-schema version and layout digest
	Rules        string   `json:"rules"`        // the merged evidence-rules document
	Folders      []string `json:"folder_roles"` // the route's population roles, sorted
	Settings     string   `json:"settings"`     // every route-affecting setting
}

// routeKeySettingKeys are the settings whose change can change a route
// answer. They are also the settings whose change must start a run: the two
// lists have one owner, so a new key input cannot be added without its
// trigger.
var routeKeySettingKeys = func() []string {
	out := append([]string(nil), settingKeys...)
	out = append(out, triage.DeciderSetting, triage.SettingFolderRoles, "time.zone",
		evidence.SettingRulesFile, evidence.SettingRulesZ, evidence.SettingRulesMinPrecision,
		evidence.SettingFeaturesFile, evidence.SettingFeaturesHashBuckets,
		threads.SettingBuckets)
	out = append(out, threads.GroupingSettings()...)
	slices.Sort(out)
	return slices.Compact(out)
}()

// routeSettingsDigest hashes the value of every route-affecting setting.
func routeSettingsDigest(cfg *config.Config) (string, error) {
	s := cfg.Snapshot()
	parts := make([][2]any, 0, len(routeKeySettingKeys))
	for _, k := range routeKeySettingKeys {
		v, _ := s.Value(k)
		parts = append(parts, [2]any{k, v})
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	return digest.Hex(raw, 32), nil
}

// routeKeyFileSettings are the six documents whose exact bytes can change a
// route answer. The memo reads them; only a miss parses them.
var routeKeyFileSettings = []string{
	taxonomy.ProfileFileSetting,
	SettingTeachersFile,
	SettingRequirementsFile,
	engine.ProvidersFileSetting,
	evidence.SettingRulesFile,
	evidence.SettingFeaturesFile,
}

// routeKeyInputsDigest is content-exact: it hashes the bytes of every input
// file plus the route settings. A same-size, same-mtime rewrite still moves
// it, which an fsx stamp cannot promise.
func routeKeyInputsDigest(cfg *config.Config) (string, error) {
	s := cfg.Snapshot()
	parts := make([][]byte, 0, len(routeKeyFileSettings)*2+1)
	for _, key := range routeKeyFileSettings {
		path := cfg.Paths().Resolve(s.String(key))
		parts = append(parts, []byte(key), []byte(path))
		sum, _, err := digest.File(path)
		switch {
		case err == nil:
			parts = append(parts, []byte(sum))
		case errors.Is(err, fs.ErrNotExist):
			parts = append(parts, []byte("absent"))
		default:
			return "", err
		}
	}
	settings, err := routeSettingsDigest(cfg)
	if err != nil {
		return "", err
	}
	return digest.Of(append(parts, []byte(settings))...), nil
}

// routeKeyMemo holds the last composed key under the exact input digest.
// Only the key is cached: Train mutates a Plan's KNN config, so sharing a
// Plan across requests would be unsafe.
var routeKeyMemo struct {
	mu     sync.Mutex
	inputs string
	key    string
}

// RouteKey returns the memoized key while every input file and route setting
// is byte-identical. A composition that overlaps an edit is retried against
// the settled content, so a cached key always belongs to one configuration.
func RouteKey(cfg *config.Config) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		inputs, err := routeKeyInputsDigest(cfg)
		if err != nil {
			return "", fmt.Errorf("layer1: route key: %w", err)
		}
		routeKeyMemo.mu.Lock()
		if routeKeyMemo.inputs == inputs {
			key := routeKeyMemo.key
			routeKeyMemo.mu.Unlock()
			return key, nil
		}
		routeKeyMemo.mu.Unlock()

		key, err := composeRouteKey(cfg)
		if err != nil {
			return "", err
		}
		after, err := routeKeyInputsDigest(cfg)
		if err != nil {
			return "", fmt.Errorf("layer1: route key: %w", err)
		}
		if after != inputs {
			continue
		}
		routeKeyMemo.mu.Lock()
		routeKeyMemo.inputs, routeKeyMemo.key = inputs, key
		routeKeyMemo.mu.Unlock()
		return key, nil
	}
	return "", errors.New("layer1: route key: the configuration changed while it was read")
}

// routeKeyOf is the one composition of the route key. The decider identity
// is passed in (not selected here): a first-run install has no provider, but
// the key must still exist so the setup screens can read an empty route.
func routeKeyOf(p *taxonomy.Profile, t *Teachers, r *Requirements, decider string, tolerance float64, cfg *config.Config) (string, error) {
	profile, err := p.Digest()
	if err != nil {
		return "", err
	}
	rules, err := evidence.RulesDigest(cfg)
	if err != nil {
		return "", err
	}
	features, err := evidence.FeaturesDigest(cfg)
	if err != nil {
		return "", err
	}
	settings, err := routeSettingsDigest(cfg)
	if err != nil {
		return "", err
	}
	folders := append([]string(nil), cfg.Snapshot().Strings(triage.SettingFolderRoles)...)
	slices.Sort(folders)
	raw, err := json.Marshal(routeKeyParts{
		Profile: profile, Teachers: t.Digest, Requirements: r.Digest,
		Decider: decider, Tolerance: tolerance, Algorithm: algorithmVersion,
		Features: featureVersion + ":" + features, Rules: rules, Folders: folders, Settings: settings,
	})
	if err != nil {
		return "", err
	}
	return routeKeyPrefix + digest.Hex(raw, 32), nil
}

// deciderIdentity is the configuration identity of the configured decider.
// Unlike NewPlan's strict selection, it succeeds before any provider is
// usable: Brief, Priority and Triage must render their setup state on a
// first run, not fail with a provider error. The fallback includes the
// provider catalog's digest, so configuring the role moves the key.
func deciderIdentity(cfg *config.Config) (string, float64, error) {
	id := cfg.Snapshot().String(triage.DeciderSetting)
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return "", 0, err
	}
	d, err := cat.Select(id, engine.KindDecider)
	if err != nil {
		return "unconfigured:" + id + ":" + cat.Digest, 0, nil
	}
	return d.Key(), d.ProbabilityTolerance, nil
}

// RouteKey is the identity of every input that can change the route's
// answers. Route and preview rows are stored under it, so a configuration
// change can never read an answer the new configuration did not compute.
func (p *Plan) RouteKey() string { return p.routeKey }

// composeRouteKey is the one composition, run only on a memo miss: it loads
// and parses every input. It never trains and never requires a configured
// decider provider, so a first-run install still has a route key.
func composeRouteKey(cfg *config.Config) (string, error) {
	p, err := taxonomy.Load(cfg)
	if err != nil {
		return "", fmt.Errorf("layer1: route key: %w", err)
	}
	names := p.QuestionNames()
	t, err := LoadTeachers(cfg, names)
	if err != nil {
		return "", fmt.Errorf("layer1: route key: %w", err)
	}
	r, err := LoadRequirements(cfg, names)
	if err != nil {
		return "", fmt.Errorf("layer1: route key: %w", err)
	}
	decider, tolerance, err := deciderIdentity(cfg)
	if err != nil {
		return "", fmt.Errorf("layer1: route key: %w", err)
	}
	return routeKeyOf(p, t, r, decider, tolerance, cfg)
}
