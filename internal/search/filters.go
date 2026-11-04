package search

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// allowed is the messages the filter selects (always computed: even the
// zero filter excludes unpicked stores and inactive folders). The sets are
// kept per filter and updated with the new mail instead of being listed
// again after every sync: a new message is appended from a row-id range
// read, and the two interactive filters (all visible mail, owner scope)
// survive a mail generation. A message-owned filter (a sender, a date, an
// attachment flag, a further folder) cannot be appended that way — a
// re-sync rewrites those columns in place — and is rebuilt.
//
// The set only grows between rebuilds, so it is a superset of the filter:
// the semantic ranking may see a message that has since left the filter.
// That is never visible in a result: exact keeps only the candidates the
// filter selects now (one bounded read, FilterSelects).
func (s *Searcher) allowed(ctx context.Context, q Query) ([]int64, error) {
	key := filterKey(q.Filter)
	if ids, ok := s.cachedFilter(key, s.st.MailGeneration()); ok {
		return ids, nil
	}
	// One slow path at a time: two searches that both found the set stale
	// would otherwise read the same watermark and append the same delta
	// twice. The fast path above never waits here.
	s.filterMu.Lock()
	defer s.filterMu.Unlock()
	gen := s.st.MailGeneration()
	if ids, ok := s.cachedFilter(key, gen); ok {
		return ids, nil
	}
	stamp, err := s.st.FilterStamp(ctx, q.Filter.OwnerOnly)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.filterEntry(key); ok && cached.extendable(q.Filter, stamp) {
		ids, maxID, err := s.st.FilterIDsSince(ctx, q.Filter, cached.watermark)
		if err != nil {
			return nil, err
		}
		// Always copy: a reader still holds the previous slice, so appending
		// in place could write past its length under a concurrent search.
		cached.ids = append(slices.Clone(cached.ids), ids...)
		cached.watermark = max(maxID, cached.watermark)
		cached.gen = gen
		cached.extends++
		s.installFilter(key, cached)
		return cached.ids, nil
	}
	ids, err := s.st.FilterIDs(ctx, q.Filter)
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []int64{}
	}
	s.filterScans.Add(1)
	s.installFilter(key, filterSet{gen: gen, ids: ids, watermark: stamp.MaxID, folders: stamp.Folders,
		verdicts: stamp.Verdicts, scopedAt: stamp.ScopedAt, builtAt: stamp.Now})
	return ids, nil
}

// cachedFilter returns the set when it is current for gen.
func (s *Searcher) cachedFilter(key string, gen uint64) ([]int64, bool) {
	set, ok := s.filterEntry(key)
	if !ok || set.gen != gen {
		return nil, false
	}
	return set.ids, true
}

// filterEntry is the cached set under the map lock.
func (s *Searcher) filterEntry(key string) (filterSet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.filters[key]
	return set, ok
}

// installFilter stores one filter set, keeping the cache bounded. A new
// filter must not grow the map without limit; evicting every set only costs
// the next search a list, never a wrong answer.
func (s *Searcher) installFilter(key string, set filterSet) {
	s.mu.Lock()
	if s.filters == nil || len(s.filters) >= filterCacheSize {
		s.filters = make(map[string]filterSet, filterCacheSize)
	}
	s.filters[key] = set
	s.mu.Unlock()
}

// filterCacheSize bounds the filter sets kept: a handful of distinct
// filters is all an interactive session asks for, and a new filter must not
// grow the map without limit.
const filterCacheSize = 8

// filterRefreshEvery bounds how long a set may grow by appends before it is
// listed again. The set never drops an id, so removed or moved messages stay
// in it until the next rebuild; a rebuild is the old cost, amortized over
// this many mail generations (a sync counts once thanks to HoldMailWrites).
const filterRefreshEvery = 64

// filterKey renders a filter as a stable cache key. Every field that changes
// the selected ids is part of it; the zero filter is one key like any other.
func filterKey(f store.SearchFilter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%d|%v|%v|%s|%t|", f.Since.UnixNano(), f.Until.UnixNano(), f.Roles, f.Stores, f.Sender, f.OwnerOnly)
	if f.HasAttachments == nil {
		b.WriteString("-")
	} else if *f.HasAttachments {
		b.WriteString("1")
	} else {
		b.WriteString("0")
	}
	return b.String()
}

// filterSet is one filter's messages, valid while the mail generation is the
// one it was built or extended at. ids is a superset of the filter between
// rebuilds (see allowed).
type filterSet struct {
	gen       uint64 // MailGeneration the ids reflect
	ids       []int64
	watermark int64  // highest messages.id listed so far: the append point
	folders   string // FilterStamp.Folders at the last full build
	verdicts  int    // scope_verdicts count at the last full build
	scopedAt  string // latest verdict write at the last full build
	builtAt   string // store time of the last full build (scope epoch)
	extends   int    // appends since the last full build
}

// extendable reports whether the new mail can be appended to set instead of
// rebuilding it. Everything that is not the new mail itself must be provably
// unchanged: the folder rows (visibility, role, store kind and key), the
// scope verdicts for an owner-scoped filter, and the highest row id (a
// lower one means the top message was removed and its id can be reused).
// A message-owned filter is never extended: a re-sync rewrites the message
// row in place.
func (c filterSet) extendable(f store.SearchFilter, st store.FilterStamp) bool {
	if f.MessageFields() || c.extends >= filterRefreshEvery {
		return false
	}
	if st.MaxID < c.watermark || st.Folders != c.folders {
		return false
	}
	if f.OwnerOnly && (st.Verdicts != c.verdicts || st.ScopedAt >= c.builtAt) {
		// A verdict written at the build second may not be in the scan.
		return false
	}
	return true
}

// exact keeps the semantic candidates the filter selects now. The vector
// ranking runs over the cached set, which between rebuilds is a superset;
// dropping the candidates that left the filter keeps every result inside the
// filter (owner scope included) and keeps Total, paging and the fused order
// the ones an exact set would produce. Candidates the full-text ranking
// already returned are exact by its SQL, so only the semantic-only ones are
// checked (at most search.max_hits, usually none).
func (s *Searcher) exact(ctx context.Context, f store.SearchFilter, scored []vector.Scored, known map[int64]bool) ([]vector.Scored, error) {
	ids := make([]int64, 0, len(scored))
	for _, c := range scored {
		if !known[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		return scored, nil
	}
	ok, err := s.st.FilterSelects(ctx, f, ids)
	if err != nil {
		return nil, err
	}
	kept := scored[:0]
	for _, c := range scored {
		if known[c.ID] || ok[c.ID] {
			kept = append(kept, c)
		}
	}
	return kept, nil
}
