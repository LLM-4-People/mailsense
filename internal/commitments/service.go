// Package commitments builds the Brief's open-loop read model: the owner's
// due follow-ups, the conversations that still owe the owner a reply, and the
// mail that asks for a decision. It merges evidence the rest of the app
// already owns and never derives a promise from message text.
//
// v1 boundary: every item is backed by a stored fact — a message follow-up
// date, a resolved conversation state, or the decision signal of the message
// ranker. There is no text-extracted promise, no model call and no invented
// deadline. A future extraction layer may add stored promises as another
// source; until then, an item without one of those three facts does not
// exist.
//
// The service is deterministic: the same stored state and the same clock
// produce the same items, ids, order and counts. Items dedupe by a stable id
// derived from their evidence, and each carries the message ids it came from.
package commitments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Kind is the evidence behind one commitment.
type Kind string

// Commitment kinds.
const (
	KindDue      Kind = "due"      // a stored follow-up date the owner set
	KindReply    Kind = "reply"    // a conversation state the owner owes an answer
	KindDecision Kind = "decision" // a message the ranker flags as needing a decision
	KindAction   Kind = "action"   // another concrete owner action: pay, send, schedule, do, verify
)

// Kinds is the one ordered list of kinds: it drives validation, counts and
// the deterministic kind tie-break.
var Kinds = []Kind{KindDue, KindReply, KindDecision, KindAction}

// Direction says who must act.
type Direction string

// Commitment directions.
const (
	DirectionOwner Direction = "owner" // the owner owes or acts
	DirectionOther Direction = "other" // the owner is waiting on someone else
)

// Directions is the one ordered list of directions.
var Directions = []Direction{DirectionOwner, DirectionOther}

// Status is the effective state of one commitment.
type Status string

// Commitment statuses. Done is reserved for the extraction layer that will
// store completed promises; the current sources only produce open or
// overdue. Unknown marks evidence that cannot decide, and is likewise
// reserved: an item that cannot be evidenced is omitted, never guessed.
const (
	StatusOpen    Status = "open"
	StatusOverdue Status = "overdue"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
)

// Statuses is the one ordered list of statuses.
var Statuses = []Status{StatusOpen, StatusOverdue, StatusDone, StatusUnknown}

// Commitment is one open loop as the Brief serves it. Due and LastAt use the
// store's timestamp form (UTC, second precision). Evidence lists the
// owner-visible message ids that justify the item; it is never empty.
//
// Person is the counterpart the evidence names: normally a deadline or thread
// participant address, or the decision sender's address, falling back to the
// stored display name when no address exists. Callers that need a richer
// directory entry can resolve the address; keeping it here makes the item
// linkable and stable.
type Commitment struct {
	ID                string    `json:"id"`
	Kind              Kind      `json:"kind"`
	Direction         Direction `json:"direction"`
	Status            Status    `json:"status"`
	Title             string    `json:"title"`
	Subject           string    `json:"subject"`
	Due               string    `json:"due,omitempty"`
	ConversationID    string    `json:"conversation_id,omitempty"`
	MessageID         int64     `json:"message_id,omitempty"`
	OrganizationKey   string    `json:"organization_key,omitempty"`
	OrganizationLabel string    `json:"organization_label,omitempty"`
	Person            string    `json:"person,omitempty"`
	LastAt            string    `json:"last_at,omitempty"`
	Evidence          []int64   `json:"evidence"`
	// Action and Waiting are the unified action classification. Kind stays
	// the evidence family (due, reply, decision, action); Action names the
	// concrete step and Waiting says who must move.
	Action    string `json:"action,omitempty"`
	Waiting   string `json:"waiting,omitempty"`
	DueSource string `json:"due_source,omitempty"`
	DueLevel  int    `json:"due_level,omitempty"`
	// Topic and Subtopic are the subject hierarchy of the evidence.
	Topic    string `json:"topic,omitempty"`
	Subtopic string `json:"subtopic,omitempty"`
}

// Filter narrows one List request. Empty fields mean no narrowing. Limit is
// the page size; zero uses DefaultLimit and values above MaxLimit are
// clamped.
type Filter struct {
	Kind      Kind      `json:"kind,omitempty"`
	Direction Direction `json:"direction,omitempty"`
	Status    Status    `json:"status,omitempty"`
	Limit     int       `json:"limit,omitempty"`
}

// Result is one page plus the facet counts the Brief shows. Counts cover the
// whole deduped set before Filter and Limit, so a facet can display the size
// of the other choices.
type Result struct {
	Items       []Commitment   `json:"items"`
	Counts      map[string]int `json:"counts"`
	GeneratedAt string         `json:"generated_at"`
}

// OrganizationItemCap bounds the Items list of one OrganizationOpenLoops row.
// The organization's Total and partition counts stay uncapped so a consumer
// can state "20 of 27 shown" honestly instead of presenting the cap as the
// total.
const OrganizationItemCap = 20

// OrganizationOpenLoops is one counterparty's share of the deduped set:
// every open loop whose evidence names that organization. Owner, Other,
// Overdue and StoredDue partition Total; Kinds always carries every kind key
// (zero when absent), and Items is never null.
//
// StoredDue counts items whose surviving evidence carries the stored
// follow-up source, which is not the same set as Kinds["due"]: dedupe keeps
// the strongest kind and can fold a stored due date into a reply, decision or
// action row, and a due item may have the source without a date.
type OrganizationOpenLoops struct {
	Key       string         `json:"key"`
	Label     string         `json:"label"`
	Total     int            `json:"total"`
	Owner     int            `json:"owner"`
	Other     int            `json:"other"`
	Overdue   int            `json:"overdue"`
	StoredDue int            `json:"stored_due"`
	Kinds     map[string]int `json:"kinds"`
	Items     []Commitment   `json:"items"`
}

// OrganizationOpenLoopsResult is the whole deduped set grouped by
// counterparty, in the deterministic order indexOpenLoops defines. Total is
// the number of organizations with at least one open loop, not the number of
// loops; each organization's own Total carries its loop count.
type OrganizationOpenLoopsResult struct {
	GeneratedAt   string                  `json:"generated_at"`
	Total         int                     `json:"total"`
	Organizations []OrganizationOpenLoops `json:"organizations"`
}

// Page bounds.
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// Errors the API maps to its 400 contract. Kinds, directions and statuses are
// a closed set; a caller may pass an empty value to leave that facet
// unfiltered. ErrInvalidLimit is the page-size failure for adapters that
// enforce the explicit 1…MaxLimit request bound; the service itself keeps its
// documented clamp (zero uses DefaultLimit, values above MaxLimit clamp).
var (
	ErrInvalidKind      = errors.New("commitments: invalid kind")
	ErrInvalidDirection = errors.New("commitments: invalid direction")
	ErrInvalidStatus    = errors.New("commitments: invalid status")
	ErrInvalidLimit     = errors.New("commitments: invalid limit")
)

// Read windows and evidence caps. The due window keeps recently missed
// follow-ups visible without turning the Brief into an archive, and bounds
// the planning horizon. Replies are capped at the newest candidates because
// the Brief is a working set; decisions use the ranker's own bounded page.
const (
	dueLookback     = 30 * 24 * time.Hour
	dueHorizon      = 90 * 24 * time.Hour
	replyCandidates = 2000
	decisionLimit   = 200
	actionLimit     = 200
)

// CacheTTL is how long one built set is fresh. A stale set is served while a
// single background refresh runs, so no request waits for a store read.
const CacheTTL = 15 * time.Second

// cacheMaxBytes bounds the estimated retained bytes of the one built set: the
// deduped items dominate (the read windows above cap them), so 4 MiB fits
// every plausible set while still capping a pathological mailbox.
const cacheMaxBytes = 4 << 20

// The background rebuild's bound is viewcache's default (two minutes), so a
// stuck read cannot keep the refresh flag set forever.

// Store is the world-model read side the service needs.
type Store interface {
	DueDeadlines(ctx context.Context, since, until time.Time) ([]agenda.Deadline, error)
	ThreadSummaries(ctx context.Context, conversationIDs []string) (map[string]agenda.ThreadSummary, error)
	CompanyBook(ctx context.Context) (*store.CompanyBook, error)
	// MailGeneration moves on mail and folder writes; the commitment set
	// reads no labels.
	MailGeneration() uint64
	// RouteGeneration moves when new route answers commit: the decision
	// source reads them, so a cached set must not outlive that commit.
	RouteGeneration() uint64
}

// Threads is the one conversation-truth resolver. The priority service
// implements it, so a reply item cannot disagree with Agenda or the Reply
// filter.
type Threads interface {
	ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error)
}

// Decisions is the ranked decision source. The priority service implements
// it; the commitment service asks only for live owner decisions and a
// bounded page, never for the API's screen-sized ranking page.
type Decisions interface {
	Decisions(ctx context.Context, limit int) ([]priority.Item, error)
}

// Actions is the ranked concrete-action source. It returns the owner actions
// the ranking cache holds, already classified by internal/action; the
// commitment service never re-scores a message.
type Actions interface {
	Actions(ctx context.Context, limit int) ([]priority.Item, error)
}

// The production collaborators implement the narrow read sides.
var (
	_ Store     = (*store.Store)(nil)
	_ Threads   = (*priority.Service)(nil)
	_ Decisions = (*priority.Service)(nil)
	_ Actions   = (*priority.Service)(nil)
)

// Deps are the service collaborators. Store is required. A nil Threads,
// Decisions or Actions omits that source instead of inventing one, which lets
// a caller wire the evidence it has while the other services start.
type Deps struct {
	Store     Store
	Threads   Threads
	Decisions Decisions
	Actions   Actions
	Clock     clock.Clock
}

// Service is the cached, evidence-linked commitments read model.
type Service struct {
	store     Store
	threads   Threads
	decisions Decisions
	actions   Actions
	clock     clock.Clock

	// cache holds the one built set under a constant key. Its value carries
	// the generations it read, so Keep can reject an install that raced a
	// write while the installed set stays served.
	cache *viewcache.Cache[struct{}, snapshot]
}

// snapshot is one immutable built set, valid for the generation it read.
type snapshot struct {
	gen      uint64
	routeGen uint64
	at       time.Time
	items    []Commitment
	counts   map[string]int
	loops    []OrganizationOpenLoops
}

// Per-row estimates behind weighSnapshot: a commitment row is a dozen short
// strings plus its evidence ids (counted exactly); a map entry is one header.
const (
	commitmentRowBytes  = 256
	commitmentMapBytes  = 64
	commitmentLoopBytes = 256
)

// weighSnapshot estimates one built set from its slices and maps. The
// organization rollups copy row headers and their bounded item lists, so they
// are counted beside the full item list: shared strings are counted twice,
// which keeps the estimate an upper bound.
func weighSnapshot(s snapshot) int {
	n := 512
	for _, item := range s.items {
		n += commitmentRowBytes + len(item.Evidence)*8
	}
	n += len(s.counts) * commitmentMapBytes
	for _, org := range s.loops {
		n += commitmentLoopBytes + len(org.Kinds)*commitmentMapBytes + len(org.Items)*commitmentRowBytes
	}
	return n
}

// New returns a commitments service. It does not read the store.
func New(d Deps) *Service {
	s := &Service{
		store:     d.Store,
		threads:   d.Threads,
		decisions: d.Decisions,
		actions:   d.Actions,
		clock:     clock.Or(d.Clock),
	}
	s.cache = viewcache.New[struct{}, snapshot](viewcache.Options[struct{}, snapshot]{
		TTL: CacheTTL, Size: 1, MaxBytes: cacheMaxBytes, Weigh: weighSnapshot,
		Now: s.clock.Now, Component: "commitments",
		// The key is constant, so a generation move is not a new entry: the
		// installed set is served stale while one refresh runs, and this gate
		// refuses to install a set built for the moved-from generation.
		Keep: func(_ struct{}, value snapshot) bool {
			return value.gen == s.store.MailGeneration() && value.routeGen == s.store.RouteGeneration()
		},
	})
	return s
}

// Close joins the background rebuild of the one cached set, bounded by ctx.
// It reports whether it joined: false means a store read outlived ctx, so the
// store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool { return s.cache.Close(ctx) }

// List returns the filtered page, the facet counts of the whole set, and the
// instant the set was built. A nil store is an error; unknown filter values
// are rejected rather than silently returning nothing.
func (s *Service) List(ctx context.Context, f Filter) (Result, error) {
	if s == nil || s.store == nil {
		return Result{}, errors.New("commitments: a store is required")
	}
	norm, err := normalizeFilter(f)
	if err != nil {
		return Result{}, err
	}
	gen, routeGen := s.store.MailGeneration(), s.store.RouteGeneration()
	now := s.clock.Now()
	c, err := s.load(ctx, gen, routeGen, now)
	if err != nil {
		return Result{}, err
	}
	items := filterItems(c.items, norm)
	if len(items) > norm.Limit {
		items = items[:norm.Limit]
	}
	return Result{
		Items:       cloneItems(items),
		Counts:      maps.Clone(c.counts),
		GeneratedAt: c.at.UTC().Format(time.RFC3339),
	}, nil
}

// OpenLoops returns the same deduped set grouped by counterparty, in the
// order indexOpenLoops defines. It reads the one cache List reads: one build
// serves both, so a company's open loops and the commitment list can never
// disagree about what the set holds.
func (s *Service) OpenLoops(ctx context.Context) (OrganizationOpenLoopsResult, error) {
	if s == nil || s.store == nil {
		return OrganizationOpenLoopsResult{}, errors.New("commitments: a store is required")
	}
	gen, routeGen := s.store.MailGeneration(), s.store.RouteGeneration()
	now := s.clock.Now()
	c, err := s.load(ctx, gen, routeGen, now)
	if err != nil {
		return OrganizationOpenLoopsResult{}, err
	}
	return OrganizationOpenLoopsResult{
		GeneratedAt:   c.at.UTC().Format(time.RFC3339),
		Total:         len(c.loops),
		Organizations: cloneOpenLoops(c.loops),
	}, nil
}

// load reads or builds the one cached set. The build is pinned to the
// generations and the instant the caller sampled before the read, so the
// refreshed entry and the served-stale entry stay coherent.
func (s *Service) load(ctx context.Context, gen, routeGen uint64, now time.Time) (snapshot, error) {
	return s.cache.Get(ctx, struct{}{}, func(ctx context.Context) (snapshot, error) {
		built, err := s.build(ctx, gen, routeGen, now)
		if err != nil {
			return snapshot{}, err
		}
		return *built, nil
	})
}

// build computes one complete set without touching the cache or its lock.
// Every enabled source is read; a failure fails the build rather than
// dropping a kind, because a Brief that silently hides obligations
// misrepresents the day. The returned snapshot is immutable.
func (s *Service) build(ctx context.Context, gen, routeGen uint64, now time.Time) (*snapshot, error) {
	book, err := s.store.CompanyBook(ctx)
	if err != nil {
		return nil, fmt.Errorf("commitments: company book: %w", err)
	}
	due, err := s.dueItems(ctx, now, book)
	if err != nil {
		return nil, err
	}
	replies, err := s.replyItems(ctx, book)
	if err != nil {
		return nil, err
	}
	decisions, err := s.decisionItems(ctx, book)
	if err != nil {
		return nil, err
	}
	actions, err := s.actionItems(ctx, book)
	if err != nil {
		return nil, err
	}

	items := make([]Commitment, 0, len(due)+len(replies)+len(decisions)+len(actions))
	items = append(items, due...)
	items = append(items, replies...)
	items = append(items, decisions...)
	items = append(items, actions...)
	items = dedupe(items)
	sortItems(items)
	return &snapshot{gen: gen, routeGen: routeGen, at: now, items: items,
		counts: countItems(items), loops: indexOpenLoops(items)}, nil
}

// dueItems reads stored follow-up dates inside the v1 window. A due date
// before now is overdue; a missing date is open, never overdue.
func (s *Service) dueItems(ctx context.Context, now time.Time, book *store.CompanyBook) ([]Commitment, error) {
	deadlines, err := s.store.DueDeadlines(ctx, now.Add(-dueLookback), now.Add(dueHorizon))
	if err != nil {
		return nil, fmt.Errorf("commitments: due deadlines: %w", err)
	}
	out := make([]Commitment, 0, len(deadlines))
	for _, d := range deadlines {
		if d.MessageID == 0 {
			continue // no evidence to link or open
		}
		status := StatusOpen
		if !d.Due.IsZero() && d.Due.Before(now) {
			status = StatusOverdue
		}
		due := stamp(d.Due)
		orgKey, orgLabel, person := counterpart(book, d.Participants, d.SenderName)
		out = append(out, Commitment{
			ID:                itemID(KindDue, DirectionOwner, d.MessageID, d.ConversationID, due),
			Kind:              KindDue,
			Direction:         DirectionOwner,
			Status:            status,
			Title:             d.Subject,
			Subject:           d.Subject,
			Due:               due,
			ConversationID:    d.ConversationID,
			MessageID:         d.MessageID,
			OrganizationKey:   orgKey,
			OrganizationLabel: orgLabel,
			Person:            person,
			Evidence:          []int64{d.MessageID},
			Action:            string(action.FollowUp),
			Waiting:           string(action.WaitingOwner),
			DueSource:         string(action.DueStored),
		})
	}
	return out, nil
}

// replyItems reads the resolved conversation truth, keeps the newest
// candidates and enriches them with the owner-safe summaries. A state with no
// visible summary or no evidence message is omitted: it has no subject,
// person or link the Brief could honestly show.
func (s *Service) replyItems(ctx context.Context, book *store.CompanyBook) ([]Commitment, error) {
	if s.threads == nil {
		return nil, nil
	}
	states, err := s.threads.ThreadStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("commitments: thread states: %w", err)
	}
	ids := newestReplyIDs(states, replyCandidates)
	if len(ids) == 0 {
		return nil, nil
	}
	summaries, err := s.store.ThreadSummaries(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("commitments: thread summaries: %w", err)
	}
	out := make([]Commitment, 0, len(ids))
	for _, conv := range ids {
		state := states[conv]
		sum, ok := summaries[conv]
		if !ok {
			continue
		}
		evidenceID := state.LastMessageID
		if evidenceID == 0 {
			evidenceID = sum.MessageID
		}
		if evidenceID == 0 {
			continue
		}
		direction := DirectionOwner
		act, waiting := string(action.Reply), string(action.WaitingOwner)
		switch state.State {
		case threadintel.AwaitingOwner:
		case threadintel.AwaitingOther:
			direction = DirectionOther
			act, waiting = string(action.Wait), string(action.WaitingOther)
		default:
			continue
		}
		lastAt := state.LastAt
		if lastAt == "" {
			lastAt = sum.LastAt
		}
		// The counterpart is always the other side. For awaiting_owner that
		// is the sender of the inbound request; for awaiting_other it is the
		// participant the owner wrote to, never the owner's own actor.
		fallback := state.OwnerActorAddress
		if fallback == "" {
			fallback = state.OwnerActorName
		}
		if direction == DirectionOther {
			fallback = firstParticipant(sum.Participants)
		}
		orgKey, orgLabel, person := counterpart(book, sum.Participants, fallback)
		out = append(out, Commitment{
			ID:                itemID(KindReply, direction, evidenceID, conv, ""),
			Kind:              KindReply,
			Direction:         direction,
			Status:            StatusOpen,
			Title:             sum.Subject,
			Subject:           sum.Subject,
			ConversationID:    conv,
			MessageID:         evidenceID,
			OrganizationKey:   orgKey,
			OrganizationLabel: orgLabel,
			Person:            person,
			LastAt:            lastAt,
			Evidence:          []int64{evidenceID},
			Action:            act,
			Waiting:           waiting,
			Topic:             state.Topic,
			Subtopic:          state.Subtopic,
		})
	}
	return out, nil
}

// commitmentFromRanked builds the commitment both ranker-backed sources
// describe. Both read one ranked message, so every message-derived field —
// subject, counterpart, received instant, evidence and the action class — is
// one rule here. The caller names only what the ranker does not own: the
// evidence family (kind) and who acts (direction). The action classification
// stays the ranker's answer; this package never derives it from the kind.
func commitmentFromRanked(kind Kind, direction Direction, it priority.Item, book *store.CompanyBook) Commitment {
	// The sender is the counterpart. Without an address the stored display
	// name still names them; the book decides the organization.
	fallback := it.SenderAddress
	if fallback == "" {
		fallback = it.SenderName
	}
	orgKey, orgLabel, person := counterpart(book, []string{it.SenderAddress}, fallback)
	return Commitment{
		ID:                itemID(kind, direction, it.MessageID, it.ConversationID, ""),
		Kind:              kind,
		Direction:         direction,
		Status:            StatusOpen,
		Title:             it.Subject,
		Subject:           it.Subject,
		ConversationID:    it.ConversationID,
		MessageID:         it.MessageID,
		OrganizationKey:   orgKey,
		OrganizationLabel: orgLabel,
		Person:            person,
		LastAt:            it.ReceivedAt,
		Evidence:          []int64{it.MessageID},
		Action:            string(it.Action.Primary),
		Waiting:           string(it.Action.Waiting),
		DueSource:         string(it.Action.DueSource),
		DueLevel:          it.Action.DueLevel,
		Topic:             it.Signals.Subject.Topic,
		Subtopic:          it.Signals.Subject.Subtopic,
	}
}

// decisionItems reads the ranker's decision page. The ranker owns the
// decision signal; this package does not re-score it.
func (s *Service) decisionItems(ctx context.Context, book *store.CompanyBook) ([]Commitment, error) {
	if s.decisions == nil {
		return nil, nil
	}
	items, err := s.decisions.Decisions(ctx, decisionLimit)
	if err != nil {
		return nil, fmt.Errorf("commitments: decisions: %w", err)
	}
	out := make([]Commitment, 0, len(items))
	for _, it := range items {
		// The ranking's action classification decides whether a decision is
		// still owed. A history row, an automated message or a thread that
		// already answered is not a decision commitment.
		if it.MessageID == 0 || it.Action.Primary != action.Decide || it.Action.Waiting != action.WaitingOwner {
			continue
		}
		out = append(out, commitmentFromRanked(KindDecision, DirectionOwner, it, book))
	}
	return out, nil
}

// actionItems reads the ranker's concrete owner actions and keeps only the
// steps the reply and decision sources do not already own. A payment, a
// document, a scheduled step, a task or a verification is an obligation of
// its own; a reply or a verdict is already a reply/decision commitment and
// must not appear twice.
func (s *Service) actionItems(ctx context.Context, book *store.CompanyBook) ([]Commitment, error) {
	if s.actions == nil {
		return nil, nil
	}
	items, err := s.actions.Actions(ctx, actionLimit)
	if err != nil {
		return nil, fmt.Errorf("commitments: actions: %w", err)
	}
	out := make([]Commitment, 0, len(items))
	for _, it := range items {
		if it.MessageID == 0 || !it.Action.Primary.OwnerAction() {
			continue
		}
		switch it.Action.Primary {
		case action.Reply, action.Decide, action.FollowUp:
			continue // already a reply, decision or due commitment
		}
		out = append(out, commitmentFromRanked(KindAction, DirectionOwner, it, book))
	}
	return out, nil
}

// newestReplyIDs selects the awaiting conversations with the newest resolved
// activity, capped at limit. Unparseable or absent instants sort last; the
// conversation id is the stable tie-break so map iteration never decides the
// working set.
func newestReplyIDs(states map[string]threadintel.Assessment, limit int) []string {
	type candidate struct {
		id string
		at time.Time
		ok bool
	}
	cands := make([]candidate, 0, len(states))
	for conv, state := range states {
		if conv == "" || (state.State != threadintel.AwaitingOwner && state.State != threadintel.AwaitingOther) {
			continue
		}
		at, ok := mail.Parse(state.LastAt)
		cands = append(cands, candidate{id: conv, at: at, ok: ok})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.ok != b.ok {
			return a.ok
		}
		if a.ok && !a.at.Equal(b.at) {
			return a.at.After(b.at)
		}
		return a.id < b.id
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	ids := make([]string, len(cands))
	for i, c := range cands {
		ids[i] = c.id
	}
	return ids
}

// counterpart picks the organization and person of an ordered participant
// list. The first participant the book can place externally wins, so the
// label and the person always describe the same address; when nothing
// resolves, the first participant (or the source fallback) is still named
// without an invented organization.
func firstParticipant(addresses []string) string {
	for _, address := range addresses {
		if address = strings.TrimSpace(address); address != "" {
			return address
		}
	}
	return ""
}

func counterpart(book *store.CompanyBook, addresses []string, fallback string) (orgKey, orgLabel, person string) {
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if book != nil {
			if org, ok := book.ResolveExternal(address); ok {
				return org.Key, org.Label, address
			}
		}
		if person == "" {
			person = address
		}
	}
	if person == "" {
		person = strings.TrimSpace(fallback)
	}
	return "", "", person
}

// itemID is the stable identity of one piece of evidence. The same kind,
// direction, message, conversation and due date always hash to the same id,
// so a duplicated source row collapses instead of appearing twice.
func itemID(kind Kind, direction Direction, messageID int64, conversationID, due string) string {
	key := strings.Join([]string{
		string(kind),
		string(direction),
		strconv.FormatInt(messageID, 10),
		conversationID,
		due,
	}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// dedupe keeps one obligation per conversation (or one message when the
// evidence has no conversation). The four sources can describe the same loop;
// showing a due, a reply and a decision for one thread three times is a false
// count. The strongest kind wins, the next due date and every evidence id are
// carried over, and the merge order is fixed so the result is deterministic.
func dedupe(items []Commitment) []Commitment {
	out := make([]Commitment, 0, len(items))
	index := make(map[string]int, len(items))
	for _, c := range items {
		key := c.ConversationID
		if key == "" {
			key = "m:" + strconv.FormatInt(c.MessageID, 10)
		}
		if i, ok := index[key]; ok {
			out[i] = mergeCommitment(out[i], c)
			continue
		}
		index[key] = len(out)
		out = append(out, c)
	}
	return out
}

// mergeCommitment folds two descriptions of the same loop. The kept identity
// is the stronger kind's; the other item's evidence and due date are not
// lost.
func mergeCommitment(a, b Commitment) Commitment {
	if kindStrength(b.Kind) > kindStrength(a.Kind) {
		a, b = b, a
	}
	if a.Status != StatusOverdue && b.Status == StatusOverdue {
		a.Status = StatusOverdue
	}
	// The direction follows the kept kind. An overlapping due (owner) must
	// never turn a kept awaiting_other reply into an owner obligation.
	if a.Direction == "" {
		a.Direction = b.Direction
	}
	if a.Title == "" {
		a.Title = b.Title
	}
	if a.Subject == "" {
		a.Subject = b.Subject
	}
	if a.Due == "" {
		a.Due = b.Due
	} else if b.Due != "" && b.Due < a.Due {
		a.Due = b.Due
	}
	if a.ConversationID == "" {
		a.ConversationID = b.ConversationID
	}
	if a.MessageID == 0 {
		a.MessageID = b.MessageID
	}
	if a.OrganizationKey == "" {
		a.OrganizationKey, a.OrganizationLabel = b.OrganizationKey, b.OrganizationLabel
	}
	if a.Person == "" {
		a.Person = b.Person
	}
	if a.LastAt == "" || (b.LastAt != "" && b.LastAt > a.LastAt) {
		a.LastAt = b.LastAt
	}
	if a.Action == "" {
		a.Action = b.Action
	}
	if a.Waiting == "" {
		a.Waiting = b.Waiting
	}
	if a.DueSource == "" {
		a.DueSource = b.DueSource
		a.DueLevel = b.DueLevel
	}
	if a.Topic == "" {
		a.Topic, a.Subtopic = b.Topic, b.Subtopic
	}
	a.Evidence = mergeEvidence(a.Evidence, b.Evidence)
	return a
}

func kindStrength(k Kind) int {
	switch k {
	case KindDecision:
		return 4
	case KindAction:
		return 3
	case KindReply:
		return 2
	case KindDue:
		return 1
	default:
		return 0
	}
}

func mergeEvidence(a, b []int64) []int64 {
	seen := make(map[int64]bool, len(a)+len(b))
	out := make([]int64, 0, len(a)+len(b))
	for _, ids := range [][]int64{a, b} {
		for _, id := range ids {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// sortItems orders overdue first, then by the item's instant (due for a
// deadline, last activity for a reply or decision) ascending with missing
// instants last, then kind in declaration order, then id.
func sortItems(items []Commitment) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		ao, bo := a.Status == StatusOverdue, b.Status == StatusOverdue
		if ao != bo {
			return ao
		}
		at, aok := instant(a)
		bt, bok := instant(b)
		if aok != bok {
			return aok
		}
		if aok && !at.Equal(bt) {
			return at.Before(bt)
		}
		if ka, kb := kindRank(a.Kind), kindRank(b.Kind); ka != kb {
			return ka < kb
		}
		return a.ID < b.ID
	})
}

// instant is when an item matters for ordering.
func instant(c Commitment) (time.Time, bool) {
	if c.Due != "" {
		return mail.Parse(c.Due)
	}
	if c.LastAt != "" {
		return mail.Parse(c.LastAt)
	}
	return time.Time{}, false
}

func kindRank(k Kind) int {
	if i := slices.Index(Kinds, k); i >= 0 {
		return i
	}
	return len(Kinds)
}

// countItems counts the whole deduped set. Every canonical key is present,
// with zero for an absent facet, so the Brief never has to tell "none" from
// "not reported".
func countItems(items []Commitment) map[string]int {
	counts := make(map[string]int, len(Kinds)+len(Directions)+len(Statuses))
	for _, k := range Kinds {
		counts[string(k)] = 0
	}
	for _, d := range Directions {
		counts[string(d)] = 0
	}
	for _, s := range Statuses {
		counts[string(s)] = 0
	}
	for _, c := range items {
		counts[string(c.Kind)]++
		counts[string(c.Direction)]++
		counts[string(c.Status)]++
	}
	return counts
}

// indexOpenLoops groups the deduped, sorted set by counterparty. It is pure
// and runs once per build, after sortItems, so every per-organization Items
// list reuses that one ordering and OpenLoops can never disagree with List
// about which loop is first. An item without an organization key is excluded:
// a bare person is not a counterparty a company screen can open.
//
// Organizations are ordered overdue-first, then by their first loop's instant
// ascending with missing instants last (the sortItems rule), then by key. An
// organization's counts cover all its items; Items carries at most
// OrganizationItemCap of them.
func indexOpenLoops(items []Commitment) []OrganizationOpenLoops {
	index := make(map[string]int, 8)
	out := make([]OrganizationOpenLoops, 0, 8)
	for _, c := range items {
		if c.OrganizationKey == "" {
			continue
		}
		i, ok := index[c.OrganizationKey]
		if !ok {
			i = len(out)
			index[c.OrganizationKey] = i
			kinds := make(map[string]int, len(Kinds))
			for _, k := range Kinds {
				kinds[string(k)] = 0
			}
			out = append(out, OrganizationOpenLoops{
				Key:   c.OrganizationKey,
				Label: c.OrganizationLabel,
				Kinds: kinds,
			})
		}
		org := &out[i]
		if org.Label == "" {
			org.Label = c.OrganizationLabel
		}
		org.Total++
		switch c.Direction {
		case DirectionOwner:
			org.Owner++
		case DirectionOther:
			org.Other++
		}
		if c.Status == StatusOverdue {
			org.Overdue++
		}
		if c.DueSource == string(action.DueStored) {
			org.StoredDue++
		}
		org.Kinds[string(c.Kind)]++
		if len(org.Items) < OrganizationItemCap {
			org.Items = append(org.Items, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Overdue > 0) != (b.Overdue > 0) {
			return a.Overdue > 0
		}
		at, aok := orgInstant(a)
		bt, bok := orgInstant(b)
		if aok != bok {
			return aok
		}
		if aok && !at.Equal(bt) {
			return at.Before(bt)
		}
		return a.Key < b.Key
	})
	return out
}

// orgInstant is when an organization's first loop matters, using the same
// instant rule as sortItems; an organization whose first loop has no instant
// sorts after the dated ones.
func orgInstant(org OrganizationOpenLoops) (time.Time, bool) {
	if len(org.Items) == 0 {
		return time.Time{}, false
	}
	return instant(org.Items[0])
}

// cloneOpenLoops copies the grouped set, including every item's evidence and
// every kind map, so a caller cannot mutate the cached build. An empty set
// stays an empty non-nil slice, never null.
func cloneOpenLoops(orgs []OrganizationOpenLoops) []OrganizationOpenLoops {
	out := make([]OrganizationOpenLoops, len(orgs))
	for i, org := range orgs {
		org.Items = cloneItems(org.Items)
		org.Kinds = maps.Clone(org.Kinds)
		out[i] = org
	}
	return out
}

func filterItems(items []Commitment, f Filter) []Commitment {
	out := make([]Commitment, 0, len(items))
	for _, c := range items {
		if f.Kind != "" && c.Kind != f.Kind {
			continue
		}
		if f.Direction != "" && c.Direction != f.Direction {
			continue
		}
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		out = append(out, c)
	}
	return out
}

// cloneItems copies the page, including evidence slices, so a caller cannot
// mutate the cached set.
func cloneItems(items []Commitment) []Commitment {
	out := make([]Commitment, len(items))
	for i, c := range items {
		c.Evidence = append([]int64{}, c.Evidence...)
		out[i] = c
	}
	return out
}

func normalizeFilter(f Filter) (Filter, error) {
	if f.Kind != "" && !slices.Contains(Kinds, f.Kind) {
		return Filter{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidKind, f.Kind)
	}
	if f.Direction != "" && !slices.Contains(Directions, f.Direction) {
		return Filter{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidDirection, f.Direction)
	}
	if f.Status != "" && !slices.Contains(Statuses, f.Status) {
		return Filter{}, fmt.Errorf("%w: unknown status %q", ErrInvalidStatus, f.Status)
	}
	switch {
	case f.Limit <= 0:
		f.Limit = DefaultLimit
	case f.Limit > MaxLimit:
		f.Limit = MaxLimit
	}
	return f, nil
}

// stamp is the store's timestamp form, or "" for the zero instant.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(mail.TimeLayout)
}
