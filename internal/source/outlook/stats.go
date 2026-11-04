package outlook

import (
	"maps"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Stats are the connector's cumulative measurements since it was created:
// what each phase cost, so the cost model in docs/OUTLOOK_SYNC.md is
// measured, not guessed. Durations are wall time on the apartment thread.
type Stats struct {
	mu sync.Mutex

	Calls   int64                 `json:"calls"`   // guarded COM calls (retries included)
	Retries int64                 `json:"retries"` // calls declined as busy and retried
	Members map[string]MemberCost `json:"members"` // per Class.Member
	Pace    PaceStats             `json:"pace"`    // waits for the pace (pace.go)

	Discover struct {
		Runs        int            `json:"runs"`
		Stores      int            `json:"stores"`
		StoreErrors int            `json:"store_errors"`
		Folders     int            `json:"folders"`
		Skipped     map[string]int `json:"skipped"`      // folders left out, by reason
		NewestReads int            `json:"newest_reads"` // sorted 1-row tables (no commit time)
		NewestFails int            `json:"newest_fails"` // those that failed (folder treated as changed)
		Time        time.Duration  `json:"time"`
	} `json:"discover"`

	Sync struct {
		Folders       int           `json:"folders"`
		Unchanged     int           `json:"unchanged"` // skipped by the cursor, no COM call
		SnapshotRows  int           `json:"snapshot_rows"`
		SnapshotTime  time.Duration `json:"snapshot_time"`
		TableRows     int           `json:"table_rows"` // rows read in full-column passes
		Emitted       int           `json:"emitted"`    // partial messages delivered
		TableTime     time.Duration `json:"table_time"`
		SortFallbacks int           `json:"sort_fallbacks"` // Sort refused: unsorted full read
		Incomplete    int           `json:"incomplete"`     // snapshot ≠ content count (no removals)
	} `json:"sync"`

	Details struct {
		Items            int            `json:"items"`
		Failed           int            `json:"failed"`
		Failures         map[string]int `json:"failures"` // by HRESULT
		Absent           map[string]int `json:"absent"`   // members the item's class lacks (read as empty)
		Open             time.Duration  `json:"open"`     // GetItemFromID
		Body             time.Duration  `json:"body"`
		Props            time.Duration  `json:"props"` // one GetProperties
		Recipients       time.Duration  `json:"recipients"`
		RecipientCount   int            `json:"recipient_count"`
		RecipientSMTPRow int            `json:"recipient_smtp_row"` // SMTP read from the recipient row itself
		BodyChars        int64          `json:"body_chars"`
		BodyTruncated    int            `json:"body_truncated"`
		HeadersTruncated int            `json:"headers_truncated"`
		MaxHeaderChars   int            `json:"max_header_chars"`
		// ByKind splits items and total time by store kind (cached primary
		// vs online delegate mailboxes differ a lot).
		ByKind map[mail.StoreKind]*KindCost `json:"by_kind"`
	} `json:"details"`

	Resolve struct {
		CacheHits  int `json:"cache_hits"`
		Resolved   int `json:"resolved"`
		Unresolved int `json:"unresolved"` // DN kept
	} `json:"resolve"`
}

// KindCost is the detail-pass cost for one store kind.
type KindCost struct {
	Items int           `json:"items"`
	Time  time.Duration `json:"time"`
}

func newStats() *Stats {
	s := &Stats{}
	s.Details.Failures = map[string]int{}
	s.Details.Absent = map[string]int{}
	s.Details.ByKind = map[mail.StoreKind]*KindCost{}
	s.Discover.Skipped = map[string]int{}
	return s
}

func (s *Stats) add(fn func(*Stats)) {
	s.mu.Lock()
	fn(s)
	s.mu.Unlock()
}

// Snapshot returns a copy safe to read while the connector runs.
func (s *Stats) Snapshot() *Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &Stats{Calls: s.Calls, Retries: s.Retries, Discover: s.Discover, Sync: s.Sync, Details: s.Details, Resolve: s.Resolve}
	c.Details.Failures = maps.Clone(s.Details.Failures)
	c.Details.Absent = maps.Clone(s.Details.Absent)
	c.Discover.Skipped = maps.Clone(s.Discover.Skipped)
	c.Details.ByKind = map[mail.StoreKind]*KindCost{}
	for k, v := range s.Details.ByKind {
		cp := *v
		c.Details.ByKind[k] = &cp
	}
	return c
}

// Phase is published on TopicPhase when a phase of one folder or store ends.
type Phase struct {
	Source        string         `json:"source"`
	Phase         source.Phase   `json:"phase"`     // discover, discover-store, snapshot or table
	StoreKey      string         `json:"store_key"` // settings key of the store (stable; names are localized)
	Store         string         `json:"store"`
	Kind          mail.StoreKind `json:"kind,omitempty"`
	Folder        string         `json:"folder,omitempty"`
	Rows          int            `json:"rows"`
	Calls         int64          `json:"calls"`
	Duration      time.Duration  `json:"duration"`
	RowsPerSecond float64        `json:"rows_per_second"` // Rows / Duration
}

// TopicPhase carries per-phase measurements of Outlook connectors.
var TopicPhase = events.NewTopic[Phase]("outlook.phase")
