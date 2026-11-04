package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

func wireObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return decodeMap(t, data)
}

// jsonAtPath reads one JSON path through objects and arrays. Array elements
// are numeric segments ("people.0.phone").
func jsonAtPath(value any, path string) (any, bool) {
	current := value
	for _, part := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func profileWireFixture() worldsvc.ProfileView {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	return worldsvc.ProfileView{
		GeneratedAt: "2026-11-03T09:00:00Z",
		Key:         "c:acme",
		Label:       "Acme",
		Declared:    true,
		Domains:     []string{"acme.test", "acme.example"},
		Addresses:   []string{"ada@acme.test", "support@acme.test"},
		People: []worldsvc.CompanyPerson{{
			Address: "ada@acme.test", Name: "Ada Lovelace", Company: "Acme",
			Department: "Engineering", JobTitle: "CTO", Phone: "+1 555 010 0100",
			Declared: true, Messages: 3, Sent: 1, Received: 2, LastAt: "2026-11-02T09:00:00Z",
		}},
		Messages: 5, Sent: 2, Received: 3, Threads: 2,
		FirstAt: "2026-08-01T09:00:00Z", LastAt: "2026-11-02T09:00:00Z",
		LastInboundAt: "2026-11-02T09:00:00Z", LastOutboundAt: "2026-11-01T09:00:00Z",
		Activity90: worldsvc.CompanyActivity{Messages: 5, Sent: 2, Received: 3, Threads: 2, ActiveDays: 4},
		Months:     []worldsvc.CompanyMonth{{Month: "2026-11", Messages: 5, Sent: 2, Received: 3}},
		Recent: []worldsvc.CompanyMessage{{
			MessageID: 42, ConversationID: "conv-acme", Subject: "Synthetic renewal",
			SenderName: "Ada Lovelace", SenderAddress: "ada@acme.test",
			Instant: "2026-11-02T09:00:00Z", Sent: true, HasAttachments: true,
		}},
		OpenThreads: []worldsvc.CompanyThread{{
			ConversationID: "conv-acme", Subject: "Synthetic renewal",
			LastAt: "2026-11-02T09:00:00Z", State: string(threadintel.AwaitingOwner),
			Reason: string(threadintel.ReasonInboundLast), Actor: "Ada Lovelace",
			MessageID: 42, Messages: 3, Topic: "billing", Subtopic: "invoice",
		}},
		ThreadStates: map[string]int{
			string(threadintel.AwaitingOwner): 2, string(threadintel.AwaitingOther): 1,
		},
		Events: []worldsvc.CalendarEvent{{
			SourceID: "src-1", StoreKey: "store-1", CalendarID: 9, EntryID: "ev-1",
			GlobalID: "global-1", CleanGlobalID: "clean-1", Subject: "Synthetic review",
			Location: "Room A", OrganizerName: "Ada Lovelace", OrganizerEmail: "ada@acme.test",
			Start: at, End: at.Add(time.Hour), AllDay: false, Busy: calendar.Busy,
			Meeting: calendar.Meeting, Response: calendar.Accepted, Recurring: true,
			SeriesID: "series-1", Occurrence: 2, OnlineURL: "https://meet.example.test/review",
			BodyPreview:  "A synthetic preview that must not cross the wire.",
			LastModified: "2026-11-01T08:00:00Z",
			Attendees: []calendar.Attendee{
				{Kind: calendar.Required, Name: "Ada Lovelace", Address: "ada@acme.test", ResponseStatus: 3},
				{Kind: calendar.Optional, Name: "Grace Hopper", Address: "grace@acme.test", ResponseStatus: 2},
				{Kind: calendar.Resource, Name: "Room A", Address: "room-a@acme.test", ResponseStatus: 1},
			},
		}},
		Deadlines: []worldsvc.Deadline{{
			MessageID: 42, ConversationID: "conv-acme", Subject: "Synthetic invoice",
			SenderName: "Ada Lovelace", Due: at.Add(48 * time.Hour),
			Participants: []string{"ada@acme.test"},
		}},
		PeopleContacted: 2, PeopleKnown: 4,
		AddressesTotal: 2, RecentTotal: 3, OpenThreadsTotal: 4, EventsTotal: 5, DeadlinesTotal: 6,
	}
}

// Every field the Companies screen reads survives the wire projection.
func TestCompanyProfileWireKeepsEveryReadField(t *testing.T) {
	body := wireObject(t, CompanyProfileViewOf(profileWireFixture()))
	kept := []string{
		"generated_at", "label", "declared", "domains.0",
		"people.0.address", "people.0.name", "people.0.department", "people.0.job_title",
		"people.0.phone", "people.0.declared", "people.0.messages", "people.0.sent",
		"people.0.received", "people.0.last_at",
		"messages", "sent", "received", "threads", "first_at", "last_at",
		"last_inbound_at", "last_outbound_at",
		"last_90_days.messages", "last_90_days.threads", "last_90_days.active_days",
		"months.0.month", "months.0.messages", "months.0.sent", "months.0.received",
		"recent.0.message_id", "recent.0.subject", "recent.0.sender_name",
		"recent.0.sender_address", "recent.0.instant", "recent.0.sent",
		"recent.0.has_attachments",
		"open_threads.0.subject", "open_threads.0.last_at", "open_threads.0.state",
		"open_threads.0.reason", "open_threads.0.actor", "open_threads.0.message_id",
		"open_threads.0.messages", "open_threads.0.topic", "open_threads.0.subtopic",
		"thread_states.awaiting_owner",
		"events.0.subject", "events.0.location", "events.0.organizer_name",
		"events.0.organizer_address", "events.0.start", "events.0.end", "events.0.all_day",
		"events.0.response_status", "events.0.is_recurring", "events.0.online_url",
		"events.0.attendee_count",
		"deadlines.0.message_id", "deadlines.0.subject", "deadlines.0.sender_name",
		"deadlines.0.due",
		"people_contacted", "people_known", "recent_total", "events_total", "deadlines_total",
	}
	for _, path := range kept {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("profile wire dropped %s", path)
		}
	}
	if got, ok := jsonAtPath(body, "events.0.attendee_count"); !ok || got != float64(3) {
		t.Fatalf("attendee_count = %v (present %v), want 3", got, ok)
	}
}

// Fields the world service keeps for Q&A and the thread resolver must not
// reach the profile response.
func TestCompanyProfileWireDropsServiceOnlyFields(t *testing.T) {
	body := wireObject(t, CompanyProfileViewOf(profileWireFixture()))
	dropped := []string{
		"key", "addresses", "addresses_total", "open_threads_total",
		"people.0.company",
		"last_90_days.sent", "last_90_days.received",
		"recent.0.conversation_id", "open_threads.0.conversation_id",
		"deadlines.0.conversation_id", "deadlines.0.participants",
		"events.0.source_id", "events.0.store_key", "events.0.calendar_id",
		"events.0.entry_id", "events.0.global_id", "events.0.clean_global_id",
		"events.0.busy_status", "events.0.meeting_status", "events.0.series_id",
		"events.0.occurrence_index", "events.0.body_preview", "events.0.last_modified",
		"events.0.attendees",
	}
	for _, path := range dropped {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("profile wire still carries %s", path)
		}
	}
}

// A zero service value still serializes every list as an array and the state
// map as an object.
func TestCompanyProfileWireZeroValueKeepsArrays(t *testing.T) {
	body := wireObject(t, CompanyProfileViewOf(worldsvc.ProfileView{}))
	for _, path := range []string{"domains", "people", "months", "recent", "open_threads",
		"events", "deadlines", "thread_states"} {
		value, ok := jsonAtPath(body, path)
		if !ok {
			t.Fatalf("zero profile dropped %s", path)
		}
		switch path {
		case "thread_states":
			if _, ok := value.(map[string]any); !ok {
				t.Fatalf("thread_states = %#v, want an object", value)
			}
		default:
			if _, ok := value.([]any); !ok {
				t.Fatalf("%s = %#v, want an array", path, value)
			}
		}
	}
}

func companiesWireFixture() worldsvc.ListResult {
	return worldsvc.ListResult{
		GeneratedAt: "2026-11-03T09:00:00Z",
		Companies: []worldsvc.CompanyRow{{
			Key: "c:acme", Label: "Acme", Declared: true,
			Domains: []string{"acme.test"}, People: 3, Messages: 5, Sent: 2,
			Received: 3, Threads: 4, LastAt: "2026-11-02T09:00:00Z",
		}},
		Coverage: worldsvc.CompanyCoverage{
			Contacts: 10, ContactsWithCompany: 8, Domains: 6, DeclaredOrgs: 2,
			OwnerDomains: 1, OwnerCompanies: 1,
		},
		Total: 7, HasMore: true,
	}
}

// The list row keeps the identity and volume the explorer renders; coverage
// keeps the three counts the coverage line states.
func TestCompaniesWireKeepsOnlyTheRenderedFields(t *testing.T) {
	body := wireObject(t, CompaniesViewOf(companiesWireFixture()))
	for _, path := range []string{"generated_at", "companies.0.key", "companies.0.label",
		"companies.0.domains.0", "companies.0.people", "companies.0.messages",
		"companies.0.sent", "companies.0.received", "companies.0.last_at",
		"coverage.contacts", "coverage.contacts_with_company", "coverage.declared_orgs",
		"total", "has_more"} {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("companies wire dropped %s", path)
		}
	}
	for _, path := range []string{"companies.0.declared", "companies.0.threads",
		"coverage.domains", "coverage.owner_domains", "coverage.owner_companies"} {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("companies wire still carries %s", path)
		}
	}
	empty := wireObject(t, CompaniesViewOf(worldsvc.ListResult{}))
	if got, ok := jsonAtPath(empty, "companies"); !ok {
		t.Fatal("zero companies view has no companies field")
	} else if _, ok := got.([]any); !ok {
		t.Fatalf("companies = %#v, want an array", got)
	}
}
