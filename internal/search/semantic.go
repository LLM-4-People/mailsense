package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// coverageCache keeps the ids the index is meant to cover while neither
// the mail (store generation) nor the settings moved, and the count of those
// ids that have a vector at the copy's write sequence. Counting every eligible
// id against the copy is O(vectors) work a search would otherwise repeat; the
// count is recomputed only when the eligible set or the copy's sequence moves.
type coverageCache struct {
	key      string
	eligible map[int64]bool
	// covered is how many eligible ids had a vector when the count was taken;
	// seq is the copy's write sequence (vector.Set.Seq) at that count, so a
	// count that raced a vector write is recognised and redone. counted
	// distinguishes a counted zero from an entry that was never counted.
	covered int
	seq     int64
	counted bool
}

// similar is the vector ranking: the query embedded with the model's
// query template within the caller's semantic budget, scanned against the
// current space within the filter. Any failure, including the budget
// expiring, degrades to no ranking with the reason, never to an error.
// queryCacheSize bounds the query vectors kept in one process. A typed
// prefix, its completion and the same query on the Search page are a handful
// of texts; 64 covers a session without retaining vectors for queries nobody
// repeats.
const queryCacheSize = 64

// queryCacheMaxBytes bounds the retained query vectors: a vector is
// Dimensions() float32s at 4 bytes each, so 1 MiB covers the 64-entry window
// of even a 4096-dimension model.
const queryCacheMaxBytes = 1 << 20

// weighQueryVector is the query cache's exact estimate: a vector retains one
// float32 per dimension.
func weighQueryVector(v []float32) int { return len(v) * 4 }

// queryCacheKey is (space, normalized query text). mail.Fold is the same
// normalization the full-text query parser applies, so case and diacritic
// variants share one embedding; internal spacing is kept exact.
func queryCacheKey(space, text string) string {
	return space + "\x00" + mail.Fold(text)
}

func (s *Searcher) similar(ctx context.Context, q Query, k int, budget time.Duration) ([]vector.Scored, Semantic) {
	var sem Semantic
	if q.TextOnly {
		// A text-only suggestion must not start the local model, load the
		// vector copy or read coverage. The full-text ranking is the answer.
		sem.Reason = TextOnlyReason
		return nil, sem
	}
	e, err := s.src.Embedder()
	if err != nil {
		return s.degrade(sem, err)
	}
	sem.Space = e.Space()
	release, err := s.acquirePrep(ctx, sem.Space)
	if err != nil {
		return s.degrade(sem, err)
	}
	defer release()
	if err := s.refresh(ctx, s.st, sem.Space, e.Dimensions()); err != nil {
		return s.degrade(sem, err)
	}
	sem.Covered, sem.Eligible = s.coverage(ctx)
	if sem.Eligible == 0 {
		// Nothing is meant to have a vector (an empty mailbox or a closed
		// gate). There is nothing to rank, and starting the local model only
		// to embed a query against zero messages would delay the answer for
		// no result.
		sem.Used = true
		return nil, sem
	}
	if !q.Wait && s.vectors.Len() == 0 {
		// The index has eligible mail but no vectors yet (first import, a
		// rebuilt space, maintenance catching up). Embedding the query can
		// match nothing, and waiting the whole semantic budget for it made
		// every search on a cold index take the 150 ms timeout. Answer from
		// full text immediately; the preparation worker still refreshes the
		// vector copy for the next query.
		sem.Reason = "the semantic index has no vectors yet"
		return s.degrade(sem, errors.New(sem.Reason))
	}
	if q.Wait && sem.Eligible > sem.Covered {
		// A waiting question must not rank against a partial index: trigger
		// the catch-up and let it finish before the query is embedded. The
		// interactive search box never takes this path.
		if err := s.waitCoverage(ctx, e, sem.Space); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				sem.TimedOut = true
			}
			return s.degrade(sem, err)
		}
		sem.Covered, sem.Eligible = s.coverage(ctx)
	}
	// The budget covers the embedder call, not the one-time vector load:
	// a cold index warms up with the parent context and still answers. A
	// waiting search has no budget: its caller's context is the only bound.
	ectx, cancel := ctx, context.CancelFunc(func() {})
	if budget > 0 {
		ectx, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()
	res, err := s.queries.Get(ectx, queryCacheKey(sem.Space, q.Text), func(ctx context.Context) ([]float32, error) {
		embedded, err := e.Embed(ctx, []engine.Text{{Purpose: engine.PurposeQuery, Body: q.Text}})
		if err != nil {
			return nil, err
		}
		return embedded.Vectors[0], nil
	})
	if err != nil {
		// A recovered panic is a bug, not a reason the owner can act on: the
		// cache and the worker report it to the log, and the notice stays the
		// one honest sentence instead of the panic's internal text.
		var panicErr *safe.PanicError
		if errors.As(err, &panicErr) {
			s.log.Error("query embedding panicked", "err", err)
			err = errors.New("the semantic index is unavailable")
		}
		if errors.Is(ectx.Err(), context.DeadlineExceeded) {
			sem.TimedOut = true
			err = fmt.Errorf("the query was not embedded within the semantic budget (%s)", budget)
		}
		return s.degrade(sem, err)
	}
	allowed, err := s.allowed(ctx, q)
	if err != nil {
		return s.degrade(sem, err)
	}
	sem.Used = true
	return s.vectors.Top(res, allowed, k), sem
}

// waitCoverage brings the vector copy up to the eligible messages before a
// waiting search ranks. It starts the index catch-up once, then merges the
// vectors the running job writes. A failed catch-up is not fatal here: the
// caller's context ends the wait, and the next query retries.
func (s *Searcher) waitCoverage(ctx context.Context, e engine.Embedder, space string) error {
	if s.ensure != nil {
		if err := s.ensure(ctx); err != nil {
			return err
		}
	}
	const poll = 250 * time.Millisecond
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if err := s.refresh(ctx, s.st, space, e.Dimensions()); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else if covered, eligible := s.coverage(ctx); covered >= eligible {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// degrade reports a semantic failure without publishing it: the request
// goroutine owns status publication, so a worker abandoned at the budget
// can never flip the availability state after its caller moved on.
func (s *Searcher) degrade(sem Semantic, err error) ([]vector.Scored, Semantic) {
	sem.Used, sem.Reason = false, err.Error()
	if sem.Eligible == 0 {
		if covered, eligible, ok := s.coverageSnapshot(); ok {
			sem.Covered, sem.Eligible = covered, eligible
		}
	}
	return nil, sem
}

// coverage is how many eligible messages have a vector of the space, of
// how many. The eligible set is rebuilt outside the lock when the mail or
// the settings changed; the covered count is counted once per vector copy
// sequence and reused until the copy moves, so a search serves the derived
// count instead of rescanning every vector id at every query. A failure
// shows the copy's size over 0.
func (s *Searcher) coverage(ctx context.Context) (int, int) {
	// The eligible set is mail (messages, folders, selection, scope), not
	// labels: a label click must not rebuild it. MailGeneration is the owner
	// of exactly that set; Generation also moves on label writes.
	key := fmt.Sprintf("%d|%s", s.st.MailGeneration(), s.cfg.VersionKey())
	if covered, eligible, ok := s.coverageHeld(key); ok {
		return covered, eligible
	}
	return s.coverageRebuild(ctx, key)
}

// coverageHeld reports the held coverage when it belongs to key and was
// counted at the vector copy's current write sequence. The cache field is
// read under mu; the sequence is read from the copy, whose own lock makes
// each read whole.
func (s *Searcher) coverageHeld(key string) (int, int, bool) {
	s.mu.Lock()
	cache := s.cache
	s.mu.Unlock()
	if cache.key != key || !cache.counted || cache.seq != s.vectors.Seq() {
		return 0, 0, false
	}
	return cache.covered, len(cache.eligible), true
}

// coverageRebuild is the slow path of coverage: it rebuilds the eligible set
// when the mail or the settings moved and recounts the covered vectors when
// the copy's sequence moved. covMu serializes both, so concurrent searches
// share one rebuild and one count; the re-check inside the lock makes a
// search that lost the race serve the winner's set and count.
func (s *Searcher) coverageRebuild(ctx context.Context, key string) (int, int) {
	s.covMu.Lock()
	defer s.covMu.Unlock()
	s.mu.Lock()
	cache := s.cache
	s.mu.Unlock()
	if cache.key != key {
		ids, err := s.cov.Eligible(ctx)
		if err != nil {
			s.log.Warn("index coverage unknown", "err", err)
			return s.vectors.Len(), 0
		}
		cache = coverageCache{key: key, eligible: make(map[int64]bool, len(ids))}
		for _, id := range ids {
			cache.eligible[id] = true
		}
	}
	if !cache.counted || cache.seq != s.vectors.Seq() {
		// Read the sequence before counting: a vector write that lands
		// between the two makes the next search recount instead of trusting
		// a count of the older copy. The sequence never moves backwards for
		// a space, and the space is part of the cache key.
		seq := s.vectors.Seq()
		cache.covered, cache.seq, cache.counted = s.countCovered(cache.eligible), seq, true
	}
	s.mu.Lock()
	s.cache = cache
	s.mu.Unlock()
	return cache.covered, len(cache.eligible)
}

// countCovered is the one owner of the O(vectors) coverage count: it counts
// how many eligible ids the copy holds and records the diagnostic scan count,
// so every scan is observable and the count-once tests pin it.
func (s *Searcher) countCovered(eligible map[int64]bool) int {
	s.coverageScans.Add(1)
	return s.vectors.Count(eligible)
}

// coverageSnapshot reports the coverage the cache already holds (the startup
// warm-up computes it), so a search that timed out before the coverage read
// can still disclose how much of the mailbox has vectors without paying for
// the scan on the request path. The held count is served when it matches the
// copy's sequence; otherwise the count is recomputed, as before the cache.
func (s *Searcher) coverageSnapshot() (int, int, bool) {
	s.mu.Lock()
	cache := s.cache
	s.mu.Unlock()
	if cache.key == "" || cache.eligible == nil {
		return 0, 0, false
	}
	if cache.counted && cache.seq == s.vectors.Seq() {
		return cache.covered, len(cache.eligible), true
	}
	return s.countCovered(cache.eligible), len(cache.eligible), true
}

// publishSemantic reports the outcome the request actually used. Only the
// request goroutine calls it, after the semantic result (or the budget)
// has been selected. A timeout is not an availability change: the index may
// be complete and merely slower than one request's budget, so it must not
// flip the global status to "unavailable" or log a permanent failure.
func (s *Searcher) publishSemantic(sem Semantic) {
	if sem.Used {
		s.signal(Status{Available: true})
		return
	}
	if sem.TimedOut {
		return
	}
	if sem.Reason != "" {
		s.signal(Status{Reason: sem.Reason})
	}
}

// signal publishes availability changes (and logs them).
func (s *Searcher) signal(st Status) {
	s.mu.Lock()
	changed := s.status == nil || s.status.Available != st.Available
	s.status = &st
	s.mu.Unlock()
	if !changed {
		return
	}
	if st.Available {
		s.log.Info("semantic search available")
	} else {
		s.log.Warn("semantic search unavailable: full-text only", "reason", st.Reason)
	}
	if s.bus != nil {
		events.Publish(s.bus, TopicStatus, st)
	}
}
