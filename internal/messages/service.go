// Package messages owns the Mail list read model. The first all-mail page is
// the one request the first Mail click makes, and its store build is expensive
// enough (a page scan plus whole-mailbox role counts) that the window should
// not pay it again when the owner clicks Mail. The service caches that one
// page for as long as the mail generation is unchanged; every other page
// (role, store, sender, cursor, other limits, no counts) goes straight to the
// store.
//
// The cache is one immutable entry plus at most one in-flight build per
// generation. Waiters join the build and may still cancel through their own
// context; the one build itself is detached from the request that started
// it, so one caller ending early does not fail the others, and it is
// bounded by firstPageBuildTimeout. Failures are shared with the waiters but
// never cached, so the next call retries. Callers get independent copies and
// cannot mutate the cached page.
//
// It stays a custom single-entry cache because viewcache cannot express three
// policies this page depends on, each pinned by a test:
//   - the first build is detached from its starter
//     (TestFirstPageWaiterSurvivesLeaderCancel,
//     TestFirstPageBuildIsJoinableAfterTheLeaderLeft), while viewcache's
//     buildFirst derives from the caller's context, so a leader walking away
//     would abort the read for everyone who joined;
//   - a moved generation is a wait, never a stale serve
//     (TestFirstPageCacheFollowsMailGeneration), while a viewcache entry is
//     served stale while the rebuild runs behind it;
//   - each caller receives its own copy (TestFirstPageCacheReturnsIndependentCopies),
//     while viewcache hands every caller the same value.
//
// Folding this in would mean adding a detached-build mode and a per-caller
// copy hook to the shared cache for one consumer. Do not do that without a
// second consumer that needs the same two policies.
package messages

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// ErrClosed reports that the service was closed before a detached first-page
// build could start: the store it would read is being closed, so the caller
// retries after the next start instead of reading a closed database.
var ErrClosed = errors.New("messages: the service is closed")

// firstPageBuildTimeout bounds the detached first-page build. It outlives
// the request that started it (the build is shared), but never a stopped
// store: a non-cooperative read is cut here instead of waiting forever.
const firstPageBuildTimeout = 2 * time.Minute

// Store is the one read the service owns. MailGeneration is the store's
// mail-only write counter: a label click does not rebuild the first page.
type Store interface {
	MessageList(ctx context.Context, q store.MessageListQuery) (store.MessageListPage, error)
	MailGeneration() uint64
}

// Service is the first-page cache and its single-flight.
type Service struct {
	store Store

	mu     sync.Mutex
	entry  *cacheEntry
	flight *flight
	// jobs tracks the detached first-page build: it outlives the request
	// that started it, so only this owner knows the goroutine exists. Close
	// is that missing join, which lets the store close without a reader
	// behind it.
	jobs join.Group
}

// Close refuses a new detached build and joins the one in flight, bounded by
// ctx. It reports whether it joined: false means the build outlived ctx, so
// the store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool { return s.jobs.Close(ctx) }

type cacheEntry struct {
	gen  uint64
	page store.MessageListPage
}

type flight struct {
	gen  uint64
	done chan struct{}
	page store.MessageListPage
	err  error
}

// New returns a service over st. The store is the only dependency; there is
// no TTL, setting, schema or migration.
func New(st Store) *Service {
	return &Service{store: st}
}

// List returns one message page. The default unfiltered all-mail first page
// is cached by mail generation and built once for concurrent callers.
func (s *Service) List(ctx context.Context, q store.MessageListQuery) (store.MessageListPage, error) {
	if !firstPage(q) {
		return s.store.MessageList(ctx, q)
	}
	gen := s.store.MailGeneration()

	s.mu.Lock()
	if s.entry != nil && s.entry.gen == gen {
		page := clonePage(s.entry.page)
		s.mu.Unlock()
		return page, nil
	}
	if shared := s.flight; shared != nil && shared.gen == gen {
		s.mu.Unlock()
		select {
		case <-shared.done:
			s.mu.Lock()
			page, err := clonePage(shared.page), shared.err
			s.mu.Unlock()
			return page, err
		case <-ctx.Done():
			return emptyPage(), ctx.Err()
		}
	}
	shared := &flight{gen: gen, done: make(chan struct{})}
	s.flight = shared
	s.mu.Unlock()

	// The build is detached, so it is admitted to the shutdown join here: a
	// closed service must not start a read the store may no longer serve.
	// Publishing the refusal under the lock before closing done keeps the
	// waiters from reading a half-set flight.
	if !s.jobs.Track() {
		s.mu.Lock()
		if s.flight == shared {
			s.flight = nil
		}
		shared.err = ErrClosed
		close(shared.done)
		s.mu.Unlock()
		return emptyPage(), ErrClosed
	}
	go s.build(ctx, q, gen, shared)
	select {
	case <-shared.done:
		s.mu.Lock()
		page, err := clonePage(shared.page), shared.err
		s.mu.Unlock()
		return page, err
	case <-ctx.Done():
		return emptyPage(), ctx.Err()
	}
}

// build is the one first-page build. It detaches from the request that
// started it (a response ending early must not fail the callers that joined)
// and is bounded so a stuck store read cannot hold the flight forever. It
// publishes the result to every waiter and clears the flight; a failure is
// shared but never cached. The service has no base context to cancel through:
// Close is its shutdown path, and it joins this build (the nil base).
func (s *Service) build(ctx context.Context, q store.MessageListQuery, gen uint64, shared *flight) {
	defer s.jobs.Done()
	page, err := safe.Detached(ctx, nil, firstPageBuildTimeout, func(buildCtx context.Context) (store.MessageListPage, error) {
		return s.store.MessageList(buildCtx, q)
	})

	s.mu.Lock()
	if s.flight == shared {
		s.flight = nil
	}
	shared.page, shared.err = clonePage(page), err
	if err == nil {
		// A write during the build makes this answer stale. Return it to
		// the callers that already joined, but do not pin it under the
		// generation it no longer describes.
		if s.store.MailGeneration() == gen {
			s.entry = &cacheEntry{gen: gen, page: clonePage(page)}
		}
	}
	close(shared.done)
	s.mu.Unlock()
}

// firstPage is the one cacheable shape: every store, every primary role,
// every sender, every read state, no cursor, the default limit and the role
// counts the Mail screen renders. The unread filter is never the default
// page, so it always bypasses the cache (false is the filter-off state and
// stays equivalent to absent).
func firstPage(q store.MessageListQuery) bool {
	if q.Unread || q.Store != "" || q.Sender != "" || !q.Before.IsZero() || !q.Counts {
		return false
	}
	if q.Role != "" && q.Role != store.MessageListAll {
		return false
	}
	return q.Limit == 0 || q.Limit == store.MessageListDefaultLimit
}

func clonePage(in store.MessageListPage) store.MessageListPage {
	out := in
	out.Messages = make([]store.MessageListRow, len(in.Messages))
	copy(out.Messages, in.Messages)
	if in.Counts != nil {
		out.Counts = make(map[mail.FolderRole]int, len(in.Counts))
		for role, n := range in.Counts {
			out.Counts[role] = n
		}
	}
	return out
}

func emptyPage() store.MessageListPage {
	return store.MessageListPage{Messages: []store.MessageListRow{}}
}
