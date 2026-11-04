package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/brief"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/messages"
	"github.com/LLM-4-People/mailsense/internal/oslocale"
	"github.com/LLM-4-People/mailsense/internal/render"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// wire is the production adapter between the API interfaces and the app's
// services. It is the only place that knows both worlds; handlers never
// touch a store, a config object or a connector.
type wire struct {
	app      *app
	shutdown *shutdownRequest
	hub      *hub.Service

	// providersMu serialises provider record writes (load, validate,
	// write) so two API writes can never lose one another.
	providersMu sync.Mutex

	// worldOnce builds the companies explorer on first use. Construction
	// only validates the app's invariants; the error surfaces through the
	// endpoint instead of being dropped at wiring time.
	worldOnce sync.Once
	world     *worldsvc.Service
	worldErr  error

	// messagesOnce builds the first-page Mail cache on first use. The warm
	// and the first click share it, so the click never rebuilds the page
	// the warm already built.
	messagesOnce sync.Once
	messagesSvc  *messages.Service

	// conversationsOnce builds the thread-first mailbox page on first use.
	// It depends on the priority service for classifier-backed thread
	// states; the service falls back to the store resolver when priority is
	// absent or fails.
	conversationsOnce sync.Once
	conversationsSvc  *conversations.Service
	conversationsErr  error

	// commitmentsOnce builds the deterministic open-loop read model on first
	// use. It reuses the priority service as both the thread-truth resolver
	// and the decision source, so a commitment cannot disagree with Agenda,
	// the Reply filter or the decision ranking.
	commitmentsOnce sync.Once
	commitmentsSvc  *commitments.Service
	commitmentsErr  error

	// briefOnce composes the deterministic Brief from the same conversation
	// and commitment services the API already exposes. It adds no store read
	// and no second cache; each section reads its owner's cached read model.
	// The builders are acyclic: briefing depends on both services, neither
	// depends on briefing.
	briefOnce sync.Once
	briefSvc  *brief.Service
	briefErr  error
}

func servicesFor(a *app, shutdown *shutdownRequest, boot *bootState) api.Services {
	w := &wire{app: a, shutdown: shutdown}
	// The priority service is built by wireEngines, before the API wire owns
	// the guard. Attach the one production admission here, before the first
	// read: a cold ranking build then obeys the same host ceilings as every
	// job, while a service built without it (tests, tools) keeps scanning.
	if a.priority != nil {
		a.priority.SetAdmission(priorityAdmission{guard: a.guard})
	}
	w.hub = hub.New(hub.Deps{Store: a.st, Learning: learningAPI{w}, Sync: a.syncer, Now: clock.Real().Now})
	// Q&A answers a question about a known organization from the same world
	// service the Companies screen uses. Both share its cached list and
	// profiles; Q&A is built before the API world exists, so this is its one
	// late binding.
	if a.qa != nil {
		a.qa.SetCompanies(companiesAPI{w})
	}
	// The wire owns every read model whose cache rebuilds off the request
	// path. Registering them here, beside their construction, is the one
	// place a new cached service must be added: a call site far from the
	// adapters silently omits it and lets the store close under a reader.
	a.views = readModelCloserList(w, a.learning)
	services := api.Services{
		Bus:           a.bus,
		Status:        statusAPI{w},
		Learning:      learningAPI{w},
		Jobs:          jobsAPI{w},
		HeldRemovals:  heldRemovalsAPI{w},
		Mailboxes:     mailboxesAPI{w},
		Runs:          runsAPI{w},
		Triage:        triageAPI{w},
		Search:        a.search,
		Settings:      settingsAPI{w},
		Secrets:       secretsAPI{w},
		Providers:     providersAPI{w: w},
		Sidecar:       sidecarAPI{w},
		Logs:          logsAPI{w},
		Labels:        layer1API{w},
		Hub:           hubAPI{w},
		Messages:      messagesAPI{w},
		Threads:       threadAPI{w},
		Conversations: conversationsAPI{w},
		Commitments:   commitmentsAPI{w},
		Brief:         briefAPI{w},
		Priority:      a.priority,
		Agenda:        a.agenda,
		Ask:           a.qa,
		Companies:     companiesAPI{w},
		Shutdown:      shutdown,
		Boot:          bootAPI{boot},
	}
	// A build without a guard reports no resources at all; a typed-nil
	// pointer in the interface would read as present and panic on use.
	if a.guard != nil {
		services.Resources = a.guard
	}
	return services
}

type companiesAPI struct{ w *wire }

func (a companiesAPI) List(ctx context.Context, q worldsvc.ListQuery) (worldsvc.ListResult, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return worldsvc.ListResult{}, err
	}
	return svc.List(ctx, q)
}

func (a companiesAPI) Profile(ctx context.Context, key string, months, eventDays int) (worldsvc.ProfileView, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return worldsvc.ProfileView{}, err
	}
	return svc.Profile(ctx, key, months, eventDays)
}

func (a companiesAPI) Profiles(ctx context.Context, keys []string, months, eventDays int) (map[string]worldsvc.ProfileView, error) {
	svc, err := a.w.companiesService()
	if err != nil {
		return nil, err
	}
	return svc.Profiles(ctx, keys, months, eventDays)
}

// companiesService builds the world service once from the app's store and
// priority ranking. The priority ranking owns the classifier-backed thread
// states, so the company profile never falls back to a second interpretation
// of whether the owner owes a reply.
func (w *wire) companiesService() (*worldsvc.Service, error) {
	w.worldOnce.Do(func() {
		w.world, w.worldErr = worldsvc.New(worldsvc.Deps{
			Store:    w.app.st,
			Priority: w.app.priority,
			Config:   w.app.cfg,
			Clock:    clock.Real(),
		})
	})
	return w.world, w.worldErr
}

// priorityAdmission adapts the resource guard to priority.Admission. The
// ranking is heavy analysis, so it respects every ceiling (resource.AdmitAll).
// An unreadable host is never a violation: the guard's own rule, and the same
// choice jobs.Manager makes when its sampler fails.
type priorityAdmission struct{ guard *resource.Guard }

func (a priorityAdmission) Admit(ctx context.Context) error {
	if a.guard == nil {
		// No guard means no admission, the same rule as a nil dependency.
		return nil
	}
	paused, err := a.guard.CheckAdmission(ctx, resource.AdmitAll)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logx.For("priority").Debug("resource guard readings unavailable; building the ranking", "err", err)
		return nil
	}
	if paused != nil {
		return paused
	}
	return nil
}

type hubAPI struct{ w *wire }

func (a hubAPI) Hub(ctx context.Context) (hub.Hub, error) { return a.w.hub.Hub(ctx) }

func (a hubAPI) Answers(ctx context.Context, q hub.AnswerQuery) (hub.AnswerPage, error) {
	return a.w.hub.Answers(ctx, q)
}

func (a hubAPI) LabelSummary(ctx context.Context) (hub.LabelSummary, error) {
	return a.w.hub.LabelSummary(ctx)
}

type statusAPI struct{ w *wire }

func (a statusAPI) Status(ctx context.Context) (api.StatusView, error) {
	meta, err := a.w.app.st.Meta(ctx)
	if err != nil {
		return api.StatusView{}, err
	}
	running := 0
	for _, info := range a.w.app.jobs.List() {
		if info.Status == runstate.Running {
			running++
		}
	}
	var sources []api.SourceInfo
	for _, c := range a.w.app.registry.List() {
		sources = append(sources, api.SourceInfo{ID: c.ID(), Kind: c.Kind()})
	}
	system := oslocale.Detect()
	return api.StatusView{
		DataDir:        a.w.app.cfg.Paths().DataDir,
		StorePath:      store.Path(a.w.app.cfg),
		StoreMeta:      meta,
		SystemLanguage: system.Language,
		SystemLocale:   system.Locale,
		TimeZone:       a.w.app.cfg.String(clock.SettingZone),
		Sources:        sources,
		JobsRunning:    running,
		RestartPending: a.w.app.cfg.RestartPending(),
	}, nil
}

// resourcesAPI projects the guard for the status route. The guard is nil
// only in tests and tools; its State method handles that and reports an
// empty projection instead of panicking.
type learningAPI struct{ w *wire }

func (a learningAPI) Allow(ctx context.Context) error { return a.w.app.learning.Allow(ctx) }

func (a learningAPI) GateState(ctx context.Context) learning.GateState {
	return a.w.app.learning.GateState(ctx)
}

func (a learningAPI) Status(ctx context.Context) (learning.Status, learning.GateState, error) {
	// The real gate answer, never Allow: the test preview must not make
	// /api/learning, the hub or the topbar claim the gate is open. The rule
	// is read once (GateState), and a broken latch or status read still fails
	// the call even while the preview hides the lock from Allow.
	gate := a.w.app.learning.GateState(ctx)
	if gate.Err != nil {
		return learning.Status{}, learning.GateState{}, gate.Err
	}
	status, err := a.w.app.learning.Status(ctx)
	if err != nil {
		return learning.Status{}, learning.GateState{}, err
	}
	return status, gate, nil
}

func (a learningAPI) PreviewAnswers(ctx context.Context) bool {
	return a.w.app.learning.PreviewAnswers(ctx)
}

func (a learningAPI) ConfirmZone(_ context.Context, zone string) error {
	err := learning.ConfirmZone(a.w.app.cfg, zone)
	if err == nil {
		return nil
	}
	if _, zerr := clock.Zone(zone); zerr != nil {
		return &api.APIError{Status: 422, Code: "invalid_zone", Message: "invalid time zone"}
	}
	return err
}

// heldRemovalsAPI exposes the syncer's held-removal confirmation. The list
// is a store read; confirming is the syncer's delete owner, so the API never
// removes a message itself.
type heldRemovalsAPI struct{ w *wire }

// heldAppliesAt is the one deadline rule of a held removal: the earliest held
// moment plus sync.removal.auto_apply_after, in the stored timestamp form.
// The setting is read here because the sync policy that applies it lives in
// the syncer; the projection only renders the same deadline. A zero window
// (confirm-only) returns "".
func heldAppliesAt(cfg *config.Config, since string) string {
	after := cfg.Duration(syncer.SettingRemovalAutoApplyAfter)
	if after <= 0 {
		return ""
	}
	at, ok := mail.Parse(since)
	if !ok {
		return ""
	}
	return at.Add(after).UTC().Format(mail.TimeLayout)
}

func (a heldRemovalsAPI) Held(ctx context.Context, sourceID string) ([]api.HeldFolderView, error) {
	sources := []string{sourceID}
	if sourceID == "" {
		sources = sources[:0]
		for _, c := range a.w.app.registry.List() {
			sources = append(sources, c.ID())
		}
	}
	out := []api.HeldFolderView{}
	for _, id := range sources {
		rows, err := a.w.app.st.HeldRemovals(ctx, id, func(since string) string {
			return heldAppliesAt(a.w.app.cfg, since)
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out = append(out, api.HeldFolderView{SourceID: id, HeldFolder: row})
		}
	}
	return out, nil
}

func (a heldRemovalsAPI) Confirm(ctx context.Context, folderID int64) (store.Removal, error) {
	return a.w.app.syncer.ConfirmRemovals(ctx, folderID)
}

type jobsAPI struct{ w *wire }

func (a jobsAPI) List() []jobs.Info { return a.w.app.jobs.List() }

func (a jobsAPI) Get(id string) (jobs.Info, bool) {
	j, ok := a.w.app.jobs.Get(id)
	if !ok {
		return jobs.Info{}, false
	}
	return j.Info(), true
}

func (a jobsAPI) Cancel(id string) (jobs.Info, bool) {
	j, ok := a.w.app.jobs.Get(id)
	if !ok {
		return jobs.Info{}, false
	}
	j.Cancel()
	return j.Info(), true
}

type mailboxesAPI struct{ w *wire }

func (a mailboxesAPI) List(ctx context.Context) ([]store.MailboxView, error) {
	return a.w.app.syncer.Mailboxes(ctx)
}

func (a mailboxesAPI) Discover(ctx context.Context, sourceID string) ([]store.MailboxView, error) {
	c, err := a.w.connector(sourceID)
	if err != nil {
		return nil, err
	}
	return a.w.app.syncer.Discover(ctx, c)
}

func (a mailboxesAPI) Pick(ctx context.Context, keys []string) ([]store.MailboxView, error) {
	views, err := a.w.app.syncer.Pick(ctx, keys...)
	if err != nil {
		return nil, err
	}
	// The pick is committed; its first sync runs as jobs and its failure
	// must not turn a successful selection into an API error.
	if _, err := a.w.app.syncer.StartPicked(ctx, a.w.app.jobs, a.w.app.registry.List(), views); err != nil {
		logx.For("api").Warn("mailbox picked but its first sync did not start", "err", err)
	}
	return views, nil
}

func (a mailboxesAPI) Unpick(ctx context.Context, keys []string) ([]store.MailboxView, error) {
	return a.w.app.syncer.Unpick(ctx, keys...)
}

func (a mailboxesAPI) Declare(ctx context.Context, key, address string) ([]store.MailboxView, error) {
	return a.w.app.syncer.Declare(ctx, key, address)
}

func (a mailboxesAPI) Undeclare(ctx context.Context, key, address string) ([]store.MailboxView, error) {
	return a.w.app.syncer.Undeclare(ctx, key, address)
}

// requirePicked is the one "pick a mailbox first" guard of the sync and
// backfill starters. The selection snapshot is read here, beside the check,
// so one starter cannot drift to a different selection rule.
func requirePicked(snap *config.Snapshot) error {
	if len(source.Selected(snap)) == 0 {
		return api.ErrNoMailboxPicked()
	}
	return nil
}

// startedJob is the one projection of a started job to its wire snapshot:
// the start error wins, otherwise the job answers its Info.
func startedJob(j *jobs.Job, err error) (jobs.Info, error) {
	if err != nil {
		return jobs.Info{}, err
	}
	return j.Info(), nil
}

type runsAPI struct{ w *wire }

func (a runsAPI) StartSync(ctx context.Context, sourceID string) (jobs.Info, error) {
	c, err := a.w.connector(sourceID)
	if err != nil {
		return jobs.Info{}, err
	}
	if err := requirePicked(a.w.app.cfg.Snapshot()); err != nil {
		return jobs.Info{}, err
	}
	return startedJob(a.w.app.syncer.Start(a.w.app.jobs, c))
}

func (a runsAPI) StartEvidence(ctx context.Context) (jobs.Info, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Info{}, err
	}
	return startedJob(a.w.app.evidence.Start(a.w.app.jobs))
}

func (a runsAPI) StartIndex(ctx context.Context, stores []string) (jobs.Info, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Info{}, err
	}
	picked := source.Selected(a.w.app.cfg.Snapshot())
	for _, key := range stores {
		if !picked[key] {
			return jobs.Info{}, &api.APIError{Status: 422, Code: "not_picked", Message: "a store is not picked"}
		}
	}
	return startedJob(a.w.app.index.Start(a.w.app.jobs, index.Options{Stores: stores}))
}

type triageAPI struct{ w *wire }

func (a triageAPI) Page(ctx context.Context, q triage.PageQuery) (*triage.Page, error) {
	return a.w.app.triage.Page(ctx, q)
}

func (a triageAPI) Summary(ctx context.Context) (*triage.Summary, error) {
	return a.w.app.triage.Summary(ctx)
}

func (a triageAPI) Result(ctx context.Context, rowID int64) (triage.Result, error) {
	return a.w.app.triage.Result(ctx, rowID)
}

func (a triageAPI) Questions(ctx context.Context) (api.QuestionsView, error) {
	if err := ctx.Err(); err != nil {
		return api.QuestionsView{}, err
	}
	profile, err := taxonomy.Load(a.w.app.cfg)
	if err != nil {
		logx.For("api").Error("cannot load the triage profile for the question catalogue", "err", err)
		return api.QuestionsView{}, api.ErrInvalidProfile()
	}
	return api.QuestionsView{Name: profile.Name, Fingerprint: profile.Fingerprint, Questions: profile.Questions}, nil
}

type settingsAPI struct{ w *wire }

// settingsSnapshot is the one read of the settings projection inputs: the
// current values, the restart-pending keys and the learned overrides. The
// collection, the detail and the post-write projections build it the same
// way, so one setting cannot show a different value or layer per route.
type settingsSnapshot struct {
	values  *config.Snapshot
	pending []string
	learned map[string]config.Learned
}

func settingsSnapshotOf(cfg *config.Config) settingsSnapshot {
	learned := make(map[string]config.Learned)
	for _, l := range cfg.Learned() {
		learned[l.Key] = l
	}
	return settingsSnapshot{values: cfg.Snapshot(), pending: cfg.RestartPending(), learned: learned}
}

// view projects one schema setting with the snapshot's values.
func (s settingsSnapshot) view(st *config.Setting) api.SettingView {
	return settingView(st, s.values, slices.Contains(s.pending, st.Key), s.learned)
}

func (a settingsAPI) List() ([]api.SettingView, []string) {
	snap := settingsSnapshotOf(a.w.app.cfg)
	settings := a.w.app.cfg.Schema().Settings()
	out := make([]api.SettingView, 0, len(settings))
	for _, st := range settings {
		out = append(out, snap.view(st))
	}
	return out, snap.pending
}

func (a settingsAPI) Get(key string) (api.SettingView, bool) {
	st, ok := a.w.app.cfg.Schema().Lookup(key)
	if !ok {
		return api.SettingView{}, false
	}
	return settingsSnapshotOf(a.w.app.cfg).view(st), true
}

// requireSetting returns the projection of one setting after a write, or the
// one unknown-setting problem. The read route builds the same view through
// Get, so Set and Reset cannot answer a different shape.
func (a settingsAPI) requireSetting(key string) (api.SettingView, error) {
	view, ok := a.Get(key)
	if !ok {
		return api.SettingView{}, api.ErrUnknownSetting()
	}
	return view, nil
}

func (a settingsAPI) Set(key string, raw json.RawMessage) (api.SettingView, error) {
	if forbiddenSetting(key) {
		return api.SettingView{}, api.ErrUseMailboxEndpoint()
	}
	if key == "sidecar.variant" && a.w.app.sidecar != nil {
		var variant string
		if err := json.Unmarshal(raw, &variant); err != nil {
			return api.SettingView{}, &api.APIError{Status: 400, Code: "invalid_value", Message: "sidecar.variant must be a string"}
		}
		if err := a.w.app.sidecar.ValidateVariant(variant); err != nil {
			return api.SettingView{}, &api.APIError{Status: 400, Code: "invalid_value", Message: err.Error()}
		}
	}
	if err := a.w.app.cfg.Set(key, raw); err != nil {
		return api.SettingView{}, err
	}
	return a.requireSetting(key)
}

func (a settingsAPI) Reset(key string) (api.SettingView, error) {
	if forbiddenSetting(key) {
		return api.SettingView{}, api.ErrUseMailboxEndpoint()
	}
	if err := a.w.app.cfg.Reset(key); err != nil {
		return api.SettingView{}, err
	}
	return a.requireSetting(key)
}

func (a settingsAPI) Reload() ([]string, error) {
	if err := a.w.app.cfg.Reload(); err != nil {
		return nil, err
	}
	return a.w.app.cfg.RestartPending(), nil
}

func (a settingsAPI) Learned() []config.Learned { return a.w.app.cfg.Learned() }

func (a settingsAPI) Reject(key string, raw json.RawMessage) ([]config.Learned, error) {
	if err := a.w.app.cfg.Reject(key, raw); err != nil {
		return nil, err
	}
	return a.w.app.cfg.Learned(), nil
}

func (a settingsAPI) Unreject(key string, raw json.RawMessage) ([]config.Learned, error) {
	if err := a.w.app.cfg.Unreject(key, raw); err != nil {
		return nil, err
	}
	return a.w.app.cfg.Learned(), nil
}

func (a settingsAPI) Secret(key string) bool {
	st, ok := a.w.app.cfg.Schema().Lookup(key)
	return ok && st.Secret
}

func forbiddenSetting(key string) bool {
	return key == source.SettingSelected || key == source.SettingDeclared
}

func settingView(st *config.Setting, snap *config.Snapshot, pending bool, learned map[string]config.Learned) api.SettingView {
	cp := *st
	if cp.Secret && !emptyRaw(cp.Default) {
		cp.Default = json.RawMessage(`"` + config.SecretMask + `"`)
	}
	view := api.SettingView{Setting: &cp, Value: snap.Display(st.Key), Layer: snap.Layer(st.Key), RestartPending: pending}
	if l, ok := learned[st.Key]; ok {
		lv := l
		view.Learned = &lv
	}
	return view
}

func emptyRaw(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) ||
		bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("{}"))
}

type secretsAPI struct{ w *wire }

func (a secretsAPI) List() (api.SecretsView, error) {
	names, err := a.w.app.sec.List()
	if err != nil {
		return api.SecretsView{}, err
	}
	expected, err := expectedSecrets(a.w.app.cfg)
	if err != nil {
		return api.SecretsView{}, err
	}
	return api.SecretsView{Kind: a.w.app.sec.Kind(), ReadOnly: a.w.app.sec.ReadOnly(), Names: names, Expected: expected}, nil
}

func (a secretsAPI) Set(name, value string) error { return a.w.app.sec.Set(name, value) }
func (a secretsAPI) Delete(name string) error     { return a.w.app.sec.Delete(name) }

func expectedSecrets(cfg *config.Config) ([]string, error) {
	cat, err := engine.LoadCatalog(cfg)
	if err != nil {
		return nil, err
	}
	// A non-nil slice keeps the empty answer [] instead of null; sorting
	// first lets the standard compaction drop the adjacent duplicates.
	out := make([]string, 0, len(cat.List()))
	for _, p := range cat.List() {
		if p.APIKeySecret != "" {
			out = append(out, p.APIKeySecret)
		}
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}

type providersAPI struct {
	w *wire
	// saveCatalog is engine.SaveCatalog; nil means the production function.
	// Tests replace it to count the one catalog write of a successful call.
	saveCatalog func(cfg *config.Config, cat *engine.Catalog) error
}

func (a providersAPI) List(ctx context.Context) ([]api.ProviderInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	states, err := a.w.app.engines.Providers(a.w.app.cfg)
	if err != nil {
		return nil, err
	}
	out := make([]api.ProviderInfo, 0, len(states))
	for _, st := range states {
		out = append(out, providerInfo(st.Provider, st.Status))
	}
	return out, nil
}

func (a providersAPI) Roles(ctx context.Context) ([]api.ProviderRoleInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cat, err := engine.LoadCatalog(a.w.app.cfg)
	if err != nil {
		return nil, err
	}
	states, err := a.w.app.engines.Providers(a.w.app.cfg)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]engine.ProviderStatus, len(states))
	for _, st := range states {
		byID[st.Provider.ID] = st.Status
	}
	statuses := engine.RoleStatuses(a.w.app.cfg, cat, byID)
	out := make([]api.ProviderRoleInfo, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, api.ProviderRoleInfo{
			Setting: st.Role.Setting, Label: st.Role.Label, Kind: st.Role.Kind,
			Provider: st.Provider, Configured: st.Configured, Available: st.Available, Detail: st.Detail,
		})
	}
	return out, nil
}

func (a providersAPI) Discover(ctx context.Context, req engine.DiscoverRequest) ([]engine.ProviderModel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return engine.DiscoverModels(ctx, a.w.app.engines.Deps(), req)
}

// providerInUse is the one role-conflict error of the provider write routes.
func providerInUse(roles []string) *api.APIError {
	return &api.APIError{Status: http.StatusConflict, Code: "provider_in_use",
		Message: "the record is selected by " + strings.Join(roles, ", ") + "; change or clear that role first"}
}

// writeProviders is the one read-modify-write of the provider catalog. It
// holds providersMu across load, mutate, build and save; mutate never writes
// the catalog itself, so a failed build or save leaves the stored records
// untouched.
func (a providersAPI) writeProviders(ctx context.Context, mutate func(cat *engine.Catalog) ([]engine.Provider, error)) (*engine.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.w.providersMu.Lock()
	defer a.w.providersMu.Unlock()
	cat, err := engine.LoadCatalog(a.w.app.cfg)
	if err != nil {
		return nil, err
	}
	records, err := mutate(cat)
	if err != nil {
		return nil, err
	}
	next, err := engine.BuildCatalog(a.w.app.cfg, records)
	if err != nil {
		return nil, err
	}
	save := a.saveCatalog
	if save == nil {
		save = engine.SaveCatalog
	}
	if err := save(a.w.app.cfg, next); err != nil {
		return nil, err
	}
	return next, nil
}

func (a providersAPI) Create(ctx context.Context, rec engine.Provider) (api.ProviderInfo, error) {
	next, err := a.writeProviders(ctx, func(cat *engine.Catalog) ([]engine.Provider, error) {
		if rec.ID == "" {
			return nil, api.InvalidProvider("id is required")
		}
		if !engine.ProviderIDValid(rec.ID) {
			return nil, api.InvalidProvider("id must be a lower-case slug")
		}
		if _, exists := cat.Get(rec.ID); exists {
			return nil, &api.APIError{Status: http.StatusConflict, Code: "provider_exists", Message: "a provider record with this id already exists"}
		}
		if rec.APIKeySecret == "" && rec.NeedsKey() {
			rec.APIKeySecret = engine.SuggestSecretName(rec.ID)
		}
		return append(cat.Records(), rec), nil
	})
	if err != nil {
		return api.ProviderInfo{}, err
	}
	return a.info(next, rec.ID)
}

func (a providersAPI) Update(ctx context.Context, id string, rec engine.Provider) (api.ProviderInfo, error) {
	next, err := a.writeProviders(ctx, func(cat *engine.Catalog) ([]engine.Provider, error) {
		if !engine.ProviderIDValid(id) {
			return nil, api.InvalidProviderID(false)
		}
		if rec.ID != "" && rec.ID != id {
			return nil, api.InvalidProvider("the body id does not match the path")
		}
		rec.ID = id
		current, ok := cat.Get(id)
		if !ok {
			return nil, api.UnknownProvider()
		}
		if current.Kind() != rec.Kind() {
			if roles := engine.RolesUsing(a.w.app.cfg, id); len(roles) > 0 {
				return nil, providerInUse(roles)
			}
		}
		records := cat.Records()
		for i := range records {
			if records[i].ID == id {
				records[i] = rec
				break
			}
		}
		return records, nil
	})
	if err != nil {
		return api.ProviderInfo{}, err
	}
	return a.info(next, id)
}

func (a providersAPI) Delete(ctx context.Context, id string) error {
	_, err := a.writeProviders(ctx, func(cat *engine.Catalog) ([]engine.Provider, error) {
		if !engine.ProviderIDValid(id) {
			return nil, api.InvalidProviderID(false)
		}
		if _, ok := cat.Get(id); !ok {
			return nil, api.UnknownProvider()
		}
		if roles := engine.RolesUsing(a.w.app.cfg, id); len(roles) > 0 {
			return nil, providerInUse(roles)
		}
		records := make([]engine.Provider, 0, cat.Len())
		for _, p := range cat.Records() {
			if p.ID != id {
				records = append(records, p)
			}
		}
		return records, nil
	})
	return err
}

func (a providersAPI) Test(ctx context.Context, id string) (engine.ProviderTestResult, error) {
	result, err := a.w.app.engines.TestNamed(ctx, a.w.app.cfg, id)
	if err == nil {
		return result, nil
	}
	if !errors.Is(err, engine.ErrTestFailed) && !errors.Is(err, engine.ErrUnknownProvider) && !errors.Is(err, engine.ErrNoKey) &&
		!errors.Is(err, engine.ErrNotConfigured) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logx.For("api").Error("provider test failed", "provider", id, "err", err)
		return engine.ProviderTestResult{}, api.ProviderTestFailed()
	}
	if errors.Is(err, engine.ErrTestFailed) {
		logx.For("api").Warn("provider test failed", "provider", id, "err", err)
	}
	return engine.ProviderTestResult{}, err
}

func (a providersAPI) info(cat *engine.Catalog, id string) (api.ProviderInfo, error) {
	p, ok := cat.Get(id)
	if !ok {
		return api.ProviderInfo{}, api.UnknownProvider()
	}
	st, err := a.w.app.engines.StatusOf(a.w.app.cfg, id)
	if err != nil {
		return api.ProviderInfo{}, err
	}
	return providerInfo(p, st), nil
}

func providerInfo(p engine.Provider, st engine.ProviderStatus) api.ProviderInfo {
	return api.ProviderInfo{ID: p.ID, Label: p.Label, Protocol: p.Protocol, Kind: p.Kind(), BaseURL: p.BaseURL,
		Path: p.Path, Model: p.Model, APIKeySecret: p.APIKeySecret, Runtime: p.Runtime,
		ContextTokens: p.ContextTokens, CharsPerToken: p.CharsPerToken, ReserveTokensPerByte: p.ReserveTokensPerByte,
		ReserveOutputTokens: p.ReserveOutputTokens, MaxQuestions: p.MaxQuestions,
		ProbabilityTolerance: p.ProbabilityTolerance, ResponseFormat: p.ResponseFormat,
		ReasoningEffort: p.ReasoningEffort, PriceInput: p.PriceInput, PriceOutput: p.PriceOutput,
		Retry: p.Retry, Envelope: p.Envelope, NoulFold: p.NoulFold, Embedding: p.Embedding,
		KeyPresent: st.KeyPresent, Status: st}
}

type sidecarAPI struct{ w *wire }

func (a sidecarAPI) Status() (api.SidecarStatus, error) {
	builds, err := a.w.app.sidecar.Builds()
	if err != nil {
		logx.For("api").Error("sidecar catalog is invalid", "err", err)
		return api.SidecarStatus{}, &api.APIError{Status: 422, Code: "invalid_catalog", Message: "the runtime catalog is invalid"}
	}
	return api.SidecarStatus{Builds: builds, Runtimes: a.w.app.sidecar.Status()}, nil
}

func (a sidecarAPI) Install(ctx context.Context, runtime, model, variant string) (jobs.Info, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Info{}, err
	}
	return startedJob(a.w.app.sidecar.Install(runtime, model, variant))
}

func (a sidecarAPI) Verify(ctx context.Context, runtime, model, variant string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.w.app.sidecar.Verify(runtime, model, variant)
}

type logsAPI struct{ w *wire }

func (a logsAPI) Snapshot(q api.LogQuery) (api.LogSnapshot, error) {
	ring := a.w.app.logs.Ring()
	entries, oldest, last := ring.ReadLimit(q.Since, q.Limit, !q.HasSince)
	return api.LogSnapshot{Entries: api.LogEntryViews(entries), Health: a.w.app.logs.Health(), OldestSeq: oldest, LastSeq: last,
		Truncated: logPageTruncated(q, oldest, last)}, nil
}

// logPageTruncated reports whether the ring holds more than one page after
// the requested cursor. Ring sequence numbers are contiguous by
// construction, so the bounds answer it without reading or allocating the
// entries beyond the page.
func logPageTruncated(q api.LogQuery, oldest, last uint64) bool {
	if last == 0 {
		return false
	}
	base := q.Since
	if !q.HasSince || q.Since < oldest {
		base = oldest - 1
	}
	if last <= base {
		return false
	}
	return last-base > uint64(q.Limit)
}

func (a logsAPI) ReadLimit(seq uint64, limit int, newest bool) ([]logx.Entry, uint64, uint64) {
	return a.w.app.logs.Ring().ReadLimit(seq, limit, newest)
}
func (a logsAPI) Bounds() (uint64, uint64) { return a.w.app.logs.Ring().Bounds() }

type layer1API struct{ w *wire }

func (a layer1API) Start(ctx context.Context) (jobs.Info, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Info{}, err
	}
	return startedJob(a.w.app.layer1.Start(a.w.app.jobs))
}

func (a layer1API) StartJudge(ctx context.Context, o layer1.JudgeOptions) (jobs.Info, error) {
	return startedJob(a.w.app.layer1.StartJudge(ctx, a.w.app.jobs, o))
}

func (a layer1API) LabelQueue(ctx context.Context, q layer1.LabelQuery) (layer1.LabelQueue, error) {
	return a.w.app.layer1.LabelQueue(ctx, q)
}

// Close joins the label-queue rebuild in flight, bounded by ctx.
func (a layer1API) Close(ctx context.Context) bool {
	if a.w == nil || a.w.app == nil || a.w.app.layer1 == nil {
		return true
	}
	return a.w.app.layer1.Close(ctx)
}

func (a layer1API) LabelQueueContains(ctx context.Context, q layer1.LabelQuery, messageID int64) (bool, error) {
	return a.w.app.layer1.QueueContains(ctx, q, messageID)
}

func (a layer1API) PutLabels(ctx context.Context, items []feedback.PutItem) (feedback.PutResult, error) {
	profile, err := taxonomy.Load(a.w.app.cfg)
	if err != nil {
		return feedback.PutResult{}, api.ErrInvalidProfile()
	}
	return a.w.app.labels.PutBatch(ctx, profile, items)
}

type messagesAPI struct{ w *wire }

// messagesService is the one first-page cache shared by the startup warm and
// the first Mail request.
func (w *wire) messagesService() *messages.Service {
	w.messagesOnce.Do(func() {
		w.messagesSvc = messages.New(w.app.st)
	})
	return w.messagesSvc
}

func (a messagesAPI) List(ctx context.Context, q store.MessageListQuery) (api.MessageListView, error) {
	page, err := a.w.messagesService().List(ctx, q)
	if err != nil {
		return api.MessageListView{}, err
	}
	view := api.MessageListView{
		Messages: make([]api.MessageListRow, 0, len(page.Messages)),
		Total:    page.Total, NextBefore: page.NextBefore, HasMore: page.HasMore,
	}
	if page.Counts != nil {
		view.Counts = make(map[string]int, len(page.Counts))
		for role, n := range page.Counts {
			view.Counts[string(role)] = n
		}
	}
	for _, row := range page.Messages {
		view.Messages = append(view.Messages, api.MessageListRow{
			RowID: row.RowID, Subject: row.Subject, SenderName: row.SenderName,
			SenderAddress: row.SenderAddress,
			Instant:       row.Instant, ReceivedAt: row.ReceivedAt, SentOn: row.SentOn,
			Role: row.Role, IsRead: row.IsRead, HasAttachments: row.HasAttachments, Snippet: row.Snippet,
		})
	}
	return view, nil
}

func (a messagesAPI) Attachment(ctx context.Context, rowID int64, position int) (api.AttachmentAsset, error) {
	asset, err := a.w.app.st.AttachmentData(ctx, rowID, position)
	if err != nil {
		return api.AttachmentAsset{}, err
	}
	return api.AttachmentAsset{Position: asset.Position, Name: asset.Name, ContentType: asset.ContentType,
		Size: asset.Size, Inline: asset.Inline, SHA256: asset.SHA256, Bytes: asset.Bytes}, nil
}

func (a messagesAPI) Get(ctx context.Context, rowID int64) (api.MessageView, error) {
	var out *api.MessageView
	err := a.w.app.st.EachMessage(ctx, store.MessageFilter{RowIDs: []int64{rowID}, OwnerContent: true, WithBodyHTML: true}, func(r *store.MessageRow) error {
		m := r.Message
		body := render.Body(render.Input{HTML: m.BodyHTML, HTMLKnown: m.BodyHTMLKnown,
			HTMLTruncated: m.BodyHTMLTruncated, Plain: m.Body, Attachments: m.Attachments})
		out = &api.MessageView{RowID: r.RowID, Revision: r.Revision, StoreKind: r.StoreKind, Verdict: r.Verdict, Reason: r.Reason,
			Message: api.WireMessage(&m), Sender: api.SenderOf(&m), BodyFormat: body.Format, BodyHTML: body.HTML, BodyCSS: body.CSS,
			BodyHTMLTruncated: body.Truncated,
			BodyRemoteImages:  body.RemoteImages, BodyBlockedImages: body.BlockedImages,
			BodyInlineImages: body.InlineImages, BodyMissingInline: body.MissingInline}
		return nil
	})
	if err != nil {
		return api.MessageView{}, err
	}
	if out == nil {
		return api.MessageView{}, store.ErrNotFound
	}
	return *out, nil
}

// StartBackfill starts the one-time display-HTML pass for a source. The
// pass runs as an ordinary jobs.Manager job, so the API answers at once and
// progress and cancellation use the existing /api/jobs endpoints.
func (a messagesAPI) StartBackfill(ctx context.Context, sourceID string) (jobs.Info, error) {
	if err := ctx.Err(); err != nil {
		return jobs.Info{}, err
	}
	c, err := a.w.connector(sourceID)
	if err != nil {
		return jobs.Info{}, err
	}
	if err := requirePicked(a.w.app.cfg.Snapshot()); err != nil {
		return jobs.Info{}, err
	}
	n, err := a.w.app.st.CountBackfill(ctx, c.ID())
	if err != nil {
		return jobs.Info{}, err
	}
	if n == 0 {
		return jobs.Info{}, &api.APIError{Status: 422, Code: "nothing_to_backfill",
			Message: "every stored message already has its display state and inline images"}
	}
	return startedJob(a.w.app.syncer.StartBackfill(a.w.app.jobs, c))
}

type threadAPI struct{ w *wire }

func (a threadAPI) Get(ctx context.Context, conversationID string, limit int, beforeID, atID int64) (api.ThreadView, error) {
	var res store.ThreadMessagesResult
	var err error
	if atID > 0 {
		res, err = a.w.app.st.ThreadMessagesAt(ctx, conversationID, limit, atID)
	} else {
		res, err = a.w.app.st.ThreadMessages(ctx, conversationID, limit, beforeID)
	}
	if err != nil {
		return api.ThreadView{}, err
	}
	state, err := a.threadState(ctx, conversationID)
	if err != nil {
		return api.ThreadView{}, err
	}
	view := api.ThreadView{
		ConversationID: res.ConversationID,
		Subject:        res.Subject,
		State:          api.ThreadStateViewOf(state),
		Messages:       make([]api.ThreadMessageView, 0, len(res.Messages)),
		Participants:   make([]api.ThreadParticipantView, 0, len(res.Participants)),
		FirstAt:        res.FirstAt, LastAt: res.LastAt, Count: res.Count, Truncated: res.Truncated,
		NewestMessageID: res.NewestMessageID, Newer: res.Newer,
	}
	for _, m := range res.Messages {
		view.Messages = append(view.Messages, api.ThreadMessageView{
			ID: m.ID, Subject: m.Subject, SenderName: m.SenderName, SenderAddress: m.SenderAddress,
			ReceivedAt: m.ReceivedAt, Sent: m.Sent, IsRead: m.IsRead, HasAttachments: m.HasAttachments,
			Snippet: m.Snippet,
		})
	}
	for _, p := range res.Participants {
		view.Participants = append(view.Participants, api.ThreadParticipantViewOf(p))
	}
	return view, nil
}

// threadState prefers the priority ranking's cached resolution: that is the
// exact state priority and agenda display, including the message-level
// reply signal. A conversation outside the ranking (or an app wired without
// priority, as a focused test may be) falls back to the store's one
// resolver, so the API never re-implements thread rules.
func (a threadAPI) threadState(ctx context.Context, conversationID string) (threadintel.Assessment, error) {
	if a.w.app.priority != nil {
		states, err := a.w.app.priority.ThreadStates(ctx)
		if err != nil {
			return threadintel.Assessment{}, err
		}
		if state, ok := states[conversationID]; ok {
			return state, nil
		}
	}
	states, err := a.w.app.st.ThreadStates(ctx, []string{conversationID}, nil)
	if err != nil {
		return threadintel.Assessment{}, err
	}
	state, ok := states[conversationID]
	if !ok {
		return threadintel.Assessment{}, fmt.Errorf("thread %q has messages but no resolved state", conversationID)
	}
	return state, nil
}

type conversationsAPI struct{ w *wire }

func (a conversationsAPI) Page(ctx context.Context, q conversations.Query) (conversations.Page, error) {
	svc, err := a.w.conversationsService()
	if err != nil {
		return conversations.Page{}, err
	}
	return svc.Page(ctx, q)
}

// previewGate is the app's one learning machine as the shared preview rule.
// It is nil only in a partially wired app, where preview is off.
func (w *wire) previewGate() learning.Previewer {
	if w.app.learning == nil {
		return nil
	}
	return w.app.learning
}

func (w *wire) conversationsService() (*conversations.Service, error) {
	w.conversationsOnce.Do(func() {
		var priority conversations.Priority
		if w.app.priority != nil {
			priority = w.app.priority
		}
		w.conversationsSvc, w.conversationsErr = conversations.New(conversations.Deps{
			Store:    w.app.st,
			Priority: priority,
			Gate:     w.previewGate(),
			Clock:    clock.Real(),
		})
	})
	return w.conversationsSvc, w.conversationsErr
}

type commitmentsAPI struct{ w *wire }

func (a commitmentsAPI) List(ctx context.Context, f commitments.Filter) (commitments.Result, error) {
	svc, err := a.w.commitmentsService()
	if err != nil {
		return commitments.Result{}, err
	}
	return svc.List(ctx, f)
}

// commitmentsService builds the read model once from the app's store and
// priority service. Priority is built by wireEngines before the API is
// wired, so production always has it; an app wired without it gets a clear
// endpoint error rather than a set that silently omits replies and
// decisions.
func (w *wire) commitmentsService() (*commitments.Service, error) {
	w.commitmentsOnce.Do(func() {
		if w.app.priority == nil {
			w.commitmentsErr = errors.New("commitments: the priority service is not wired")
			return
		}
		w.commitmentsSvc = commitments.New(commitments.Deps{
			Store:     w.app.st,
			Threads:   w.app.priority,
			Decisions: w.app.priority,
			Actions:   w.app.priority,
			Clock:     clock.Real(),
		})
	})
	return w.commitmentsSvc, w.commitmentsErr
}

type briefAPI struct{ w *wire }

func (a briefAPI) Brief(ctx context.Context, since string) (brief.Result, error) {
	svc, err := a.w.briefService()
	if err != nil {
		return brief.Result{}, err
	}
	return svc.Brief(ctx, since)
}

// briefService builds the Brief once from the existing conversation and
// commitment services. Reusing those instances is deliberate: the Brief must
// read the same digest snapshot, thread truth and cache generation the
// dedicated screens serve, not parallel copies. Priority is the app's one
// ranking; a nil ranking leaves Topics empty instead of inventing a census.
func (w *wire) briefService() (*brief.Service, error) {
	w.briefOnce.Do(func() {
		conversationFacts, err := w.conversationsService()
		if err != nil {
			w.briefErr = err
			return
		}
		openLoops, err := w.commitmentsService()
		if err != nil {
			w.briefErr = err
			return
		}
		var ranked brief.Priority
		if w.app.priority != nil {
			ranked = w.app.priority
		}
		w.briefSvc, w.briefErr = brief.New(brief.Deps{
			Commitments:   openLoops,
			Conversations: conversationFacts,
			Priority:      ranked,
			Store:         w.app.st,
			Gate:          w.previewGate(),
			Clock:         clock.Real(),
		})
	})
	return w.briefSvc, w.briefErr
}

func (w *wire) connector(sourceID string) (source.Connector, error) {
	list := w.app.registry.List()
	if len(list) == 0 {
		return nil, &api.APIError{Status: 503, Code: "no_source", Message: "no mail source is configured"}
	}
	if sourceID == "" {
		if len(list) > 1 {
			return nil, &api.APIError{Status: 422, Code: "source_required", Message: "source is required"}
		}
		return list[0], nil
	}
	c, ok := w.app.registry.Get(sourceID)
	if !ok {
		return nil, &api.APIError{Status: 422, Code: "unknown_source", Message: "unknown mail source"}
	}
	return c, nil
}

// searchAPI is the shutdown join of the hybrid searcher. The API uses the
// searcher directly for queries; this wrapper exists only so the read-model
// closer list can join its detached semantic worker without naming a nil
// pointer.
type searchAPI struct{ w *wire }
