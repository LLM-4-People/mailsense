package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

func TestCompaniesListAndProfileShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.companies.list = worldsvc.ListResult{
		GeneratedAt: "2026-10-26T12:00:00Z",
		Companies: []worldsvc.CompanyRow{{
			Key: "c:atelier nord", Label: "Atelier Nord", Declared: true,
			Domains: []string{"atelier-nord.test"}, People: 3, Messages: 5, Sent: 2, Received: 3,
			Threads: 5, LastAt: "2026-10-20T09:00:00Z",
		}},
		Coverage: store.CompanyCoverage{Contacts: 9, ContactsWithCompany: 7, Domains: 4,
			DeclaredOrgs: 3, OwnerDomains: 1, OwnerCompanies: 1},
		Total: 7, HasMore: true,
	}
	w.companies.profile = worldsvc.ProfileView{
		GeneratedAt: "2026-10-26T12:00:00Z",
		Key:         "c:atelier nord", Label: "Atelier Nord", Declared: true,
		Domains:   []string{"atelier-nord.example", "atelier-nord.test"},
		Addresses: []string{"alice@atelier-nord.test"},
		People:    []worldsvc.CompanyPerson{{Address: "alice@atelier-nord.test", Name: "Alice Martin", Phone: "+1 555 010 0100", Declared: true, Messages: 2, Sent: 1, Received: 1}},
		Messages:  5, Sent: 2, Received: 3, Threads: 5,
		FirstAt: "2026-09-10T09:00:00Z", LastAt: "2026-10-20T09:00:00Z",
		LastInboundAt: "2026-10-20T09:00:00Z", LastOutboundAt: "2026-10-18T09:00:00Z",
		Activity90: worldsvc.CompanyActivity{Messages: 3, Sent: 1, Received: 2, Threads: 2, ActiveDays: 2},
		Months:     []worldsvc.CompanyMonth{{Month: "2026-10", Messages: 4, Sent: 2, Received: 2}},
		Recent: []worldsvc.CompanyMessage{{MessageID: 5, ConversationID: "C-art-e", Subject: "Atelier Nord renewal",
			SenderName: "Bob Smith", SenderAddress: "bob@atelier-nord.test", Instant: "2026-10-20T09:00:00Z"}},
		OpenThreads: []worldsvc.CompanyThread{{ConversationID: "C-art-b", Subject: "Atelier Nord follow-up",
			LastAt: "2026-10-21T09:00:00Z", State: string(threadintel.AwaitingOwner),
			Reason: string(threadintel.ReasonInboundLast), Actor: "Alice Martin", MessageID: 2}},
		ThreadStates: map[string]int{
			string(threadintel.AwaitingOwner): 1, string(threadintel.AwaitingOther): 1,
			string(threadintel.NoAction): 3,
		},
		Events: []worldsvc.CalendarEvent{{SourceID: "src", EntryID: "ev-art", Subject: "Atelier Nord kickoff",
			Start: testTime(t, "2026-11-03T10:00:00Z"),
			Attendees: []calendar.Attendee{
				{Name: "Alice Martin", Address: "alice@atelier-nord.test"},
				{Name: "Bob Smith", Address: "bob@atelier-nord.test"},
				{Name: "Cara Diaz", Address: "cara@atelier-nord.test"},
			}}},
		Deadlines: []worldsvc.Deadline{{MessageID: 3, ConversationID: "C-art-d", Subject: "Atelier Nord invoice",
			Due: testTime(t, "2026-11-10T09:00:00Z"), Participants: []string{"alice@atelier-nord.test"}}},
		PeopleContacted: 3, PeopleKnown: 4,
		AddressesTotal: 1, RecentTotal: 1, OpenThreadsTotal: 1, EventsTotal: 1, DeadlinesTotal: 1,
	}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies"})
	mustStatus(t, status, 200, data)
	if w.companies.lastLimit != worldsvc.DefaultCompanyLimit {
		t.Fatalf("default limit %d", w.companies.lastLimit)
	}
	var list struct {
		GeneratedAt string `json:"generated_at"`
		Companies   []struct {
			Key      string   `json:"key"`
			Label    string   `json:"label"`
			Domains  []string `json:"domains"`
			People   int      `json:"people"`
			Messages int      `json:"messages"`
			Sent     int      `json:"sent"`
			Received int      `json:"received"`
			LastAt   string   `json:"last_at"`
		} `json:"companies"`
		Coverage CompanyCoverageWire `json:"coverage"`
		Total    int                 `json:"total"`
		HasMore  bool                `json:"has_more"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if list.GeneratedAt != "2026-10-26T12:00:00Z" || len(list.Companies) != 1 {
		t.Fatalf("list %+v", list)
	}
	row := list.Companies[0]
	if row.Key != "c:atelier nord" || row.People != 3 || row.Messages != 5 || row.Sent != 2 ||
		row.Received != 3 || row.LastAt != "2026-10-20T09:00:00Z" ||
		len(row.Domains) != 1 || row.Domains[0] != "atelier-nord.test" {
		t.Fatalf("row %+v", row)
	}
	if list.Coverage.Contacts != 9 || list.Coverage.DeclaredOrgs != 3 {
		t.Fatalf("coverage %+v", list.Coverage)
	}
	if list.Total != 7 || !list.HasMore {
		t.Fatalf("population total=%d has_more=%v", list.Total, list.HasMore)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies?limit=100"})
	mustStatus(t, status, 200, data)
	if w.companies.lastLimit != 100 {
		t.Fatalf("explicit limit %d", w.companies.lastLimit)
	}

	// The router decodes the path segment before the handler sees it.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies/c%3Aatelier%20nord"})
	mustStatus(t, status, 200, data)
	if w.companies.lastKey != "c:atelier nord" || w.companies.lastMonths != 12 || w.companies.lastEventDays != 30 {
		t.Fatalf("profile query %q months=%d event_days=%d", w.companies.lastKey, w.companies.lastMonths, w.companies.lastEventDays)
	}
	var profile CompanyProfileWire
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Label != "Atelier Nord" || !profile.Declared ||
		profile.Messages != 5 || profile.PeopleContacted != 3 || profile.PeopleKnown != 4 ||
		profile.GeneratedAt != "2026-10-26T12:00:00Z" ||
		profile.LastInboundAt != "2026-10-20T09:00:00Z" || profile.LastOutboundAt != "2026-10-18T09:00:00Z" ||
		profile.Activity90.Messages != 3 || profile.Activity90.ActiveDays != 2 ||
		profile.RecentTotal != 1 || profile.OpenThreadsTotal != 1 ||
		profile.EventsTotal != 1 || profile.DeadlinesTotal != 1 {
		t.Fatalf("profile %+v", profile)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"coverage", "key", "addresses", "addresses_total"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("the profile still carries the service-only field %q", key)
		}
	}
	if _, ok := raw["open_threads_total"]; !ok {
		t.Fatal("the profile dropped open_threads_total")
	}
	if len(profile.Months) != 1 || profile.Months[0].Month != "2026-10" ||
		len(profile.People) != 1 || profile.People[0].Phone != "+1 555 010 0100" ||
		len(profile.Recent) != 1 || len(profile.OpenThreads) != 1 ||
		len(profile.Events) != 1 || len(profile.Deadlines) != 1 {
		t.Fatalf("profile lists %+v", profile)
	}
	if profile.Events[0].AttendeeCount != 3 {
		t.Fatalf("event attendee_count = %d, want 3", profile.Events[0].AttendeeCount)
	}
	if profile.OpenThreads[0].State != string(threadintel.AwaitingOwner) ||
		profile.ThreadStates[string(threadintel.AwaitingOwner)] != 1 {
		t.Fatalf("thread view %+v / %+v", profile.OpenThreads, profile.ThreadStates)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies/d%3Adelta.co?months=2&event_days=7"})
	mustStatus(t, status, 200, data)
	if w.companies.lastKey != "d:delta.co" || w.companies.lastMonths != 2 || w.companies.lastEventDays != 7 {
		t.Fatalf("query %q months=%d event_days=%d", w.companies.lastKey, w.companies.lastMonths, w.companies.lastEventDays)
	}

	// A raw colon is a valid path segment too; the handler must see the same
	// decoded key either way.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies/c:atelier nord"})
	mustStatus(t, status, 200, data)
	if w.companies.lastKey != "c:atelier nord" {
		t.Fatalf("raw key %q", w.companies.lastKey)
	}
}

func TestCompanyProfilesBatchReadsVisibleRowsOnce(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.companies.profile = worldsvc.ProfileView{
		Key: "c:acme", Label: "Acme", Declared: true,
		Domains: []string{"acme.test"}, People: []worldsvc.CompanyPerson{},
		Months: []worldsvc.CompanyMonth{}, Recent: []worldsvc.CompanyMessage{},
		OpenThreads: []worldsvc.CompanyThread{}, ThreadStates: map[string]int{},
		Events: []worldsvc.CalendarEvent{}, Deadlines: []worldsvc.Deadline{},
	}
	status, _, data := do(t, ts, srv, request{
		method: "GET",
		path:   "/api/companies/profiles?key=c%3Aacme&key=c%3Abeta&key=c%3Aacme&months=24&event_days=7",
	})
	mustStatus(t, status, 200, data)
	if len(w.companies.lastKeys) != 2 || w.companies.lastKeys[0] != "c:acme" || w.companies.lastKeys[1] != "c:beta" {
		t.Fatalf("batch keys %+v", w.companies.lastKeys)
	}
	if w.companies.lastMonths != 24 || w.companies.lastEventDays != 7 {
		t.Fatalf("batch months=%d event_days=%d", w.companies.lastMonths, w.companies.lastEventDays)
	}
	var body struct {
		GeneratedAt string                        `json:"generated_at"`
		Profiles    map[string]CompanyProfileWire `json:"profiles"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body.GeneratedAt == "" || len(body.Profiles) != 2 {
		t.Fatalf("batch body %+v", body)
	}
	if body.Profiles["c:acme"].Label != "Acme" {
		t.Fatalf("batch profiles %+v", body.Profiles)
	}
	if body.Profiles["c:acme"].Domains == nil || body.Profiles["c:acme"].People == nil ||
		body.Profiles["c:acme"].Months == nil || body.Profiles["c:acme"].Recent == nil ||
		body.Profiles["c:acme"].OpenThreads == nil || body.Profiles["c:acme"].Events == nil ||
		body.Profiles["c:acme"].Deadlines == nil {
		t.Fatalf("batch must normalize every list: %+v", body.Profiles["c:acme"])
	}

	status, _, _ = do(t, ts, srv, request{method: "GET", path: "/api/companies/profiles?key=c%3Aacme&key=bad"})
	mustStatus(t, status, 400, nil)
	status, _, _ = do(t, ts, srv, request{method: "GET", path: "/api/companies/profiles?key="})
	mustStatus(t, status, 400, nil)
	// The accepted maximum is the world profile cache's batch bound, not a
	// second literal: exactly that many distinct keys succeed, one more is
	// refused.
	atCap := make([]string, 0, worldsvc.MaxProfileBatch)
	for i := 0; i < worldsvc.MaxProfileBatch; i++ {
		atCap = append(atCap, "key=c:co"+strconv.Itoa(i))
	}
	status, _, data = do(t, ts, srv, request{method: "GET",
		path: "/api/companies/profiles?" + strings.Join(atCap, "&")})
	mustStatus(t, status, 200, data)
	tooMany := make([]string, 0, worldsvc.MaxProfileBatch+1)
	for i := 0; i < worldsvc.MaxProfileBatch+1; i++ {
		tooMany = append(tooMany, "key=c:co"+strconv.Itoa(i))
	}
	status, _, _ = do(t, ts, srv, request{method: "GET",
		path: "/api/companies/profiles?" + strings.Join(tooMany, "&")})
	mustStatus(t, status, 400, nil)
}

func TestCompaniesListUsesTheWireProjection(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.companies.list = worldsvc.ListResult{
		GeneratedAt: "2026-10-26T12:00:00Z",
		Companies: []worldsvc.CompanyRow{{
			Key: "c:atelier nord", Label: "Atelier Nord", Declared: true,
			Domains: []string{"atelier-nord.test"}, People: 3, Messages: 5, Sent: 2,
			Received: 3, Threads: 4, LastAt: "2026-10-20T09:00:00Z",
		}},
		Coverage: store.CompanyCoverage{Contacts: 9, ContactsWithCompany: 7, Domains: 4,
			DeclaredOrgs: 3, OwnerDomains: 1, OwnerCompanies: 1},
		Total: 7, HasMore: true,
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	for _, path := range []string{"companies.0.key", "companies.0.label", "companies.0.domains.0",
		"companies.0.people", "companies.0.messages", "companies.0.sent",
		"companies.0.received", "companies.0.last_at", "coverage.contacts",
		"coverage.contacts_with_company", "coverage.declared_orgs"} {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("list handler dropped %s", path)
		}
	}
	for _, path := range []string{"companies.0.declared", "companies.0.threads",
		"coverage.domains", "coverage.owner_domains", "coverage.owner_companies"} {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("list handler still carries %s", path)
		}
	}
}

func TestCompanyProfileHandlersUseTheWireProjection(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.companies.profile = profileWireFixture()
	check := func(t *testing.T, body map[string]any) {
		t.Helper()
		for _, path := range []string{"label", "people.0.phone", "recent.0.message_id",
			"open_threads.0.state", "open_threads_total", "open_loops.total",
			"open_loops.kinds.due", "open_loops.items.0.evidence.0",
			"events.0.attendee_count", "deadlines.0.due"} {
			if _, ok := jsonAtPath(body, path); !ok {
				t.Errorf("profile handler dropped %s", path)
			}
		}
		for _, path := range []string{"key", "addresses", "addresses_total",
			"people.0.company", "recent.0.conversation_id", "open_threads.0.conversation_id",
			"deadlines.0.participants", "events.0.attendees", "events.0.body_preview"} {
			if _, ok := jsonAtPath(body, path); ok {
				t.Errorf("profile handler still carries %s", path)
			}
		}
	}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies/c%3Aacme"})
	mustStatus(t, status, 200, data)
	check(t, decodeMap(t, data))

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies/profiles?key=c%3Aacme"})
	mustStatus(t, status, 200, data)
	batch := decodeMap(t, data)
	profiles, ok := batch["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("batch profiles = %#v", batch["profiles"])
	}
	profile, ok := profiles["c:acme"].(map[string]any)
	if !ok {
		t.Fatalf("batch profile = %#v", profiles["c:acme"])
	}
	check(t, profile)
}

func TestCompaniesZeroValueListsAreArrays(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.companies.list = worldsvc.ListResult{}
	w.companies.profile = worldsvc.ProfileView{Key: "c:acme"}
	for _, path := range []string{"/api/companies", "/api/companies/c%3Aacme"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, 200, data)
		body := decodeMap(t, data)
		for _, key := range []string{"companies", "domains", "addresses", "people", "months", "recent", "open_threads", "events", "deadlines"} {
			value, ok := body[key]
			if !ok {
				continue
			}
			if _, ok := value.([]any); !ok {
				t.Fatalf("%s: %s = %#v, want an array", path, key, value)
			}
		}
		if path == "/api/companies" {
			if _, ok := body["coverage"].(map[string]any); !ok {
				t.Fatalf("coverage = %#v", body["coverage"])
			}
		} else if _, ok := body["thread_states"].(map[string]any); !ok {
			t.Fatalf("thread_states = %#v", body["thread_states"])
		}
	}
}

func TestCompaniesAuthGateAndNotAllowed(t *testing.T) {
	srv, ts, w := newTestServer(t)
	for _, in := range []request{
		noAuth(request{method: "GET", path: "/api/companies"}),
		noAuth(request{method: "GET", path: "/api/companies/c%3Aatelier%20nord"}),
	} {
		status, _, data := do(t, ts, srv, in)
		mustStatus(t, status, 401, data)
	}
	if w.companies.listCalls != 0 || w.companies.profileCalls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}

	w.learning.allowErr = lockedGate()
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies"})
	mustStatus(t, status, 423, data)
	if code := errorCode(t, data); code != "learning_locked" {
		t.Fatalf("code %q", code)
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies/c%3Aatelier%20nord"})
	mustStatus(t, status, 423, data)
	if w.companies.listCalls != 0 || w.companies.profileCalls != 0 {
		t.Fatal("a locked gate reached the service")
	}

	w.learning.allowErr = nil
	for _, in := range []request{
		{method: "POST", path: "/api/companies"},
		{method: "POST", path: "/api/companies/c%3Aatelier%20nord"},
		{method: "POST", path: "/api/companies/"},
	} {
		status, header, data := do(t, ts, srv, in)
		mustStatus(t, status, 405, data)
		if header.Get("Allow") != "GET" {
			t.Fatalf("Allow %q", header.Get("Allow"))
		}
		if code := errorCode(t, data); code != "method_not_allowed" {
			t.Fatalf("code %q", code)
		}
	}
}

func TestCompaniesValidationAndUnknownKey(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{"empty key", "/api/companies/", 400, "invalid_company"},
		{"missing prefix", "/api/companies/atelier-nord", 400, "invalid_company"},
		{"unknown prefix", "/api/companies/x%3Aatelier%20nord", 400, "invalid_company"},
		{"empty body", "/api/companies/c%3A", 400, "invalid_company"},
		{"control character", "/api/companies/c%3A%01x", 400, "invalid_company"},
		{"limit zero", "/api/companies?limit=0", 400, "invalid_limit"},
		{"limit too large", "/api/companies?limit=101", 400, "invalid_limit"},
		{"limit not a number", "/api/companies?limit=nope", 400, "invalid_limit"},
		{"months zero", "/api/companies/c%3Aatelier%20nord?months=0", 400, "invalid_months"},
		{"months too large", "/api/companies/c%3Aatelier%20nord?months=61", 400, "invalid_months"},
		{"event days zero", "/api/companies/c%3Aatelier%20nord?event_days=0", 400, "invalid_event_days"},
		{"event days too large", "/api/companies/c%3Aatelier%20nord?event_days=366", 400, "invalid_event_days"},
		{"event days not a number", "/api/companies/c%3Aatelier%20nord?event_days=nope", 400, "invalid_event_days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
	if w.companies.listCalls != 0 || w.companies.profileCalls != 0 {
		t.Fatalf("invalid requests reached the service: list %d profile %d",
			w.companies.listCalls, w.companies.profileCalls)
	}

	w.companies.profileErr = worldsvc.ErrUnknownCompany
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies/c%3Aglobex"})
	mustStatus(t, status, 404, data)
	if code := errorCode(t, data); code != "unknown_company" {
		t.Fatalf("code %q", code)
	}
	if w.companies.lastKey != "c:globex" {
		t.Fatalf("key %q", w.companies.lastKey)
	}

	// mapError owns the unknown-company answer for every company route, so
	// the list cannot answer an internal error for the sentinel the profile
	// routes map to 404.
	w.companies.listErr = worldsvc.ErrUnknownCompany
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/companies"})
	mustStatus(t, status, 404, data)
	if code := errorCode(t, data); code != "unknown_company" {
		t.Fatalf("list error code %q", code)
	}
}

// testTime parses the store's canonical instant layout without importing the
// mail package into the API test vocabulary.
func testTime(t *testing.T, value string) (out time.Time) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCompaniesListSearchAndPopulation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/companies?limit=2&q=delta"})
	mustStatus(t, status, 200, data)
	if w.companies.lastQuery != (worldsvc.ListQuery{Limit: 2, Search: "delta"}) {
		t.Fatalf("query %+v", w.companies.lastQuery)
	}
	// A search longer than the one owner's bound is rejected as a query, not
	// silently truncated.
	status, _, data = do(t, ts, srv, request{method: "GET",
		path: "/api/companies?q=" + strings.Repeat("x", worldsvc.MaxCompanySearchLen+1)})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_query" {
		t.Fatalf("code %q", code)
	}
	if w.companies.listCalls != 1 {
		t.Fatalf("an invalid search reached the service: %d", w.companies.listCalls)
	}
}
