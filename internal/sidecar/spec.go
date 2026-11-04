package sidecar

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed runtimes.json
var runtimesJSON []byte

// Variant names with a meaning of their own.
const (
	// VariantCPU is the cpu build. Not every platform ships one: Windows
	// has only the Vulkan build, while Linux dev/tests and custom
	// catalogs may keep cpu. When the platform has it, it is the canary
	// measurement fallback and the only fallback of a broken GPU build.
	VariantCPU = "cpu"
	// VariantAuto (sidecar.variant) lets the platform choose: the auto
	// list (GPU builds) is tried, measured against cpu when the platform
	// has one; without a cpu build the first candidate that passes the
	// canary serves.
	VariantAuto = "auto"
)

// RequirementVulkan (a variant's requires) is a device the machine must
// have before anything is downloaded: a usable Vulkan driver and at least
// one physical device. It is a preflight, not a fallback or a build.
const RequirementVulkan = "vulkan"

// Arg placeholders (runtimes.json args).
const (
	phModel       = "{model}"
	phHost        = "{host}"
	phPort        = "{port}"
	phThreads     = "{threads}"
	phSlots       = "{slots}"
	phSlotContext = "{slot_context}"
	phContext     = "{context}"
)

// loopback is the only address a runtime listens on.
const loopback = "127.0.0.1"

// Catalog is the pinned runtimes and models (runtimes.json merged with
// sidecar.runtimes.file).
type Catalog struct {
	Runtimes map[string]Runtime  `json:"runtimes"`
	Models   map[string]Artifact `json:"models"`
	// LocalEmbedderPin names the model this catalog serves as the app's
	// local embedder on a platform with a build for it.
	LocalEmbedderPin *LocalEmbedderPin `json:"local_embedder,omitempty"`
	Canary           string            `json:"canary"`
	TuneText         string            `json:"tune_text"`
}

// LocalEmbedderPin is the pinned local embedding selection: a runtime and
// one of its models. The model record carries the embedding spec and the
// chars_per_token estimate the generated provider record needs.
type LocalEmbedderPin struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
}

// Runtime is one server program and its builds per platform.
type Runtime struct {
	Label       string   `json:"label"`
	Version     string   `json:"version"`
	License     string   `json:"license"`
	Args        []string `json:"args"`
	Slots       int      `json:"slots"`
	SlotContext int      `json:"slot_context"` // tokens per slot: the per-input context
	KeyEnv      string   `json:"key_env"`
	EnvAllow    []string `json:"env_allow"`
	HealthPath  string   `json:"health_path"`
	// Platforms maps GOOS/GOARCH to its builds.
	Platforms map[string]Platform `json:"platforms"`
}

// Platform is the builds of one GOOS/GOARCH.
type Platform struct {
	// Auto lists the builds sidecar.variant = auto may try, in order.
	Auto     []string           `json:"auto"`
	Variants map[string]Variant `json:"variants"`
}

// Variant is one build of a runtime.
type Variant struct {
	Exe  string            `json:"exe"` // path inside the unpacked artifacts
	GPU  bool              `json:"gpu"` // checked by the canary against the pinned reference or the cpu build
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	// Requires names the machine capability this build cannot run without;
	// "" needs nothing. The manager checks it before any download.
	Requires  string     `json:"requires,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one pinned download. A model record may also pin the
// reference embedding the GPU canary is checked against.
type Artifact struct {
	Name    string `json:"name"` // file name (downloads, model directory)
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Unpack  string `json:"unpack"` // "zip", "tar.gz" or "" (used as is)
	License string `json:"license,omitempty"`
	// CharsPerToken is the conservative characters-per-token estimate of a
	// model pinned as the local embedder (no tokenizer is shipped).
	CharsPerToken float64 `json:"chars_per_token,omitempty"`
	// Embedding is the complete embedding spec of a model pinned as the
	// local embedder: the same data a provider record carries.
	Embedding *engine.EmbeddingSpec `json:"embedding,omitempty"`
	// CanaryVector is the model's pinned embedding of Catalog.Canary:
	// used verbatim (never rescaled), so a platform without a cpu build
	// still has a canary reference.
	CanaryVector []float32 `json:"canary_vector,omitempty"`
	// CanarySentenceSHA256 is the SHA-256 of the canary sentence the
	// vector belongs to.
	CanarySentenceSHA256 string `json:"canary_sentence_sha256,omitempty"`
}

// Unpack formats.
const (
	UnpackNone  = ""
	UnpackZip   = "zip"
	UnpackTarGz = "tar.gz"
)

// LoadCatalog reads the built-in pins merged with sidecar.runtimes.file
// (absent = none), reporting every problem at once.
func LoadCatalog(cfg *config.Config) (*Catalog, error) {
	merged, err := cfg.Overlay("sidecar.runtimes.file", runtimesJSON, runtimesDeep...)
	if err != nil {
		return nil, fmt.Errorf("sidecar: runtimes: %w", err)
	}
	return parseCatalog(merged)
}

// runtimesDeep is how a runtimes file overrides the pins: per runtime and
// per model (null removes).
var runtimesDeep = []string{"runtimes", "models"}

func parseCatalog(merged []byte) (*Catalog, error) {
	base, err := objectOf(merged, "runtimes")
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(base.Delete("_doc"))
	var c Catalog
	if err := jsonx.DecodeStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("sidecar: runtimes: %w", err)
	}
	if problems := c.problems(); len(problems) > 0 {
		return nil, fmt.Errorf("sidecar: invalid runtimes:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return &c, nil
}

func objectOf(data []byte, where string) (jsonx.Object, error) {
	v, err := jsonx.DecodeOrdered(data)
	if err != nil {
		return nil, fmt.Errorf("sidecar: %s: %w", where, err)
	}
	o, ok := v.(jsonx.Object)
	if !ok {
		return nil, fmt.Errorf("sidecar: %s: must be an object", where)
	}
	return o, nil
}

var (
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// nameRE is every name that becomes a path element.
	nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func (c *Catalog) problems() []string {
	var out []string
	if strings.TrimSpace(c.Canary) == "" || strings.TrimSpace(c.TuneText) == "" {
		out = append(out, "canary and tune_text are required")
	}
	for name, m := range c.Models {
		out = append(out, nameProblem("models", name)...)
		out = append(out, m.problems("models/"+name)...)
		if m.Unpack != UnpackNone {
			out = append(out, fmt.Sprintf("models/%s: a model file is used as is (unpack must be empty)", name))
		}
	}
	for name, rt := range c.Runtimes {
		out = append(out, nameProblem("runtimes", name)...)
		out = append(out, rt.problems("runtimes/"+name)...)
	}
	out = append(out, c.localEmbedderProblems()...)
	slices.Sort(out)
	return out
}

// localEmbedderProblems validates the pinned local selection against the
// catalog it names: a runtime, a model, a complete embedding spec and the
// chars_per_token estimate are all required. A platform without a build is
// not a catalog problem: LocalEmbedder reports it when the app asks.
func (c *Catalog) localEmbedderProblems() []string {
	pin := c.LocalEmbedderPin
	if pin == nil {
		return nil
	}
	const where = "local_embedder"
	if pin.Runtime == "" || pin.Model == "" {
		return []string{where + ": runtime and model are required"}
	}
	if _, ok := c.Runtimes[pin.Runtime]; !ok {
		return []string{fmt.Sprintf("%s: unknown runtime %q", where, pin.Runtime)}
	}
	model, ok := c.Models[pin.Model]
	if !ok {
		return []string{fmt.Sprintf("%s: unknown model %q", where, pin.Model)}
	}
	var out []string
	if model.Embedding == nil {
		out = append(out, fmt.Sprintf("%s: model %q has no embedding spec", where, pin.Model))
	} else if err := model.Embedding.Validate(); err != nil {
		out = append(out, fmt.Sprintf("%s: model %q: embedding: %v", where, pin.Model, err))
	}
	if model.CharsPerToken <= 0 {
		out = append(out, fmt.Sprintf("%s: model %q: chars_per_token must be > 0", where, pin.Model))
	}
	return out
}

func nameProblem(where, name string) []string {
	if nameRE.MatchString(name) && name != VariantAuto {
		return nil
	}
	return []string{fmt.Sprintf("%s/%s: names must be plain file names (and not %q)", where, name, VariantAuto)}
}

func (rt *Runtime) problems(where string) []string {
	var out []string
	joined := strings.Join(rt.Args, " ")
	if !nameRE.MatchString(rt.Version) || !strings.Contains(joined, phModel) || !strings.Contains(joined, phPort) || !strings.Contains(joined, phHost) {
		out = append(out, where+": a plain-name version and args with {model}, {host} and {port} are required")
	}
	if rt.KeyEnv == "" || !strings.HasPrefix(rt.HealthPath, "/") || rt.Slots < 1 || rt.SlotContext < 1 {
		out = append(out, where+": key_env, an absolute health_path, slots ≥ 1 and slot_context ≥ 1 are required")
	}
	for name, p := range rt.Platforms {
		out = append(out, p.problems(fmt.Sprintf("%s/platforms/%s", where, name))...)
	}
	return out
}

func (p *Platform) problems(where string) []string {
	var out []string
	if len(p.Variants) == 0 {
		out = append(out, where+": at least one variant is required")
	}
	if len(p.Auto) == 0 && !p.hasCPU() {
		out = append(out, where+": a cpu variant is required when auto lists no GPU build")
	}
	for _, a := range p.Auto {
		if v, ok := p.Variants[a]; !ok || !v.GPU {
			out = append(out, fmt.Sprintf("%s/auto: %q must be a gpu variant of the platform", where, a))
		}
	}
	for name, v := range p.Variants {
		out = append(out, nameProblem(where+"/variants", name)...)
		out = append(out, v.problems(where+"/variants/"+name)...)
	}
	return out
}

// hasCPU reports whether the platform has a cpu build. The fallback, the
// canary reference and auto candidacy all read this one fact, so "this machine
// can run cpu" cannot differ between them. A value receiver keeps it callable
// on a map element (a platform read out of the catalog).
func (p Platform) hasCPU() bool {
	_, ok := p.Variants[VariantCPU]
	return ok
}

// defaultVariant is the build used when nothing selects one: cpu when the
// platform has it, else the first auto entry, else the only variant.
func (p *Platform) defaultVariant() (string, error) {
	if p.hasCPU() {
		return VariantCPU, nil
	}
	if len(p.Auto) > 0 {
		if _, ok := p.Variants[p.Auto[0]]; ok {
			return p.Auto[0], nil
		}
	}
	if len(p.Variants) == 1 {
		for name := range p.Variants {
			return name, nil
		}
	}
	return "", fmt.Errorf("no default build (%d variants, auto %v)", len(p.Variants), p.Auto)
}

func (v *Variant) problems(where string) []string {
	var out []string
	switch v.Requires {
	case "", RequirementVulkan:
	default:
		out = append(out, fmt.Sprintf("%s: unknown requires %q (known: %q)", where, v.Requires, RequirementVulkan))
	}
	if !filepath.IsLocal(filepath.FromSlash(v.Exe)) || v.Exe == "" {
		out = append(out, where+": exe must be a relative path inside the artifacts")
	}
	if len(v.Artifacts) == 0 {
		out = append(out, where+": at least one artifact is required")
	}
	for i, a := range v.Artifacts {
		out = append(out, a.problems(fmt.Sprintf("%s/artifacts/%d", where, i))...)
		if a.Unpack == UnpackNone {
			out = append(out, fmt.Sprintf("%s/artifacts/%d: runtime artifacts are archives (zip or tar.gz)", where, i))
		}
	}
	return out
}

func (a *Artifact) problems(where string) []string {
	var out []string
	if !nameRE.MatchString(a.Name) {
		out = append(out, fmt.Sprintf("%s: name %q must be a plain file name", where, a.Name))
	}
	if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		out = append(out, fmt.Sprintf("%s: url %q must be an absolute http(s) URL", where, a.URL))
	}
	if !sha256RE.MatchString(a.SHA256) || a.Size < 1 {
		out = append(out, where+": sha256 (64 lower-case hex digits) and a positive size are required")
	}
	if a.Unpack != UnpackNone && a.Unpack != UnpackZip && a.Unpack != UnpackTarGz {
		out = append(out, fmt.Sprintf("%s: unpack must be empty, zip or tar.gz, got %q", where, a.Unpack))
	}
	switch {
	case len(a.CanaryVector) > 0:
		if len(a.CanaryVector) > 8192 {
			out = append(out, fmt.Sprintf("%s: canary_vector has %d values (1 to 8192 are allowed)", where, len(a.CanaryVector)))
		}
		for i, f := range a.CanaryVector {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				out = append(out, fmt.Sprintf("%s: canary_vector[%d] is not finite", where, i))
				break
			}
		}
		if !sha256RE.MatchString(a.CanarySentenceSHA256) {
			out = append(out, where+": canary_sentence_sha256 (64 lower-case hex digits) is required with canary_vector")
		}
	case a.CanarySentenceSHA256 != "":
		out = append(out, where+": canary_sentence_sha256 requires a canary_vector")
	}
	return out
}

// LocalEmbedder is the pinned local embedder of one platform: the
// generated provider record's data plus the resolved build.
type LocalEmbedder struct {
	Runtime       string
	Model         string
	Build         Build
	CharsPerToken float64
	Embedding     engine.EmbeddingSpec
}

// LocalEmbedder resolves the pinned local embedder for platform. ok is
// false when the catalog pins none or the platform has no build for it;
// the catalog's own validation has already checked the runtime, the model,
// the embedding spec and chars_per_token, and this repeats only the cheap
// existence checks.
func (c *Catalog) LocalEmbedder(platform string) (LocalEmbedder, bool) {
	if c == nil || c.LocalEmbedderPin == nil {
		return LocalEmbedder{}, false
	}
	pin := *c.LocalEmbedderPin
	model, ok := c.Models[pin.Model]
	if !ok || model.Embedding == nil || model.CharsPerToken <= 0 {
		return LocalEmbedder{}, false
	}
	if _, ok := c.Runtimes[pin.Runtime]; !ok {
		return LocalEmbedder{}, false
	}
	build, err := c.DefaultBuild(pin.Runtime, platform, pin.Model)
	if err != nil {
		return LocalEmbedder{}, false
	}
	return LocalEmbedder{Runtime: pin.Runtime, Model: pin.Model, Build: build,
		CharsPerToken: model.CharsPerToken, Embedding: *model.Embedding}, true
}

// Build is a variant resolved for this machine.
type Build struct {
	Runtime     string
	RuntimeSpec Runtime
	Variant     string
	Spec        Variant
	Model       string
	ModelFile   Artifact
	Canary      string // the catalog's canary sentence
	TuneText    string // the catalog's tuning text
}

// platformOf resolves a catalog runtime and its platform, the one
// ErrUnsupported classification: an unknown runtime and a runtime without
// builds for this machine answer the same typed error, so every caller
// (Resolve, DefaultBuild, a probe, a choice key) agrees on what
// "unsupported" means and on its text.
func (c *Catalog) platformOf(runtime, platform string) (Runtime, Platform, error) {
	rt, ok := c.Runtimes[runtime]
	if !ok {
		return Runtime{}, Platform{}, fmt.Errorf("%w: unknown runtime %q", ErrUnsupported, runtime)
	}
	plat, ok := rt.Platforms[platform]
	if !ok {
		return Runtime{}, Platform{}, fmt.Errorf("%w: runtime %s has no builds for %s", ErrUnsupported, runtime, platform)
	}
	return rt, plat, nil
}

// Resolve finds the build of a runtime variant for a platform and the
// model it serves.
func (c *Catalog) Resolve(runtime, variant, platform, model string) (Build, error) {
	rt, plat, err := c.platformOf(runtime, platform)
	if err != nil {
		return Build{}, err
	}
	v, ok := plat.Variants[variant]
	if !ok {
		return Build{}, fmt.Errorf("%w: runtime %s has no %q build for %s (it has %v, or auto)", ErrUnsupported, runtime, variant, platform,
			slices.Sorted(maps.Keys(plat.Variants)))
	}
	m, ok := c.Models[model]
	if !ok {
		return Build{}, fmt.Errorf("%w: no pinned model %q", ErrUnsupported, model)
	}
	return Build{Runtime: runtime, RuntimeSpec: rt, Variant: variant, Spec: v, Model: model, ModelFile: m, Canary: c.Canary, TuneText: c.TuneText}, nil
}

// DefaultBuild is the build a runtime uses when nothing selects one: cpu
// when the platform has it, else the first auto entry, else the only
// variant. Model metadata and an auto list with nothing to try use it.
func (c *Catalog) DefaultBuild(runtime, platform, model string) (Build, error) {
	_, plat, err := c.platformOf(runtime, platform)
	if err != nil {
		return Build{}, err
	}
	variant, err := plat.defaultVariant()
	if err != nil {
		return Build{}, fmt.Errorf("%w: runtime %s for %s: %v", ErrUnsupported, runtime, platform, err)
	}
	return c.Resolve(runtime, variant, platform, model)
}
