// Package conversations owns the thread-first mailbox page: one digest row
// per conversation, its resolved state, search, keyset pagination and the
// page-only enrichment (participants, snippet, organization). It reads the
// owner-facing store views; shared/delegate content never reaches a row.
//
// Signals, topics and commitments are outside Phase A. They will extend the
// row, not replace this service.
package conversations

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Page bounds and internals.
const (
	DefaultLimit  = 30
	MinLimit      = 1
	MaxLimit      = 100
	MaxQueryRunes = 200
	// ParticipantCap is the number of participants a page row carries; the
	// total unique count travels beside it.
	ParticipantCap = 3
	// SnapshotTTL bounds how long a digest snapshot is reused while no mail
	// write changes the store generation. An expired snapshot is served
	// immediately while one detached, bounded rebuild runs.
	SnapshotTTL = 15 * time.Second
	// PageCacheTTL bounds one enriched page. An expired page is served
	// immediately while one background refresh runs, so a route never waits
	// on the 15-second boundary.
	PageCacheTTL = 15 * time.Second
	// pageCacheSize bounds the enriched pages kept in memory. Pages are at
	// most MaxLimit rows with bounded participants and snippets.
	pageCacheSize = 32
	// Both detached rebuilds use viewcache's default bound (two minutes), so a
	// stuck store read cannot keep the refresh flag set forever.
	// Fact capacities. The Brief reads a bounded working set, never the
	// whole mailbox.
	DefaultRecent        = 12
	MaxRecent            = 50
	DefaultOrganizations = 8
	MaxOrganizations     = 30
)

// Errors the API maps to its 400 contract.
var (
	ErrInvalidLimit  = errors.New("conversations: invalid limit")
	ErrInvalidQuery  = errors.New("conversations: invalid query")
	ErrInvalidState  = errors.New("conversations: invalid state")
	ErrInvalidCursor = errors.New("conversations: invalid cursor")
	ErrInvalidSince  = errors.New("conversations: invalid since")
)

// Priority is the classifier-backed thread resolution (the priority
// service). It is optional: a nil or failing provider leaves every
// conversation to the store resolver.
type Priority interface {
	ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error)
}

// storeReader is the store surface one mailbox read uses. The production
// value is *store.Store; tests wrap it to count the reads a request performs.
type storeReader interface {
	MailGeneration() uint64
	ConversationDigests(context.Context) (store.ConversationDigestSet, error)
	ThreadStates(context.Context, []string, map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error)
	CompanyBook(context.Context) (*store.CompanyBook, error)
	ConversationParticipants(context.Context, []string, int) (map[string]store.ConversationParticipants, error)
	ConversationSnippets(context.Context, []int64) (map[int64]string, error)
}

// Deps are the service collaborators. Store is required; Clock defaults to
// the wall clock, and Priority and Gate are optional. A nil Gate is preview
// off.
type Deps struct {
	Store    *store.Store
	Priority Priority
	Gate     learning.Previewer
	Clock    clock.Clock
}

// Query is one mailbox page request. Limit zero means DefaultLimit; State
// empty means every state; Before is the opaque keyset cursor.
type Query struct {
	Limit  int
	Before string
	State  threadintel.State
	Q      string
}

// Organization is the external organization label of a conversation. It is
// resolved from the most recent participant only; internal organizations are
// omitted.
type Organization struct {
	Key      string
	Label    string
	Declared bool
}

// Row is one conversation as the page serves it.
type Row struct {
	ConversationID   string
	Subject          string
	SenderName       string
	SenderAddress    string
	LastMessageID    int64
	LastAt           string
	FirstAt          string
	Messages         int
	Sent             int
	Received         int
	Unread           int
	LastSent         bool
	LastIsRead       bool
	HasAttachments   bool
	Snippet          string
	DueDate          string
	Participants     []store.ThreadParticipant
	ParticipantCount int
	Organization     *Organization
	State            threadintel.Assessment
	// StateSource is "priority" for a classifier-backed assessment and
	// "store" for the store resolver.
	StateSource string
}

// Page is one mailbox page. Conversations is never nil; Counts always has
// every threadintel.States key.
type Page struct {
	GeneratedAt   string
	Conversations []Row
	Total         int
	Counts        map[string]int
	NextBefore    string
	HasMore       bool
	Preview       bool
	Coverage      store.ConversationCoverage
}

// Fact is one conversation as the Brief reads it. It carries the digest and
// its resolved state only; participants and snippets stay page-only.
type Fact struct {
	ConversationID string
	Subject        string
	SenderName     string
	SenderAddress  string
	LastAt         string
	LastMessageID  int64
	Messages       int
	Unread         int
	State          threadintel.Assessment
	Organization   *Organization
}

// OrganizationFact is the conversation rollup of one external organization.
// LastAt, LastMessageID and LastSubject describe its latest conversation
// message; no participant or snippet is read for it.
type OrganizationFact struct {
	Organization  Organization
	Conversations int
	Messages      int
	AwaitingOwner int
	AwaitingOther int
	LastAt        string
	LastMessageID int64
	LastSubject   string
}

// Facts is the Brief's conversation evidence. Counts always carries every
// threadintel.States key over the whole digest; Recent and Organizations are
// bounded, deterministic slices and never nil.
type Facts struct {
	GeneratedAt string
	// Total is the conversation count; Messages is the owner-facing message
	// count of the whole mailbox, the universe the Brief's topic census
	// measures.
	Total    int
	Messages int
	Counts   map[string]int
	// RecentTotal and OrganizationsOpenTotal count every match before the
	// display caps, so the Brief can report an honest section total while
	// carrying only the bounded rows.
	RecentTotal            int
	Recent                 []Fact
	OrganizationsOpenTotal int
	Organizations          []OrganizationFact
}

// Service is the cached, gated mailbox page.
type Service struct {
	store    storeReader
	priority Priority
	gate     learning.Previewer
	clock    clock.Clock

	// snapshots is the shared digest+state snapshot of one preview rule.
	// viewcache owns its lifecycle: one build per key, a stale snapshot
	// served immediately, one detached bounded rebuild, and an install that
	// is rejected if the mail generation moved during the build.
	snapshots *viewcache.Cache[snapshotKey, *snapshot]

	// pages is the bounded enriched-page cache. A page is immutable after
	// build, so encoding it never holds the snapshot lock. Its key carries
	// the snapshot generation the page was built from, not the mail
	// generation of the request, so a page stays usable while the snapshot's
	// detached rebuild runs.
	pages *viewcache.Cache[pageKey, pageValue]
}

// snapshotKey is the one snapshot dimension that must never be shared by a
// cache hit: the effective preview rule, whose thread states come from the
// preview-keyed priority ranking. A snapshot's identity is exactly the mail
// generation and this preview rule; the generation travels in the value and
// is checked by Keep instead of being part of the map key, because a
// generation-keyed entry is a miss by definition and would rebuild on the
// request path after every mail write. Keep still checks both, so a build
// that raced a write is never installed and a stale snapshot is served
// immediately while one detached rebuild runs.
type snapshotKey struct {
	preview bool
}

// pageKey is one normalized page request. The snapshot generation and
// effective preview rule are part of the key, so a page is only ever served
// for the snapshot it was built from and a preview-rule flip never reuses an
// old page.
type pageKey struct {
	gen     uint64
	limit   int
	before  string
	state   string
	query   string
	preview bool
}

// pageValue is one built page and the snapshot generation it was built from.
type pageValue struct {
	view Page
	gen  uint64
}

type snapshot struct {
	gen      uint64
	preview  bool
	digests  []store.ConversationDigest
	coverage store.ConversationCoverage
	states   map[string]threadintel.Assessment
	sources  map[string]string
	store    storeReader

	// baseOnce builds the per-snapshot row projection once: every state chip
	// of a warm reads the same 10k digests, counts and order.
	baseOnce sync.Once
	base     *pageBase
	baseErr  error

	// book is the company book of this snapshot, read once and shared by
	// every page and by Facts.
	bookMu     sync.Mutex
	book       *store.CompanyBook
	bookLoaded bool
}

// pageBase is the immutable projection of one snapshot: every conversation
// row in display order, the same rows partitioned by state, and the state
// counts. Building it once turns six redundant chip scans and sorts into
// slice selections.
type pageBase struct {
	all     []Row
	byState map[threadintel.State][]Row
	counts  map[string]int
}

func (s *snapshot) pageBase() (*pageBase, error) {
	s.baseOnce.Do(func() { s.base, s.baseErr = buildPageBase(s) })
	return s.base, s.baseErr
}

// noState is the one failure of a digest whose conversation has mail but no
// resolved state: the snapshot would paint a row with no thread meaning. Every
// snapshot projection reports it through assessment, so a new reader cannot
// invent a second message or silently drop the row.
func noState(id string) error {
	return fmt.Errorf("conversations: conversation %q has mail but no resolved state", id)
}

// assessment is one conversation's resolved state in this snapshot.
func (s *snapshot) assessment(id string) (threadintel.Assessment, error) {
	state, ok := s.states[id]
	if !ok {
		return threadintel.Assessment{}, noState(id)
	}
	return state, nil
}

func buildPageBase(snap *snapshot) (*pageBase, error) {
	base := &pageBase{
		all:     make([]Row, 0, len(snap.digests)),
		byState: make(map[threadintel.State][]Row, len(threadintel.States)),
		counts:  threadintel.StateCounts(nil),
	}
	for _, d := range snap.digests {
		state, err := snap.assessment(d.ConversationID)
		if err != nil {
			return nil, err
		}
		row := digestRow(d, state, snap.sources[d.ConversationID])
		base.all = append(base.all, row)
		base.byState[state.State] = append(base.byState[state.State], row)
		base.counts[string(state.State)]++
	}
	slices.SortFunc(base.all, compareRows)
	for state := range base.byState {
		slices.SortFunc(base.byState[state], compareRows)
	}
	return base, nil
}

// companyBook reads the book once per snapshot. A failed read is not cached:
// the next caller retries instead of inheriting a transient error.
func (s *snapshot) companyBook(ctx context.Context) (*store.CompanyBook, error) {
	s.bookMu.Lock()
	defer s.bookMu.Unlock()
	if s.bookLoaded {
		return s.book, nil
	}
	book, err := s.store.CompanyBook(ctx)
	if err != nil {
		return nil, err
	}
	s.book, s.bookLoaded = book, true
	return book, nil
}

// New returns a conversation mailbox service.
func New(d Deps) (*Service, error) {
	if d.Store == nil {
		return nil, errors.New("conversations: a store is required")
	}
	return newService(d, d.Store), nil
}

// newService builds one service over the store reads it must use. Production
// passes the concrete store; a test passes a wrapper to count or fail a read.
func newService(d Deps, read storeReader) *Service {
	s := &Service{store: read, priority: d.Priority, gate: d.Gate, clock: clock.Or(d.Clock)}
	s.snapshots = viewcache.New[snapshotKey, *snapshot](viewcache.Options[snapshotKey, *snapshot]{
		TTL: SnapshotTTL, Size: 2, Now: s.clock.Now, Component: "conversations",
		Keep: func(key snapshotKey, value *snapshot) bool {
			return value.preview == key.preview && value.gen == s.store.MailGeneration()
		},
	})
	s.pages = viewcache.New[pageKey, pageValue](viewcache.Options[pageKey, pageValue]{
		TTL: PageCacheTTL, Size: pageCacheSize, Now: s.clock.Now,
		Component: "conversations",
		// The key carries the snapshot generation the page was built from.
		// A page built for the still-installed stale snapshot stays
		// installable while the snapshot's detached rebuild runs; the next
		// request after the rebuild lands keys a new page.
		Keep: func(key pageKey, value pageValue) bool {
			return value.gen == key.gen
		},
	})
	return s
}

// Close joins the background rebuilds of the snapshot and page caches,
// bounded by ctx. It reports whether both joined: false means a store read
// outlived ctx, so the store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool {
	joined := s.snapshots.Close(ctx)
	return s.pages.Close(ctx) && joined
}

// preview reports the one preview rule. Without a gate the preview is off.
func (s *Service) preview(ctx context.Context) bool {
	return s.gate != nil && s.gate.PreviewAnswers(ctx)
}

// Page validates and returns one enriched mailbox page. The shared snapshot is
// read first, so the page identity is the snapshot it was built from: a mail
// write moves the store generation but keeps serving the installed snapshot
// until its one detached rebuild lands, and every repeated request keeps
// hitting the warmed page in the meantime. A fresh page is served directly; an
// expired page is returned immediately while one background rebuild runs. Only
// the first request for a key blocks, and startup warms the default page before
// the API opens.
func (s *Service) Page(ctx context.Context, q Query) (Page, error) {
	norm, err := normalizeQuery(q)
	if err != nil {
		return Page{}, err
	}
	preview := s.preview(ctx)
	snap, err := s.snapshot(ctx, preview)
	if err != nil {
		return Page{}, err
	}
	key := pageKey{
		gen:     snap.gen,
		limit:   norm.Limit,
		before:  norm.Before,
		state:   string(norm.State),
		query:   norm.Q,
		preview: preview,
	}
	value, err := s.pages.Get(ctx, key, func(ctx context.Context) (pageValue, error) {
		view, err := s.buildPageSnapshot(ctx, norm, preview, snap)
		if err != nil {
			return pageValue{}, err
		}
		return pageValue{view: view, gen: snap.gen}, nil
	})
	if err != nil {
		return Page{}, err
	}
	return value.view, nil
}

// buildPage does the uncached work for one normalized request. It is the
// test-facing wrapper; Page also needs the snapshot generation the page was
// built from, so the page cache can attach the page to that exact snapshot.
func (s *Service) buildPage(ctx context.Context, norm Query, preview bool) (Page, error) {
	page, _, err := s.buildPageGen(ctx, norm, preview)
	return page, err
}

func (s *Service) buildPageGen(ctx context.Context, norm Query, preview bool) (Page, uint64, error) {
	snap, err := s.snapshot(ctx, preview)
	if err != nil {
		return Page{}, 0, err
	}
	page, err := s.buildPageSnapshot(ctx, norm, preview, snap)
	return page, snap.gen, err
}

// buildPageSnapshot projects one normalized request from an immutable
// snapshot. The page cache builds from the exact snapshot it keyed, so a
// rebuild that races a snapshot refresh still answers from one consistent
// generation.
func (s *Service) buildPageSnapshot(ctx context.Context, norm Query, preview bool, snap *snapshot) (Page, error) {
	if norm.Q == "" {
		return s.buildPageFromBase(ctx, norm, preview, snap)
	}

	// A text query keeps the digest scan: the per-snapshot base is the
	// unfiltered projection and cannot answer a folded search.
	needle := norm.Q
	matched := make([]Row, 0, len(snap.digests))
	for _, d := range snap.digests {
		if !strings.Contains(d.SearchText, needle) {
			continue
		}
		state, err := snap.assessment(d.ConversationID)
		if err != nil {
			return Page{}, err
		}
		matched = append(matched, digestRow(d, state, snap.sources[d.ConversationID]))
	}

	counts := threadintel.StateCounts(nil)
	for _, row := range matched {
		counts[string(row.State.State)]++
	}

	filtered := make([]Row, 0, len(matched))
	for _, row := range matched {
		if norm.State != "" && row.State.State != norm.State {
			continue
		}
		filtered = append(filtered, row)
	}
	slices.SortFunc(filtered, compareRows)

	page, err := s.pageStep(ctx, filtered, norm, preview, snap)
	if err != nil {
		return Page{}, err
	}
	// Counts covers every state of the matches, not just the requested one,
	// so the caller keeps it instead of deriving it from the page rows.
	page.Counts = counts
	return page, nil
}

// buildPageFromBase serves an unfiltered page (the default and every state
// chip) from the snapshot projection. Its rows are already in display order,
// so it only selects the state slice and then runs the shared page step.
func (s *Service) buildPageFromBase(ctx context.Context, norm Query, preview bool, snap *snapshot) (Page, error) {
	base, err := snap.pageBase()
	if err != nil {
		return Page{}, err
	}
	matched := base.all
	if norm.State != "" {
		matched = base.byState[norm.State]
	}

	page, err := s.pageStep(ctx, matched, norm, preview, snap)
	if err != nil {
		return Page{}, err
	}
	page.Counts = make(map[string]int, len(base.counts))
	for state, count := range base.counts {
		page.Counts[state] = count
	}
	return page, nil
}

// pageStep applies the one keyset step of every conversation page: it
// validates the cursor against the normalized query and state, keeps the rows
// after it, slices the limit, encodes the next cursor and enriches the visible
// page. rows must already be in display order; filtering by the cursor is
// order-independent, so both paths may sort first. GeneratedAt, Conversations,
// Total (the rows before the cursor), NextBefore, HasMore, Preview and
// Coverage are the same on both paths. Counts stays with the caller because
// the text path computes it over every state of its matches while the base
// path reuses the snapshot projection.
func (s *Service) pageStep(ctx context.Context, rows []Row, norm Query, preview bool, snap *snapshot) (Page, error) {
	total := len(rows)
	if norm.Before != "" {
		decoded, err := decodeCursor(norm.Before)
		if err != nil {
			return Page{}, err
		}
		if decoded.Q != norm.Q || decoded.State != string(norm.State) {
			return Page{}, fmt.Errorf("%w: cursor belongs to another query", ErrInvalidCursor)
		}
		after := make([]Row, 0, len(rows))
		for _, row := range rows {
			if afterCursor(row, decoded) {
				after = append(after, row)
			}
		}
		rows = after
	}

	hasMore := len(rows) > norm.Limit
	pageRows := rows
	if hasMore {
		pageRows = rows[:norm.Limit]
	}
	nextBefore := ""
	if hasMore && len(pageRows) > 0 {
		last := pageRows[len(pageRows)-1]
		nextBefore = encodeCursor(cursor{At: last.LastAt, ID: last.ConversationID, Q: norm.Q, State: string(norm.State)})
	}

	visible, err := s.enrichRows(ctx, pageRows, snap)
	if err != nil {
		return Page{}, err
	}
	return Page{
		GeneratedAt:   mail.Stamp(s.clock.Now()),
		Conversations: visible,
		Total:         total,
		NextBefore:    nextBefore,
		HasMore:       hasMore,
		Preview:       preview,
		Coverage:      snap.coverage,
	}, nil
}

// enrichRows reads participants, snippets and the organization once for one
// page, never for the whole mailbox.
func (s *Service) enrichRows(ctx context.Context, pageRows []Row, snap *snapshot) ([]Row, error) {
	rows := make([]Row, 0, len(pageRows))
	if len(pageRows) == 0 {
		return rows, nil
	}
	ids := make([]string, 0, len(pageRows))
	messageIDs := make([]int64, 0, len(pageRows))
	for _, row := range pageRows {
		ids = append(ids, row.ConversationID)
		messageIDs = append(messageIDs, row.LastMessageID)
	}
	participants, err := s.store.ConversationParticipants(ctx, ids, ParticipantCap)
	if err != nil {
		return nil, err
	}
	snippets, err := s.store.ConversationSnippets(ctx, messageIDs)
	if err != nil {
		return nil, err
	}
	book, err := snap.companyBook(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range pageRows {
		p := participants[row.ConversationID]
		row.Participants = p.Participants
		if row.Participants == nil {
			row.Participants = []store.ThreadParticipant{}
		}
		row.ParticipantCount = p.Count
		row.Snippet = snippets[row.LastMessageID]
		if len(row.Participants) > 0 {
			if org, ok := book.ResolveExternal(row.Participants[0].Address); ok {
				row.Organization = &Organization{Key: org.Key, Label: org.Label, Declared: org.Declared}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Facts returns the Brief's conversation evidence from the same digest
// snapshot Page serves. It never rebuilds the snapshot or reads a
// participant, snippet or message body. since is a stored mail timestamp;
// empty means no cutoff, and a value that does not parse is
// ErrInvalidSince. Rows at or before since are excluded, as are undated rows
// while a cutoff is set. Organizations are resolved once from the company
// book; an unresolved or internal sender has no organization.
func (s *Service) Facts(ctx context.Context, since string, recentLimit, orgLimit int) (Facts, error) {
	since = strings.TrimSpace(since)
	var cutoff time.Time
	hasCutoff := since != ""
	if hasCutoff {
		at, ok := mail.Parse(since)
		if !ok {
			return Facts{}, fmt.Errorf("%w: %q", ErrInvalidSince, since)
		}
		cutoff = at
	}
	recentLimit = mathx.Clamp(recentLimit, DefaultRecent, MaxRecent)
	orgLimit = mathx.Clamp(orgLimit, DefaultOrganizations, MaxOrganizations)

	snap, err := s.snapshot(ctx, s.preview(ctx))
	if err != nil {
		return Facts{}, err
	}

	var book *store.CompanyBook
	if len(snap.digests) > 0 {
		book, err = snap.companyBook(ctx)
		if err != nil {
			return Facts{}, err
		}
	}

	counts := threadintel.StateCounts(nil)
	all := make([]Fact, 0, len(snap.digests))
	for _, d := range snap.digests {
		state, err := snap.assessment(d.ConversationID)
		if err != nil {
			return Facts{}, err
		}
		counts[string(state.State)]++
		fact := Fact{
			ConversationID: d.ConversationID,
			Subject:        d.Subject,
			SenderName:     d.SenderName,
			SenderAddress:  d.SenderAddress,
			LastAt:         d.LastAt,
			LastMessageID:  d.LastMessageID,
			Messages:       d.Messages,
			Unread:         d.Unread,
			State:          state,
		}
		if book != nil {
			// The latest received sender names the external organization even
			// when the owner sent the last message; fall back to the latest
			// sender only for conversations with no received mail.
			address := d.CounterpartyAddress
			if address == "" {
				address = d.SenderAddress
			}
			if org, ok := book.ResolveExternal(address); ok {
				fact.Organization = &Organization{Key: org.Key, Label: org.Label, Declared: org.Declared}
			}
		}
		all = append(all, fact)
	}

	recent := make([]Fact, 0, min(recentLimit, len(all)))
	for _, fact := range all {
		if hasCutoff {
			at, ok := mail.Parse(fact.LastAt)
			if !ok || !at.After(cutoff) {
				continue
			}
		}
		recent = append(recent, fact)
	}
	slices.SortFunc(recent, func(a, b Fact) int {
		if c := compareInstant(a.LastAt, b.LastAt); c != 0 {
			return c
		}
		return strings.Compare(b.ConversationID, a.ConversationID)
	})
	recentTotal := len(recent)
	if len(recent) > recentLimit {
		recent = recent[:recentLimit]
	}

	type rollup struct {
		Organization  Organization
		Conversations int
		Messages      int
		AwaitingOwner int
		AwaitingOther int
		LastAt        string
		LastMessageID int64
		LastSubject   string
		lastConvID    string
		hasLatest     bool
	}
	byKey := make(map[string]*rollup)
	for _, fact := range all {
		if fact.Organization == nil {
			continue
		}
		org := byKey[fact.Organization.Key]
		if org == nil {
			org = &rollup{Organization: *fact.Organization}
			byKey[fact.Organization.Key] = org
		}
		org.Conversations++
		org.Messages += fact.Messages
		switch fact.State.State {
		case threadintel.AwaitingOwner:
			org.AwaitingOwner++
		case threadintel.AwaitingOther:
			org.AwaitingOther++
		}
		newer := !org.hasLatest
		if !newer {
			c := compareInstant(fact.LastAt, org.LastAt)
			newer = c < 0 ||
				(c == 0 && fact.LastMessageID > org.LastMessageID) ||
				(c == 0 && fact.LastMessageID == org.LastMessageID && fact.ConversationID < org.lastConvID)
		}
		if newer {
			org.LastAt = fact.LastAt
			org.LastMessageID = fact.LastMessageID
			org.LastSubject = fact.Subject
			org.lastConvID = fact.ConversationID
			org.hasLatest = true
		}
	}
	organizations := make([]OrganizationFact, 0, len(byKey))
	openOrganizations := 0
	for _, org := range byKey {
		if org.AwaitingOwner+org.AwaitingOther > 0 {
			openOrganizations++
		}
		organizations = append(organizations, OrganizationFact{
			Organization:  org.Organization,
			Conversations: org.Conversations,
			Messages:      org.Messages,
			AwaitingOwner: org.AwaitingOwner,
			AwaitingOther: org.AwaitingOther,
			LastAt:        org.LastAt,
			LastMessageID: org.LastMessageID,
			LastSubject:   org.LastSubject,
		})
	}
	slices.SortFunc(organizations, func(a, b OrganizationFact) int {
		if a.AwaitingOwner != b.AwaitingOwner {
			return cmp.Compare(b.AwaitingOwner, a.AwaitingOwner)
		}
		if c := compareInstant(a.LastAt, b.LastAt); c != 0 {
			return c
		}
		return strings.Compare(a.Organization.Key, b.Organization.Key)
	})
	if len(organizations) > orgLimit {
		organizations = organizations[:orgLimit]
	}

	return Facts{
		GeneratedAt:            mail.Stamp(s.clock.Now()),
		Total:                  len(snap.digests),
		Messages:               snap.coverage.Messages,
		Counts:                 counts,
		RecentTotal:            recentTotal,
		Recent:                 recent,
		OrganizationsOpenTotal: openOrganizations,
		Organizations:          organizations,
	}, nil
}

// digestRow maps one store digest to the page row before enrichment.
func digestRow(d store.ConversationDigest, state threadintel.Assessment, source string) Row {
	return Row{
		ConversationID: d.ConversationID,
		Subject:        d.Subject,
		SenderName:     d.SenderName,
		SenderAddress:  d.SenderAddress,
		LastMessageID:  d.LastMessageID,
		LastAt:         d.LastAt,
		FirstAt:        d.FirstAt,
		Messages:       d.Messages,
		Sent:           d.Sent,
		Received:       d.Received,
		Unread:         d.Unread,
		LastSent:       d.LastSent,
		LastIsRead:     d.LastIsRead,
		HasAttachments: d.LastHasAttachments,
		DueDate:        d.DueDate,
		State:          state,
		StateSource:    source,
	}
}

func validState(state threadintel.State) bool {
	return slices.Contains(threadintel.States, state)
}

// normalizeQuery applies the one query contract the API and direct callers
// share.
func normalizeQuery(q Query) (Query, error) {
	if q.Limit == 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit < MinLimit || q.Limit > MaxLimit {
		return Query{}, fmt.Errorf("%w: must be between %d and %d, got %d", ErrInvalidLimit, MinLimit, MaxLimit, q.Limit)
	}
	q.Q = strings.TrimSpace(q.Q)
	if utf8.RuneCountInString(q.Q) > MaxQueryRunes {
		return Query{}, fmt.Errorf("%w: longer than %d characters", ErrInvalidQuery, MaxQueryRunes)
	}
	if strings.ContainsFunc(q.Q, unicode.IsControl) {
		return Query{}, fmt.Errorf("%w: contains a control character", ErrInvalidQuery)
	}
	if q.State != "" {
		state, ok := threadintel.ParseState(string(q.State))
		if !ok {
			return Query{}, fmt.Errorf("%w: %q", ErrInvalidState, q.State)
		}
		q.State = state
	}
	// The digest search text is folded, so case and accent variants match
	// the same conversations. Fold the key too, or every variant would
	// build and keep its own identical page.
	q.Q = mail.Fold(q.Q)
	return q, nil
}

// snapshot returns the shared digest+state snapshot for one effective preview
// state. viewcache owns the lifecycle: a fresh snapshot is returned directly,
// and one whose TTL expired or whose mail generation moved is returned
// immediately while one detached, timeout-bounded rebuild runs. A preview flip
// has no snapshot of that rule and blocks once, because the thread states it
// carries come from the priority ranking, which is itself preview-keyed; the
// two preview rules are separate keys, so neither overwrites the other.
func (s *Service) snapshot(ctx context.Context, preview bool) (*snapshot, error) {
	key := snapshotKey{preview: preview}
	return s.snapshots.Get(ctx, key, func(ctx context.Context) (*snapshot, error) {
		return s.buildSnapshot(ctx, preview)
	})
}

// buildSnapshot reads the full digest and resolves every state without
// touching the cache or its lock. The generation is read before the store so a
// write that lands during the build is visible to Keep, which then refuses to
// install a snapshot the write already made stale. The returned snapshot is
// immutable after this call.
func (s *Service) buildSnapshot(ctx context.Context, preview bool) (*snapshot, error) {
	gen := s.store.MailGeneration()
	set, err := s.store.ConversationDigests(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(set.Conversations))
	for _, d := range set.Conversations {
		ids = append(ids, d.ConversationID)
	}
	states, sources, err := s.resolveStates(ctx, ids)
	if err != nil {
		return nil, err
	}
	return &snapshot{gen: gen, preview: preview, digests: set.Conversations, coverage: set.Coverage,
		states: states, sources: sources, store: s.store}, nil
}

// resolveStates resolves every digest conversation once. The priority
// resolver wins for the ids it covers; bounded store batches fill every
// remaining id. A nil or failing priority falls back to the same batches for
// all ids. A conversation still without a state fails the page instead of
// inventing one.
func (s *Service) resolveStates(ctx context.Context, ids []string) (map[string]threadintel.Assessment, map[string]string, error) {
	states := make(map[string]threadintel.Assessment, len(ids))
	sources := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return states, sources, nil
	}
	if s.priority != nil {
		resolved, err := s.priority.ThreadStates(ctx)
		if err != nil {
			logx.For("conversations").Warn("priority thread states unavailable; using the store resolver", "err", err)
		} else {
			for _, id := range ids {
				if state, ok := resolved[id]; ok && validState(state.State) {
					states[id] = state
					sources[id] = "priority"
				}
			}
		}
	}
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := states[id]; !ok {
			missing = append(missing, id)
		}
	}
	// The store owns the read scale (store.ThreadStatesBatch); the caller
	// chunks by it so one request never builds a bigger array than the store
	// resolves at once.
	for start := 0; start < len(missing); start += store.ThreadStatesBatch {
		batch := missing[start:min(start+store.ThreadStatesBatch, len(missing))]
		resolved, err := s.store.ThreadStates(ctx, batch, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("conversations: store thread states: %w", err)
		}
		for _, id := range batch {
			if state, ok := resolved[id]; ok && validState(state.State) {
				states[id] = state
				sources[id] = "store"
			}
		}
	}
	for _, id := range ids {
		if _, ok := states[id]; !ok {
			return nil, nil, noState(id)
		}
	}
	return states, sources, nil
}

// cursor is the opaque keyset position: the last row's instant and id plus
// the query and state the page was built for, so a cursor cannot be reused
// under another filter.
type cursor struct {
	At    string `json:"at"`
	ID    string `json:"id"`
	Q     string `json:"q"`
	State string `json:"state"`
}

func encodeCursor(c cursor) string {
	raw, _ := json.Marshal(c) // strings only: never fails
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (cursor, error) {
	invalid := fmt.Errorf("%w: malformed before cursor", ErrInvalidCursor)
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursor{}, invalid
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return cursor{}, invalid
	}
	if c.At != "" {
		if _, ok := mail.Parse(c.At); !ok {
			return cursor{}, invalid
		}
	}
	return c, nil
}

// compareRows orders rows newest first with undated mail last, then by
// conversation id descending. It is the one order of the digest, the page
// cursor, the Brief facts and the tests.
func compareRows(a, b Row) int {
	if c := compareInstant(a.LastAt, b.LastAt); c != 0 {
		return c
	}
	return strings.Compare(b.ConversationID, a.ConversationID)
}

// compareInstant orders one mail instant before another: dated first, newest
// first, undated last. It is the shared instant order of rows and rollups.
func compareInstant(a, b string) int {
	aDated, bDated := a != "", b != ""
	if aDated != bDated {
		if aDated {
			return -1
		}
		return 1
	}
	return strings.Compare(b, a)
}

func afterCursor(row Row, c cursor) bool {
	return compareRows(row, Row{LastAt: c.At, ConversationID: c.ID}) > 0
}
