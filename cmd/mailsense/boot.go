package main

import (
	"context"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// warmView is one start-up stage. The identifiers are the wire contract:
// they are the boot view names, the warm log labels and the UI's
// boot.view.<name> keys.
type warmView string

const (
	viewMail           warmView = "mail"
	viewLearning       warmView = "learning"
	viewPriority       warmView = "priority"
	viewAgenda         warmView = "agenda"
	viewTriageSummary  warmView = "triage_summary"
	viewTriagePage     warmView = "triage_page"
	viewHome           warmView = "home"
	viewConversations  warmView = "conversations"
	viewCommitments    warmView = "commitments"
	viewCompanies      warmView = "companies"
	viewBrief          warmView = "brief"
	viewSemanticSearch warmView = "semantic_search"
)

// warmViewOrder is the one list of start-up stages. The boot snapshot, the
// warm calls and the UI labels all derive from it; a new view is added here
// once, not in four parallel string lists.
var warmViewOrder = []warmView{
	viewMail,
	viewLearning,
	viewPriority,
	viewAgenda,
	viewTriageSummary,
	viewTriagePage,
	viewHome,
	viewConversations,
	viewCommitments,
	viewCompanies,
	viewBrief,
	viewSemanticSearch,
}

const (
	bootWarming = "warming"
	bootReady   = "ready"

	stagePending = "pending"
	stageRunning = "running"
	stageDone    = "done"
	stageFailed  = "failed"
	// stageSkipped is a stage the resource guard refused before it built.
	// It is neither done nor failed: the route it would have warmed builds
	// the view on first use, and reason names the ceiling that refused it.
	stageSkipped = "skipped"
)

type bootStage struct {
	state    string
	started  time.Time
	duration time.Duration
	reason   string
}

// bootState is the thread-safe start-up state. The learning status and the
// priority ranking start concurrently, so every method takes the mutex.
type bootState struct {
	mu      sync.Mutex
	started time.Time
	state   string
	views   map[warmView]*bootStage
	current warmView
}

func newBootState() *bootState {
	views := make(map[warmView]*bootStage, len(warmViewOrder))
	for _, view := range warmViewOrder {
		views[view] = &bootStage{state: stagePending}
	}
	return &bootState{started: time.Now(), state: bootWarming, views: views}
}

// skip records a stage the warm did not build because the resource guard
// refused it. It is final for this warm: the route's first use retries, this
// warm does not, and nothing that never built may report a duration.
func (b *bootState) skip(view warmView, reason string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stage := b.views[view]
	if stage == nil {
		stage = &bootStage{}
		b.views[view] = stage
	}
	stage.state = stageSkipped
	stage.reason = reason
	if b.current == view {
		b.current = ""
		for _, candidate := range warmViewOrder {
			if other := b.views[candidate]; other != nil && other.state == stageRunning {
				b.current = candidate
				break
			}
		}
	}
}

func (b *bootState) start(view warmView) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stage := b.views[view]
	if stage == nil {
		stage = &bootStage{}
		b.views[view] = stage
	}
	stage.state = stageRunning
	stage.started = time.Now()
	b.current = view
}

func (b *bootState) finish(view warmView, err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stage := b.views[view]
	if stage == nil {
		stage = &bootStage{}
		b.views[view] = stage
	}
	stage.state = stageDone
	if err != nil {
		stage.state = stageFailed
	}
	if !stage.started.IsZero() {
		stage.duration = time.Since(stage.started)
	}
	if b.current == view {
		b.current = ""
		// Learning and priority overlap: keep naming a view that is still
		// running instead of leaving the strip blank mid-warm.
		for _, candidate := range warmViewOrder {
			if stage := b.views[candidate]; stage != nil && stage.state == stageRunning {
				b.current = candidate
				break
			}
		}
	}
}

// ready closes the boot contract. Every view that never finished is failed:
// readiness is not a claim that the warm succeeded, and a silent pending
// stage would leave the strip waiting forever. A skipped stage keeps its own
// state and reason: it never ran, so it is never reported ready or failed.
func (b *bootState) ready() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, view := range warmViewOrder {
		stage := b.views[view]
		if stage == nil {
			stage = &bootStage{}
			b.views[view] = stage
		}
		if stage.state == stagePending || stage.state == stageRunning {
			stage.state = stageFailed
		}
	}
	b.current = ""
	b.state = bootReady
}

func (b *bootState) snapshot() api.BootView {
	b.mu.Lock()
	defer b.mu.Unlock()
	views := make([]api.BootStageView, 0, len(warmViewOrder))
	done, failed, skipped := 0, 0, 0
	for _, view := range warmViewOrder {
		stage := b.views[view]
		if stage == nil {
			stage = &bootStage{state: stagePending}
		}
		switch stage.state {
		case stageDone:
			done++
		case stageFailed:
			failed++
		case stageSkipped:
			skipped++
		}
		views = append(views, api.BootStageView{
			Name:       string(view),
			State:      stage.state,
			DurationMS: stage.duration.Milliseconds(),
			// The refused ceiling is the stage's own error string, so the
			// boot projection names it without a second field or a second
			// wording of the same fact.
			Error: stage.reason,
		})
	}
	return api.BootView{
		State:     b.state,
		StartedAt: b.started,
		ElapsedMS: time.Since(b.started).Milliseconds(),
		Done:      done,
		Total:     len(warmViewOrder),
		Failed:    failed,
		Skipped:   skipped,
		Current:   string(b.current),
		Views:     views,
	}
}

// bootAPI adapts the start-up state to the API's read-only contract.
type bootAPI struct{ state *bootState }

func (b bootAPI) Boot(context.Context) (api.BootView, error) {
	return b.state.snapshot(), nil
}

// startWarm runs the derived warm and the semantic prepare in the
// background under one admission policy: one warmHeavyBuilds bound covers both.
// The listener and the desktop window are already open; ready is announced
// only when this returns. The caller owns the context and joins the returned
// channel before the store closes.
func startWarm(ctx context.Context, a *app, services api.Services, boot *bootState, url string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		log := logx.For("startup")
		ctx, cancel := context.WithTimeout(ctx, warmTimeout)
		defer cancel()
		warmStartup(ctx, services, a.search.Prepare, boot, guardAdmission{guard: a.guard})
		if ctx.Err() != nil {
			return
		}
		boot.ready()
		log.Info("mailsense ready", "db", store.Path(a.cfg), "url", url)
	}()
	return done
}

// shutdownJoinTimeout bounds shutdown's wait for one background task: the
// warm and every worker started by startWhenReady share it.
const shutdownJoinTimeout = 15 * time.Second

// joinWarm waits a bounded time for the background warm. It reports whether
// the warm joined: a miss is a warning, not a hang, but the store owner must
// keep the store open because the warm may still read it.
func joinWarm(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(shutdownJoinTimeout):
		logx.For("startup").Warn("the startup warm did not finish before shutdown")
		return false
	}
}
