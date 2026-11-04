// Package agenda turns the world model into "what should I do in the next X
// time". It joins three kinds of evidence the rest of the app already owns:
// calendar occurrences, conversations where the owner still owes a reply,
// and mail with a due date. Nothing here reads mail bodies or classifies
// text; the ranker is structural.
//
// The distinctive join is meeting-linked obligations: when a waiting thread
// shares a participant with an upcoming event, the obligation is scheduled
// before that event instead of floating in a generic "to do" list. The
// result explains itself with reason codes, never with invented urgency.
package agenda

import (
	"sort"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
)

// SettingDefaultHours is the settings key of the agenda's default horizon, in
// hours. The schema owns the value; the runtime converts it to a duration.
const SettingDefaultHours = "agenda.default_hours"

// Kind is the evidence behind one agenda item.
type Kind string

// Agenda item kinds.
const (
	KindEvent    Kind = "event"
	KindReply    Kind = "reply"
	KindDeadline Kind = "deadline"
)

// Reason explains why an item is in the agenda.
type Reason string

// Item reasons.
const (
	ReasonMeeting         Reason = "meeting"
	ReasonMeetingWithThem Reason = "meeting_with_them"
	ReasonWaitingSince    Reason = "waiting_since"
	ReasonDue             Reason = "due"
)

// Obligation is one conversation where the owner still owes a reply. The
// thread resolver produced the state; this package never re-derives it.
type Obligation struct {
	ConversationID string    `json:"conversation_id"`
	MessageID      int64     `json:"message_id"`
	Subject        string    `json:"subject"`
	ActorName      string    `json:"actor_name"`
	LastAt         time.Time `json:"last_at"`
	Participants   []string  `json:"participants"` // normalized, owner excluded
}

// Deadline is one message the owner flagged with a due date.
type Deadline struct {
	MessageID      int64     `json:"message_id"`
	ConversationID string    `json:"conversation_id"`
	Subject        string    `json:"subject"`
	SenderName     string    `json:"sender_name"`
	Due            time.Time `json:"due"`
	Participants   []string  `json:"participants"`
}

// ThreadSummary is the owner-safe view of one conversation: subject,
// recency and non-owner participants, never a shared body.
type ThreadSummary struct {
	ConversationID string   `json:"conversation_id"`
	Subject        string   `json:"subject"`
	MessageID      int64    `json:"message_id"`
	LastAt         string   `json:"last_at"`
	Participants   []string `json:"participants"`
}

// Input is the whole evidence set of one agenda build. Owner is the set of
// the owner's normalized addresses.
type Input struct {
	Now         time.Time
	Horizon     time.Duration
	Owner       map[string]bool
	Events      []calendar.Event
	Obligations []Obligation
	Deadlines   []Deadline
}

// Item is one line of the agenda. At is when it matters: an event's start, a
// deadline's due instant, or, for a reply, the start of the meeting it must
// precede (or now when nothing links it).
type Item struct {
	Kind           Kind       `json:"kind"`
	At             time.Time  `json:"at"`                // when to act (event start, due, or now)
	LastAt         *time.Time `json:"last_at,omitempty"` // reply: when the triggering mail arrived
	End            *time.Time `json:"end,omitempty"`
	Title          string     `json:"title"`
	Detail         string     `json:"detail,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	MessageID      int64      `json:"message_id,omitempty"`
	EventID        string     `json:"event_id,omitempty"`
	Location       string     `json:"location,omitempty"`
	AllDay         bool       `json:"all_day,omitempty"`
	Participants   int        `json:"participants,omitempty"`
	Slack          float64    `json:"slack_seconds"` // At - Now; negative = overdue
	LinkedEventID  string     `json:"linked_event_id,omitempty"`
	LinkedEvent    string     `json:"linked_event_title,omitempty"`
	Reasons        []Reason   `json:"reasons"`
}

// Result is the rendered agenda plus the counts the screen shows.
type Result struct {
	Now          time.Time `json:"now"`
	HorizonHours float64   `json:"horizon_hours"`
	Until        time.Time `json:"until"`
	Items        []Item    `json:"items"`
	Events       int       `json:"events"`
	Obligations  int       `json:"obligations"` // replies included before the horizon
	Backlog      int       `json:"backlog"`     // waiting threads left out (older or beyond the cap)
	Deadlines    int       `json:"deadlines"`
	NextEvent    *Item     `json:"next_event,omitempty"`
	BusySeconds  int64     `json:"busy_seconds"`
	Truncated    bool      `json:"truncated"`
}

// MaxItems bounds one agenda response. The cut is reported, never silent.
const MaxItems = 60

// Bounds that keep "next X" about the near future instead of the whole
// backlog. Unlinked replies are included only when they are recent, and at
// most maxUnlinked of them; older or surplus threads are counted in
// Backlog. A follow-up more than a day overdue is backlog too: it stays in
// Mail, not in a three-hour agenda.
const (
	maxUnlinked     = 5
	unlinkedWindow  = 7 * 24 * time.Hour
	overdueDeadline = 24 * time.Hour
)

// Build computes the agenda for [Now, Now+Horizon]. It is pure: same input,
// same output. Events outside the window, canceled/declined/free events and
// duplicate occurrences (same clean global id and start) are dropped.
func Build(in Input) Result {
	if in.Horizon <= 0 {
		in.Horizon = 3 * time.Hour
	}
	until := in.Now.Add(in.Horizon)
	out := Result{Now: in.Now.UTC(), HorizonHours: in.Horizon.Hours(),
		Until: until.UTC(), Items: []Item{}}

	events := dedupeEvents(in.Events, in.Owner)
	for _, e := range events {
		if !e.Active() || !e.Overlaps(in.Now, until) {
			continue
		}
		if e.Start.After(in.Now) {
			out.BusySeconds += int64(e.End.Sub(e.Start).Seconds())
		} else if e.End.After(in.Now) {
			out.BusySeconds += int64(e.End.Sub(in.Now).Seconds())
		}
		end := e.End.UTC()
		it := Item{Kind: KindEvent, At: e.Start.UTC(), End: &end, Title: e.Subject,
			EventID: e.EntryID, Location: e.Location, AllDay: e.AllDay,
			Participants: len(e.ParticipantAddresses(in.Owner)), Reasons: []Reason{ReasonMeeting}}
		it.Slack = it.At.Sub(in.Now).Seconds()
		out.Items = append(out.Items, it)
		out.Events++
	}

	// An obligation is scheduled before the earliest upcoming meeting it
	// shares a participant with. Unlinked obligations are recent ones only,
	// capped: the rest are a backlog, counted but not scheduled.
	unlinked := 0
	for _, o := range in.Obligations {
		it := Item{Kind: KindReply, At: in.Now.UTC(), Title: o.Subject, ConversationID: o.ConversationID,
			MessageID: o.MessageID, Detail: o.ActorName, Reasons: []Reason{ReasonWaitingSince}}
		if !o.LastAt.IsZero() {
			last := o.LastAt.UTC()
			it.LastAt = &last
		}
		if ev := earliestMeetingWith(events, o.Participants, in.Now); ev != nil {
			it.At = ev.Start.UTC()
			it.LinkedEventID = ev.EntryID
			it.LinkedEvent = ev.Subject
			it.Reasons = append(it.Reasons, ReasonMeetingWithThem)
		} else {
			if !o.LastAt.IsZero() && o.LastAt.Before(in.Now.Add(-unlinkedWindow)) {
				out.Backlog++
				continue
			}
			if unlinked >= maxUnlinked {
				out.Backlog++
				continue
			}
			unlinked++
		}
		it.Slack = it.At.Sub(in.Now).Seconds()
		out.Items = append(out.Items, it)
		out.Obligations++
	}

	for _, d := range in.Deadlines {
		if !d.Due.IsZero() && d.Due.Before(in.Now.Add(-overdueDeadline)) {
			continue
		}
		if !d.Due.IsZero() && d.Due.After(until) {
			continue
		}
		it := Item{Kind: KindDeadline, At: d.Due.UTC(), Title: d.Subject, ConversationID: d.ConversationID,
			MessageID: d.MessageID, Detail: d.SenderName, Reasons: []Reason{ReasonDue}}
		if it.At.IsZero() {
			it.At = in.Now.UTC()
		}
		it.Slack = it.At.Sub(in.Now).Seconds()
		out.Items = append(out.Items, it)
		out.Deadlines++
	}

	sortItems(out.Items)
	if len(out.Items) > MaxItems {
		out.Items = out.Items[:MaxItems]
		out.Truncated = true
	}
	for i := range out.Items {
		if out.Items[i].Kind == KindEvent && !out.Items[i].At.Before(in.Now) {
			it := out.Items[i]
			out.NextEvent = &it
			break
		}
	}
	return out
}

// sortItems orders by relevance bucket, then by time. Immediate obligations
// (At == now, no linked meeting) come first, then everything by At, then
// events before replies before deadlines at the same instant.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if kindOrder(a.Kind) != kindOrder(b.Kind) {
			return kindOrder(a.Kind) < kindOrder(b.Kind)
		}
		return a.Title < b.Title
	})
}

func kindOrder(k Kind) int {
	switch k {
	case KindReply:
		return 0
	case KindEvent:
		return 1
	default:
		return 2
	}
}

// dedupeEvents drops canceled duplicates and collapses occurrences that the
// same meeting produced in more than one calendar. The first occurrence in
// start order wins; it is the one the owner sees.
func dedupeEvents(events []calendar.Event, owner map[string]bool) []calendar.Event {
	out := make([]calendar.Event, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		key := e.CleanGlobalID
		if key == "" {
			key = e.EntryID
		}
		k := key + "|" + e.Start.UTC().Format(time.RFC3339Nano)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	calendar.Sort(out)
	return out
}

// earliestMeetingWith returns the first active event at or after now that
// shares a participant with the obligation.
func earliestMeetingWith(events []calendar.Event, participants []string, now time.Time) *calendar.Event {
	if len(participants) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, p := range participants {
		want[contacts.Normalize(p)] = true
	}
	for i := range events {
		e := &events[i]
		if !e.Active() || e.End.Before(now) {
			continue
		}
		for _, p := range e.ParticipantAddresses(nil) {
			if want[p] {
				return e
			}
		}
	}
	return nil
}
