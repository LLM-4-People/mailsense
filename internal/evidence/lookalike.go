package evidence

import (
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/locale"
)

// Look-alike kinds (signal lookalike_domain).
const (
	LookNone     = "none"
	LookSkeleton = "skeleton" // the same UTS #39 skeleton as a correspondent's domain (раураl.com, paypal.com)
	LookEdit     = "edit"     // within evidence.lookalike.max_edits edits of one (examp1e.com, example.com)
)

// lookalikes compares a sender's registrable domain with the domains the
// owner wrote to (and the owner's own): causally, a correspondent counts
// from the first time the owner wrote to it.
type lookalikes struct {
	since    map[string]time.Time // domain → first time the owner wrote to it (zero: the owner's own)
	skels    map[string]string    // domain → its skeleton (seal)
	maxEdits int
	minChars int
	cache    sync.Map // domain → []target
}

type target struct {
	kind  string
	since time.Time
}

func newLookalikes(maxEdits, minChars int) *lookalikes {
	return &lookalikes{since: map[string]time.Time{}, maxEdits: maxEdits, minChars: minChars}
}

// correspond records that the owner wrote to a domain at t (own: always).
func (l *lookalikes) correspond(domain string, t time.Time, own bool) {
	if domain == "" {
		return
	}
	if own {
		t = time.Time{}
	}
	if old, ok := l.since[domain]; !ok || t.Before(old) {
		l.since[domain] = t
	}
}

// seal computes the correspondents' skeletons once (before any at).
func (l *lookalikes) seal() {
	l.skels = make(map[string]string, len(l.since))
	for c := range l.since {
		l.skels[c] = locale.Skeleton(c)
	}
}

// at is the look-alike kind of a sender domain at t: none when the owner
// wrote to that very domain before, else the strongest kind among the
// correspondents known by then.
func (l *lookalikes) at(domain string, t time.Time) string {
	if s, ok := l.since[domain]; ok && s.Before(t) {
		return LookNone
	}
	best := LookNone
	for _, tg := range l.targets(domain) {
		if tg.since.Before(t) || tg.since.IsZero() {
			if tg.kind == LookSkeleton {
				return LookSkeleton
			}
			best = LookEdit
		}
	}
	return best
}

func (l *lookalikes) targets(domain string) []target {
	if v, ok := l.cache.Load(domain); ok {
		return v.([]target)
	}
	var out []target
	name, _, _ := strings.Cut(domain, ".")
	if len([]rune(name)) >= l.minChars {
		skel := locale.Skeleton(domain)
		for c, since := range l.since {
			switch {
			case c == domain:
			case l.skels[c] == skel:
				out = append(out, target{kind: LookSkeleton, since: since})
			case withinEdits([]rune(domain), []rune(c), l.maxEdits):
				out = append(out, target{kind: LookEdit, since: since})
			}
		}
	}
	v, _ := l.cache.LoadOrStore(domain, out)
	return v.([]target)
}

// withinEdits reports whether the optimal-string-alignment distance
// (insertions, deletions, substitutions, adjacent transpositions) of a
// and b is at most k (and more than 0).
func withinEdits(a, b []rune, k int) bool {
	if d := len(a) - len(b); d > k || -d > k || k <= 0 {
		return false
	}
	prev2, prev, cur := make([]int, len(b)+1), make([]int, len(b)+1), make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		low := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			low = min(low, cur[j])
		}
		if low > k {
			return false
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)] > 0 && prev[len(b)] <= k
}
