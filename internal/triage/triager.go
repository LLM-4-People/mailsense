package triage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// ErrTriageBusy is returned when a triage run is already in progress (in
// this process or, through the store lease, another one): two runs would
// plan and pay for the same messages twice.
var ErrTriageBusy = errors.New("triage: a run is already in progress")

// Triager runs triage over the store.
type Triager struct {
	cfg     *config.Config
	st      *store.Store
	bus     *events.Bus
	src     DeciderSource
	gate    learning.Gate
	clock   clock.Clock
	log     *slog.Logger
	running sync.Mutex // single flight in this process (TryLock)
	holder  string     // this triager's lease holder id
	// cache is the cache path's label overlay (withLabels) of the shared
	// mail core, keyed by the store generation (which label writes bump).
	cacheMu sync.Mutex
	cache   *cached
	// mail is the ONE mail-derived core of this triager: profile, rules,
	// threads, behaviour, settings, scales, history, evidence facts and
	// the picked stores, without labels. The cache path and ModelState
	// derive their label overlays from it (withLabels), so one mail stamp
	// pays one heavy build (threads.Load) whatever asks first.
	mailMu  sync.Mutex
	mail    *core
	mailKey string
	// model is ModelState's overlay (withLabels) of the mail core. Its key
	// excludes the label generation, so a judge run's own label writes
	// never rebuild it and never change the state (the model view reads no
	// labels, examples or history).
	modelMu  sync.Mutex
	model    *core
	modelKey string
	// id is the cached answer identity (profile fingerprint and decider
	// key) of the pages and summaries, and pages/summaries are their
	// bounded read-model caches (viewcache). The identity is guarded by
	// idMu; each cache guards itself.
	idMu      sync.Mutex
	id        *viewIdentity
	pages     *viewcache.Cache[viewKey, *Page]
	summaries *viewcache.Cache[viewKey, *Summary]
	// evidence supplies the evidence facts of triage.facts.evidence
	// (UseEvidence); nil while no evidence engine is wired.
	evidence EvidenceFacts
	// route supplies the certified route's stored answers (UseRoute); nil
	// keeps the paid decider's cache (tests only; the app always wires it).
	route RouteReader
	// coverage is the summary's one store read: the route-coverage
	// aggregate. New wires the store; the call is a field so the single
	// read can be observed without reaching into the store.
	coverage routeCoverageReader
	// pending is the store's existence probe, candidates and loadDecisions
	// are the batch planner's decision reads. They are fields so the
	// trigger's no-work path can be pinned to one probe and zero decision
	// rows (never a whole-cache load) without reaching into the store.
	pending       func(context.Context, store.DecisionScope) (bool, error)
	candidates    func(context.Context, store.DecisionScope) ([]int64, error)
	loadDecisions func(context.Context, store.TriageKey, []int64) (map[int64]store.Decision, error)
	// build is buildMailCore as seen by the trigger's cost tests (nil uses
	// buildMailCore). Like pending, candidates and loadDecisions it is a
	// test seam only; the app never sets it.
	build func(context.Context) (*core, error)
	// routeKey is the configuration identity the route rows are stored
	// under (layer1.RouteKey in the app). It is injected because layer1
	// imports triage's finished topic, so triage cannot import layer1.
	routeKey RouteKeyer
}

// RouteKeyer is the configuration identity of the route rows. The app
// injects layer1.RouteKey; a routed triager without a keyer fails closed
// instead of falling back to the legacy question-spec fingerprint.
type RouteKeyer interface {
	RouteKey() (string, error)
}

// RouteKeyFunc adapts a function to RouteKeyer.
type RouteKeyFunc func() (string, error)

func (f RouteKeyFunc) RouteKey() (string, error) { return f() }

// routeCoverageReader is the store's route-coverage aggregate: the summary
// population, its covered messages, their freshness and their failures. It
// is one read, never a per-message listing or a Go decode. Route rows live
// under routeKey; failures under the triage key (fingerprint, decider).
type routeCoverageReader func(context.Context, string, string, string, store.MessageFilter, []store.QuestionKey) (store.RouteCoverage, error)

// EvidenceFacts supplies the observed evidence facts of the visible
// messages (evidence.Engine.Facts) and a version that changes whenever
// they may have changed.
type EvidenceFacts interface {
	Facts(ctx context.Context) (map[int64]string, error)
	Version() string
}

// RouteReader supplies the certified route's stored answers under the
// route key: one answer (an abstention included) per question and message,
// with the message revision they were computed at.
type RouteReader interface {
	LoadRouteRecords(ctx context.Context, routeKey string, ids []int64) (map[int64]store.RouteRecord, error)
}

// PreviewReader supplies the layer-1 threshold-free preview answers under
// the same route key (the store is the production reader). It is optional:
// a route reader without it serves the route abstentions.
type PreviewReader interface {
	LoadPreviewRecords(ctx context.Context, routeKey string, ids []int64) (map[int64]store.PreviewRecord, error)
}

// previewGate is the learning machine's preview rule. A gate that does not
// implement it is treated as preview off (tests, fakes).
type previewGate interface {
	PreviewAnswers(ctx context.Context) bool
}

// previewing reports whether preview values may substitute answers: the
// gate owns the rule (setting on and real gate closed), so a certified route
// is never relabelled as a best guess.
func (t *Triager) previewing(ctx context.Context) bool {
	g, ok := t.gate.(previewGate)
	return ok && g.PreviewAnswers(ctx)
}

// UseRoute makes the certified route the one source of the user-facing
// answers (call before the first read): the paid cache is then never
// read for a displayed answer, only planned and run. The page and summary
// caches hold route snapshots only (a triager without a route is not
// cached), so a route wired before the first read needs no eviction.
func (t *Triager) UseRoute(r RouteReader, keys RouteKeyer) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	t.route, t.routeKey = r, keys
}

// UseEvidence wires the evidence engine whose facts join the observed
// facts of the states while triage.facts.evidence is on (call before the
// first run). Every core the engine's facts were baked into is dropped: the
// shared mail core and both label overlays.
func (t *Triager) UseEvidence(f EvidenceFacts) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	t.evidence, t.cache = f, nil
	t.modelMu.Lock()
	t.model, t.modelKey = nil, ""
	t.mailMu.Lock()
	t.mail, t.mailKey = nil, ""
	t.mailMu.Unlock()
	t.modelMu.Unlock()
}

// Read-model cache bounds. Pages and summaries are keyed by the full answer
// identity, so the TTL only bounds staleness a generation cannot see; the
// byte budgets bound what those views may pin. A page holds at most
// TriagePageMax rows, and its Profile pointer is shared with the triager, so
// it is never counted per entry.
const (
	pageCacheMaxBytes    = 4 << 20
	summaryCacheMaxBytes = 256 << 10
)

// pageRowBytes is weighPage's basis: a row is a few ints, a taxonomy value
// and a handful of short strings.
const pageRowBytes = 256

// weighPage estimates one page from its row map and id order.
func weighPage(p *Page) int {
	return len(p.Results)*pageRowBytes + len(p.IDs)*8
}

// weighSummary estimates one summary: a key and a handful of coverage counts.
func weighSummary(*Summary) int { return 256 }

// New returns a Triager; bus and clk may be nil.
func New(cfg *config.Config, st *store.Store, bus *events.Bus, src DeciderSource, gate learning.Gate, clk clock.Clock) (*Triager, error) {
	if src == nil || gate == nil {
		return nil, errors.New("triage: a decider source and the learning gate are required")
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	t := &Triager{cfg: cfg, st: st, bus: bus, src: src, gate: gate, clock: clock.Or(clk), log: logx.For("triage"),
		holder: fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(id)), coverage: st.RouteCoverage,
		pending: st.HasPendingDecisions, candidates: st.DecisionCandidates, loadDecisions: st.LoadDecisionsFor}
	t.pages = viewcache.New[viewKey, *Page](viewcache.Options[viewKey, *Page]{
		TTL: 30 * time.Second, Size: 16, MaxBytes: pageCacheMaxBytes, Weigh: weighPage,
		Now: t.clock.Now, Component: "triage",
	})
	t.summaries = viewcache.New[viewKey, *Summary](viewcache.Options[viewKey, *Summary]{
		TTL: 30 * time.Second, Size: 16, MaxBytes: summaryCacheMaxBytes, Weigh: weighSummary,
		Now: t.clock.Now, Component: "triage",
	})
	return t, nil
}

// Close joins the page and summary caches' detached rebuilds, bounded by ctx.
// They read the store after the request that started them returned, so only
// this owner can wait for them before the store closes. It reports whether
// they joined and is idempotent.
func (t *Triager) Close(ctx context.Context) bool {
	pages := t.pages.Close(ctx)
	summaries := t.summaries.Close(ctx)
	return pages && summaries
}

// session is everything one plan, run or read needs.
type session struct {
	*core
	decider engine.Decider
	key     store.TriageKey
	// answerContext is the reader identity a failure's quarantine is scoped
	// to (route key and preview flag): a route or preview change makes the
	// message a candidate again.
	answerContext string
}

// core is the decider-independent part, reused while nothing it reads
// changed (store generation, settings snapshot, profile and rules files).
// The mail-derived fields are shared by every label overlay and between the
// cache and the model (mailCore + withLabels); labels, composer and priors
// belong to the overlay.
type core struct {
	profile   *taxonomy.Profile
	rules     *Rules
	analysis  *threads.Analysis
	behaviour *threads.Profile
	composer  *Composer
	priors    *Priors
	labels    []feedback.Label // every source; consumers apply feedback.Teaching/Reference
	stores    map[string]bool  // picked store keys (Options.Stores must name one)
	roles     []mail.FolderRole
	// built from the mail, reused across label changes (withLabels)
	set     threads.Settings
	scales  scales
	history *threads.History
	fb      feedback.Settings
	facts   map[int64]string // evidence facts by row (triage.facts.evidence; nil when off)
}

// cached is the cache path's label overlay and the store stamp it was built
// at; the shared mail core's own stamp is Triager.mailKey.
type cached struct {
	stamp string
	core  *core
}

// prepare builds (or reuses) the core and builds the decider afresh.
func (t *Triager) prepare(ctx context.Context) (*session, error) {
	c, err := t.core(ctx)
	if err != nil {
		return nil, err
	}
	d, _, err := t.src.Decider(t.cfg)
	if err != nil {
		return nil, err
	}
	answerContext, err := t.answerContext(ctx)
	if err != nil {
		return nil, err
	}
	return &session{core: c, decider: d, key: store.TriageKey{Fingerprint: c.profile.Fingerprint, Decider: engine.DeciderKey(d.Name(), d.Model(), d.Fingerprint())},
		answerContext: answerContext}, nil
}

// answerContext is the reader identity a stored failure is quarantined
// under: the route key and the preview flag. A route or preview change makes
// every earlier permanent failure retryable; a triager without a route
// (tests) has one empty context (the paid cache).
func (t *Triager) answerContext(ctx context.Context) (string, error) {
	if t.route == nil {
		return "", nil
	}
	routeKey, err := t.configuredRouteKey()
	if err != nil {
		return "", err
	}
	return routeKey + "|preview=" + strconv.FormatBool(t.previewing(ctx)), nil
}

// configuredRouteKey is the route identity the stored route rows live under.
// Every routed surface asks it here, so a routed triager without a keyer
// fails closed with one error instead of falling back to another identity.
func (t *Triager) configuredRouteKey() (string, error) {
	if t.routeKey == nil {
		return "", errors.New("triage: a routed triager needs a route key")
	}
	return t.routeKey.RouteKey()
}

// prepareForRead builds the session a user-facing read needs. A routed read
// never calls the decider: its answers come from the route table, so it uses
// the cached identity instead of constructing an adapter and HTTP client per
// message.
func (t *Triager) prepareForRead(ctx context.Context) (*session, error) {
	if t.route != nil {
		return t.prepareRoute(ctx)
	}
	return t.prepare(ctx)
}

// prepareRoute is the routed read's session: the core built from the mail
// plus the response identity. It never selects a decider provider.
func (t *Triager) prepareRoute(ctx context.Context) (*session, error) {
	c, err := t.core(ctx)
	if err != nil {
		return nil, err
	}
	id, err := t.identity()
	if err != nil {
		return nil, err
	}
	return &session{core: c, key: store.TriageKey{Fingerprint: id.fingerprint, Decider: id.decider}}, nil
}

// viewSession is the whole dependency set of a routed view (the page, the
// summary and any other read that renders stored route answers): the profile
// its questions come from, the answer identity and the triaged folder roles.
// It never loads the causal core (threads, behaviour, evidence facts, priors,
// labels, history and the composer) that the full read, analytics and model
// state need.
type viewSession struct {
	profile *taxonomy.Profile
	key     store.TriageKey
	roles   []mail.FolderRole
}

// prepareView builds a routed view's session: profile, identity and roles
// only, never the core. It used to exist twice, once per view.
func (t *Triager) prepareView(ctx context.Context) (*viewSession, error) {
	profile, err := taxonomy.Load(t.cfg)
	if err != nil {
		return nil, err
	}
	id, err := t.identity()
	if err != nil {
		return nil, err
	}
	return &viewSession{
		profile: profile,
		key:     store.TriageKey{Fingerprint: id.fingerprint, Decider: id.decider},
		roles:   folderRoles(t.cfg),
	}, nil
}

// probeSession is everything the automatic trigger's pending probe needs:
// the answer identity, the profile's questions, the triaged roles and the
// picked stores. It is built without the causal core (thread analysis,
// behaviour, labels, history and priors).
type probeSession struct {
	profile *taxonomy.Profile
	key     store.TriageKey
	roles   []mail.FolderRole
	stores  map[string]bool
	answer  string
}

// prepareProbe builds the probe's session. A routed triager (the app) reuses
// the cached view identity, so a probe does not build a decider adapter again
// while its configuration is unchanged; an unrouted triager (tests, the paid
// cache) names the decider directly, exactly as prepare does. Neither path
// builds the causal core.
func (t *Triager) prepareProbe(ctx context.Context) (*probeSession, error) {
	profile, err := taxonomy.Load(t.cfg)
	if err != nil {
		return nil, err
	}
	key := store.TriageKey{Fingerprint: profile.Fingerprint}
	if t.routeKey != nil {
		id, err := t.identity()
		if err != nil {
			return nil, err
		}
		key = store.TriageKey{Fingerprint: id.fingerprint, Decider: id.decider}
	} else {
		d, _, err := t.src.Decider(t.cfg)
		if err != nil {
			return nil, err
		}
		key.Decider = engine.DeciderKey(d.Name(), d.Model(), d.Fingerprint())
	}
	answer, err := t.answerContext(ctx)
	if err != nil {
		return nil, err
	}
	return &probeSession{profile: profile, key: key, roles: folderRoles(t.cfg),
		stores: source.Selected(t.cfg.Snapshot()), answer: answer}, nil
}

// identity returns the answer identity a cached page or summary is keyed
// by: the profile fingerprint its route rows live under and the decider key
// its response names. It is reused under the config version (which follows
// the profile, rules and provider files and every setting), so serving a
// warm view does not reload those files or rebuild the decider.
func (t *Triager) identity() (*viewIdentity, error) {
	// The route key is computed on every call, not cached behind a file
	// stamp: it is the identity the rows are stored under, and a stale key
	// would silently serve another configuration's answers.
	routeKey, err := t.configuredRouteKey()
	if err != nil {
		return nil, err
	}
	key := t.cfg.VersionKey(taxonomy.ProfileFileSetting, RulesFileSetting, engine.ProvidersFileSetting) +
		"|" + t.cfg.String(DeciderSetting) + "|" + routeKey
	t.idMu.Lock()
	defer t.idMu.Unlock()
	if t.id != nil && t.id.key == key {
		return t.id, nil
	}
	profile, err := taxonomy.Load(t.cfg)
	if err != nil {
		return nil, err
	}
	d, _, err := t.src.Decider(t.cfg)
	if err != nil {
		return nil, err
	}
	t.id = &viewIdentity{key: key, fingerprint: profile.Fingerprint, routeKey: routeKey,
		decider: engine.DeciderKey(d.Name(), d.Model(), d.Fingerprint())}
	return t.id, nil
}

// core is the cache path's core: the one mail-derived core (mailCore) with
// the current labels (withLabels). Its stamp includes the store generation,
// so a label write reloads the label overlay; a mail change rebuilds the
// shared mail core.
func (t *Triager) core(ctx context.Context) (*core, error) {
	stamp, mailStamp := t.stamp(t.st.Generation()), t.stamp(t.st.MailGeneration())
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	if t.cache != nil && t.cache.stamp == stamp {
		return t.cache.core, nil
	}
	m, err := t.mailCore(ctx, mailStamp)
	if err != nil {
		return nil, err
	}
	c, err := m.withLabels(ctx, t.st)
	if err != nil {
		return nil, err
	}
	t.cache = &cached{stamp: stamp, core: c}
	return c, nil
}

// mailCore is the ONE mail-derived core per mail stamp: profile, rules,
// threads and behaviour, settings, scales, history, evidence facts and the
// picked stores, without labels. The cache path and ModelState derive their
// label overlays from it (withLabels), so a mail stamp pays the heavy build
// (threads.Load) once, whoever asks first. The stamp is the caller's, so the
// two paths keep their own key semantics.
func (t *Triager) mailCore(ctx context.Context, key string) (*core, error) {
	t.mailMu.Lock()
	defer t.mailMu.Unlock()
	if t.mail != nil && t.mailKey == key {
		return t.mail, nil
	}
	var c *core
	var err error
	if t.build != nil {
		c, err = t.build(ctx)
	} else {
		c, err = t.buildMailCore(ctx)
	}
	if err != nil {
		return nil, err
	}
	t.mail, t.mailKey = c, key
	return c, nil
}

// stamp identifies what a core was built from (gen: a store generation).
func (t *Triager) stamp(gen uint64) string {
	evidence := ""
	if t.cfg.Bool(SettingFactsEvidence) && t.evidence != nil {
		evidence = "evidence " + t.evidence.Version()
	}
	return fmt.Sprintf("%d|%s|%v|%s", gen, t.cfg.VersionKey(taxonomy.ProfileFileSetting, RulesFileSetting),
		t.cfg.Strings(source.SettingSelected), evidence)
}

// FolderRoles are the folder roles of the triaged population, converted by
// the one owner so a role the store scopes and a role the triage ranks are the
// same value.
func FolderRoles(cfg *config.Config) []mail.FolderRole { return store.TriageFolderRoles(cfg) }

// folderRoles is the package's own call site of the one conversion.
func folderRoles(cfg *config.Config) []mail.FolderRole { return FolderRoles(cfg) }

// buildMailCore builds the mail-derived core: everything the mail, the
// profile, the rules and the evidence say, without labels (withLabels adds
// them). It is the one heavy build of a mail stamp, shared by the cache
// path and ModelState through mailCore.
func (t *Triager) buildMailCore(ctx context.Context) (*core, error) {
	profile, err := taxonomy.Load(t.cfg)
	if err != nil {
		return nil, err
	}
	rules, err := LoadRules(t.cfg)
	if err != nil {
		return nil, err
	}
	set, err := threads.SettingsFrom(t.cfg)
	if err != nil {
		return nil, err
	}
	// Triage reads the causal view only (lead decision 2026-10-15): what a
	// message could see at its own instant, never where mail is filed
	// now, so the owner's later filing or a later bridging message never
	// changes another message's state (and its paid answer).
	set.Roles = threads.CausalRoles()
	analysis, err := threads.Load(ctx, t.st, set)
	if err != nil {
		return nil, err
	}
	behaviour, err := analysis.Behaviour(set.Options)
	if err != nil {
		return nil, err
	}
	facts, err := t.evidenceFacts(ctx)
	if err != nil {
		return nil, err
	}
	sc := newScales(set.Options.BucketMinutes, rules.Urgency.BoundsMinutes, rules.Urgency.ResolutionMinutes)
	return &core{profile: profile, rules: rules, analysis: analysis, behaviour: behaviour, set: set, scales: sc, facts: facts,
		history: analysis.KnownHistory(sc.fine, set.Options.ExamplesPerSender, set.Options.Zone, set.Domains),
		fb:      feedback.SettingsFrom(t.cfg), stores: source.Selected(t.cfg.Snapshot()), roles: folderRoles(t.cfg)}, nil
}

// evidenceFacts are the evidence facts while triage.facts.evidence is on
// (nil when off); on without an evidence engine is an error, never a
// silent state without them.
func (t *Triager) evidenceFacts(ctx context.Context) (map[int64]string, error) {
	if !t.cfg.Bool(SettingFactsEvidence) {
		return nil, nil
	}
	if t.evidence == nil {
		return nil, errors.New("triage: triage.facts.evidence is on but no evidence engine is wired")
	}
	return t.evidence.Facts(ctx)
}

// withLabels is the core with the current labels (few-shot examples and
// priors read them); everything built from the mail itself is shared. It
// applies feedback.Teaching once here, at the load boundary: the composer
// and priors receive only manual and judge reference labels, never the
// model's own production answers, and per-message selectors do not rescan or
// copy the full set. c.labels keeps every source for analytics, which scores
// feedback.Reference and reports SourceCounts.
func (c *core) withLabels(ctx context.Context, st *store.Store) (*core, error) {
	labels, _, err := feedback.Load(ctx, st, c.profile)
	if err != nil {
		return nil, err
	}
	teaching := feedback.Teaching(labels)
	n := *c
	n.labels = labels
	n.composer = &Composer{profile: c.profile, rules: c.rules, history: c.history, labels: teaching, norm: c.set.Normalizer,
		domains: c.set.Domains, scales: c.scales, evidence: c.facts}
	n.priors = &Priors{history: c.history, labels: teaching, rules: c.rules, scales: c.scales, smoothing: c.profile.PriorMinSamples,
		addrMin: c.fb.SuspiciousAddrIn, domMin: c.fb.SuspiciousDomIn, domains: c.set.Domains}
	return &n, nil
}

// eachEligible streams the eligible messages (rows nil = all) with their state.
func (c *core) each(ctx context.Context, st *store.Store, only []string, rows []int64, fn func(r *store.MessageRow, state string) error) error {
	return c.eachEligible(ctx, st, only, rows, func(r *store.MessageRow) error {
		return fn(r, c.composer.State(r.RowID, &r.Message))
	})
}

// eachEligible streams the eligible messages without composing their
// state (a reader whose freshness is a stored revision, not the state).
func (c *core) eachEligible(ctx context.Context, st *store.Store, only []string, rows []int64, fn func(r *store.MessageRow) error) error {
	return eachTriaged(ctx, st, c.roles, only, rows, fn)
}

// eachTriaged streams the full message projection of the triaged population
// of roles (rows nil = all), optionally limited to the picked stores. Every
// caller that reads message content (priors, state, subject) asks here, so the
// zero projection keeps its full read; a caller that reads only a row's
// identity and revision asks eachTriagedFields directly.
func eachTriaged(ctx context.Context, st *store.Store, roles []mail.FolderRole, only []string, rows []int64, fn func(r *store.MessageRow) error) error {
	return eachTriagedFields(ctx, st, roles, only, rows, store.MessageFields{}, fn)
}

// eachTriagedFields is the one row read of the triaged population: fields is
// the mail.Message projection the caller needs. EachMessage already holds
// owner-scope mail of the picked stores in the triaged folder roles only
// (store.rolesSQL); only is the planning/read filter on top. The row's
// identity is outside the projection and always read: row id, revision,
// source/store/folder identity, store kind, verdict and reason.
func eachTriagedFields(ctx context.Context, st *store.Store, roles []mail.FolderRole, only []string, rows []int64,
	fields store.MessageFields, fn func(r *store.MessageRow) error) error {
	// The triaged population is one fact: the triaged folder roles of
	// owner-scope mail. Every read that streams it (the detail read, the
	// page window, the coverage aggregate, the export) starts from this
	// filter, so a read cannot silently widen or narrow the population.
	filter := triagedFilter(roles)
	filter.RowIDs, filter.Fields = rows, fields
	return st.EachMessage(ctx, filter, func(r *store.MessageRow) error {
		if len(only) > 0 && !slices.Contains(only, source.StoreKey(r.Message.SourceID, r.Message.StoreID)) {
			return nil
		}
		return fn(r)
	})
}

// triagedFilter is the one triaged-population filter: the triaged folder roles
// of owner-scope mail. Callers add their own RowIDs or Fields on top; nothing
// builds the base by hand.
func triagedFilter(roles []mail.FolderRole) store.MessageFilter {
	return store.MessageFilter{Roles: roles, OwnerOnly: true}
}

// acquire takes the single-run lock and the store lease.
func (t *Triager) acquire(ctx context.Context) (func(), error) {
	if !t.running.TryLock() {
		return nil, ErrTriageBusy
	}
	ok, err := t.st.AcquireLease(ctx, JobKind, t.holder, t.clock.Now().Add(t.cfg.Duration(SettingLeaseTTL)))
	if err != nil || !ok {
		t.running.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrTriageBusy
	}
	return func() {
		_ = t.st.ReleaseLease(context.WithoutCancel(ctx), JobKind, t.holder)
		t.running.Unlock()
	}, nil
}

// renew extends the lease while a long run is working.
func (t *Triager) renew(ctx context.Context) {
	if _, err := t.st.AcquireLease(context.WithoutCancel(ctx), JobKind, t.holder, t.clock.Now().Add(t.cfg.Duration(SettingLeaseTTL))); err != nil {
		t.log.Warn("triage lease not renewed", "err", err)
	}
}
