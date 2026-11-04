// Package world composes the owner-facing view of the external organizations
// Mailsense has observed. It owns no mail read of its own: the store provides
// the ranked companies, the deep profile, the calendar and the deadlines,
// while this package overlays the authoritative conversation truth from the
// priority service so a colleague's reply (visible only to the thread
// resolver) suppresses the owner's reply obligation on the company screen too.
package world

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/safe"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Bounds and defaults of the companies explorer. The API validates against
// these values; the service clamps direct callers so a malformed direct call
// cannot ask for an unbounded read.
//
// The profile window and the company ranking limit are aliases of their real
// owners: store.CompanyProfile* for months and event days, and
// mailquery.MaxCompanyLimit for the ranking. MinCompanyMonths and
// MinCompanyEventDays are this layer's valid-input floor, not store bounds:
// the store treats a zero or negative request as "use the default".
const (
	DefaultCompanyLimit = 25
	MinCompanyLimit     = 1
	MaxCompanyLimit     = mailquery.MaxCompanyLimit

	DefaultCompanyMonths = store.CompanyProfileDefaultMonths
	MinCompanyMonths     = 1
	MaxCompanyMonths     = store.CompanyProfileMaxMonths

	DefaultCompanyEventDays = store.CompanyProfileEventDaysDefault
	MinCompanyEventDays     = 1
	MaxCompanyEventDays     = store.CompanyProfileEventDaysMax

	// MaxCompanySearchLen bounds the server-side company search. It is a
	// query, not a ranked page: the store filters the whole ranking before
	// applying the limit, so a match beyond the first page is reachable.
	MaxCompanySearchLen = 120
)

// maxCompanyKeyLen bounds the company key the router decodes before the
// store decides whether it names an organization.
const maxCompanyKeyLen = 512

// Cache bounds. A cached view is reused while the store generations and the
// TTL agree. An expired view, or a profile whose generations moved, is served
// once while one background build refreshes it, so only the first read after
// startup pays the full store/priority cost.
const (
	cacheTTL            = 30 * time.Second
	cacheRefreshTimeout = 2 * time.Minute
	profileCacheSize    = 16
)

// MaxProfileBatch is the largest profile prefetch one request may ask for,
// derived from the profile cache: a larger batch would evict the profiles the
// same cache just built before the screen can click them.
const MaxProfileBatch = profileCacheSize

// Configuration keys this package reads. The store owns the same settings
// for its own company reads; the world service needs them to resolve the
// owner's zone and the shared-domain rule at the view boundary.
const ()

// ErrUnknownCompany is the store's unknown-key error, re-exported so API
// callers can map one sentinel without importing the store's mail model.
var ErrUnknownCompany = store.ErrUnknownCompany

// The profile view carries the domain types its consumers render. They are
// aliases, not copies: the store and the agenda remain their owners.
type (
	CompanyPerson   = store.CompanyPerson
	CompanyMonth    = store.CompanyMonth
	CompanyMessage  = store.CompanyMessage
	CompanyThread   = store.CompanyThread
	CompanyCoverage = store.CompanyCoverage
	CompanyActivity = store.CompanyActivity
	CalendarEvent   = calendar.Event
	Deadline        = agenda.Deadline
)

// Priority is the authoritative classifier-backed thread resolution. It is
// optional: a nil provider leaves the store's nil-signal states in place.
type Priority interface {
	ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error)
}

// Deps are the world service collaborators. Store and Config are required;
// Priority and Clock are optional (the wall clock is the default).
type Deps struct {
	Store    *store.Store
	Priority Priority
	Config   *config.Config
	Clock    clock.Clock
}

// Service composes the companies list and one company profile.
type Service struct {
	store    *store.Store
	priority Priority
	cfg      *config.Config
	clock    clock.Clock
	log      *slog.Logger

	// mu guards the custom list cache only; profiles use their own viewcache
	// lock. It is never held while a view is built, so a background refresh
	// cannot block a cache read.
	mu       sync.Mutex
	list     *listEntry
	profiles *viewcache.Cache[string, profileValue]

	// base is canceled by Close and bounds every list refresh, and refreshes
	// tracks them for the shutdown join: like the profile cache's rebuilds,
	// they read the store after the request that started them returned.
	base      context.Context
	cancel    context.CancelFunc
	refreshes *join.Group
}

// listEntry is the one cached unfiltered company list. A wider entry serves
// every smaller page as a prefix; a search bypasses the cache.
//
// It stays a custom single-entry cache because viewcache cannot express two
// policies List depends on. viewcache.Get selects by exact key, so "any entry
// whose width is at least this request" needs an enumeration it does not
// offer; and viewcache.Keep rejects an install whose generation moved, while
// refreshList installs a completed scan under the generation it sampled and
// lets the next read refresh behind it. Both are observable: a narrower
// request served from a wider snapshot, and the raced snapshot a caller sees.
// A generation in the key would turn that next read into a synchronous miss;
// a generation in Keep would discard the raced scan. Do not fold this into
// viewcache without a cache that can select an entry by width and accept a
// build at an older generation.
type listEntry struct {
	gen        uint64 // store.MailGeneration: the ranking reads no labels
	limit      int
	result     ListResult
	at         time.Time
	refreshing bool
}

// profileValue is one built company profile and the generations it was built
// from. The key is profileCacheKey.
type profileValue struct {
	view     ProfileView
	gen      uint64 // store.MailGeneration
	routeGen uint64 // store.RouteGeneration: the thread overlay
}

// New returns a world service.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Config == nil {
		return nil, errors.New("world: a store and config are required")
	}
	s := &Service{store: d.Store, priority: d.Priority, cfg: d.Config, clock: clock.Or(d.Clock),
		log: logx.For("world")}
	s.base, s.cancel = context.WithCancel(context.Background())
	s.refreshes = join.New()
	s.profiles = viewcache.New[string, profileValue](viewcache.Options[string, profileValue]{
		TTL: cacheTTL, Size: profileCacheSize, Now: s.clock.Now,
		Component: "world",
		Keep: func(_ string, value profileValue) bool {
			return value.gen == s.store.MailGeneration() && value.routeGen == s.store.RouteGeneration()
		},
	})
	return s, nil
}

// CompanyRow is one ranked external organization. Declared distinguishes a
// company named by the address book (key c:<folded name>) from a bare domain
// organization (key d:<domain>). Domains carries the ranking's most-used
// non-shared domain, empty for a declared company known only through a shared
// domain. People is the number of distinct addresses merged into the row.
type CompanyRow struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Declared bool     `json:"declared"`
	Domains  []string `json:"domains"`
	People   int      `json:"people"`
	Messages int      `json:"messages"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	Threads  int      `json:"threads"`
	LastAt   string   `json:"last_at"`
}

// ListQuery is one companies page request. Search filters the ranked
// companies by label or domain; empty is the full ranking.
type ListQuery struct {
	Limit  int
	Search string
}

// ListResult is the companies explorer page: the ranked external companies
// with at least one exchange and the address-book coverage of the whole book.
// Total is the whole matching population and HasMore says the returned
// Companies are a prefix of it, so the UI never presents a page as the list.
type ListResult struct {
	GeneratedAt string          `json:"generated_at"`
	Companies   []CompanyRow    `json:"companies"`
	Coverage    CompanyCoverage `json:"coverage"`
	Total       int             `json:"total"`
	HasMore     bool            `json:"has_more"`
}

// ProfileView is one organization's deep profile as the UI consumes it. Every
// list is always an array, never null. Totals and months come from the
// owner-visible mail; recent messages are metadata only; open threads and
// their states are the shared thread truth. No body or shared/delegate
// content crosses this view.
type ProfileView struct {
	GeneratedAt string          `json:"generated_at"`
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Declared    bool            `json:"declared"`
	Domains     []string        `json:"domains"`
	Addresses   []string        `json:"addresses"`
	People      []CompanyPerson `json:"people"`

	Messages       int             `json:"messages"`
	Sent           int             `json:"sent"`
	Received       int             `json:"received"`
	Threads        int             `json:"threads"`
	FirstAt        string          `json:"first_at,omitempty"`
	LastAt         string          `json:"last_at,omitempty"`
	LastInboundAt  string          `json:"last_inbound_at,omitempty"`
	LastOutboundAt string          `json:"last_outbound_at,omitempty"`
	Activity90     CompanyActivity `json:"last_90_days"`

	Months       []CompanyMonth   `json:"months"`
	Recent       []CompanyMessage `json:"recent"`
	OpenThreads  []CompanyThread  `json:"open_threads"`
	ThreadStates map[string]int   `json:"thread_states"`

	Events    []CalendarEvent `json:"events"`
	Deadlines []Deadline      `json:"deadlines"`

	PeopleContacted int `json:"people_contacted"`
	PeopleKnown     int `json:"people_known"`

	AddressesTotal   int `json:"addresses_total"`
	RecentTotal      int `json:"recent_total"`
	OpenThreadsTotal int `json:"open_threads_total"`
	EventsTotal      int `json:"events_total"`
	DeadlinesTotal   int `json:"deadlines_total"`
}

// List returns the ranked external organizations with at least one message
// in either direction. A one-way correspondent is a real company the owner
// needs to see, even when the owner never replied; the profile states that
// direction honestly instead of hiding the organization. A company with no
// mail volume never appears: the book's declared-only organizations are
// directory entries, not ranked companies. The API accepts up to
// MaxCompanyLimit, the mailquery validator's own company bound, so a larger
// request returns the executable bound rather than failing.
func (s *Service) List(ctx context.Context, q ListQuery) (ListResult, error) {
	limit := mathx.Clamp(q.Limit, DefaultCompanyLimit, MaxCompanyLimit)
	search := strings.TrimSpace(q.Search)
	// The ranking reads mail and the address book, never labels: a label
	// click must not rebuild the ~0.9 s companies scan.
	gen := s.store.MailGeneration()
	now := s.clock.Now()

	// The single cached list is the unfiltered ranking. Caching every query
	// would grow without bound, and a search is already a bounded read.
	if search != "" {
		return s.buildList(ctx, limit, search)
	}

	s.mu.Lock()
	if entry := s.list; entry != nil && entry.limit >= limit {
		if entry.gen != gen || now.Sub(entry.at) >= cacheTTL {
			if !entry.refreshing && s.refreshes.Track() {
				entry.refreshing = true
				// Refresh the widest cached list, not the narrower request:
				// every smaller limit stays a slice of the same snapshot.
				go s.refreshList(entry.limit, gen, entry)
			}
		}
		result := listSlice(entry.result, limit)
		s.mu.Unlock()
		return result, nil
	}
	s.mu.Unlock()

	result, err := s.buildList(ctx, limit, "")
	if err != nil {
		return ListResult{}, err
	}
	s.mu.Lock()
	s.list = &listEntry{gen: gen, limit: limit, result: result, at: s.clock.Now()}
	s.mu.Unlock()
	return result, nil
}

// listSlice serves a narrower request from one wider cached list. The list is
// ranked descending, so the first limit rows are exactly the requested page.
// The returned slice never aliases the cached backing array.
func listSlice(result ListResult, limit int) ListResult {
	if limit < 0 || limit >= len(result.Companies) {
		return result
	}
	result.Companies = append([]CompanyRow{}, result.Companies[:limit]...)
	result.HasMore = result.Total > len(result.Companies)
	return result
}

// buildList computes one company list without touching the cache or its lock.
func (s *Service) buildList(ctx context.Context, limit int, search string) (ListResult, error) {
	plan := mailquery.Plan{
		Kind:      mailquery.KindCompanies,
		Action:    mailquery.ActionRank,
		Direction: mailquery.DirectionBoth,
		Metric:    mailquery.MetricMessages,
		TimeRange: mailquery.TimeAll,
		Contact:   search,
		// Bound the executable plan by the validator's own company limit,
		// so a lowered mailquery bound can never make List send an invalid
		// plan even if a wider caller reaches this path.
		Limit: min(limit, MaxCompanyLimit),
		Order: mailquery.OrderDesc,
	}
	result, err := s.store.ExecuteMailQuery(ctx, plan, s.zone())
	if err != nil {
		return ListResult{}, err
	}
	book, err := s.store.CompanyBook(ctx)
	if err != nil {
		return ListResult{}, err
	}
	companies := make([]CompanyRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		if row.Messages == 0 {
			continue
		}
		domains := []string{}
		if row.Address != "" {
			domains = []string{row.Address}
		}
		companies = append(companies, CompanyRow{
			Key:      row.Key,
			Label:    row.Label,
			Declared: strings.HasPrefix(row.Key, "c:"),
			Domains:  domains,
			People:   row.Contacts,
			Messages: row.Messages,
			Sent:     row.Sent,
			Received: row.Received,
			Threads:  row.Threads,
			LastAt:   row.LastAt,
		})
	}
	total := result.Matched
	if total == 0 && len(companies) > 0 {
		// Older executors report no group count; a non-empty page is still a
		// truthful lower bound, never a reason to claim zero.
		total = len(companies)
	}
	return ListResult{
		GeneratedAt: mail.Stamp(s.clock.Now()),
		Companies:   companies,
		Coverage:    book.Coverage(),
		Total:       total,
		HasMore:     total > len(companies),
	}, nil
}

// refreshList rebuilds one stale company list off the request path. The
// request context is detached (it ends with the response), bounded by
// cacheRefreshTimeout and canceled by Close, which joins it. A failure keeps
// the stale entry. A completed result is installed under the generation it
// sampled even when a write moved the store meanwhile: it is still a coherent
// list, and the next read sees the newer generation and refreshes behind it.
// Discarding the completed scan would not save that rebuild: it would only
// make the interfering read serve the older pre-refresh snapshot instead.
func (s *Service) refreshList(limit int, gen uint64, entry *listEntry) {
	defer s.refreshes.Done()
	// safe.Detached owns the build context: it is bounded by
	// cacheRefreshTimeout and cancelled by Close through s.base. There is no
	// caller to detach from, so the parent is Background; the base is what
	// ends it.
	result, err := safe.Detached(context.Background(), s.base, cacheRefreshTimeout,
		func(ctx context.Context) (ListResult, error) { return s.buildList(ctx, limit, "") })
	at := s.clock.Now()
	s.mu.Lock()
	if err == nil && s.list == entry && !s.refreshes.Closed() {
		s.list = &listEntry{gen: gen, limit: limit, result: result, at: at}
	}
	entry.refreshing = false
	closed := s.refreshes.Closed()
	s.mu.Unlock()
	if err != nil && !closed {
		s.log.Warn("company list refresh failed; serving the cached list", "err", err)
	}
}

// Close refuses new list refreshes, cancels the running ones and joins them
// and the profile cache's rebuilds, bounded by ctx. It reports the
// conjunction; false means a build outlived ctx, so the store owner keeps the
// store open. It is idempotent: a second call observes the same outcome.
func (s *Service) Close(ctx context.Context) bool {
	s.cancel()
	joined := s.refreshes.Close(ctx)
	return s.profiles.Close(ctx) && joined
}

// Profile returns one organization's deep profile with its thread states
// overlaid from the priority service. The store owns the mail read, the
// calendar window and the deadline matching; the service owns the event
// window the API exposes and the thread overlay.
func (s *Service) Profile(ctx context.Context, key string, months, eventDays int) (ProfileView, error) {
	months, eventDays = profileWindow(months, eventDays)
	cacheKey := profileCacheKey(key, months, eventDays)
	// Mail plus route truth: the profile overlays priority thread states and
	// reads no labels.
	gen, routeGen := s.store.MailGeneration(), s.store.RouteGeneration()
	now := s.clock.Now()

	value, err := s.profiles.Get(ctx, cacheKey, func(ctx context.Context) (profileValue, error) {
		view, err := s.buildProfile(ctx, key, months, eventDays, now)
		if err != nil {
			return profileValue{}, err
		}
		return profileValue{view: view, gen: gen, routeGen: routeGen}, nil
	})
	if err != nil {
		return ProfileView{}, err
	}
	return value.view, nil
}

// buildProfile computes one company profile without touching the cache or
// its lock. The caller owns the returned view.
func (s *Service) buildProfile(ctx context.Context, key string, months, eventDays int, now time.Time) (ProfileView, error) {
	views, err := s.buildProfiles(ctx, []string{key}, months, eventDays, now)
	if err != nil {
		return ProfileView{}, err
	}
	return views[key], nil
}

// buildProfiles computes a set of company profiles from one store batch and
// one shared thread/calendar read. The store owns the mailbox scan; this
// service overlays the priority thread truth and the requested calendar
// window, once for the whole set.
func (s *Service) buildProfiles(ctx context.Context, keys []string, months, eventDays int, now time.Time) (map[string]ProfileView, error) {
	// The priority ranking is the authoritative reply truth for the
	// conversations it contains. Resolve it first, then tell the store which
	// conversations it already covers: the store used to resolve all of them
	// and this layer discarded almost every assessment under the overlay.
	var authoritative map[string]threadintel.Assessment
	if s.priority != nil {
		states, err := s.priority.ThreadStates(ctx)
		if err != nil {
			return nil, err
		}
		authoritative = states
	}
	read := store.CompanyProfileRead{}
	if len(authoritative) > 0 {
		read.SkipStates = func(conversationID string) bool {
			_, ok := authoritative[conversationID]
			return ok
		}
		// Summaries are only rendered for open threads; a covered closed
		// conversation would otherwise be read and immediately discarded.
		read.OpenState = func(conversationID string) bool {
			state, ok := authoritative[conversationID]
			return !ok || threadintel.IsOpen(state.State)
		}
	}
	// The store owns the mail scan, the calendar window and the organization
	// match. It returns only active occurrences that resolve to the
	// organization, so this layer never re-derives a looser domain match.
	profiles, err := s.store.CompanyProfiles(ctx, keys, time.Time{}, now, months, eventDays, now, read)
	if err != nil {
		return nil, err
	}
	generatedAt := mail.Stamp(now)
	views := make(map[string]ProfileView, len(profiles))
	for key, profile := range profiles {
		states := profile.States
		if states == nil {
			states = map[string]threadintel.Assessment{}
		}
		for _, id := range profile.ConversationIDs {
			if state, ok := authoritative[id]; ok {
				states[id] = state
			}
		}
		counts, open := profileThreadsFrom(profile, states, profile.Summaries)
		// The store's total was computed from its nil-signal states. After
		// the authoritative overlay it is stale, so recompute it from the
		// counts this view actually serves.
		openTotal := threadintel.OpenTotal(counts)
		views[key] = ProfileView{
			GeneratedAt:      generatedAt,
			Key:              profile.Key,
			Label:            profile.Label,
			Declared:         profile.Declared,
			Domains:          nonNil(profile.Domains),
			Addresses:        nonNil(profile.Addresses),
			People:           nonNil(profile.People),
			Messages:         profile.Messages,
			Sent:             profile.Sent,
			Received:         profile.Received,
			Threads:          profile.Threads,
			FirstAt:          profile.FirstAt,
			LastAt:           profile.LastAt,
			LastInboundAt:    profile.LastInboundAt,
			LastOutboundAt:   profile.LastOutboundAt,
			Activity90:       profile.Activity90,
			Months:           nonNil(profile.Months),
			Recent:           nonNil(profile.Recent),
			OpenThreads:      open,
			ThreadStates:     counts,
			Events:           nonNil(profile.Events),
			Deadlines:        nonNil(profile.Deadlines),
			PeopleContacted:  profile.PeopleContacted,
			PeopleKnown:      profile.PeopleKnown,
			AddressesTotal:   profile.AddressesTotal,
			RecentTotal:      profile.RecentTotal,
			OpenThreadsTotal: openTotal,
			EventsTotal:      profile.EventsTotal,
			DeadlinesTotal:   profile.DeadlinesTotal,
		}
	}
	return views, nil
}

// Profiles returns the deep profiles of several keys. Fresh views are served
// from the same keyed cache as Profile; the rest are built in one store batch
// and installed through each key's Get, so concurrent callers of the same
// profile share one build. It is the start-up and Companies-screen path: five
// profiles cost one mailbox scan, not five.
func (s *Service) Profiles(ctx context.Context, keys []string, months, eventDays int) (map[string]ProfileView, error) {
	months, eventDays = profileWindow(months, eventDays)
	gen, routeGen := s.store.MailGeneration(), s.store.RouteGeneration()
	now := s.clock.Now()

	out := make(map[string]ProfileView, len(keys))
	missing := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		missing = append(missing, key)
	}

	// The first missing key builds the batch; later misses in this call take
	// their value from it. The mutex also covers a detached refresh of an
	// expired entry, which may run this closure after Profiles returned.
	var mu sync.Mutex
	var batch map[string]ProfileView
	var batchErr error
	loaded := false
	build := func(key string) func(context.Context) (profileValue, error) {
		return func(ctx context.Context) (profileValue, error) {
			mu.Lock()
			defer mu.Unlock()
			if !loaded {
				loaded = true
				batch, batchErr = s.buildProfiles(ctx, missing, months, eventDays, now)
			}
			if batchErr != nil {
				return profileValue{}, batchErr
			}
			view, ok := batch[key]
			if !ok {
				return profileValue{}, fmt.Errorf("world: no profile built for %q", key)
			}
			return profileValue{view: view, gen: gen, routeGen: routeGen}, nil
		}
	}
	for _, key := range missing {
		value, err := s.profiles.Get(ctx, profileCacheKey(key, months, eventDays), build(key))
		if err != nil {
			return nil, err
		}
		out[key] = value.view
	}
	return out, nil
}

// profileCacheKey names one cached profile by every input that changes its
// content.
// profileWindow is the one clamp prologue of the deep-profile reads: the
// months and event-day windows in their documented bounds. The single profile
// and the batch clamp identically, so a key built by one is a key the other
// can find.
func profileWindow(months, eventDays int) (int, int) {
	return mathx.Clamp(months, DefaultCompanyMonths, MaxCompanyMonths),
		mathx.Clamp(eventDays, DefaultCompanyEventDays, MaxCompanyEventDays)
}

func profileCacheKey(key string, months, eventDays int) string {
	return key + "|" + strconv.Itoa(months) + "|" + strconv.Itoa(eventDays)
}

// profileThreadsFrom assembles the state counts and the open-thread list from
// thread states and summaries already read. The batch profile reads them once
// for every organization.
func profileThreadsFrom(profile store.CompanyProfile, states map[string]threadintel.Assessment,
	summaries map[string]agenda.ThreadSummary) (map[string]int, []store.CompanyThread) {
	counts := threadintel.StateCounts(nil)
	open := []store.CompanyThread{}
	ids := profile.ConversationIDs
	recent := recentByConversation(profile.Recent)
	for _, id := range ids {
		state, ok := states[id]
		if !ok {
			continue
		}
		counts[string(state.State)]++
		if !threadintel.IsOpen(state.State) {
			continue
		}
		summary := summaries[id]
		message := recent[id]
		lastAt := state.LastAt
		if lastAt == "" {
			lastAt = summary.LastAt
		}
		if lastAt == "" {
			lastAt = message.Instant
		}
		subject := summary.Subject
		if subject == "" {
			subject = message.Subject
		}
		// The link target must be owner-visible: the resolver's last message
		// may live in a shared/delegate store that the message API refuses.
		messageID := profile.ConversationMessageIDs[id]
		if messageID == 0 {
			messageID = message.MessageID
		}
		if messageID == 0 {
			messageID = summary.MessageID
		}
		if messageID == 0 {
			messageID = state.LastMessageID
		}
		// The actor always names the other side. Awaiting_other means the
		// owner moved last, so the resolver's owner-facing actor is the owner
		// and must not be presented as the counterpart.
		actor := ""
		if state.State == threadintel.AwaitingOwner {
			actor = state.OwnerActorName
			if actor == "" {
				actor = state.OwnerActorAddress
			}
		}
		open = append(open, store.CompanyThread{
			ConversationID: id,
			Subject:        subject,
			LastAt:         lastAt,
			State:          string(state.State),
			Reason:         string(state.Reason),
			Actor:          actor,
			MessageID:      messageID,
			Messages:       state.Messages,
			Topic:          state.Topic,
			Subtopic:       state.Subtopic,
		})
	}
	return counts, store.OrderCompanyThreads(open)
}

// ValidCompanyKey reports whether key has the shape of a company key: the
// c: (declared company) or d: (domain) prefix, a non-empty body, at most 512
// bytes and no control characters. The router decodes the path segment before
// this runs; the store remains the owner of whether the key resolves.
func ValidCompanyKey(key string) bool {
	if len(key) < 3 || len(key) > maxCompanyKeyLen {
		return false
	}
	if !strings.HasPrefix(key, "c:") && !strings.HasPrefix(key, "d:") {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// recentByConversation picks the newest recent message of each conversation.
// profile.Recent is already newest first; the comparison keeps the helper
// correct if a caller ever hands it another order.
func recentByConversation(messages []store.CompanyMessage) map[string]store.CompanyMessage {
	out := map[string]store.CompanyMessage{}
	for _, message := range messages {
		if message.ConversationID == "" {
			continue
		}
		previous, ok := out[message.ConversationID]
		if !ok || message.Instant > previous.Instant ||
			(message.Instant == previous.Instant && message.MessageID > previous.MessageID) {
			out[message.ConversationID] = message
		}
	}
	return out
}

// zone is the owner's configured zone, UTC when it is unset or invalid. The
// mail query layer only needs it to resolve a relative time range; the
// explorer uses an all-time range.
func (s *Service) zone() *time.Location {
	return clock.ZoneOrUTC(s.cfg.String(clock.SettingZone))
}

// nonNil returns a copy of in, and an empty non-nil slice when in is empty.
// Every list a profile view exposes is an array, never null: JSON null in a
// list field would make the UI's "no rows" case a special case.
func nonNil[T any](in []T) []T {
	if len(in) == 0 {
		return []T{}
	}
	return append([]T{}, in...)
}
