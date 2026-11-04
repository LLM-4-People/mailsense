// Package index is the semantic index: it embeds the mail of the enabled
// stores and folder roles (index.*), newest first so semantic search
// works progressively, with the cleaned subject and body (quotes and
// signatures removed, mail.CleanBody). It is incremental: a message is
// looked at again only when it was written since (messages.revision) or
// the cleaning rules changed (each vector carries the version of its
// text), and embedded again only when the exact input changed (its hash). Vectors
// are stored int8-quantised per vector space. A message that keeps
// failing is quarantined until it changes. A failed or cancelled run
// never removes a vector; coverage is always disclosed, per store and
// folder role too.
package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// ErrBusy is returned while another index run is in progress.
var ErrBusy = errors.New("index: a run is already in progress")

// ErrStoreBusy marks a batch whose vectors could not be stored because
// another writer held the database past every configured attempt. It is the
// store policy's final error: the indexer owns no retry loop and no second
// busy vocabulary. The batch's messages are not failures: the next run
// embeds and stores them.
var ErrStoreBusy = store.ErrStoreBusy

// EmbedderSource builds the configured embedder (embed.Source).
type EmbedderSource interface {
	Embedder() (engine.Embedder, error)
}

// Indexer runs index jobs over one store.
type Indexer struct {
	cfg    *config.Config
	st     *store.Store
	bus    *events.Bus
	src    EmbedderSource
	clock  clock.Clock
	log    *slog.Logger
	single jobs.Single // one run at a time; a run wanted meanwhile follows it
	// planGate is the planning slot: one planner at a time, so concurrent
	// Ensure calls coalesce on one state read instead of each scanning the
	// mailbox. It is a buffered channel rather than a mutex so a waiter
	// whose context ends leaves without scanning. It is never held across a
	// run: the run slot (single) is separate and long-lived.
	planGate chan struct{}
	// textVersion is the version of the text vectors are computed from:
	// the digest of the cleaning data (mail.DataDigest), a value of a
	// test's choosing in tests.
	textVersion func() string
	// live is the current run's progress, written only by the run
	// goroutine and read by Stage.Status. It answers a status call
	// without a coverage plan while the run is active.
	liveMu sync.Mutex
	live   liveRun
	// planCache is the last plan (coverage and, when collected, work list)
	// and the exact inputs it was computed from. Status and Coverage read
	// the coverage repeatedly while nothing changed; Run and Ensure reuse
	// the work list instead of scanning. One entry only: a plan is a
	// mailbox-wide read, and keeping its history would grow with every
	// write. planMu guards the pointer, never a database read.
	planMu    sync.Mutex
	planCache *planEntry
	// planScans counts the coverage state reads this process made. It is the
	// measurement point of the plan cache: one read per changed mailbox and
	// none while its inputs are unchanged.
	planScans atomic.Uint64
	// runSeq counts finished index runs. The stored run report is the only
	// input of the stage's unavailable check that has no store generation:
	// it changes only when a run finishes, so one sequence value is its
	// exact key. It is not a second data generation.
	runSeq atomic.Uint64
	// lastRun is the unavailable detail of the newest run and the sequence
	// it belongs to. It is one entry: the newest report is the only one a
	// status reads.
	runMu   sync.Mutex
	lastRun *lastRunEntry
	// retry is the process-local wait after an unavailable stop: the
	// automatic reconcile does not plan or start a run until it ends, a
	// setting or scope change lands, the provider reports healthy or the
	// managed runtime becomes ready. Protected by retryMu; the Indexer is
	// always used by pointer.
	retryMu sync.Mutex
	retry   retryState
}

// liveRun is the state of the run in progress (zero when none). It is
// derived from the run's Report, never a second counter.
type liveRun struct {
	active    bool
	space     string
	eligible  int
	done      int
	total     int
	embedded  int
	unchanged int
	failed    int
	startedAt time.Time
}

// New returns an indexer; bus and clk may be nil.
func New(cfg *config.Config, st *store.Store, bus *events.Bus, src EmbedderSource, clk clock.Clock) *Indexer {
	ix := &Indexer{cfg: cfg, st: st, bus: bus, src: src, clock: clock.Or(clk), log: logx.For("index"),
		textVersion: func() string { return mail.DataDigest()[:textVersionDigits] },
		planGate:    make(chan struct{}, 1)}
	ix.planGate <- struct{}{}
	return ix
}

// textVersionDigits is the length of a stored text version (hex digits).
const textVersionDigits = 16

// beginLive starts the live state of one run. Only the run goroutine
// writes it; status readers take the small mutex.
func (ix *Indexer) beginLive(space string, eligible, total int, start time.Time) {
	ix.liveMu.Lock()
	ix.live = liveRun{active: true, space: space, eligible: eligible, total: total, startedAt: start}
	ix.liveMu.Unlock()
}

// updateLive copies the run's report counters into the live state. The
// report is the owner of those numbers; this only publishes them for
// status reads.
func (ix *Indexer) updateLive(p Progress) {
	ix.liveMu.Lock()
	if ix.live.active {
		ix.live.done, ix.live.total = p.Done, p.Total
		ix.live.embedded, ix.live.unchanged, ix.live.failed = p.Embedded, p.Unchanged, p.Failed
	}
	ix.liveMu.Unlock()
}

// endLive marks the run over: the next status call reads the full
// coverage from the store again.
func (ix *Indexer) endLive() {
	ix.liveMu.Lock()
	ix.live.active = false
	ix.liveMu.Unlock()
}

// liveFor returns the live state when a run is active in space.
func (ix *Indexer) liveFor(space string) (liveRun, bool) {
	ix.liveMu.Lock()
	defer ix.liveMu.Unlock()
	return ix.live, ix.live.active && ix.live.space == space
}

// Options select what a run indexes.
type Options struct {
	// Stores restricts the run to these store keys (each must be picked:
	// source.SettingSelected; nil = every picked store).
	Stores []string `json:"stores,omitempty"`
}

// Counts are coverage numbers of a set of messages.
type Counts struct {
	Eligible    int `json:"eligible"`    // mail of the picked stores and index.folder_roles
	Indexed     int `json:"indexed"`     // a vector of the space, message unchanged since
	Stale       int `json:"stale"`       // a vector of the space, message changed since (checked by the next run)
	Pending     int `json:"pending"`     // no vector of the space yet
	Partial     int `json:"partial"`     // of Pending: waiting for their details
	Quarantined int `json:"quarantined"` // of Pending or Stale: failed index.quarantine_after times at this revision
}

// Searchable is how many messages have a vector of the space.
func (c Counts) Searchable() int { return c.Indexed + c.Stale }

// Coverage is how much of the eligible mail the current vector space
// covers, in all and per store key and folder role ("primary/inbox").
type Coverage struct {
	Space string `json:"space"`
	Counts
	Groups map[string]Counts `json:"groups,omitempty"`
}

// Coverage reports the current coverage.
func (ix *Indexer) Coverage(ctx context.Context) (Coverage, error) {
	e, err := ix.src.Embedder()
	if err != nil {
		return Coverage{}, err
	}
	return ix.coverage(ctx, e.Space())
}

// planInput is one snapshot of everything a coverage plan depends on
// besides the message rows: the vector space, the cleaning rules version,
// the covered folder roles, the picked stores and the quarantine threshold.
// A plan reads it once so its cache key and its query cannot disagree.
type planInput struct {
	space       string
	textVersion string
	roles       []mail.FolderRole
	stores      map[string]bool
	quarantine  int
}

// coverageInput snapshots the current coverage inputs.
func (ix *Indexer) coverageInput(space string) planInput {
	s := ix.cfg.Snapshot()
	roles, stores := scopeOf(s)
	return planInput{
		space:       space,
		textVersion: ix.textVersion(),
		roles:       roles,
		stores:      stores,
		quarantine:  s.Int(SettingQuarantineAfter),
	}
}

// coverageKey is the exact input set of a cached plan. A write that can
// change any count or the work list appears here: mail and detail writes
// advance MailGeneration, embedding writes advance EmbeddingGeneration, and
// the remaining inputs (space, text version, roles, picked stores,
// quarantine) are read from the same snapshot as the query.
type coverageKey struct {
	space       string
	textVersion string
	mailGen     uint64
	embedGen    uint64
	roles       string
	stores      string
	quarantine  int
}

// planEntry is the one cached plan and the inputs it belongs to. The work
// list is absent when only a coverage was collected (workKnown false): the
// next call that needs the work list reads the states again, because a
// coverage alone cannot answer which messages need a vector.
type planEntry struct {
	key       coverageKey
	cov       Coverage
	work      []store.EmbeddingState
	workKnown bool
}

// keyFor is the cache key of a plan input. Reading the generations before
// the store read is deliberate: a write that lands during the read advances
// one of them, so this result is cached under the old key and the next read
// recomputes. The reverse order could publish the new key with the old rows.
func (ix *Indexer) keyFor(in planInput) coverageKey {
	return coverageKey{
		space:       in.space,
		textVersion: in.textVersion,
		mailGen:     ix.st.MailGeneration(),
		embedGen:    ix.st.EmbeddingGeneration(),
		roles:       rolesKey(in.roles),
		stores:      storesKey(in.stores),
		quarantine:  in.quarantine,
	}
}

// coverage reports the current coverage, reusing the last result while its
// exact inputs are unchanged: every coalesced learning-status refresh and
// progress event asks for it, and a full mailbox read per refresh would
// repeat work the last refresh already did. A miss classifies the states
// without collecting the run's work list.
func (ix *Indexer) coverage(ctx context.Context, space string) (Coverage, error) {
	_, cov, err := ix.planStates(ctx, ix.coverageInput(space), Options{}, false)
	return cov, err
}

// clone copies the groups map so a coverage handed out by the cache shares
// no mutable state with it.
func (c Coverage) clone() Coverage {
	out := c
	if c.Groups != nil {
		out.Groups = make(map[string]Counts, len(c.Groups))
		for k, v := range c.Groups {
			out.Groups[k] = v
		}
	}
	return out
}

// plan lists the eligible messages that need work, newest first, and the
// coverage they make up. Run and Ensure call it and both need the work list;
// an unchanged mailbox answers from the cached plan without a state read.
func (ix *Indexer) plan(ctx context.Context, space string, o Options) ([]store.EmbeddingState, Coverage, error) {
	return ix.planStates(ctx, ix.coverageInput(space), o, true)
}

// planStates classifies every eligible message once, reusing the last plan
// while its exact inputs are unchanged: a reconcile over an unchanged
// mailbox performs no state read at all. collectWork also keeps the
// messages that need a vector now, newest first; the coverage path leaves
// the list out so status refreshes allocate no work slice.
//
// One planner at a time (planGate) owns the state read, so N concurrent
// Ensure calls perform one plan and the rest reuse its result. A failed
// plan is never cached: the entry is written only after the scan succeeded.
// A write invalidates the entry through the generations in the key.
func (ix *Indexer) planStates(ctx context.Context, in planInput, o Options, collectWork bool) ([]store.EmbeddingState, Coverage, error) {
	for _, k := range o.Stores {
		if !in.stores[k] {
			return nil, Coverage{}, fmt.Errorf("index: store %q is not picked (%s)", k, source.SettingSelected)
		}
	}
	key := ix.keyFor(in)
	if work, cov, ok := ix.cachedPlan(key, o, collectWork); ok {
		return work, cov, nil
	}
	if err := ix.beginPlan(ctx); err != nil {
		return nil, Coverage{}, err
	}
	defer ix.endPlan()
	// A planner that finished while this one waited may have filled the
	// entry. The key is read again: the wait may have covered a write.
	key = ix.keyFor(in)
	if work, cov, ok := ix.cachedPlan(key, o, collectWork); ok {
		return work, cov, nil
	}
	work, cov, err := ix.scanStates(ctx, in, o, collectWork)
	if err != nil {
		return nil, cov, err
	}
	ix.cachePlan(key, cov, work, collectWork)
	return work, cov, nil
}

// beginPlan claims the planning slot or returns the context's error. A
// canceled caller never waits behind a scan and never starts one.
func (ix *Indexer) beginPlan(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ix.planGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// endPlan releases the planning slot.
func (ix *Indexer) endPlan() { ix.planGate <- struct{}{} }

// scanStates performs the one state read and classifies it. The caller holds
// the planning slot; planStates caches the result only when this returns no
// error.
func (ix *Indexer) scanStates(ctx context.Context, in planInput, o Options, collectWork bool) ([]store.EmbeddingState, Coverage, error) {
	ix.planScans.Add(1)
	states, err := ix.st.EmbeddingStates(ctx, in.roles)
	cov := Coverage{Space: in.space, Groups: map[string]Counts{}}
	if err != nil {
		return nil, cov, err
	}
	var work []store.EmbeddingState
	for _, st := range states {
		key := source.StoreKey(st.SourceID, st.StoreID)
		group := cov.Groups[key+"/"+string(st.Role)]
		needed := classify(&st, in.space, in.textVersion, in.quarantine, &cov.Counts, &group)
		cov.Groups[key+"/"+string(st.Role)] = group
		if collectWork && needed && (len(o.Stores) == 0 || slices.Contains(o.Stores, key)) {
			work = append(work, st)
		}
	}
	return work, cov, nil
}

// cachedPlan returns the cached plan for key, copied so a caller cannot
// mutate the cached Groups map. collectWork needs an entry whose work list
// was collected; a coverage-only entry is not enough. The entry always holds
// the unrestricted plan (the classifier counts every state before the store
// filter), so a restricted request filters the cached list exactly as the
// scan would have.
func (ix *Indexer) cachedPlan(key coverageKey, o Options, collectWork bool) ([]store.EmbeddingState, Coverage, bool) {
	ix.planMu.Lock()
	entry := ix.planCache
	ix.planMu.Unlock()
	if entry == nil || entry.key != key {
		return nil, Coverage{}, false
	}
	if !collectWork {
		return nil, entry.cov.clone(), true
	}
	if !entry.workKnown {
		return nil, Coverage{}, false
	}
	return filterWork(entry.work, o.Stores), entry.cov.clone(), true
}

// filterWork restricts a cached work list to the requested stores, the same
// filter the classification applies. The entry's slice is read-only after
// caching, so the unrestricted request returns it without a copy.
func filterWork(work []store.EmbeddingState, stores []string) []store.EmbeddingState {
	if len(stores) == 0 {
		return work
	}
	out := make([]store.EmbeddingState, 0, len(work))
	for _, st := range work {
		if slices.Contains(stores, source.StoreKey(st.SourceID, st.StoreID)) {
			out = append(out, st)
		}
	}
	return out
}

// cachePlan replaces the one cached plan.
func (ix *Indexer) cachePlan(key coverageKey, cov Coverage, work []store.EmbeddingState, withWork bool) {
	ix.planMu.Lock()
	ix.planCache = &planEntry{key: key, cov: cov.clone(), work: work, workKnown: withWork}
	ix.planMu.Unlock()
}

// runningCoverage is Ensure's answer while a run owns the index slot: the
// last planned coverage for the space, read without touching the store. A
// run plans before it writes, so the newest entry is its starting coverage
// (the counts the run reports as Before). The running job itself is never
// hidden: Stage.Status reports its live progress instead of this coverage.
func (ix *Indexer) runningCoverage(space string) Coverage {
	ix.planMu.Lock()
	entry := ix.planCache
	ix.planMu.Unlock()
	if entry != nil && entry.cov.Space == space {
		return entry.cov.clone()
	}
	return Coverage{Space: space}
}

// rolesKey fingerprints the covered folder roles. The list is sorted, so
// reordering index.folder_roles is the same coverage instead of a cache miss,
// and NUL cannot appear in a role, so two different lists cannot collide.
func rolesKey(roles []mail.FolderRole) string {
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = string(r)
	}
	slices.Sort(parts)
	parts = slices.Compact(parts)
	return strings.Join(parts, "\x00")
}

// storesKey fingerprints the picked store keys in a stable order.
func storesKey(stores map[string]bool) string {
	keys := make([]string, 0, len(stores))
	for k, picked := range stores {
		if picked {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, "\x00")
}

// classify counts one message in both counts and reports whether it needs
// work now: a vector is current when its message is unchanged since and
// its text was cleaned by the current rules (version); otherwise it is
// stale and checked again (its text hash decides whether it is embedded
// again).
func classify(st *store.EmbeddingState, space, version string, quarantine int, all, group *Counts) bool {
	add := func(f func(c *Counts)) { f(all); f(group) }
	add(func(c *Counts) { c.Eligible++ })
	switch {
	case st.Space == space && st.Checked == st.Revision && st.TextVersion == version:
		add(func(c *Counts) { c.Indexed++ })
		return false
	case st.Space == space:
		add(func(c *Counts) { c.Stale++ })
	default:
		add(func(c *Counts) { c.Pending++ })
		if st.Partial {
			add(func(c *Counts) { c.Partial++ })
		}
	}
	if st.Failures >= quarantine {
		add(func(c *Counts) { c.Quarantined++ })
		return false
	}
	return !st.Partial
}

// Eligible lists the ids of the messages the index can actually cover
// (picked stores and index.folder_roles; search counts its coverage
// against them). It is the semantic target, not the UI's Coverage
// denominator: a partial or quarantined message stays visible in Coverage
// but can never receive a vector, so counting it here would make the
// waiting target unreachable.
func (ix *Indexer) Eligible(ctx context.Context) ([]int64, error) {
	roles, _ := scopeOf(ix.cfg.Snapshot())
	return ix.st.IndexableIDs(ctx, roles, ix.cfg.Int(SettingQuarantineAfter))
}

// scopeOf is what the index covers: index.folder_roles of the picked
// stores.
func scopeOf(s *config.Snapshot) ([]mail.FolderRole, map[string]bool) {
	var roles []mail.FolderRole
	for _, r := range s.Strings(SettingFolderRoles) {
		roles = append(roles, mail.FolderRole(r))
	}
	return roles, source.Selected(s)
}
