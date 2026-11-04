package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// Bounds of one company profile. They keep a single read from becoming the
// whole mailbox: the streaming pass is bounded by the requested window, the
// thread resolver sees at most 2000 conversations, and every rendered list
// has a fixed cap.
//
// The exported month and event-day bounds are the one numeric owner of the
// profile window: the world service aliases them for its clamps and the API
// validates against those aliases, so no layer keeps a second number.
const (
	CompanyProfileDefaultMonths    = 12
	CompanyProfileMaxMonths        = 60
	companyProfileRecentCap        = 20
	companyProfileThreadCap        = 2000
	CompanyProfileOpenCap          = 20
	CompanyProfileEventDaysDefault = 30
	CompanyProfileEventDaysMax     = 365
	companyProfileEventCap         = 10
	companyProfileDeadlinePast     = 30 * 24 * time.Hour
	companyProfileDeadlineFuture   = 90 * 24 * time.Hour
	companyProfileDeadlineCap      = 20
	companyProfileAddressCap       = 100
	companyProfilePeopleCap        = 100
	companyProfileActivityWindow   = 90 * 24 * time.Hour
	// One profile reads the bodies of at most this many of its newest
	// received messages. Attributing by sender covers one distinct person
	// per body, where a per-person sample wasted bodies on repeat senders.
	companyProfileEvidenceMessages = 12
)

// CompanyActivity is the organization's mail activity over one fixed
// window, anchored on the profile's until instant. Messages, Sent and
// Received count distinct messages; Threads counts distinct conversations
// (a message without a conversation id is its own thread); ActiveDays counts
// distinct owner-local calendar days with at least one message.
type CompanyActivity struct {
	Messages   int `json:"messages"`
	Sent       int `json:"sent"`
	Received   int `json:"received"`
	Threads    int `json:"threads"`
	ActiveDays int `json:"active_days"`
}

// CompanyMonth is one local calendar month of mail volume. Months are
// zero-filled and ascending, ending with the month of the profile's until
// bound in the owner's time zone.
type CompanyMonth struct {
	Month    string `json:"month"`
	Messages int    `json:"messages"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
}

// CompanyMessage is one owner-visible message, metadata only. The body and
// the HTML alternative are never returned. A profile's bounded evidence
// pass may read a body locally to fill an empty person field, but the body
// never enters this view.
type CompanyMessage struct {
	MessageID      int64  `json:"message_id"`
	ConversationID string `json:"conversation_id,omitempty"`
	Subject        string `json:"subject"`
	SenderName     string `json:"sender_name"`
	SenderAddress  string `json:"sender_address"`
	Instant        string `json:"instant"`
	Sent           bool   `json:"sent"`
	HasAttachments bool   `json:"has_attachments"`
}

// CompanyThread is one conversation the organization still has open, as the
// one thread resolver sees it. Reason and Actor are the resolver's evidence,
// never a second interpretation.
type CompanyThread struct {
	ConversationID string `json:"conversation_id"`
	Subject        string `json:"subject"`
	LastAt         string `json:"last_at"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	Actor          string `json:"actor,omitempty"`
	MessageID      int64  `json:"message_id,omitempty"`
	Messages       int    `json:"messages,omitempty"`
	Topic          string `json:"topic,omitempty"`
	Subtopic       string `json:"subtopic,omitempty"`
}

// CompanyProfile is the deep view of one organization: its declared
// directory entry, the mail the owner exchanged with it, the recent
// messages (metadata only), the conversations still open, upcoming
// calendar evidence and due follow-ups. The profile is assembled from
// existing owner-visible mail, never calls a model and never returns a
// body. A bounded evidence pass reads the twelve newest received messages
// per organization locally to fill an empty name, phone or title.
//
// Totals are all time; Activity90 is the last 90 days anchored on the
// profile's until instant, and Months covers only the requested month
// buckets. The *Total fields report the size of each full list before its
// display cap, so a consumer can state that a list is truncated instead of
// silently showing a partial answer.
type CompanyProfile struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	Declared  bool            `json:"declared"`
	Domains   []string        `json:"domains"`
	Addresses []string        `json:"addresses"`
	People    []CompanyPerson `json:"people"`

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

	Events    []calendar.Event  `json:"events"`
	Deadlines []agenda.Deadline `json:"deadlines"`

	PeopleContacted int `json:"people_contacted"`
	PeopleKnown     int `json:"people_known"`

	AddressesTotal   int `json:"addresses_total"`
	RecentTotal      int `json:"recent_total"`
	OpenThreadsTotal int `json:"open_threads_total"`
	EventsTotal      int `json:"events_total"`
	DeadlinesTotal   int `json:"deadlines_total"`

	// ConversationIDs is every distinct conversation the organization's
	// in-window mail names, newest first and capped at the profile's
	// 2000-conversation resolver bound. It is not part of the JSON profile: a
	// caller that owns a richer reply signal (the world service) overlays the
	// thread states without reading the organization's mail again.
	ConversationIDs []string `json:"-"`
	// ConversationMessageIDs maps each capped conversation to its newest
	// owner-visible message id. The thread resolver may name a shared or
	// delegate message, which the owner-only message API refuses; callers
	// link to this id instead. It is not part of the JSON profile.
	ConversationMessageIDs map[string]int64 `json:"-"`
	// States and Summaries are the resolved conversation evidence for this
	// profile's window. A view layer that owns a richer reply signal overlays
	// States in place instead of re-reading the mailbox. Neither is part of
	// the JSON profile.
	States    map[string]threadintel.Assessment `json:"-"`
	Summaries map[string]agenda.ThreadSummary   `json:"-"`
}

// companyPersonStat accumulates one observed counterpart of the profile.
type companyPersonStat struct {
	names    map[string]int
	phone    string
	title    string
	messages map[string]bool
	sent     map[string]bool
	received map[string]bool
	lastAt   string
}

// CompanyProfile builds one organization's deep profile from owner-visible
// mail. key must be a CompanyBook key (a declared company key, or a domain
// key from Resolve/List/Org); an unknown key reports ErrUnknownCompany.
//
// The window is [since, until): a zero since is all time and a zero until is
// now. months is the number of local months to bucket (1..60, zero and
// negative mean 12, larger values are capped at 60). The buckets end with
// until's month in the owner's time zone (time.zone, UTC when unconfirmed).
// Messages, Sent and Received cover the whole window; Months covers only the
// last N local months, so its sum is lower when the window is longer.
//
// The mail pass is one stream over the same counterpart pairs as the
// company ranking, with the same visibility, owner and direction rules. A
// message is counted once per organization even when it has several
// recipients there, and a pair whose address is internal to the owner is
// skipped. A message with no parseable instant still counts in Messages and
// Recent, but cannot enter a month bucket and does not move FirstAt/LastAt.
// Threads counts a message with no conversation id as its own thread;
// open-thread resolution needs an id and therefore only sees real
// conversations. Domains, addresses and people merge the declared book entry
// with what mail observed; the order is observed people first by mail volume,
// then declared-only people by address.
//
// Open threads reuse s.ThreadStates with nil reply signals. That is the v1
// boundary: the resolver never claims the owner owes a reply without the
// classifier's message-level signal, so awaiting_owner and unknown stay at
// zero here until the caller supplies those signals. ConversationIDs exposes
// the capped conversation set so the world service can overlay the priority
// service's classifier-backed resolution. The counts of all six states
// returned here still reflect the nil-signal read. ThreadStates itself is the
// only thread-rule owner: the profile never re-derives a state. The
// conversation set is bounded by the profile window, but each state is
// resolved over every visible message in that conversation, including later
// mail: the open list is current truth, not a replay at until.
//
// Cost: one address-book read (CompanyBook), one in-memory pass over the
// cached counterpart population's requested window, then one ThreadStates and
// one ThreadSummaries query over at most 2000 conversations, one calendar
// read over the next 30 days, and one due-deadline read over [-30d, +90d).
// The population itself is built at most once per mail generation and is
// shared with the company ranking. No model call and no new table. The
// profile's own mail query selects metadata only; the reused ThreadStates read
// loads the conversation members it already needs to apply its existing reply
// rules. Memory is bounded by the organization's distinct messages in the
// window (identity sets and recent candidates) plus the capped conversation
// list.
// CompanyProfile reads one organization's deep profile. It is the
// single-key form of CompanyProfiles; every consumer that reads more than one
// profile at a time must use the batch so the mailbox is scanned once.
func (s *Store) CompanyProfile(ctx context.Context, key string, since, until time.Time, months, eventDays int, now time.Time) (CompanyProfile, error) {
	profiles, err := s.CompanyProfiles(ctx, []string{key}, since, until, months, eventDays, now)
	if err != nil {
		return CompanyProfile{}, err
	}
	return profiles[key], nil
}

// companyProfileAgg is the streaming accumulator of one organization. One
// counterpart scan feeds every requested organization, so the address book
// and the mailbox are read once for the whole companies screen instead of
// once per profile.
type companyProfileAgg struct {
	key           string
	org           CompanyOrg
	out           CompanyProfile
	seen          map[string]bool
	threads       map[string]bool
	conversations map[string]bool
	convLast      map[string]string
	convSubject   map[string]string
	convMessage   map[string]int64
	recent        map[string]CompanyMessage
	observed      map[string]*companyPersonStat
	domainSet     map[string]bool
	addressSet    map[string]bool

	activityMessages map[string]bool
	activitySent     map[string]bool
	activityReceived map[string]bool
	activityThreads  map[string]bool
	activityDays     map[string]bool
}

func newCompanyProfileAgg(key string, org CompanyOrg, months []CompanyMonth) *companyProfileAgg {
	return &companyProfileAgg{
		key: key, org: org,
		out: CompanyProfile{
			Key: org.Key, Label: org.Label, Declared: org.Declared,
			Domains: []string{}, Addresses: []string{}, People: []CompanyPerson{},
			Months: append([]CompanyMonth{}, months...),
			Recent: []CompanyMessage{}, OpenThreads: []CompanyThread{},
			ThreadStates: map[string]int{}, Events: []calendar.Event{}, Deadlines: []agenda.Deadline{},
		},
		seen: map[string]bool{}, threads: map[string]bool{}, conversations: map[string]bool{},
		convLast: map[string]string{}, convSubject: map[string]string{}, convMessage: map[string]int64{},
		recent: map[string]CompanyMessage{}, observed: map[string]*companyPersonStat{},
		domainSet: map[string]bool{}, addressSet: map[string]bool{},
		activityMessages: map[string]bool{}, activitySent: map[string]bool{},
		activityReceived: map[string]bool{}, activityThreads: map[string]bool{},
		activityDays: map[string]bool{},
	}
}

// CompanyProfiles reads every requested organization's deep profile from one
// address-book read and the counterpart population cached for the current
// mail generation. The profiles are independent views of the same mailbox, so
// building them together removes the repeated scan, thread resolution,
// calendar read and deadline read without changing any profile's content. A
// key that does not resolve fails the whole batch: a profile is display data,
// never silently absent.
//
// eventDays is the forward calendar window (1..365, zero means 30). Only
// active occurrences (not canceled, declined or free) whose organizer or
// attendee resolves to the organization are returned. The store owns that
// match so a consumer cannot re-derive a looser one from domains alone.
// CompanyProfileRead is an optional rule of a profile batch. SkipStates lets
// a caller that already holds the authoritative assessment of a conversation
// (the priority overlay) keep the store from resolving it a second time; the
// caller must fill those states before reading open threads.
type CompanyProfileRead struct {
	SkipStates func(conversationID string) bool
	// OpenState lets a caller that resolved the state elsewhere limit the
	// thread summaries to conversations that are still open. A summary of a
	// closed conversation is never rendered, so reading it is pure payload.
	OpenState func(conversationID string) bool
}

// companyProfileRowsQuery is the one counterpart-pair shape behind the SQL
// parity oracle: every pair the counterpart CTE selected, joined to the
// message metadata the accumulators read. Every accumulator is
// order-independent (FirstAt is a MIN; LastAt, the recent member and the
// conversation last member compare explicitly with message-id tie-breaks;
// contacts.BestName counts names), so the read deliberately has no ORDER BY:
// the matching sequence is not a page order and sorting the whole counterpart
// population in a temporary B-tree only costs time. It is a function so the
// plan test pins the statement.
func companyProfileRowsQuery(cte string) string {
	return cte + ` SELECT p.source_id, p.store_id, p.msg_id, p.message_id,
		p.conversation_id, p.instant, p.is_sent, p.address_norm, p.name,
		m.subject, m.sender_name, m.sender_address, m.has_attachments
		FROM clean p JOIN messages m ON m.id = p.message_id`
}

func (s *Store) CompanyProfiles(ctx context.Context, keys []string, since, until time.Time, months, eventDays int, now time.Time, reads ...CompanyProfileRead) (map[string]CompanyProfile, error) {
	if len(keys) == 0 {
		return map[string]CompanyProfile{}, nil
	}
	book, err := s.CompanyBook(ctx)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = s.clock.Now()
	}
	if until.IsZero() {
		until = now
	}
	if !since.IsZero() && !since.Before(until) {
		return nil, fmt.Errorf("store: company profile window: since %s is not before until %s",
			instantString(since), instantString(until))
	}
	switch {
	case months < 1:
		months = CompanyProfileDefaultMonths
	case months > CompanyProfileMaxMonths:
		months = CompanyProfileMaxMonths
	}
	switch {
	case eventDays < 1:
		eventDays = CompanyProfileEventDaysDefault
	case eventDays > CompanyProfileEventDaysMax:
		eventDays = CompanyProfileEventDaysMax
	}
	loc := s.companyProfileZone()
	monthList, monthAt := companyProfileMonths(loc, until, months)
	activityStart := until.Add(-companyProfileActivityWindow)
	aggs, requested, err := companyProfileAggs(keys, book, monthList)
	if err != nil {
		return nil, err
	}

	counterparts, err := s.CompanyCounterparts(ctx)
	if err != nil {
		return nil, err
	}
	rows := newCachedCounterpartRows(counterparts, instantString(since), instantString(until))
	return s.companyProfilesFromRows(ctx, book, aggs, requested, now, until, eventDays, loc, monthAt,
		activityStart, rows, reads...)
}

// companyProfileAggs creates one accumulator per requested organization and
// fails the whole batch on an unresolved key. It is the one key-validation
// owner of the batch, shared by the production read and its parity oracle.
// requested keeps every spelling the caller asked for, including two that
// resolve to the same organization.
func companyProfileAggs(keys []string, book *CompanyBook, months []CompanyMonth) (map[string]*companyProfileAgg, map[string]string, error) {
	aggs := make(map[string]*companyProfileAgg, len(keys))
	requested := make(map[string]string, len(keys))
	for _, key := range keys {
		if _, ok := requested[key]; ok {
			continue
		}
		org, ok := book.Org(key)
		if !ok {
			return nil, nil, fmt.Errorf("%w: %q", ErrUnknownCompany, key)
		}
		requested[key] = org.Key
		if _, ok := aggs[org.Key]; !ok {
			aggs[org.Key] = newCompanyProfileAgg(key, org, months)
		}
	}
	return aggs, requested, nil
}

// companyProfilesWithQuery builds the requested profiles from one SQL
// counterpart-pair stream. Tests pass the previous ordered statement here as
// the parity oracle, so the unordered production shape cannot drift from it.
func (s *Store) companyProfilesWithQuery(ctx context.Context, book *CompanyBook,
	aggs map[string]*companyProfileAgg, requested map[string]string,
	now, until time.Time, eventDays int, loc *time.Location, monthAt map[string]int,
	activityStart time.Time, query string, args []any, reads ...CompanyProfileRead) (map[string]CompanyProfile, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: company profiles: %w", err)
	}
	return s.companyProfilesFromRows(ctx, book, aggs, requested, now, until, eventDays, loc, monthAt,
		activityStart, &sqlCounterpartRows{rows: rows}, reads...)
}

// foldCompanyProfileRows is the one row->accumulator fold of a company
// profile. Every accumulator is order-independent (FirstAt is a MIN; LastAt,
// the recent member and the conversation last member compare explicitly with
// message-id tie-breaks; contacts.BestName counts names), so the cached stream
// and the ordered SQL oracle can share it without changing a profile.
func foldCompanyProfileRows(rows counterpartRowReader, book *CompanyBook,
	aggs map[string]*companyProfileAgg, monthAt map[string]int, loc *time.Location,
	activityStart, until time.Time) error {
	closeRows := func() { _ = rows.Close() }
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			closeRows()
			return err
		}
		resolved, ok := book.resolve(row.addressNorm)
		if !ok || book.isInternalResolution(resolved) {
			continue
		}
		agg := aggs[resolved.org.Key]
		if agg == nil {
			continue
		}
		out := &agg.out
		message := messageIdentity(row.sourceID, row.storeID, row.msgID, row.messageID)
		agg.addressSet[row.addressNorm] = true
		if resolved.domain != "" {
			agg.domainSet[resolved.domain] = true
		}

		if !agg.seen[message] {
			agg.seen[message] = true
			out.Messages++
			if row.isSent {
				out.Sent++
			} else {
				out.Received++
			}
			if at, ok := mail.Parse(row.instant); ok {
				if i, ok := monthAt[mail.MonthKey(at.In(loc))]; ok {
					out.Months[i].Messages++
					if row.isSent {
						out.Months[i].Sent++
					} else {
						out.Months[i].Received++
					}
				}
				// Activity90 is anchored on until, not on the wall clock, so
				// the same stored mail produces the same counts when a test
				// or a historical view supplies a different bound.
				if !at.Before(activityStart) && at.Before(until) {
					agg.activityMessages[message] = true
					agg.activityDays[at.In(loc).Format("2006-01-02")] = true
					if row.isSent {
						agg.activitySent[message] = true
					} else {
						agg.activityReceived[message] = true
					}
					if row.conversationID != "" {
						agg.activityThreads["c:"+row.conversationID] = true
					} else {
						agg.activityThreads[fmt.Sprintf("m:%d", row.messageID)] = true
					}
				}
			}
		}
		// FirstAt is the oldest dated pair. An undated pair has instant
		// "", and must not reset an already-seen date: with no ORDER BY
		// its position is arbitrary, so the fold has to ignore it exactly
		// as the old ordered statement's MIN did.
		if row.instant != "" && (out.FirstAt == "" || row.instant < out.FirstAt) {
			out.FirstAt = row.instant
		}
		if row.instant > out.LastAt {
			out.LastAt = row.instant
		}
		if row.isSent {
			if out.LastOutboundAt == "" || row.instant > out.LastOutboundAt {
				out.LastOutboundAt = row.instant
			}
		} else if out.LastInboundAt == "" || row.instant > out.LastInboundAt {
			out.LastInboundAt = row.instant
		}

		thread := ""
		if row.conversationID != "" {
			thread = "c:" + row.conversationID
			agg.conversations[row.conversationID] = true
			if row.instant > agg.convLast[row.conversationID] ||
				(row.instant == agg.convLast[row.conversationID] && row.messageID > agg.convMessage[row.conversationID]) {
				agg.convLast[row.conversationID] = row.instant
				agg.convSubject[row.conversationID] = row.subject
				agg.convMessage[row.conversationID] = row.messageID
			}
		} else {
			thread = fmt.Sprintf("m:%d", row.messageID)
		}
		agg.threads[thread] = true

		person := agg.observed[row.addressNorm]
		if person == nil {
			person = &companyPersonStat{names: map[string]int{},
				messages: map[string]bool{}, sent: map[string]bool{}, received: map[string]bool{}}
			agg.observed[row.addressNorm] = person
		}
		if observed := strings.TrimSpace(row.name); observed != "" {
			person.names[observed]++
		}
		if !person.messages[message] {
			person.messages[message] = true
			if row.isSent {
				person.sent[message] = true
			} else {
				person.received[message] = true
			}
		}
		if row.instant > person.lastAt {
			person.lastAt = row.instant
		}

		entry := CompanyMessage{MessageID: row.messageID, ConversationID: row.conversationID, Subject: row.subject,
			SenderName: row.senderName, SenderAddress: row.senderAddress, Instant: row.instant,
			Sent: row.isSent, HasAttachments: row.hasAttachments}
		if previous, found := agg.recent[message]; !found ||
			entry.Instant > previous.Instant ||
			(entry.Instant == previous.Instant && entry.MessageID > previous.MessageID) {
			agg.recent[message] = entry
		}
	}
	if err := rows.Err(); err != nil {
		closeRows()
		return err
	}
	// Release the streaming read before the thread, calendar and deadline
	// reads: with store.max_open_conns = 1 they must not wait on this rows
	// handle.
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close company profile rows: %w", err)
	}
	return nil
}

// companyProfilesFromRows folds one counterpart stream into the requested
// accumulators, then performs the shared evidence, thread, calendar and
// deadline reads. The cached production read and the SQL parity oracle share
// this body; only their counterpartRowReader differs.
func (s *Store) companyProfilesFromRows(ctx context.Context, book *CompanyBook,
	aggs map[string]*companyProfileAgg, requested map[string]string,
	now, until time.Time, eventDays int, loc *time.Location, monthAt map[string]int,
	activityStart time.Time, rows counterpartRowReader, reads ...CompanyProfileRead) (map[string]CompanyProfile, error) {
	if err := foldCompanyProfileRows(rows, book, aggs, monthAt, loc, activityStart, until); err != nil {
		return nil, err
	}
	if err := s.enrichCompanyPeople(ctx, aggs); err != nil {
		return nil, err
	}

	// Finalize the per-organization sets before the batched shared reads.
	idsByOrg := make(map[string][]string, len(aggs))
	var allIDs []string
	seenIDs := map[string]bool{}
	for key, agg := range aggs {
		agg.out.Threads = len(agg.threads)
		ids := companyConversationIDs(agg.conversations, agg.convLast)
		agg.out.ConversationIDs = ids
		agg.out.ConversationMessageIDs = make(map[string]int64, len(ids))
		for _, id := range ids {
			agg.out.ConversationMessageIDs[id] = agg.convMessage[id]
			if !seenIDs[id] {
				seenIDs[id] = true
				allIDs = append(allIDs, id)
			}
		}
		idsByOrg[key] = ids
	}

	var states map[string]threadintel.Assessment
	var summaries map[string]agenda.ThreadSummary
	var err error
	if len(allIDs) > 0 {
		var read CompanyProfileRead
		if len(reads) > 0 {
			read = reads[0]
		}
		stateIDs := allIDs
		if read.SkipStates != nil {
			stateIDs = make([]string, 0, len(allIDs))
			for _, id := range allIDs {
				if !read.SkipStates(id) {
					stateIDs = append(stateIDs, id)
				}
			}
		}
		if len(stateIDs) > 0 {
			states, err = s.ThreadStates(ctx, stateIDs, nil)
			if err != nil {
				return nil, err
			}
		}
		summaryIDs := allIDs
		if read.OpenState != nil {
			summaryIDs = make([]string, 0, len(allIDs))
			for _, id := range allIDs {
				if read.OpenState(id) {
					summaryIDs = append(summaryIDs, id)
				}
			}
		}
		if len(summaryIDs) > 0 {
			summaries, err = s.ThreadSummaries(ctx, summaryIDs)
			if err != nil {
				return nil, err
			}
		}
	}
	events, err := s.CalendarEvents(ctx, now, now.Add(time.Duration(eventDays)*24*time.Hour))
	if err != nil {
		return nil, err
	}
	deadlines, err := s.DueDeadlines(ctx, now.Add(-companyProfileDeadlinePast), now.Add(companyProfileDeadlineFuture))
	if err != nil {
		return nil, err
	}

	profiles := make(map[string]CompanyProfile, len(requested))
	for key, agg := range aggs {
		org := agg.org
		out := agg.out
		if !org.Declared {
			observedNames := map[string]int{}
			for _, stat := range agg.observed {
				for name, count := range stat.names {
					observedNames[name] += count
				}
			}
			domain := ""
			if len(org.Domains) > 0 {
				domain = org.Domains[0]
			}
			if label := domainDisplayName(domain, observedNames); label != "" {
				out.Label = label
			}
		}
		declared := make(map[string]CompanyPerson, len(org.People))
		for _, person := range org.People {
			declared[person.Address] = person
		}

		// People: observed first (most mail first), then declared-only people.
		addresses := make([]string, 0, len(agg.observed))
		for address := range agg.observed {
			addresses = append(addresses, address)
		}
		sort.Slice(addresses, func(i, j int) bool {
			pi, pj := agg.observed[addresses[i]], agg.observed[addresses[j]]
			if len(pi.messages) != len(pj.messages) {
				return len(pi.messages) > len(pj.messages)
			}
			if pi.lastAt != pj.lastAt {
				return pi.lastAt > pj.lastAt
			}
			return addresses[i] < addresses[j]
		})
		for _, address := range addresses {
			stat := agg.observed[address]
			person := CompanyPerson{Address: address, Name: contacts.BestName(stat.names, ""), Declared: false,
				Phone: stat.phone, JobTitle: stat.title,
				Messages: len(stat.messages), Sent: len(stat.sent), Received: len(stat.received), LastAt: stat.lastAt}
			if known, found := declared[address]; found {
				person.Declared = true
				person.Company = known.Company
				person.Department = known.Department
				person.JobTitle = firstNonEmpty(known.JobTitle, stat.title)
				person.Phone = firstNonEmpty(known.Phone, stat.phone)
				person.Name = contacts.BestName(stat.names, known.Name)
			}
			out.People = append(out.People, person)
		}
		declaredOnly := make([]CompanyPerson, 0, len(declared))
		for address, person := range declared {
			if _, found := agg.observed[address]; !found {
				declaredOnly = append(declaredOnly, person)
			}
		}
		sort.Slice(declaredOnly, func(i, j int) bool { return declaredOnly[i].Address < declaredOnly[j].Address })
		out.People = append(out.People, declaredOnly...)
		out.PeopleContacted = len(agg.observed)
		out.PeopleKnown = len(out.People)
		if len(out.People) > companyProfilePeopleCap {
			out.People = out.People[:companyProfilePeopleCap]
		}

		// Recent messages: newest first, metadata only.
		recentList := make([]CompanyMessage, 0, len(agg.recent))
		for _, message := range agg.recent {
			recentList = append(recentList, message)
		}
		sort.Slice(recentList, func(i, j int) bool {
			if recentList[i].Instant != recentList[j].Instant {
				return recentList[i].Instant > recentList[j].Instant
			}
			return recentList[i].MessageID > recentList[j].MessageID
		})
		out.RecentTotal = len(recentList)
		if len(recentList) > companyProfileRecentCap {
			recentList = recentList[:companyProfileRecentCap]
		}
		out.Recent = recentList

		out.Domains = sortedUniqueStrings(append(append([]string{}, org.Domains...), companyKeys(agg.domainSet)...))
		allAddresses := sortedUniqueStrings(append(append([]string{}, org.Addresses...), companyKeys(agg.addressSet)...))
		out.AddressesTotal = len(allAddresses)
		if len(allAddresses) > companyProfileAddressCap {
			allAddresses = allAddresses[:companyProfileAddressCap]
		}
		out.Addresses = allAddresses
		out.Activity90 = CompanyActivity{
			Messages:   len(agg.activityMessages),
			Sent:       len(agg.activitySent),
			Received:   len(agg.activityReceived),
			Threads:    len(agg.activityThreads),
			ActiveDays: len(agg.activityDays),
		}

		out.States = make(map[string]threadintel.Assessment, len(idsByOrg[key]))
		out.Summaries = make(map[string]agenda.ThreadSummary, len(idsByOrg[key]))
		for _, id := range idsByOrg[key] {
			if state, ok := states[id]; ok {
				out.States[id] = state
			}
			if summary, ok := summaries[id]; ok {
				out.Summaries[id] = summary
			}
		}
		open, counts := companyOpenThreadsFrom(idsByOrg[key], agg.convLast, agg.convSubject, agg.convMessage, states, summaries)
		out.OpenThreads = open
		out.ThreadStates = counts
		out.OpenThreadsTotal = threadintel.OpenTotal(counts)

		for _, event := range events {
			if !event.Active() || !companyEventMatches(book, org, event) {
				continue
			}
			out.EventsTotal++
			if len(out.Events) < companyProfileEventCap {
				out.Events = append(out.Events, event)
			}
		}
		for _, deadline := range deadlines {
			matched := false
			for _, participant := range deadline.Participants {
				if book.matchesOrg(org, participant) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			out.DeadlinesTotal++
			if len(out.Deadlines) < companyProfileDeadlineCap {
				out.Deadlines = append(out.Deadlines, deadline)
			}
		}

		// A domain key with no contact and no observed mail names no
		// organization this mailbox knows. It must not become an empty
		// invented profile.
		if !org.Declared && out.Messages == 0 && len(org.People) == 0 && len(org.Addresses) == 0 {
			return nil, fmt.Errorf("%w: %q", ErrUnknownCompany, agg.key)
		}
		profiles[agg.key] = out
		// Also key by the resolved organization so a second requested
		// spelling that resolves here is always found.
		profiles[org.Key] = out
	}
	// Return every requested spelling, including two spellings that resolve
	// to the same organization.
	for key, orgKey := range requested {
		if _, ok := profiles[key]; !ok {
			profiles[key] = profiles[orgKey]
		}
	}
	return profiles, nil
}

// companyOpenThreads resolves the organization's conversations and returns
// the open ones plus the count of every state. ids is the capped, newest-first
// set from companyConversationIDs. replySignals is nil: see the CompanyProfile
// contract for that v1 boundary.
func (s *Store) companyOpenThreads(ctx context.Context, ids []string, convLast, convSubject map[string]string, convMessage map[string]int64) ([]CompanyThread, map[string]int, error) {
	if len(ids) == 0 {
		open, counts := companyOpenThreadsFrom(ids, convLast, convSubject, convMessage, nil, nil)
		return open, counts, nil
	}
	states, err := s.ThreadStates(ctx, ids, nil)
	if err != nil {
		return nil, nil, err
	}
	summaries, err := s.ThreadSummaries(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	open, counts := companyOpenThreadsFrom(ids, convLast, convSubject, convMessage, states, summaries)
	return open, counts, nil
}

// companyOpenThreadsFrom is the pure assembly of the open-thread list from
// states and summaries already read. The batch profile reads them once for
// every organization; the single profile reads them for its own ids.
func companyOpenThreadsFrom(ids []string, convLast, convSubject map[string]string, convMessage map[string]int64,
	states map[string]threadintel.Assessment, summaries map[string]agenda.ThreadSummary) ([]CompanyThread, map[string]int) {
	counts := threadintel.StateCounts(nil)
	if len(ids) == 0 {
		return []CompanyThread{}, counts
	}
	open := make([]CompanyThread, 0, len(states))
	for _, id := range ids {
		assessment, found := states[id]
		if !found {
			continue
		}
		counts[string(assessment.State)]++
		if !threadintel.IsOpen(assessment.State) {
			continue
		}
		summary := summaries[id]
		lastAt := assessment.LastAt
		if lastAt == "" {
			lastAt = summary.LastAt
		}
		if lastAt == "" {
			lastAt = convLast[id]
		}
		subject := summary.Subject
		if subject == "" {
			subject = convSubject[id]
		}
		// Actor always names the other side. For awaiting_other the last move
		// was the owner's, so the resolver's owner-facing actor is the owner:
		// showing it as the counterpart would invert the thread. The state
		// label already says "waiting on them", so the actor stays empty.
		actor := ""
		if assessment.State == threadintel.AwaitingOwner {
			actor = assessment.OwnerActorName
			if actor == "" {
				actor = assessment.OwnerActorAddress
			}
		}
		// The resolver may name a shared/delegate message, but this view is
		// owner-only. Link to the newest owner-visible message instead, and
		// fall back to the resolver only when the pass never saw one.
		messageID := convMessage[id]
		if messageID == 0 {
			messageID = assessment.LastMessageID
		}
		if messageID == 0 {
			messageID = summary.MessageID
		}
		open = append(open, CompanyThread{ConversationID: id, Subject: subject, LastAt: lastAt,
			State: string(assessment.State), Reason: string(assessment.Reason), Actor: actor,
			MessageID: messageID, Messages: assessment.Messages,
			Topic: assessment.Topic, Subtopic: assessment.Subtopic})
	}
	return OrderCompanyThreads(open), counts
}

// OrderCompanyThreads orders a company's open threads the one way the owner
// reads them (most urgent state first, then newest, then conversation id as
// the stable tie-break) and applies the display cap. The order and the cap are
// the list's contract, not each producer's: the profile route and the hub's
// company projection show the same list, so a second spelling could drift by
// a tie-break or an uncapped list. It sorts in place and returns the capped
// slice.
func OrderCompanyThreads(open []CompanyThread) []CompanyThread {
	sort.Slice(open, func(i, j int) bool {
		ri, rj := threadintel.StateOrder(threadintel.State(open[i].State)), threadintel.StateOrder(threadintel.State(open[j].State))
		if ri != rj {
			return ri < rj
		}
		if open[i].LastAt != open[j].LastAt {
			return open[i].LastAt > open[j].LastAt
		}
		return open[i].ConversationID < open[j].ConversationID
	})
	if len(open) > CompanyProfileOpenCap {
		open = open[:CompanyProfileOpenCap]
	}
	return open
}

// enrichCompanyPeople reads a bounded set of received evidence blocks for the
// people a profile can show. It is local evidence only: the body is never
// returned, the names and phones fill empty fields, and a declared address
// book value still wins. The newest received messages of each organization
// are read once and attributed by sender, so one body always covers one
// distinct person instead of repeating a frequent sender's evidence block.
func (s *Store) enrichCompanyPeople(ctx context.Context, aggs map[string]*companyProfileAgg) error {
	type target struct {
		stat    *companyPersonStat
		address string
		entry   CompanyMessage
	}
	var targets []target
	var ids []int64
	seenIDs := map[int64]bool{}
	for _, agg := range aggs {
		received := make([]CompanyMessage, 0, len(agg.recent))
		for _, entry := range agg.recent {
			if entry.Sent || entry.MessageID <= 0 || strings.TrimSpace(entry.SenderAddress) == "" {
				continue
			}
			received = append(received, entry)
		}
		sort.Slice(received, func(i, j int) bool {
			if received[i].Instant != received[j].Instant {
				return received[i].Instant > received[j].Instant
			}
			return received[i].MessageID > received[j].MessageID
		})
		// At most two messages per sender, so a burst from one correspondent
		// cannot consume the whole budget and hide every other signer.
		perSender := map[string]int{}
		taken := 0
		for _, entry := range received {
			if taken >= companyProfileEvidenceMessages {
				break
			}
			address := mail.NormalizeAddress(entry.SenderAddress)
			stat := agg.observed[address]
			if stat == nil || perSender[address] >= 2 {
				continue
			}
			if contacts.BestName(stat.names, "") != "" && stat.phone != "" && stat.title != "" {
				continue
			}
			perSender[address]++
			taken++
			targets = append(targets, target{stat: stat, address: address, entry: entry})
			if !seenIDs[entry.MessageID] {
				seenIDs[entry.MessageID] = true
				ids = append(ids, entry.MessageID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("store: company evidence ids: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, body FROM messages WHERE id IN `+stringSetSQL, string(idJSON))
	if err != nil {
		return fmt.Errorf("store: company evidence bodies: %w", err)
	}
	defer rows.Close()
	bodies := make(map[int64]string, len(ids))
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return fmt.Errorf("store: company evidence body: %w", err)
		}
		bodies[id] = body
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: company evidence bodies: %w", err)
	}
	for _, target := range targets {
		body := bodies[target.entry.MessageID]
		if body == "" {
			continue
		}
		evidenceText := mail.EvidenceText(body)
		if evidenceText == "" {
			continue
		}
		header := contacts.BestName(target.stat.names, target.entry.SenderName)
		evidence := contacts.ExtractSignature(evidenceText, header, target.address)
		if evidence.Name != "" {
			target.stat.names[evidence.Name] += 2
		}
		if target.stat.phone == "" && evidence.Phone != "" {
			target.stat.phone = evidence.Phone
		}
		if target.stat.title == "" && evidence.Title != "" {
			target.stat.title = evidence.Title
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// companyConversationIDs returns the distinct conversations in the profile
// window, newest last-mail first, then by id, capped at the resolver bound.
// A conversation with no parseable instant sorts last but still participates:
// its thread state can be resolved even when it cannot enter a month bucket.
func companyConversationIDs(conversations map[string]bool, convLast map[string]string) []string {
	ids := make([]string, 0, len(conversations))
	for id := range conversations {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if convLast[ids[i]] != convLast[ids[j]] {
			return convLast[ids[i]] > convLast[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > companyProfileThreadCap {
		ids = ids[:companyProfileThreadCap]
	}
	return ids
}

// companyEventMatches reports whether the organizer or an attendee names the
// organization.
func companyEventMatches(book *CompanyBook, org CompanyOrg, event calendar.Event) bool {
	if book.matchesOrg(org, event.OrganizerEmail) {
		return true
	}
	for _, attendee := range event.Attendees {
		if book.matchesOrg(org, attendee.Address) {
			return true
		}
	}
	return false
}

// companyProfileZone is the owner's configured zone, UTC when it is not
// confirmed. A profile must still read rather than fail on an unset zone.
func (s *Store) companyProfileZone() *time.Location {
	return clock.ZoneOrUTC(s.cfg.String(clock.SettingZone))
}

// companyProfileMonths returns n zero-filled local months ending with the
// month of until, and the index from month key to slice position.
func companyProfileMonths(loc *time.Location, until time.Time, n int) ([]CompanyMonth, map[string]int) {
	local := until.In(loc)
	end := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	months := make([]CompanyMonth, n)
	index := make(map[string]int, n)
	for i := range months {
		key := mail.MonthKey(end.AddDate(0, i-(n-1), 0))
		months[i] = CompanyMonth{Month: key}
		index[key] = i
	}
	return months, index
}

// companyKeys returns the keys of a string set.
func companyKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}
