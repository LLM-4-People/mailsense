package main

// The startup warm: the policy that admits, bounds and reports every derived
// view built before ready. It reads the API services only, so it lives beside
// the wire rather than inside it.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/api"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// warmTimeout bounds the derived-view warm and the semantic prepare. It is a
// startup safety bound: ready is not announced until both return or this
// expires, while the API and the window are already usable.
const warmTimeout = 90 * time.Second

// warmProfiles is the number of ranked company profiles warmed at startup:
// the Companies screen prefetches the three visible profiles
// (COMPANY_PREFETCH_BATCH in internal/ui/js/00-core.js), so a fourth and
// fifth warm are never read by the first screen. The list itself is warm;
// any other profile builds on demand. The UI script and this warm cannot
// import one another, so the contract is stated here and the two values are
// changed together.
const warmProfiles = 3

// warmHeavyBuilds bounds how many store-scanning warm stages build at once.
// The bound is measured, not guessed: starting the four derived scans beside
// the ranking build made ready 15.7 s instead of 13.6 s on the live host's
// 4-connection / 8 MiB SQLite cache (docs/PROGRESS.md, 2026-11-03). The three
// slots leave one pool connection for the owner's first click while the warm
// runs, and they are one short of the four-scan group that measured slower
// beside the ranking. Cheap stages compose cached read models and do not take
// a slot.
const warmHeavyBuilds = 3

// warmScans reports whether a stage runs its own store work. The three cheap
// stages read what the warm has already built: Agenda and Commitments read the
// ranking's cached thread truth, and the Brief composes both (0.00-0.03 s
// each on the live host). Every other stage scans the store and is counted
// against warmHeavyBuilds.
func warmScans(view warmView) bool {
	switch view {
	case viewAgenda, viewCommitments, viewBrief:
		return false
	default:
		return true
	}
}

// warmGuard is the resource guard as the warm reads it: one admission call
// before a stage builds. The production value wraps the app's guard with the
// heavy-job policy (resource.AdmitAll); a test scripts the host so the policy
// can be proved at an exact point of the warm instead of by timing a sampler.
type warmGuard interface {
	Admission(ctx context.Context, stage warmView) (*resource.LimitError, error)
}

// guardAdmission is the one production warm admission: every warm stage is
// heavy analysis and respects every ceiling, exactly like a formatting job.
type guardAdmission struct{ guard *resource.Guard }

func (a guardAdmission) Admission(ctx context.Context, _ warmView) (*resource.LimitError, error) {
	if a.guard == nil {
		return nil, nil
	}
	return a.guard.CheckAdmission(ctx, resource.AdmitAll)
}

// warmSlots is a FIFO counting semaphore: warmHeavyBuilds permits, granted in
// reservation order. A plain buffered channel would let the Go scheduler
// reorder waiters, and the warm needs its launch order to decide who builds
// first (the ranking is the base the other views read).
type warmSlots struct {
	mu    sync.Mutex
	free  int
	queue []chan struct{}
}

func newWarmSlots(n int) *warmSlots {
	return &warmSlots{free: n}
}

// reserve claims the next permit in arrival order without blocking. The
// returned ticket is closed once the permit is granted, immediately for the
// first warmHeavyBuilds reservations. Reserving in the launcher, not in the
// stage goroutine, is what makes the order the launch order.
func (s *warmSlots) reserve() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := make(chan struct{})
	if s.free > 0 {
		s.free--
		close(ready)
		return ready
	}
	s.queue = append(s.queue, ready)
	return ready
}

// wait waits for the ticket's permit. It reports false when ctx ends first,
// and withdraws the ticket so a cancelled stage never swallows a permit that
// was handed to it after it gave up.
func (s *warmSlots) wait(ctx context.Context, ready <-chan struct{}) bool {
	select {
	case <-ready:
		return true
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, waiter := range s.queue {
		if waiter == ready {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return false
		}
	}
	// The permit was handed over as ctx ended: pass it to the queue.
	s.releaseLocked()
	return false
}

func (s *warmSlots) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked()
}

func (s *warmSlots) releaseLocked() {
	if len(s.queue) > 0 {
		close(s.queue[0])
		s.queue = s.queue[1:]
		return
	}
	s.free++
}

// warmPolicy is the warm's one admission and concurrency owner. Every stage
// asks it first: a refusal is recorded skipped, never failed and never ready,
// and the route the stage would have warmed builds it on first use. A skipped
// stage is not retried by this warm; the normal route owns the retry.
type warmPolicy struct {
	guard  warmGuard
	boot   *bootState
	log    *slog.Logger
	slots  *warmSlots
	origin time.Time
}

func newWarmPolicy(guard warmGuard, boot *bootState, origin time.Time) *warmPolicy {
	return &warmPolicy{
		guard:  guard,
		boot:   boot,
		log:    logx.For("startup"),
		slots:  newWarmSlots(warmHeavyBuilds),
		origin: origin,
	}
}

// launch runs one stage in its own goroutine. A store-scanning stage reserves a
// heavy permit here, in launch order, and waits for it inside the goroutine, so
// the order is the warm's order while the launcher never blocks.
func (p *warmPolicy) launch(ctx context.Context, wg *sync.WaitGroup, view warmView, fn func(context.Context) error) {
	wg.Add(1)
	var ticket <-chan struct{}
	if warmScans(view) {
		ticket = p.slots.reserve()
	}
	go func() {
		defer wg.Done()
		if ticket != nil {
			if !p.slots.wait(ctx, ticket) {
				p.boot.finish(view, ctx.Err())
				return
			}
			defer p.slots.release()
		}
		p.build(ctx, view, fn)
	}()
}

// build admits and runs one stage inside its already reserved slot. A refused
// stage is recorded skipped, never started and never timed.
func (p *warmPolicy) build(ctx context.Context, view warmView, fn func(context.Context) error) {
	if err := ctx.Err(); err != nil {
		// The warm was cancelled before this stage could start; record it
		// rather than leaving it pending for ready to convert later.
		p.boot.finish(view, err)
		return
	}
	if reason := p.admit(ctx, view); reason != nil {
		p.boot.skip(view, logx.Redact(reason.Error()))
		return
	}
	viewStart := time.Now()
	p.boot.start(view)
	err := fn(ctx)
	switch {
	case err == nil:
		p.log.Info("startup view warm", "view", string(view),
			"duration_ms", time.Since(viewStart).Milliseconds(),
			"offset_ms", viewStart.Sub(p.origin).Milliseconds())
		p.boot.finish(view, nil)
	case p.refused(err):
		// The service's own late-bound admission refused mid-build (priority
		// keeps its guard for cold API reads). That is a refusal, not a
		// failure: record it skipped and let the route retry on first use.
		p.boot.skip(view, logx.Redact(err.Error()))
	default:
		p.log.Info("startup view warm", "view", string(view),
			"duration_ms", time.Since(viewStart).Milliseconds(),
			"offset_ms", viewStart.Sub(p.origin).Milliseconds())
		p.log.Warn("startup warm incomplete", "view", string(view), "err", err)
		p.boot.finish(view, err)
	}
}

// refused reports whether a build ended because a resource admission refused
// it, as opposed to failing. A timeout is a failure of that view's warm, not a
// refusal: it names no ceiling the owner could lower.
func (p *warmPolicy) refused(err error) bool {
	var limited *resource.LimitError
	return errors.As(err, &limited)
}

// admit asks the guard once. A sampler error admits the stage: an unreadable
// host is never a violation, the same rule jobs.Manager applies.
func (p *warmPolicy) admit(ctx context.Context, view warmView) *resource.LimitError {
	if p.guard == nil {
		return nil
	}
	paused, err := p.guard.Admission(ctx, view)
	if err != nil {
		p.log.Debug("resource guard readings unavailable; building the startup view",
			"view", string(view), "err", err)
		return nil
	}
	if paused == nil {
		return nil
	}
	p.log.Info("startup view skipped by the resource guard", "view", string(view),
		"resource", string(paused.Kind), "percent", paused.Percent, "limit", paused.Limit)
	return paused
}

// warmDerived is the old blocking wrapper used by tests and by any caller
// that has no boot state and no guard. Production starts it in the background
// through startWarm.
func warmDerived(ctx context.Context, services api.Services) {
	warmDerivedTracked(ctx, services, nil, nil)
}

// warmDerivedTracked warms only the derived views. It is the seam the warm
// tests use; production goes through warmStartup, which shares this policy's
// heavy bound with the semantic prepare.
func warmDerivedTracked(ctx context.Context, services api.Services, boot *bootState, guard warmGuard) {
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	warmStartup(ctx, services, nil, boot, guard)
}

// warmStartup is the one owner of every DB-heavy start-up build: the derived
// views and the semantic prepare share one warmPolicy, so one bound governs
// all of them. prepare may be nil (the derived-only seam).
func warmStartup(ctx context.Context, services api.Services, prepare func(context.Context) error,
	boot *bootState, guard warmGuard) {
	log := logx.For("startup")
	started := time.Now()
	warm := newWarmPolicy(guard, boot, started)

	// Loading the vector set has no dependency on a derived view: start it
	// first, in its own heavy slot, and let it run beside them.
	var prep sync.WaitGroup
	if prepare != nil {
		warm.launch(ctx, &prep, viewSemanticSearch, prepare)
	}

	warm.derived(ctx, services)
	prep.Wait()
	log.Info("derived views warm", "duration_ms", time.Since(started).Milliseconds())
}

// derived builds the derived read models with this policy's admission and
// heavy bound. The caller owns the context; every launch is joined before it
// returns, so a stage that outlives its siblings is never reported by ready.
// The stage dependencies are the warm's own: the ranking is the base every
// derived view reads, and the Brief composes the four that precede it.
func (p *warmPolicy) derived(ctx context.Context, services api.Services) {
	// Priority ranks the mailbox and is the base every derived view joins.
	// Starting the four beside it was measured on the live host and made
	// ready slower (15.7 s vs 13.6 s): their scans contend with the ranking
	// build on the 4-connection / 8 MiB SQLite cache. Its heavy slot is
	// reserved first, so the base is never queued behind a screen that waits
	// for it, and it still overlaps the independent early screens. The
	// ranking keeps its own late-bound admission in the priority service for
	// cold API reads; this stage check is the warm's one decision for that
	// same build, so a refused ranking is reported skipped instead of
	// surfacing as a warm failure.
	var rank sync.WaitGroup
	p.launch(ctx, &rank, viewPriority, func(ctx context.Context) error {
		_, _, _, err := services.Priority.Rank(ctx, priority.FilterAll, 1)
		return err
	})

	// Mail is the first-click screen, but it is independent of the ranking:
	// start its first page beside learning and the other independent screens.
	// The click shares wire.messagesOnce, so a click that wins joins this
	// same build. Triage (summary and page) and Home read mail and route
	// rows, never the ranking either, so a sync-invalidated priority build
	// cannot delay three screens that do not wait for it.
	var early sync.WaitGroup
	p.launch(ctx, &early, viewMail, func(ctx context.Context) error {
		_, err := services.Messages.List(ctx, store.MessageListQuery{
			Limit:  store.MessageListDefaultLimit,
			Counts: true,
		})
		return err
	})

	// The learning status has no dependency on the ranking: start its first
	// build beside priority so the learning screen is ready without paying
	// for it serially. The machine serializes first builds itself, so a
	// priority gate that needs the status waits on this same build.
	p.launch(ctx, &early, viewLearning, func(ctx context.Context) error {
		_, _, err := services.Learning.Status(ctx)
		return err
	})
	p.launch(ctx, &early, viewTriageSummary, func(ctx context.Context) error {
		_, err := services.Triage.Summary(ctx)
		return err
	})
	p.launch(ctx, &early, viewTriagePage, func(ctx context.Context) error {
		_, err := services.Triage.Page(ctx, triage.PageQuery{Limit: triage.TriagePageDefault})
		return err
	})
	p.launch(ctx, &early, viewHome, func(ctx context.Context) error {
		_, err := services.Hub.Hub(ctx)
		return err
	})

	// Join the ranking build before launching the four dependent views: the
	// live host measured them starting beside it as slower overall (15.7 s vs
	// 13.6 s) on the one connection pool. A skipped ranking returns at once,
	// so they still build on their own and are never handed a nil cache.
	rank.Wait()
	var wg sync.WaitGroup
	p.launch(ctx, &wg, viewAgenda, func(ctx context.Context) error {
		// Only the configured default horizon is warmed. The Hub's 8 h
		// and 24 h chips build and cache their window on click, so
		// warming them here paid for windows nobody opened.
		_, err := services.Agenda.Build(ctx, services.Agenda.DefaultHorizon())
		return err
	})
	p.launch(ctx, &wg, viewConversations, func(ctx context.Context) error {
		// Warm the default page and every state chip the Mail tab can
		// open. The page cache is the reason a later click is instant;
		// a narrower limit is sliced from the same digest snapshot but
		// still needs its own enriched page.
		if _, err := services.Conversations.Page(ctx, conversations.Query{Limit: conversations.DefaultLimit}); err != nil {
			return err
		}
		for _, state := range threadintel.States {
			if _, err := services.Conversations.Page(ctx, conversations.Query{
				Limit: conversations.DefaultLimit,
				State: state,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	p.launch(ctx, &wg, viewCommitments, func(ctx context.Context) error {
		_, err := services.Commitments.List(ctx, commitments.Filter{Limit: 1})
		return err
	})
	p.launch(ctx, &wg, viewCompanies, func(ctx context.Context) error {
		// Warm the widest list: the Companies screen, Ask's entity
		// linking and narrower page requests all slice this one cache.
		list, err := services.Companies.List(ctx, worldsvc.ListQuery{Limit: worldsvc.MaxCompanyLimit})
		if err != nil {
			return err
		}
		keys := make([]string, 0, warmProfiles)
		for i, org := range list.Companies {
			if i >= warmProfiles {
				break
			}
			keys = append(keys, org.Key)
		}
		// One store batch reads the mailbox once for every warmed
		// profile; the per-key path would scan it once per company.
		views, err := services.Companies.Profiles(ctx, keys,
			worldsvc.DefaultCompanyMonths, worldsvc.DefaultCompanyEventDays)
		if err != nil {
			return err
		}
		if len(views) != len(keys) {
			return fmt.Errorf("companies warm: %d of %d profiles", len(views), len(keys))
		}
		return nil
	})
	wg.Wait()

	// Brief composes the ranking and the four read models that just warmed;
	// it is last so it never races their first build. It reads their caches,
	// so it composes in the caller and takes no heavy slot.
	p.build(ctx, viewBrief, func(ctx context.Context) error {
		_, err := services.Brief.Brief(ctx, "")
		return err
	})

	// The early screens are not Brief inputs: it composes the ranking and the
	// four read models. Ready still covers them: join them before the warm
	// returns, so a view that outlives its siblings is never reported failed
	// by ready.
	early.Wait()
}
