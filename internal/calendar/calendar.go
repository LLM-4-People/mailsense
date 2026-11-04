// Package calendar holds the calendar-event entity the world model links to
// mail obligations. A source expands recurrences inside a bounded window
// and reports one Event per occurrence; this package only validates,
// normalizes and answers window questions. It never reads a clock of its
// own: callers pass the window.
package calendar

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// BusyStatus is the owner's availability during an event.
type BusyStatus int

// Busy statuses (Outlook olBusyStatus).
const (
	Free BusyStatus = iota
	Tentative
	Busy
	OutOfOffice
)

// ResponseStatus is the owner's reply to a meeting request.
type ResponseStatus int

// Response statuses (Outlook olResponseStatus).
const (
	NoResponse ResponseStatus = iota
	Organized
	TentativeResponse
	Accepted
	Declined
	NotResponded
)

// MeetingStatus is the meeting lifecycle of an appointment.
type MeetingStatus int

// Meeting statuses (Outlook olMeetingStatus).
const (
	NonMeeting MeetingStatus = iota
	Meeting
	MeetingReceived
	MeetingCanceled
	MeetingForwarded
)

// AttendeeKind is the attendee's role.
type AttendeeKind string

// Attendee roles.
const (
	Required AttendeeKind = "required"
	Optional AttendeeKind = "optional"
	Resource AttendeeKind = "resource"
)

// Attendee is one participant of an event.
type Attendee struct {
	Kind           AttendeeKind `json:"kind"`
	Name           string       `json:"name"`
	Address        string       `json:"address"`
	ResponseStatus int          `json:"response_status"`
}

// Normalized returns the attendee's address in matching form.
func (a Attendee) Normalized() string { return contacts.Normalize(a.Address) }

// Event is one calendar occurrence. Start and End are UTC instants; End is
// exclusive. An all-day event starts at the owner's local midnight and ends
// at the next local midnight, so its UTC bounds already encode the day.
type Event struct {
	SourceID       string         `json:"source_id"`
	StoreKey       string         `json:"store_key"`
	CalendarID     int64          `json:"calendar_id,omitempty"`
	EntryID        string         `json:"entry_id"`
	GlobalID       string         `json:"global_id"`
	CleanGlobalID  string         `json:"clean_global_id"`
	Subject        string         `json:"subject"`
	Location       string         `json:"location"`
	OrganizerName  string         `json:"organizer_name"`
	OrganizerEmail string         `json:"organizer_address"`
	Start          time.Time      `json:"start"`
	End            time.Time      `json:"end"`
	AllDay         bool           `json:"all_day"`
	Busy           BusyStatus     `json:"busy_status"`
	Meeting        MeetingStatus  `json:"meeting_status"`
	Response       ResponseStatus `json:"response_status"`
	Recurring      bool           `json:"is_recurring"`
	SeriesID       string         `json:"series_id"`
	Occurrence     int            `json:"occurrence_index"`
	OnlineURL      string         `json:"online_url"`
	BodyPreview    string         `json:"body_preview"`
	LastModified   string         `json:"last_modified"`
	Attendees      []Attendee     `json:"attendees"`
}

// Validate checks the invariants the store relies on: a bounded, non-empty
// window, a clean identity and attendee addresses that never carry control
// characters. A source must fix its data, not the store.
func (e Event) Validate() error {
	if strings.TrimSpace(e.EntryID) == "" {
		return errors.New("calendar: event entry id is required")
	}
	if strings.TrimSpace(e.SourceID) == "" || strings.TrimSpace(e.StoreKey) == "" {
		return errors.New("calendar: event source and store key are required")
	}
	if e.Start.IsZero() || e.End.IsZero() {
		return errors.New("calendar: event start and end are required")
	}
	if !e.Start.Before(e.End) {
		return fmt.Errorf("calendar: event %q ends before it starts", e.EntryID)
	}
	if e.Busy < Free || e.Busy > OutOfOffice {
		return fmt.Errorf("calendar: event %q has invalid busy status %d", e.EntryID, e.Busy)
	}
	for _, a := range e.Attendees {
		if mail.HasControl(a.Address) || mail.HasControl(a.Name) {
			return fmt.Errorf("calendar: event %q has a control character in an attendee", e.EntryID)
		}
		switch a.Kind {
		case Required, Optional, Resource:
		default:
			return fmt.Errorf("calendar: event %q has invalid attendee kind %q", e.EntryID, a.Kind)
		}
	}
	return nil
}

// Active reports whether the occurrence still occupies the owner's time: it
// is not canceled, not declined, and not marked free.
func (e Event) Active() bool {
	if e.Meeting == MeetingCanceled || e.Response == Declined || e.Busy == Free {
		return false
	}
	return true
}

// Overlaps reports whether the event intersects the half-open window.
func (e Event) Overlaps(since, until time.Time) bool {
	if since.IsZero() && until.IsZero() {
		return true
	}
	if !until.IsZero() && !e.Start.Before(until) {
		return false
	}
	if !since.IsZero() && !e.End.After(since) {
		return false
	}
	return true
}

// ParticipantAddresses returns the event's non-owner participants in
// normalized form, organizer first, de-duplicated. owner is the set of the
// owner's own addresses, so a meeting never links to the owner.
func (e Event) ParticipantAddresses(owner map[string]bool) []string {
	out := make([]string, 0, len(e.Attendees)+1)
	seen := map[string]bool{}
	add := func(a string) {
		n := contacts.Normalize(a)
		if n == "" || seen[n] || owner[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	add(e.OrganizerEmail)
	for _, a := range e.Attendees {
		add(a.Address)
	}
	return out
}

// Sort orders events by start, then end, then entry id.
func Sort(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Start.Equal(events[j].Start) {
			return events[i].Start.Before(events[j].Start)
		}
		if !events[i].End.Equal(events[j].End) {
			return events[i].End.Before(events[j].End)
		}
		return events[i].EntryID < events[j].EntryID
	})
}

// AllDayStart returns the event's start as a local date in loc.
func (e Event) AllDayStart(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t := e.Start.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
