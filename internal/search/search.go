// Package search is hybrid retrieval: FTS5 BM25 over every message plus
// cosine similarity over the semantic index (an in-memory copy of the
// current vector space, scanned exactly), fused with reciprocal rank
// fusion, narrowed by one filter that both rankings share
// (store.SearchFilter) and by triage answers. Every result says how much
// of the mail the semantic index covers, and a search whose query cannot
// be embedded (embedder down, not installed, too slow) answers from full
// text only and says why.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Query is one search.
type Query struct {
	Text   string             `json:"text"`
	Filter store.SearchFilter `json:"filter"`
	Triage []TriageFilter     `json:"triage,omitempty"`
	Limit  int                `json:"limit,omitempty"`  // 0 = search.limit
	Offset int                `json:"offset,omitempty"` // after fusion and triage, before hydration
	// Budget overrides search.embed_timeout for one call. It is internal: the
	// HTTP API never decodes it, because a client must not choose how long a
	// request holds a worker. Question answering sets it so its accuracy-
	// critical retrieval is not judged by the latency budget of the search
	// box; 0 keeps the configured interactive budget.
	Budget time.Duration `json:"-"`
	// Wait makes the semantic leg complete instead of racing a timer, and
	// waits for an incomplete index to catch up before ranking. Question
	// answering uses it: a written answer may take seconds, but it must never
	// miss a message because a ranking was slow. The caller's context is the
	// only bound.
	Wait bool `json:"-"`
	// TextOnly skips the semantic leg without consulting the embedder. The
	// omnibox uses it for suggestions: they show the same full-text hits but
	// never pay a model call for a prefix that is about to change.
	TextOnly bool `json:"-"`
}

// Result is the answer to a query.
type Result struct {
	Hits []Hit `json:"hits"`
	// Total counts every fused, triage-filtered candidate before Offset and
	// Limit are applied. It is a lower bound when Capped is true.
	Total int `json:"total"`
	// Capped reports that a ranking returned search.max_hits rows or the
	// text count reached search.count_cap: there may be more candidates.
	Capped bool `json:"capped"`
	// TextMatches counts the full-text matches within the filter, up to
	// search.count_cap (TextMatchesCapped: there are more).
	TextMatches       int  `json:"text_matches"`
	TextMatchesCapped bool `json:"text_matches_capped"`
	// Recall reports that the strict full-text query matched nothing and the
	// terms were ORed as a recall fallback. The hits are related mail, not
	// exact matches, and the UI should say so.
	Recall bool `json:"recall,omitempty"`
	// Terms disclose how each query term matched: whole words, substrings
	// (scripts without spaces) or a scan (terms shorter than a trigram).
	Terms    []store.TermMatch `json:"terms"`
	Semantic Semantic          `json:"semantic"`
	Notice   string            `json:"notice"` // the coverage sentence the UI shows
	Took     time.Duration     `json:"took"`
}

// Semantic discloses the vector side of a search.
type Semantic struct {
	Used     bool   `json:"used"`
	Reason   string `json:"reason,omitempty"` // why it was not used
	TimedOut bool   `json:"timed_out,omitempty"`
	Covered  int    `json:"covered"`  // messages with a vector of the current space
	Eligible int    `json:"eligible"` // messages the index is meant to cover
	Space    string `json:"space,omitempty"`
}

// Hit is one result.
type Hit struct {
	store.Summary
	Score      float64 `json:"score"`
	TextRank   int     `json:"text_rank,omitempty"`   // 1-based rank in full text (0 = not found there)
	VectorRank int     `json:"vector_rank,omitempty"` // 1-based rank by similarity (0 = not found there)
	Similarity float32 `json:"similarity,omitempty"`
	Snippet    string  `json:"snippet"`
}

// Status is published when semantic search becomes available or not.
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// TopicStatus carries semantic-search availability changes.
var TopicStatus = events.NewTopic[Status]("search.semantic")

// CoverageSource lists the messages the index is meant to cover
// (index.Indexer).
type CoverageSource interface {
	Eligible(ctx context.Context) ([]int64, error)
}

// EmbedderSource builds the configured embedder (embed.Source).
type EmbedderSource interface {
	Embedder() (engine.Embedder, error)
}

// Searcher answers queries over one store.
type Searcher struct {
	cfg     *config.Config
	st      *store.Store
	src     EmbedderSource
	cov     CoverageSource
	triage  TriageReader
	gate    learning.Gate
	bus     *events.Bus
	clock   clock.Clock
	log     *slog.Logger
	vectors vector.Set
	// refresh is vector.Set.Refresh. It is a field so a test can model a
	// cold index slower than the semantic budget; production wires it once
	// in New and never changes it.
	refresh func(context.Context, vector.Source, string, int) error
	// ensure starts the index catch-up for the messages that have no vector
	// yet (index.Indexer.Ensure). It is optional: without it, a waiting
	// search only re-reads the vectors the indexer writes.
	ensure func(context.Context) error
	// base is cancelled by Close and bounds every detached semantic worker;
	// workers is the join that waits for them. The worker outlives the request
	// that started it, so only the searcher can join it before the store closes
	// (the API's handler join never saw it).
	base    context.Context
	cancel  context.CancelFunc
	workers *join.Group
	mu      sync.Mutex
	cache   coverageCache
	filters map[string]filterSet
	// filterMu serializes the slow path of the filter set (the stamp, the
	// delta read and the install). The fast path only takes mu; a search
	// that finds its set current never waits here.
	filterMu sync.Mutex
	// covMu serializes the slow path of coverage: the eligible-set rebuild
	// and the count of the copy. Two searches that both find the set stale
	// or the sequence moved would otherwise scan the mailbox or the vectors
	// twice for one state; the re-check inside the lock makes the second one
	// read what the first installed.
	covMu sync.Mutex
	// filterScans counts the full FilterIDs scans: the work a cached filter
	// set removes from the first search after a sync. Diagnostic, and the
	// pin of "a mail write extends the set instead of listing the mailbox".
	filterScans atomic.Uint64
	// coverageScans counts the O(vectors) Count scans of the coverage count:
	// the work the cached count removes from every search after the first at
	// a vector copy's write sequence. Diagnostic, and the pin of "one count
	// serves repeated searches until the copy moves".
	coverageScans atomic.Uint64
	status        *Status
	// prepMu guards prep, the per-space preparation gate. One search may
	// load the vector copy and wait for the index catch-up at a time; ten
	// concurrent queries must not spawn ten workers contending for the same
	// embedder and vector lock. The map is bounded by the configured model
	// spaces, which change only when the owner changes the embedder.
	prepMu sync.Mutex
	prep   map[string]chan struct{}
	// queries is the one bounded single-flight cache of query vectors,
	// keyed by (space, folded text). It is per Searcher, hence per process.
	// queries is the query-embedding cache: the same bounded, single-flight
	// cache every read service uses, keyed by the vector space and the folded
	// query text. Its key covers every input that can change the vector, so an
	// installed entry never expires (see viewcache.Options.NeverExpires).
	queries *viewcache.Cache[string, []float32]
}

// Deps are the searcher's collaborators; Triage, Bus, Clock and Ensure may be
// nil. Gate is required: search answers nothing until the picked mailboxes
// are fully learned.
type Deps struct {
	Store    *store.Store
	Embedder EmbedderSource
	Coverage CoverageSource
	Triage   TriageReader
	Gate     learning.Gate
	Bus      *events.Bus
	Clock    clock.Clock
	// Ensure starts the semantic index catch-up. A waiting search uses it to
	// complete an index that is still missing vectors instead of answering
	// from a partial one.
	Ensure func(context.Context) error
}

// New returns a searcher.
func New(cfg *config.Config, d Deps) (*Searcher, error) {
	if d.Gate == nil || d.Store == nil {
		return nil, ErrNoGate
	}
	s := &Searcher{cfg: cfg, st: d.Store, src: d.Embedder, cov: d.Coverage, triage: d.Triage, gate: d.Gate, bus: d.Bus,
		clock: clock.Or(d.Clock), log: logx.For("search"), ensure: d.Ensure}
	s.queries = viewcache.New(viewcache.Options[string, []float32]{
		NeverExpires: true, Size: queryCacheSize, MaxBytes: queryCacheMaxBytes, Weigh: weighQueryVector,
		Now: s.clock.Now, Component: "search",
	})
	s.base, s.cancel = context.WithCancel(context.Background())
	s.workers = join.New()
	s.refresh = s.vectors.Refresh
	return s, nil
}

// ErrNoGate is a wiring error: a searcher without the learning gate (or
// without a store) is refused at construction.
var ErrNoGate = errors.New("search: no learning gate or store")

// ErrEmptyQuery is returned for a query without words.
var ErrEmptyQuery = errors.New("search: empty query")

// textError maps the store's typed empty search to the client contract
// (400 empty_query); every other error passes through unchanged.
func textError(err error) error {
	if errors.Is(err, store.ErrEmptySearch) {
		return ErrEmptyQuery
	}
	return err
}

// ErrQueryTooLong is returned when the query exceeds
// search.query_max_chars: one request must not become thousands of
// full-text terms or one oversized embedding.
var ErrQueryTooLong = errors.New("search: query too long")

// QueryTooLongError names the ceiling that rejected the query, so the API
// can return the limit the owner can change.
type QueryTooLongError struct{ Max int }

// Error implements error.
func (e *QueryTooLongError) Error() string {
	return fmt.Sprintf("search: query is longer than %d characters", e.Max)
}

// Unwrap makes errors.Is(err, ErrQueryTooLong) true.
func (e *QueryTooLongError) Unwrap() error { return ErrQueryTooLong }

// ErrInvalidLimit is returned for an explicit limit below 1 or above
// search.max_hits (0 means search.limit).
var ErrInvalidLimit = errors.New("search: invalid limit")

// ErrInvalidOffset is returned for a negative offset or one beyond
// MaxOffset.
var ErrInvalidOffset = errors.New("search: invalid offset")

// MaxOffset bounds the fused candidates a caller can skip. It keeps one
// request from forcing an unbounded triage and hydration pass.
const MaxOffset = 1000

// Search budgets. The interactive budget is a setting; these bound any
// caller (and an internal override) to values that still answer a person.
const (
	minSearchBudget = 50 * time.Millisecond
	maxSearchBudget = 30 * time.Second
	// semanticPrepTimeout is how long an abandoned semantic worker may keep
	// preparing the vector copy, coverage and filter set for the next query.
	semanticPrepTimeout = 30 * time.Second
)

// timeoutSemantic is the disclosure of a semantic leg that did not finish in
// time. It keeps the coverage the warm-up already computed, so a caller can
// tell a genuinely incomplete index from a warm index that was merely slow.
// It is not a status change: the index did not become unavailable.
func (s *Searcher) timeoutSemantic(budget time.Duration) Semantic {
	sem := Semantic{TimedOut: true, Reason: fmt.Sprintf("the semantic index did not finish within the semantic budget (%s)", budget)}
	if covered, eligible, ok := s.coverageSnapshot(); ok {
		sem.Covered, sem.Eligible = covered, eligible
	}
	return sem
}

// acquirePrep serializes the vector preparation of one space. The returned
// release is idempotent; a context that ends while another search prepares
// returns the context error and never starts a second worker. The gate is
// held for the whole semantic call, so a burst of search-box queries cannot
// contend for the embedder and the vector lock.
func (s *Searcher) acquirePrep(ctx context.Context, space string) (func(), error) {
	s.prepMu.Lock()
	if s.prep == nil {
		s.prep = map[string]chan struct{}{}
	}
	gate := s.prep[space]
	if gate == nil {
		gate = make(chan struct{}, 1)
		s.prep[space] = gate
	}
	s.prepMu.Unlock()
	select {
	case gate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-gate }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Search runs both rankings, fuses, filters and cuts snippets. Full text
// runs first; the semantic side runs concurrently. By default it races
// search.embed_timeout (150 ms), so a slow embedder delays a typed query by at
// most the budget and the notice says why the ranking is missing. A waiting
// query (Query.Wait, used by question answering) has no timer: it completes
// the semantic leg and lets an incomplete index catch up before ranking.
func (s *Searcher) Search(ctx context.Context, q Query) (Result, error) {
	start := s.clock.Now()
	if err := s.gate.Allow(ctx); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(q.Text) == "" {
		return Result{}, ErrEmptyQuery
	}
	if max := s.cfg.Int(store.SettingSearchQueryMaxChars); max > 0 && utf8.RuneCountInString(q.Text) > max {
		return Result{}, &QueryTooLongError{Max: max}
	}
	for i := range q.Triage {
		if err := q.Triage[i].validate(); err != nil {
			return Result{}, err
		}
	}
	snap := s.cfg.Snapshot()
	maxHits := snap.Int(store.SettingSearchMaxHits)
	limit := q.Limit
	if limit == 0 {
		limit = snap.Int(store.SettingSearchLimit)
	} else if limit < 1 || limit > maxHits {
		return Result{}, fmt.Errorf("%w: %d", ErrInvalidLimit, q.Limit)
	}
	if q.Offset < 0 || q.Offset > MaxOffset {
		return Result{}, fmt.Errorf("%w: %d", ErrInvalidOffset, q.Offset)
	}
	budget := q.Budget
	if q.Wait {
		// A waiting search completes: the caller's context is the only bound.
		budget = 0
	} else {
		if budget <= 0 {
			budget = snap.Duration(store.SettingSearchEmbedTimeout)
		}
		budget = min(max(budget, minSearchBudget), maxSearchBudget)
	}
	// The semantic worker outlives the interactive wait below. A cold vector
	// load, a rebuilt eligible set or a busy embedder is preparation work that
	// must finish once: cancelling it at the interactive budget made every
	// retry repeat the same cold work and kept a warm index answering
	// "warming". A waiting search is not detached at all: its caller is
	// waiting for the answer, so its own context is the bound.
	//
	// safe.Detached is the one owner of that detached, bounded, base-cancelled
	// build context, and s.workers is its join: the API's handler join cannot
	// see this goroutine, so Close is the only thing that can wait for it
	// before the store closes.
	type semanticOutcome struct {
		scored []vector.Scored
		sem    Semantic
		err    error
	}
	done := make(chan semanticOutcome, 1)
	if !s.workers.Track() {
		// Closed: no new store reader may start behind the shutdown join. The
		// full-text ranking is the answer, and a waiting caller must not hang
		// on a worker that was never started.
		done <- semanticOutcome{}
	} else {
		go func() {
			defer s.workers.Done()
			// A waiting search answers the caller, so its own context is the
			// bound; an interactive one detaches so its preparation survives
			// the request. Both are tracked, because both read the store.
			run := func() (semanticOutcome, error) {
				if q.Wait {
					out, err := safe.Value(func() (semanticOutcome, error) {
						scored, sem := s.similar(ctx, q, maxHits, budget)
						return semanticOutcome{scored: scored, sem: sem}, nil
					})
					out.err = err
					return out, nil
				}
				return safe.Detached(ctx, s.base, max(semanticPrepTimeout, budget+5*time.Second),
					func(buildCtx context.Context) (semanticOutcome, error) {
						scored, sem := s.similar(buildCtx, q, maxHits, budget)
						return semanticOutcome{scored: scored, sem: sem}, nil
					})
			}
			out, err := run()
			if err != nil {
				// A stopped preparation only costs the semantic ranking of this
				// query; the full-text answer is still the answer.
				s.log.Warn("semantic preparation stopped", "err", err)
				out = semanticOutcome{err: err}
			}
			done <- out
		}()
	}
	text, err := s.st.SearchText(ctx, q.Text, q.Filter, maxHits)
	if err != nil {
		return Result{}, textError(err)
	}
	var outcome semanticOutcome
	if q.Wait {
		select {
		case outcome = <-done:
			if outcome.err != nil {
				return Result{}, outcome.err
			}
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	} else {
		// The semantic side must never hold the answer past the budget. A cold
		// vector refresh uses the preparation context and can outlive the embed
		// timeout, so wait on the result, the budget and cancellation together.
		// The worker is not cancelled: its channel is buffered, and what it
		// completes (vector copy, coverage, filter ids) serves the next query.
		timer := time.NewTimer(budget)
		defer timer.Stop()
		select {
		case outcome = <-done:
		case <-timer.C:
			// The worker may finish in the same instant. A successful result
			// that won the race is still used.
			select {
			case outcome = <-done:
				if !outcome.sem.Used {
					outcome.sem = s.timeoutSemantic(budget)
				}
			default:
				outcome.sem = s.timeoutSemantic(budget)
			}
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	if !q.TextOnly {
		// A text-only request attempted nothing, so it must not publish an
		// availability state the embedder never reported.
		s.publishSemantic(outcome.sem)
	}
	recall := false
	if q.Wait && text.Total == 0 && len(outcome.scored) == 0 {
		// A natural-language question is not a keyword query: its function
		// words ("what", "the", "is") are absent from the message, so the
		// strict AND match can be zero even when the answer is in the
		// mailbox. Only a waiting (question-answering) search pays for the
		// recall fallback, and only when neither ranking found anything.
		alt, err := s.st.SearchTextAny(ctx, q.Text, q.Filter, maxHits)
		if err != nil {
			return Result{}, textError(err)
		}
		if alt.Total > 0 {
			text, recall = alt, true
		}
	}
	ids := make([]int64, len(text.Hits))
	known := make(map[int64]bool, len(text.Hits))
	for i, h := range text.Hits {
		ids[i], known[h.RowID] = h.RowID, true
	}
	scored, err := s.exact(ctx, q.Filter, outcome.scored, known)
	if err != nil {
		return Result{}, err
	}
	cands, err := keepTriaged(ctx, s.triage, q.Triage, fuse(ids, scored, snap.Int(store.SettingSearchRRFK)))
	if err != nil {
		return Result{}, err
	}
	total := len(cands)
	capped := text.TotalCapped || len(text.Hits) >= maxHits || len(outcome.scored) >= maxHits
	if q.Offset >= len(cands) {
		cands = nil
	} else {
		cands = cands[q.Offset:]
	}
	hits, err := s.hydrate(ctx, cands, text.Terms, limit, snap.Int(store.SettingSearchSnippetChars), q.Filter.OwnerOnly)
	res := Result{Hits: hits, Total: total, Capped: capped, TextMatches: text.Total, TextMatchesCapped: text.TotalCapped,
		Recall: recall, Terms: text.Terms, Semantic: outcome.sem, Notice: notice(outcome.sem, text.Terms), Took: s.clock.Now().Sub(start)}
	s.log.Debug("search", "hits", len(hits), "total", total, "text_matches", text.Total, "semantic", outcome.sem.Used, "took", res.Took)
	return res, err
}

// Close refuses new semantic workers, cancels the running ones and joins them,
// bounded by ctx. It reports whether every worker finished: a miss means a
// store reader is still running, so the store owner must keep the store open.
// The vector copy, the eligible set and the filter set are not touched here;
// the workers only read, and the join is what makes closing the store safe.
func (s *Searcher) Close(ctx context.Context) bool {
	s.cancel()
	workers := s.workers.Close(ctx)
	queries := s.queries.Close(ctx)
	return workers && queries
}

// hydrate turns the first limit candidates into hits (a candidate removed
// meanwhile is skipped). An owner-scoped read hydrates through the owner
// boundary: a candidate that moved to a shared, delegate or hidden folder
// after ranking must not leak into an owner-only answer.
func (s *Searcher) hydrate(ctx context.Context, cands []candidate, terms []store.TermMatch, limit, snippetChars int, ownerOnly bool) ([]Hit, error) {
	page := cands[:min(limit, len(cands))]
	ids := make([]int64, len(page))
	for i, c := range page {
		ids[i] = c.id
	}
	var sums map[int64]store.Summary
	var err error
	if ownerOnly {
		sums, err = s.st.SummariesOwner(ctx, ids)
	} else {
		sums, err = s.st.Summaries(ctx, ids)
	}
	if err != nil {
		return nil, err
	}
	plan := snippetPlan(terms)
	hits := make([]Hit, 0, len(page))
	for _, c := range page {
		sum, ok := sums[c.id]
		if !ok {
			continue
		}
		hits = append(hits, Hit{Summary: sum, Score: c.score, TextRank: c.textRank, VectorRank: c.vectorRank,
			Similarity: c.similarity, Snippet: hitSnippet(sum, plan, c.textRank, snippetChars)})
	}
	return hits, nil
}

// TextOnlyReason is the disclosure of a search that asked not to rank by
// vector. It is not an availability failure: nothing was attempted.
const TextOnlyReason = "semantic ranking was not requested"

// notice is the sentence a result shows about its semantic side.
func notice(sem Semantic, terms []store.TermMatch) string {
	out := fmt.Sprintf("semantic index covers %d of %d messages", sem.Covered, sem.Eligible)
	if !sem.Used {
		switch {
		case sem.Reason == TextOnlyReason:
			out = "full-text results only (semantic ranking was not requested)"
		case sem.TimedOut:
			out = "full-text results only (" + sem.Reason + ")"
		default:
			out = "full-text results only (semantic search unavailable: " + sem.Reason + ")"
		}
		// A timeout before the coverage read knows nothing about the
		// index; do not report a made-up "0 of 0".
		if sem.Eligible > 0 {
			out += "; " + fmt.Sprintf("semantic index covers %d of %d messages", sem.Covered, sem.Eligible)
		}
	}
	var scanned []string
	for _, t := range terms {
		if t.Mode == store.ModeScan {
			scanned = append(scanned, t.Term)
		}
	}
	if len(scanned) > 0 {
		out += "; short terms matched by scanning: " + strings.Join(scanned, ", ")
	}
	return out
}
