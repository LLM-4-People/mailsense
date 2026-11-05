// Package brief assembles the deterministic, evidence-linked Brief from read
// models other owners already built: commitments, conversation facts and the
// priority ranking. It is pure composition. It calls no model, starts no
// mailbox scan, extracts no promise from message text and invents no fact.
//
// The three source services own their own caches. The Brief also keeps one
// bounded stale-while-revalidate copy of its composed result, keyed by the
// store generation and the since boundary, so an expired Brief is served
// immediately while one background composition runs.
package brief

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Section bounds. The Brief is mostly a bounded working set; Topics is the
// one whole-mailbox census, with its mailbox total carried beside it.
const (
	DefaultSince   = 7 * 24 * time.Hour
	CommitmentScan = 500
	MustLimit      = 12
	WaitingLimit   = 8
	ChangedLimit   = conversations.DefaultRecent
	TopicLimit     = 12
	// CacheTTL bounds one composed Brief. An expired Brief is served
	// immediately while one background composition runs.
	CacheTTL = 15 * time.Second
	// cacheSize bounds the composed Briefs kept in memory; since is part of
	// the key, a client's moving boundary cannot grow it without bound.
	cacheSize = 16
)

// TopicScope names the evidence behind the topics section: the classifier's
// topic answer over every owner-facing message in the mailbox, not a sample.
const TopicScope = "owner_mailbox"

// ErrInvalidSince is the typed query failure the API maps to its 400
// contract.
var ErrInvalidSince = errors.New("brief: invalid since")

// Org is one external organization on the wire.
type Org struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Item is one evidence-linked Brief row. Kind is the commitment kind
// ("due", "reply" or "decision"), "activity", "organization" or "topic".
// Count and the count fields are item-specific; a field that does not apply
// is omitted from JSON.
type Item struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Title          string  `json:"title"`
	Subject        string  `json:"subject,omitempty"`
	Organization   *Org    `json:"organization,omitempty"`
	Person         string  `json:"person,omitempty"`
	Due            string  `json:"due,omitempty"`
	Status         string  `json:"status,omitempty"`
	Direction      string  `json:"direction,omitempty"`
	ConversationID string  `json:"conversation_id,omitempty"`
	MessageID      int64   `json:"message_id,omitempty"`
	Evidence       []int64 `json:"evidence"`
	LastAt         string  `json:"last_at,omitempty"`
	State          string  `json:"state,omitempty"`
	StateReason    string  `json:"state_reason,omitempty"`
	Count          int     `json:"count,omitempty"`
	// Overdue and StoredDue are the relationship row's open-loop partition:
	// how many of the organization's loops are overdue and how many carry the
	// stored follow-up source. They are zero for every non-relationship row.
	Overdue   int  `json:"overdue,omitempty"`
	StoredDue int  `json:"stored_due,omitempty"`
	Suggested bool `json:"suggested,omitempty"`
	// Action and Waiting are the one action classification of a commitment
	// row. Topic and Subtopic are the one subject hierarchy. Both are
	// identifiers; the screen owns their copy.
	Action    string `json:"action,omitempty"`
	Waiting   string `json:"waiting,omitempty"`
	DueSource string `json:"due_source,omitempty"`
	DueLevel  int    `json:"due_level,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Subtopic  string `json:"subtopic,omitempty"`
	// Subtopics is the nested breakdown of a topic row. It is empty for
	// every non-topic item.
	Subtopics []Item `json:"subtopics,omitempty"`
}

// Section is one Brief column. Items is always an array. Count is the
// section's honest total: the direction total for the commitment sections,
// the activity total for Changed, the organizations still carrying an open
// loop for Relationships and the classified message total for Topics. Total
// is the universe a census section measures; only Topics sets it today.
type Section struct {
	Count     int    `json:"count"`
	Items     []Item `json:"items"`
	Since     string `json:"since,omitempty"`
	Suggested bool   `json:"suggested"`
	Scope     string `json:"scope,omitempty"`
	Total     int    `json:"total,omitempty"`
}

// Result is the whole Brief. Every section is always present.
type Result struct {
	GeneratedAt   string  `json:"generated_at"`
	Must          Section `json:"must"`
	Waiting       Section `json:"waiting"`
	Changed       Section `json:"changed"`
	Relationships Section `json:"relationships"`
	Topics        Section `json:"topics"`
}

// Commitments is the open-loop read model: the flat deduped set and its
// per-organization grouping. One service owns both, so the relationship
// section and the commitment sections can never disagree.
type Commitments interface {
	List(ctx context.Context, f commitments.Filter) (commitments.Result, error)
	OpenLoops(ctx context.Context) (commitments.OrganizationOpenLoopsResult, error)
}

// Conversations is the conversation facts read model.
type Conversations interface {
	Facts(ctx context.Context, since string, recent int) (conversations.Facts, error)
}

// Priority is the ranked message read model and its whole-mailbox topic
// census. It is optional: without it the Topics section is empty rather
// than guessed.
type Priority interface {
	Topics(ctx context.Context) (priority.TopicCensus, error)
}

// Store is the app store's generation seam. It is optional; without it the
// cache is bounded by time only.
type Store interface {
	// MailGeneration moves on mail and folder writes; the Brief reads no
	// labels.
	MailGeneration() uint64
	// RouteGeneration moves when new route answers commit: the Brief's
	// commitments and priority sections read them.
	RouteGeneration() uint64
}

// Deps are the service collaborators. Commitments, Conversations and Clock
// are required; Priority, Store and Gate are optional. A nil Gate is preview
// off.
type Deps struct {
	Commitments   Commitments
	Conversations Conversations
	Priority      Priority
	Store         Store
	Gate          learning.Previewer
	Clock         clock.Clock
}

// Service composes the Brief from the three cached read models.
type Service struct {
	commitments   Commitments
	conversations Conversations
	priority      Priority
	store         Store
	gate          learning.Previewer
	clock         clock.Clock

	cached *viewcache.Cache[briefKey, Result]
}

// briefKey is one composed Brief. The generation and effective preview rule
// are part of the key, so a write or a preview-rule flip never reuses an old
// section set.
type briefKey struct {
	gen      uint64
	routeGen uint64
	since    string
	preview  bool
}

// New returns a Brief service. It does not read the store or call a source.
func New(d Deps) (*Service, error) {
	if d.Commitments == nil {
		return nil, errors.New("brief: a commitments service is required")
	}
	if d.Conversations == nil {
		return nil, errors.New("brief: a conversations service is required")
	}
	if d.Clock == nil {
		return nil, errors.New("brief: a clock is required")
	}
	s := &Service{
		commitments:   d.Commitments,
		conversations: d.Conversations,
		priority:      d.Priority,
		store:         d.Store,
		gate:          d.Gate,
		clock:         d.Clock,
	}
	s.cached = viewcache.New[briefKey, Result](viewcache.Options[briefKey, Result]{
		TTL: CacheTTL, Size: cacheSize, Now: s.clock.Now, Component: "brief",
		Keep: func(key briefKey, _ Result) bool {
			return s.store == nil || (key.gen == s.store.MailGeneration() && key.routeGen == s.store.RouteGeneration())
		},
	})
	return s, nil
}

// Close joins the background rebuild of the one cached Brief, bounded by ctx.
// It reports whether it joined: false means a store read outlived ctx, so the
// store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool { return s.cached.Close(ctx) }

// preview reports the one preview rule. Without a gate the preview is off.
func (s *Service) preview(ctx context.Context) bool {
	return s.gate != nil && s.gate.PreviewAnswers(ctx)
}

// Brief returns the deterministic Brief for one since boundary. An empty
// since means the last seven days; a value that is not an RFC3339/stored
// timestamp, or is in the future, is ErrInvalidSince. Only the Topics
// source may fail without failing the Brief: its section is then empty and
// the failure is logged.
func (s *Service) Brief(ctx context.Context, since string) (Result, error) {
	now := s.clock.Now().UTC()
	effective, err := effectiveSince(since, now)
	if err != nil {
		return Result{}, err
	}
	sinceStamp := mail.Stamp(effective)
	preview := s.preview(ctx)
	var gen, routeGen uint64
	if s.store != nil {
		gen, routeGen = s.store.MailGeneration(), s.store.RouteGeneration()
	}
	key := briefKey{gen: gen, routeGen: routeGen, since: sinceStamp, preview: preview}
	return s.cached.Get(ctx, key, func(ctx context.Context) (Result, error) {
		return s.build(ctx, sinceStamp)
	})
}

// build composes one Brief without touching the cache.
func (s *Service) build(ctx context.Context, sinceStamp string) (Result, error) {
	open, err := s.commitments.List(ctx, commitments.Filter{Limit: CommitmentScan})
	if err != nil {
		return Result{}, fmt.Errorf("brief: commitments: %w", err)
	}
	loops, err := s.commitments.OpenLoops(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("brief: open loops: %w", err)
	}
	facts, err := s.conversations.Facts(ctx, sinceStamp, ChangedLimit)
	if err != nil {
		return Result{}, fmt.Errorf("brief: conversations: %w", err)
	}
	return Result{
		GeneratedAt:   mail.Stamp(s.clock.Now()),
		Must:          commitmentSection(open, commitments.DirectionOwner, MustLimit),
		Waiting:       commitmentSection(open, commitments.DirectionOther, WaitingLimit),
		Changed:       changedSection(facts, sinceStamp),
		Relationships: relationshipSection(loops),
		Topics:        s.topicSection(ctx, facts.Messages),
	}, nil
}

// effectiveSince validates one request boundary. The default is seven days
// before now. Both RFC3339 and the stored mail layout are accepted, because
// the setting, the client and stored timestamps use the two forms; a future
// boundary would make "changed" describe mail that has not happened.
func effectiveSince(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		// Anchor the default window to the start of the UTC day. A boundary
		// that moved with every request would give every call a new cache
		// key and defeat stale-while-revalidate.
		return now.UTC().Truncate(24 * time.Hour).Add(-DefaultSince), nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		if stored, ok := mail.Parse(raw); ok {
			at = stored
		} else {
			return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidSince, raw)
		}
	}
	at = at.UTC()
	if at.After(now) {
		return time.Time{}, fmt.Errorf("%w: %q is in the future", ErrInvalidSince, raw)
	}
	return at, nil
}

// sectionCount is the one count rule of a section: it reports at least the
// items it shows. A source may under-report (a per-direction count map with no
// entry, a filtered list with no total), and a section that showed three rows
// while claiming two would break the UI's "1-3 of N" arithmetic. An honest
// total is never inflated.
func sectionCount(total, shown int) int { return max(total, shown) }

func commitmentSection(open commitments.Result, direction commitments.Direction, limit int) Section {
	items := make([]Item, 0, min(limit, len(open.Items)))
	for _, c := range open.Items {
		if c.Direction != direction || c.Status == commitments.StatusDone {
			continue
		}
		title := c.Title
		if title == "" {
			title = c.Subject
		}
		items = append(items, Item{
			ID:             c.ID,
			Kind:           string(c.Kind),
			Title:          title,
			Subject:        c.Subject,
			Organization:   commitmentOrg(c),
			Person:         c.Person,
			Due:            c.Due,
			Status:         string(c.Status),
			Direction:      string(c.Direction),
			ConversationID: c.ConversationID,
			MessageID:      c.MessageID,
			Evidence:       evidence(c.Evidence),
			LastAt:         c.LastAt,
			Action:         c.Action,
			Waiting:        c.Waiting,
			DueSource:      c.DueSource,
			DueLevel:       c.DueLevel,
			Topic:          c.Topic,
			Subtopic:       c.Subtopic,
		})
		if len(items) == limit {
			break
		}
	}
	return Section{Count: sectionCount(open.Counts[string(direction)], len(items)), Items: items}
}

func commitmentOrg(c commitments.Commitment) *Org {
	if c.OrganizationKey == "" {
		return nil
	}
	return &Org{Key: c.OrganizationKey, Label: c.OrganizationLabel}
}

func changedSection(facts conversations.Facts, since string) Section {
	items := make([]Item, 0, len(facts.Recent))
	for _, fact := range facts.Recent {
		items = append(items, Item{
			ID:             "activity:" + fact.ConversationID,
			Kind:           "activity",
			Title:          fact.Subject,
			Subject:        fact.Subject,
			Organization:   factOrg(fact.Organization),
			Person:         factPerson(fact),
			ConversationID: fact.ConversationID,
			MessageID:      fact.LastMessageID,
			Evidence:       []int64{fact.LastMessageID},
			LastAt:         fact.LastAt,
			State:          string(fact.State.State),
			StateReason:    string(fact.State.Reason),
		})
	}
	return Section{Count: sectionCount(facts.RecentTotal, len(items)), Items: items, Since: since}
}

// relationshipSection turns the commitments service's per-organization open
// loops into the Brief column. Every organization in the result carries at
// least one open loop; the section keeps the service's own order and its
// uncapped organization total, so a capped or under-reported source can never
// make count trail the rows shown.
func relationshipSection(loops commitments.OrganizationOpenLoopsResult) Section {
	items := make([]Item, 0, len(loops.Organizations))
	for _, org := range loops.Organizations {
		title := org.Label
		if title == "" {
			title = org.Key
		}
		items = append(items, Item{
			ID:           "organization:" + org.Key,
			Kind:         "organization",
			Title:        title,
			Organization: &Org{Key: org.Key, Label: org.Label},
			Count:        org.Total,
			Overdue:      org.Overdue,
			StoredDue:    org.StoredDue,
		})
	}
	return Section{Count: sectionCount(loops.Total, len(items)), Items: items}
}

func factOrg(org *conversations.Organization) *Org {
	if org == nil {
		return nil
	}
	return &Org{Key: org.Key, Label: org.Label}
}

func factPerson(fact conversations.Fact) string {
	if fact.SenderAddress != "" {
		return fact.SenderAddress
	}
	return fact.SenderName
}

func evidence(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return append([]int64{}, ids...)
}

// topicSection turns the priority service's whole-mailbox census into the
// Brief column. total is the owner-facing message count behind the mailbox;
// it is raised to the classified total if the two sources disagree, so the
// section can never claim more classified mail than the mailbox holds.
func (s *Service) topicSection(ctx context.Context, total int) Section {
	if total < 0 {
		total = 0
	}
	empty := Section{Items: []Item{}, Suggested: true, Scope: TopicScope, Total: total}
	if s.priority == nil {
		logx.For("brief").Warn("priority topics unavailable; returning an empty topics section", "reason", "priority not wired")
		return empty
	}
	census, err := s.priority.Topics(ctx)
	if err != nil {
		logx.For("brief").Warn("priority topics unavailable; returning an empty topics section", "err", err)
		return empty
	}
	if census.Scanned > total {
		total = census.Scanned
	}
	if census.Classified > total {
		total = census.Classified
	}

	items := make([]Item, 0, min(TopicLimit, len(census.Topics)))
	for _, topic := range census.Topics {
		if len(items) == TopicLimit {
			break
		}
		ids := topic.Evidence
		if len(ids) > priority.TopicEvidence {
			ids = ids[:priority.TopicEvidence]
		}
		subs := make([]Item, 0, len(topic.Subtopics))
		for _, sub := range topic.Subtopics {
			subIDs := sub.Evidence
			if len(subIDs) > priority.TopicEvidence {
				subIDs = subIDs[:priority.TopicEvidence]
			}
			subs = append(subs, Item{
				ID:        "subtopic:" + topic.Topic + "/" + sub.Subtopic,
				Kind:      "subtopic",
				Title:     sub.Subtopic,
				Topic:     topic.Topic,
				Subtopic:  sub.Subtopic,
				Evidence:  evidence(subIDs),
				Count:     sub.Count,
				Suggested: true,
			})
		}
		items = append(items, Item{
			ID:        "topic:" + topic.Topic,
			Kind:      "topic",
			Title:     topic.Topic,
			Topic:     topic.Topic,
			Evidence:  evidence(ids),
			Count:     topic.Count,
			Suggested: true,
			Subtopics: subs,
		})
	}
	return Section{Count: sectionCount(census.Classified, len(items)), Items: items, Suggested: true, Scope: TopicScope, Total: total}
}
