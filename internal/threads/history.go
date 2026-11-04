package threads

import (
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Point is the owner's standing toward a correspondent at an instant,
// computed only from mail dated strictly before it: later mail, later
// replies and the message at that instant itself can never change it.
type Point struct {
	Received int `json:"received"` // inbox messages before the instant
	Replied  int `json:"replied"`  // of those, answered before the instant
	// Bucket is the fine bucket (History bounds) of the median response
	// time of those replies (mathx.Median: the average of the two middle
	// values for an even count); -1 when there is none.
	Bucket   int       `json:"bucket"`
	Examples []Example `json:"examples,omitempty"` // latest replies before the instant, newest first
}

// History answers causal standings for any instant (O(log n) a query).
type History struct {
	bounds   []float64 // fine response-time bounds in minutes, ascending
	examples int
	senders  map[string]*timeline
	domains  map[string]*timeline
	overall  *timeline
	groups   mail.Domains // the organisation key of a sender
}

type replyEvent struct {
	at      time.Time
	minutes float64
	example Example
}

// timeline is one correspondent's arrivals, the owner's replies and the
// owner's own messages to it, each sorted by time, with per-bucket prefix
// counts of the replies and the instants a calendar year first held mail
// both ways.
type timeline struct {
	arrivals []time.Time
	replies  []replyEvent
	prefix   [][]int // prefix[k][b]: replies among the first k in fine bucket b
	sent     []time.Time
	both     []time.Time // per calendar year (UTC) with mail both ways: when the second direction first appeared in it
}

// KnownHistory indexes the analysis for causal queries over the replies
// as paired at their own instants (KnownReplies): no later message can
// re-pair one, so every standing it answers is causal — the one history
// of evidence and triage (the filed view's history over the final
// conversations was removed with triage.causal_threads, lead decision
// 2026-10-15). bounds are the fine bucket bounds (minutes, ascending;
// callers merge every scale they band on), examples the replies kept per
// point.
func (a *Analysis) KnownHistory(bounds []float64, examples int, zone *time.Location, groups mail.Domains) *History {
	replies := a.KnownReplies
	h := &History{bounds: slices.Clone(bounds), examples: examples, senders: map[string]*timeline{}, domains: map[string]*timeline{},
		overall: &timeline{}, groups: groups}
	for i := range a.Messages {
		m := &a.Messages[i]
		if !m.Dated() || a.copyOf[i] >= 0 { // a copy is its original, counted once
			continue
		}
		if m.Role == mail.RoleSent {
			h.addSent(m)
			continue
		}
		var ev *replyEvent
		if r, ok := replies[m.RowID]; ok {
			ev = &replyEvent{at: m.At.Add(time.Duration(r.Seconds * float64(time.Second))), minutes: r.Seconds / 60,
				example: newExample(m, r, zone)}
		}
		for _, t := range []*timeline{h.into(h.senders, m.Sender), h.into(h.domains, m.Domain), h.overall} {
			if t != nil {
				t.arrivals = append(t.arrivals, m.At)
				if ev != nil {
					t.replies = append(t.replies, *ev)
				}
			}
		}
	}
	for _, t := range append(append(slices.Collect(maps.Values(h.senders)), slices.Collect(maps.Values(h.domains))...), h.overall) {
		t.seal(h.bounds)
	}
	return h
}

// addSent records the owner's message to each of its recipients and
// their organisations (once per organisation).
func (h *History) addSent(m *Message) {
	orgs := map[string]bool{}
	for _, r := range m.Recipients {
		if t := h.into(h.senders, r); t != nil {
			t.sent = append(t.sent, m.At)
		}
		if o := h.groups.Of(r); o != "" && !orgs[o] {
			orgs[o] = true
			h.into(h.domains, o).sent = append(h.into(h.domains, o).sent, m.At)
		}
	}
}

func (h *History) into(set map[string]*timeline, key string) *timeline {
	if key == "" {
		return nil
	}
	if set[key] == nil {
		set[key] = &timeline{}
	}
	return set[key]
}

func (t *timeline) seal(bounds []float64) {
	slices.SortStableFunc(t.arrivals, func(a, b time.Time) int { return a.Compare(b) })
	slices.SortStableFunc(t.replies, func(a, b replyEvent) int { return a.at.Compare(b.at) })
	slices.SortStableFunc(t.sent, func(a, b time.Time) int { return a.Compare(b) })
	t.both = twoWayYears(t.arrivals, t.sent)
	t.prefix = make([][]int, len(t.replies)+1)
	t.prefix[0] = make([]int, len(bounds)+1)
	for k, r := range t.replies {
		row := slices.Clone(t.prefix[k])
		row[BucketIndex(r.minutes, bounds)]++
		t.prefix[k+1] = row
	}
}

// point answers the standing of a timeline at an instant.
func (h *History) point(t *timeline, at time.Time) (Point, bool) {
	if t == nil {
		return Point{}, false
	}
	received := before(t.arrivals, at)
	if received == 0 {
		return Point{}, false
	}
	k := sort.Search(len(t.replies), func(i int) bool { return !t.replies[i].at.Before(at) })
	p := Point{Received: received, Replied: k, Bucket: h.medianBucket(t, k)}
	for i := k - 1; i >= 0 && len(p.Examples) < h.examples; i-- {
		p.Examples = append(p.Examples, t.replies[i].example)
	}
	return p, true
}

// medianBucket is the bucket of the median of the first k replies: the
// bucket of the middle one (odd k), or of the average of the two middle
// ones. Those share a bucket unless they straddle a bound; then the lower
// middle is the largest of its bucket and the upper the smallest of the
// next non-empty one, found in one scan (O(k), only for straddles).
func (h *History) medianBucket(t *timeline, k int) int {
	if k == 0 {
		return -1
	}
	lo, hi := t.bucketOf(k, (k-1)/2), t.bucketOf(k, k/2)
	if lo == hi {
		return lo
	}
	low, high := -1.0, -1.0
	for _, r := range t.replies[:k] {
		switch BucketIndex(r.minutes, h.bounds) {
		case lo:
			low = max(low, r.minutes)
		case hi:
			if high < 0 || r.minutes < high {
				high = r.minutes
			}
		}
	}
	return BucketIndex((low+high)/2, h.bounds)
}

// bucketOf is the bucket of the i-th smallest (0-based) of the first k replies.
func (t *timeline) bucketOf(k, i int) int {
	cum := 0
	for b, n := range t.prefix[k] {
		if cum += n; cum > i {
			return b
		}
	}
	return -1
}

// Sender is the standing toward an address before at.
func (h *History) Sender(address string, at time.Time) (Point, bool) {
	return h.point(h.senders[mail.NormalizeAddress(address)], at)
}

// Domain is the standing toward the organisation of address before at
// (none for a shared domain: mail.Domains).
func (h *History) Domain(address string, at time.Time) (Point, bool) {
	d := h.groups.Of(address)
	if d == "" {
		return Point{}, false
	}
	return h.point(h.domains[d], at)
}

// Overall is the standing toward all incoming mail before at.
func (h *History) Overall(at time.Time) (Point, bool) { return h.point(h.overall, at) }

// Bounds are the fine bucket bounds in minutes.
func (h *History) Bounds() []float64 { return slices.Clone(h.bounds) }

// Exchange is the owner's exchange with a correspondent before an
// instant, both ways: messages received from it and sent to it, the
// first and latest of them (zero when none), and the calendar years (UTC)
// with mail both ways.
type Exchange struct {
	Received, Sent int
	First, Last    time.Time
	Years          int
}

// AddressExchange is the exchange with an address before at.
func (h *History) AddressExchange(address string, at time.Time) Exchange {
	return h.senders[mail.NormalizeAddress(address)].exchange(at)
}

// OrganisationExchange is the exchange with an organisation key
// (mail.Domains.Of) before at.
func (h *History) OrganisationExchange(org string, at time.Time) Exchange {
	return h.domains[org].exchange(at)
}

func (t *timeline) exchange(at time.Time) Exchange {
	if t == nil {
		return Exchange{}
	}
	x := Exchange{Received: before(t.arrivals, at), Sent: before(t.sent, at), Years: before(t.both, at)}
	for _, l := range [][]time.Time{t.arrivals[:x.Received], t.sent[:x.Sent]} {
		if len(l) == 0 {
			continue
		}
		if x.First.IsZero() || l[0].Before(x.First) {
			x.First = l[0]
		}
		if l[len(l)-1].After(x.Last) {
			x.Last = l[len(l)-1]
		}
	}
	return x
}

// before counts the instants of a sorted list strictly before t.
func before(ts []time.Time, t time.Time) int {
	return sort.Search(len(ts), func(i int) bool { return !ts[i].Before(t) })
}

// twoWayYears is, per calendar year (UTC) with mail both ways, the
// instant the second direction first appeared in it, sorted.
func twoWayYears(recv, sent []time.Time) []time.Time {
	first := func(ts []time.Time) map[int]time.Time {
		out := map[int]time.Time{}
		for _, t := range ts {
			if _, ok := out[t.UTC().Year()]; !ok {
				out[t.UTC().Year()] = t
			}
		}
		return out
	}
	r, s := first(recv), first(sent)
	var out []time.Time
	for y, a := range r {
		if b, ok := s[y]; ok {
			out = append(out, later(a, b))
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
