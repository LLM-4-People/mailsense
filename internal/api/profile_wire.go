package api

import (
	"maps"
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// CompaniesView is the companies explorer page as the wire carries it. The
// service ranks every matching organization and reports the book-wide
// coverage; this projection keeps the fields the explorer renders and drops
// the ranking internals.
type CompaniesView struct {
	GeneratedAt string              `json:"generated_at"`
	Companies   []CompanyRowWire    `json:"companies"`
	Coverage    CompanyCoverageWire `json:"coverage"`
	Total       int                 `json:"total"`
	HasMore     bool                `json:"has_more"`
}

// CompanyRowWire is one ranked organization. The list row renders the label,
// domains, volume, direction and recency; Declared and Threads stay in the
// service result because the ranker and Ask read them, not on the list wire.
type CompanyRowWire struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Domains  []string `json:"domains"`
	People   int      `json:"people"`
	Messages int      `json:"messages"`
	Sent     int      `json:"sent"`
	Received int      `json:"received"`
	LastAt   string   `json:"last_at"`
}

// CompanyCoverageWire is the address-book coverage line the list renders.
type CompanyCoverageWire struct {
	Contacts            int `json:"contacts"`
	ContactsWithCompany int `json:"contacts_with_company"`
	DeclaredOrgs        int `json:"declared_orgs"`
}

// CompaniesViewOf is the one projection of the company ranking to the wire.
func CompaniesViewOf(result worldsvc.ListResult) CompaniesView {
	out := CompaniesView{
		GeneratedAt: result.GeneratedAt,
		Companies:   make([]CompanyRowWire, len(result.Companies)),
		Coverage: CompanyCoverageWire{
			Contacts:            result.Coverage.Contacts,
			ContactsWithCompany: result.Coverage.ContactsWithCompany,
			DeclaredOrgs:        result.Coverage.DeclaredOrgs,
		},
		Total:   result.Total,
		HasMore: result.HasMore,
	}
	for i, row := range result.Companies {
		out.Companies[i] = CompanyRowWire{
			Key: row.Key, Label: row.Label, Domains: copyStrings(row.Domains),
			People: row.People, Messages: row.Messages, Sent: row.Sent,
			Received: row.Received, LastAt: row.LastAt,
		}
	}
	return out
}

// CompanyProfileWire is one organization's deep profile as the Companies
// screen consumes it. The wire keeps every field the screen reads and drops
// the service-only facts: the key (the route or prefetch map already names
// it), observed addresses, conversation ids, deadline participants and the
// calendar metadata used only to resolve the event. Q&A reads the full
// world.ProfileView directly; only the HTTP response is projected.
type CompanyProfileWire struct {
	GeneratedAt string              `json:"generated_at"`
	Label       string              `json:"label"`
	Declared    bool                `json:"declared"`
	Domains     []string            `json:"domains"`
	People      []CompanyPersonWire `json:"people"`

	Messages       int                 `json:"messages"`
	Sent           int                 `json:"sent"`
	Received       int                 `json:"received"`
	Threads        int                 `json:"threads"`
	FirstAt        string              `json:"first_at,omitempty"`
	LastAt         string              `json:"last_at,omitempty"`
	LastInboundAt  string              `json:"last_inbound_at,omitempty"`
	LastOutboundAt string              `json:"last_outbound_at,omitempty"`
	Activity90     CompanyActivityWire `json:"last_90_days"`

	Months       []CompanyMonthWire   `json:"months"`
	Recent       []CompanyMessageWire `json:"recent"`
	OpenThreads  []CompanyThreadWire  `json:"open_threads"`
	ThreadStates map[string]int       `json:"thread_states"`

	Events    []CompanyEventWire    `json:"events"`
	Deadlines []CompanyDeadlineWire `json:"deadlines"`

	// OpenLoops is the organization's open-loop rollup from the commitments
	// service, absent when the source is not wired or the read failed. The
	// items keep the commitments page's own row shape: the profile's loop
	// ledger is that same page narrowed to one organization.
	OpenLoops *worldsvc.OrganizationOpenLoops `json:"open_loops,omitempty"`

	PeopleContacted int `json:"people_contacted"`
	PeopleKnown     int `json:"people_known"`

	RecentTotal      int `json:"recent_total"`
	OpenThreadsTotal int `json:"open_threads_total"`
	EventsTotal      int `json:"events_total"`
	DeadlinesTotal   int `json:"deadlines_total"`
}

// CompanyActivityWire is the last-90-days block the profile renders.
type CompanyActivityWire struct {
	Messages   int `json:"messages"`
	Threads    int `json:"threads"`
	ActiveDays int `json:"active_days"`
}

// CompanyPersonWire is one organization contact. The directory's Company
// field is dropped: the profile already names the organization.
type CompanyPersonWire struct {
	Address    string `json:"address"`
	Name       string `json:"name,omitempty"`
	Department string `json:"department,omitempty"`
	JobTitle   string `json:"job_title,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Declared   bool   `json:"declared"`

	Messages int    `json:"messages"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
	LastAt   string `json:"last_at,omitempty"`
}

// CompanyMonthWire is one month of the profile volume chart.
type CompanyMonthWire struct {
	Month    string `json:"month"`
	Messages int    `json:"messages"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
}

// CompanyMessageWire is one recent owner-visible message, metadata only.
type CompanyMessageWire struct {
	MessageID      int64  `json:"message_id"`
	Subject        string `json:"subject"`
	SenderName     string `json:"sender_name"`
	SenderAddress  string `json:"sender_address"`
	Instant        string `json:"instant"`
	Sent           bool   `json:"sent"`
	HasAttachments bool   `json:"has_attachments"`
}

// CompanyThreadWire is one open conversation. The id stays service-side; the
// row links through its owner-visible MessageID.
type CompanyThreadWire struct {
	Subject   string `json:"subject"`
	LastAt    string `json:"last_at"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	Actor     string `json:"actor,omitempty"`
	MessageID int64  `json:"message_id,omitempty"`
	Messages  int    `json:"messages,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Subtopic  string `json:"subtopic,omitempty"`
}

// CompanyEventWire is one active occurrence linked to the organization. The
// calendar's resolution metadata stays service-side; the screen needs only
// the occurrence and how many people attend it.
type CompanyEventWire struct {
	Subject          string                  `json:"subject"`
	Location         string                  `json:"location"`
	OrganizerName    string                  `json:"organizer_name"`
	OrganizerAddress string                  `json:"organizer_address"`
	Start            time.Time               `json:"start"`
	End              time.Time               `json:"end"`
	AllDay           bool                    `json:"all_day"`
	ResponseStatus   calendar.ResponseStatus `json:"response_status"`
	Recurring        bool                    `json:"is_recurring"`
	OnlineURL        string                  `json:"online_url"`
	AttendeeCount    int                     `json:"attendee_count"`
}

// CompanyDeadlineWire is one due follow-up. The conversation id and the
// participant list are service-only: the row links through MessageID.
type CompanyDeadlineWire struct {
	MessageID  int64     `json:"message_id"`
	Subject    string    `json:"subject"`
	SenderName string    `json:"sender_name"`
	Due        time.Time `json:"due"`
}

// CompanyProfileViewOf is the one projection of a company profile to the
// wire. Every list is non-nil so the arrays stay arrays, including a zero
// service value.
func CompanyProfileViewOf(view worldsvc.ProfileView) CompanyProfileWire {
	out := CompanyProfileWire{
		GeneratedAt:    view.GeneratedAt,
		Label:          view.Label,
		Declared:       view.Declared,
		Domains:        copyStrings(view.Domains),
		People:         make([]CompanyPersonWire, len(view.People)),
		Messages:       view.Messages,
		Sent:           view.Sent,
		Received:       view.Received,
		Threads:        view.Threads,
		FirstAt:        view.FirstAt,
		LastAt:         view.LastAt,
		LastInboundAt:  view.LastInboundAt,
		LastOutboundAt: view.LastOutboundAt,
		Activity90: CompanyActivityWire{
			Messages:   view.Activity90.Messages,
			Threads:    view.Activity90.Threads,
			ActiveDays: view.Activity90.ActiveDays,
		},
		Months:           make([]CompanyMonthWire, len(view.Months)),
		Recent:           make([]CompanyMessageWire, len(view.Recent)),
		OpenThreads:      make([]CompanyThreadWire, len(view.OpenThreads)),
		ThreadStates:     make(map[string]int, len(view.ThreadStates)),
		Events:           make([]CompanyEventWire, len(view.Events)),
		Deadlines:        make([]CompanyDeadlineWire, len(view.Deadlines)),
		OpenLoops:        openLoopsWire(view.OpenLoops),
		PeopleContacted:  view.PeopleContacted,
		PeopleKnown:      view.PeopleKnown,
		RecentTotal:      view.RecentTotal,
		OpenThreadsTotal: view.OpenThreadsTotal,
		EventsTotal:      view.EventsTotal,
		DeadlinesTotal:   view.DeadlinesTotal,
	}
	for i, person := range view.People {
		out.People[i] = CompanyPersonWire{
			Address: person.Address, Name: person.Name, Department: person.Department,
			JobTitle: person.JobTitle, Phone: person.Phone, Declared: person.Declared,
			Messages: person.Messages, Sent: person.Sent, Received: person.Received,
			LastAt: person.LastAt,
		}
	}
	for state, count := range view.ThreadStates {
		out.ThreadStates[state] = count
	}
	for i, month := range view.Months {
		out.Months[i] = CompanyMonthWire{
			Month: month.Month, Messages: month.Messages, Sent: month.Sent, Received: month.Received,
		}
	}
	for i, message := range view.Recent {
		out.Recent[i] = CompanyMessageWire{
			MessageID: message.MessageID, Subject: message.Subject,
			SenderName: message.SenderName, SenderAddress: message.SenderAddress,
			Instant: message.Instant, Sent: message.Sent,
			HasAttachments: message.HasAttachments,
		}
	}
	for i, thread := range view.OpenThreads {
		out.OpenThreads[i] = CompanyThreadWire{
			Subject: thread.Subject, LastAt: thread.LastAt, State: thread.State,
			Reason: thread.Reason, Actor: thread.Actor, MessageID: thread.MessageID,
			Messages: thread.Messages, Topic: thread.Topic, Subtopic: thread.Subtopic,
		}
	}
	for i, event := range view.Events {
		out.Events[i] = CompanyEventWire{
			Subject: event.Subject, Location: event.Location,
			OrganizerName: event.OrganizerName, OrganizerAddress: event.OrganizerEmail,
			Start: event.Start, End: event.End, AllDay: event.AllDay,
			ResponseStatus: event.Response, Recurring: event.Recurring,
			OnlineURL: event.OnlineURL, AttendeeCount: len(event.Attendees),
		}
	}
	for i, deadline := range view.Deadlines {
		out.Deadlines[i] = CompanyDeadlineWire{
			MessageID: deadline.MessageID, Subject: deadline.Subject,
			SenderName: deadline.SenderName, Due: deadline.Due,
		}
	}
	return out
}

// openLoopsWire is the one projection of the commitments rollup to the
// profile wire: a copy whose item list, every item's evidence and the kind
// map are never null. The zero organization still marshals as
// {"items":[],"kinds":{}}. A nil rollup stays nil, so a profile without a
// commitments source omits the field entirely.
func openLoopsWire(in *worldsvc.OrganizationOpenLoops) *worldsvc.OrganizationOpenLoops {
	if in == nil {
		return nil
	}
	out := *in
	out.Kinds = nonNilMap(maps.Clone(in.Kinds))
	out.Items = nonNilSlice(slices.Clone(in.Items))
	for i := range out.Items {
		out.Items[i].Evidence = nonNilSlice(out.Items[i].Evidence)
	}
	return &out
}

// CompanyProfileViewsOf projects one prefetch batch. The key stays the map
// key; no profile repeats it.
func CompanyProfileViewsOf(views map[string]worldsvc.ProfileView) map[string]CompanyProfileWire {
	out := make(map[string]CompanyProfileWire, len(views))
	for key, view := range views {
		out[key] = CompanyProfileViewOf(view)
	}
	return out
}

// copyStrings returns a non-nil copy so a JSON array never becomes null and
// the caller cannot mutate a service-owned slice through the projection.
func copyStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
