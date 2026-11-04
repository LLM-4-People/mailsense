package priority

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Filter is the public filter set. FilterAll is the ranking without a
// filter; the others narrow after scoring.
type Filter string

const (
	FilterAll      Filter = "all"
	FilterReply    Filter = "reply"
	FilterUrgent   Filter = "urgent"
	FilterSecurity Filter = "security"
	FilterDecision Filter = "decision"
	FilterDeadline Filter = "deadline"
	FilterWorkload Filter = "workload"
)

// Filters is the one list of filters (counts and validation).
var Filters = []Filter{FilterAll, FilterReply, FilterUrgent, FilterSecurity, FilterDecision, FilterDeadline, FilterWorkload}

// ParseFilter reads a query filter; "" is all.
func ParseFilter(s string) (Filter, bool) {
	f := Filter(strings.ToLower(strings.TrimSpace(s)))
	if f == "" {
		return FilterAll, true
	}
	return f, f.Valid()
}

// Valid reports whether f is a known filter.
func (f Filter) Valid() bool { return slices.Contains(Filters, f) }

func (s *Service) Rank(ctx context.Context, filter Filter, limit int) ([]Item, int, bool, error) {
	page, err := s.RankPage(ctx, filter, limit, 0)
	return page.Items, page.Total, page.Preview, err
}

// Page is one ranking page and the instant its snapshot was built. The
// timestamp is the snapshot's, never the request's: a stale-while-revalidate
// serve must not look fresh just because it was read now.
//
// Offset is the page start the snapshot actually served (offset clamped to
// Available), Available is the ranked candidates this snapshot can page
// through (at most MaxCached), and HasMore says whether another page follows
// inside that snapshot. Total counts every candidate that passes the filter
// and may exceed Available: the ranking keeps only the top MaxCached rows.
type Page struct {
	Items       []Item
	Total       int
	Available   int
	Offset      int
	HasMore     bool
	Preview     bool
	Counts      map[string]int
	GeneratedAt time.Time
}

// RankPage is Rank plus every filter's count and the snapshot build time,
// read from the same snapshot that produced the page. The Important chips
// used to ask for six extra single-row pages: the ranking already knows
// every count, so one page answers the list and its chips together.
func (s *Service) RankPage(ctx context.Context, filter Filter, limit, offset int) (Page, error) {
	if !filter.Valid() {
		return Page{}, fmt.Errorf("priority: unknown filter %q", filter)
	}
	if limit < 1 || limit > MaxLimit {
		return Page{}, fmt.Errorf("priority: limit must be between 1 and %d, got %d", MaxLimit, limit)
	}
	if offset < 0 {
		return Page{}, fmt.Errorf("priority: offset must be 0 or more, got %d", offset)
	}
	c, err := s.ranking(ctx)
	if err != nil {
		return Page{}, err
	}
	matched := make([]Item, 0, min(MaxCached, len(c.items)))
	for _, r := range c.items {
		if filter.matches(r.item) {
			matched = append(matched, r.item)
		}
	}
	available := len(matched)
	start := min(offset, available)
	end := min(start+limit, available)
	out := make([]Item, 0, end-start)
	out = append(out, matched[start:end]...)
	counts := make(map[string]int, len(c.counts))
	for f, n := range c.counts {
		counts[string(f)] = n
	}
	return Page{Items: out, Total: c.counts[filter], Available: available, Offset: start,
		HasMore: end < available, Preview: c.preview, Counts: counts, GeneratedAt: c.at}, nil
}
