// Package mailquery is the typed, local query layer behind Ask. A plan names
// what to count, rank, list or group over owner mail; the store executes it
// with parameterized SQL. A model may translate a question into a plan, but
// it never computes a count, a name or a date: those come from the mailbox.
package mailquery

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Kind is the entity a plan reads.
type Kind string

// Plan kinds.
const (
	KindContacts  Kind = "contacts"
	KindCompanies Kind = "companies"
	KindMessages  Kind = "messages"
	KindTime      Kind = "time"
	// KindAgenda is executed by the Q&A service over the world model
	// (calendar + thread truth + due commitments), not by the mail SQL
	// executor: its evidence spans stores the mail query never reads.
	KindAgenda Kind = "agenda"
)

// Action is what a plan does with the entity.
type Action string

// Plan actions.
const (
	ActionRank  Action = "rank"
	ActionCount Action = "count"
	ActionList  Action = "list"
)

// Direction selects the side of the conversation.
type Direction string

// Conversation directions.
const (
	DirectionBoth     Direction = "both"
	DirectionSent     Direction = "sent"
	DirectionReceived Direction = "received"
)

// Metric is how a contact ranking is measured.
type Metric string

// Contact metrics.
const (
	MetricMessages  Metric = "messages"
	MetricThreads   Metric = "threads"
	MetricExchanges Metric = "exchanges"
)

// GroupBy is the time bucket of a time plan.
type GroupBy string

// Time buckets.
const (
	GroupDay     GroupBy = "day"
	GroupWeek    GroupBy = "week"
	GroupMonth   GroupBy = "month"
	GroupWeekday GroupBy = "weekday"
)

// TimeRange is a relative window resolved in the owner's zone at execution.
// Keeping the relative name (not absolute dates) lets a cached plan stay
// correct when it is asked again later.
type TimeRange string

// Relative time ranges.
const (
	TimeAll        TimeRange = "all"
	TimeToday      TimeRange = "today"
	TimeYesterday  TimeRange = "yesterday"
	TimeLast7Days  TimeRange = "last_7_days"
	TimeLast30Days TimeRange = "last_30_days"
	TimeThisMonth  TimeRange = "this_month"
	TimeLastMonth  TimeRange = "last_month"
	TimeThisYear   TimeRange = "this_year"
	TimeLastYear   TimeRange = "last_year"
)

// Order is the ranking direction.
type Order string

// Ranking orders.
const (
	OrderDesc Order = "desc"
	OrderAsc  Order = "asc"
)

// Plan is one typed mail query. Every field is validated before execution;
// the executor uses values only, never plan text as SQL.
type Plan struct {
	Kind      Kind      `json:"kind"`
	Action    Action    `json:"action"`
	Direction Direction `json:"direction,omitempty"`
	Metric    Metric    `json:"metric,omitempty"`
	GroupBy   GroupBy   `json:"group_by,omitempty"`
	TimeRange TimeRange `json:"time_range,omitempty"`
	Since     string    `json:"since,omitempty"` // YYYY-MM-DD, explicit lower bound
	Until     string    `json:"until,omitempty"` // YYYY-MM-DD, exclusive upper bound
	Contact   string    `json:"contact,omitempty"`
	// ContactAddresses is an exact, normalized address scope resolved by the
	// Q&A entity resolver. When non-empty it replaces Contact: the executor
	// matches sender or To/Cc addresses exactly, never by substring.
	ContactAddresses []string `json:"contact_addresses,omitempty"`
	Text             string   `json:"text,omitempty"`
	Unread           *bool    `json:"unread,omitempty"`
	HasAttachments   *bool    `json:"has_attachments,omitempty"`
	Limit            int      `json:"limit,omitempty"`
	Order            Order    `json:"order,omitempty"`
	// HorizonHours is the agenda window; 0 uses the configured default.
	HorizonHours float64 `json:"horizon_hours,omitempty"`
}

// Window is the resolved UTC half-open range of a plan (Until is exclusive).
// Empty bounds mean unbounded.
type Window struct {
	Since string
	Until string
}

// Result is the executor's answer: rows for a ranking/list, or Total for a
// count. It carries no mail body, only the fields the screen renders.
type Result struct {
	Kind   Kind   `json:"kind"`
	Action Action `json:"action"`
	Metric Metric `json:"metric,omitempty"`
	Rows   []Row  `json:"rows"`
	Total  int    `json:"total"`
	// Matched is the number of matching rows before Limit for a group
	// ranking. Total counts the metric (messages), not the groups, so a
	// company page needs this separate population count.
	Matched int    `json:"matched,omitempty"`
	Since   string `json:"since,omitempty"`
	Until   string `json:"until,omitempty"`
	// Capped reports that a topic ranking was built from a bounded retrieval
	// pool: the rows are the best-matching contacts, not a claim that no
	// further match exists.
	Capped bool `json:"capped,omitempty"`
}

// Row is one ranked contact or company, one time bucket or one listed
// message. Contacts is the number of distinct people merged into a company
// row; it is zero for every other kind.
type Row struct {
	Key        string `json:"key,omitempty"`
	Label      string `json:"label,omitempty"`
	Address    string `json:"address,omitempty"`
	Messages   int    `json:"messages"`
	Sent       int    `json:"sent"`
	Received   int    `json:"received"`
	Threads    int    `json:"threads"`
	Contacts   int    `json:"contacts,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
	MessageID  int64  `json:"message_id,omitempty"`
	Subject    string `json:"subject,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Instant    string `json:"instant,omitempty"`
	SentFlag   bool   `json:"sent_flag,omitempty"`
}

// Clarification is the one question a plan may ask when a request is
// genuinely underspecified. Options are complete questions, so choosing one
// is an ordinary follow-up rather than a special mode. Candidates carries the
// same choices as structured rows when the answer resolves people or
// organizations: the screen shows a name, an address and a message count
// instead of repeating the original question. Candidate.Question is always one
// of Options, so a client that only understands Options keeps working.
type Clarification struct {
	Question   string             `json:"question"`
	Options    []string           `json:"options"`
	Candidates []ClarifyCandidate `json:"candidates,omitempty"`
}

// ClarifyCandidate is one resolved person or organization offered as a
// clarification. Kind is "person", "company" or "all". Address and Domain are
// the exact identifiers the follow-up question carries; Messages is the mail
// volume that ranks it. No internal key is ever exposed.
type ClarifyCandidate struct {
	Kind     string `json:"kind"`
	Label    string `json:"label,omitempty"`
	Address  string `json:"address,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Messages int    `json:"messages,omitempty"`
	LastAt   string `json:"last_at,omitempty"`
	Question string `json:"question"`
}

// Contact scope bounds. An exact address scope is a resolved person, never a
// free-text search: one mail may name the same person through several
// addresses, so the executor accepts a bounded list.
const (
	MaxContactAddresses = 20
	// MaxContactAddressChars is the mail owner's sender-query bound: one
	// address, counted in runes like every other address bound.
	MaxContactAddressChars = mail.MaxSenderQueryRunes
)

// Plan bounds and defaults. Normalize and Validate read these values and
// PromptSchema publishes them to the model, so the prompt can never promise
// a limit the validator refuses (the companies bound used to be 100 while the
// prompt said 50, and every such plan failed as a generation error).
const (
	MaxContactLimit     = 50
	MaxCompanyLimit     = 100
	MaxMessageListLimit = 50
	MaxAgendaLimit      = 60
	MaxTimeLimit        = 60
	// AgendaHorizonMax is the longest agenda horizon a plan may ask for. The
	// agenda service owns the bound; a plan cannot ask for a window the
	// agenda itself would refuse.
	AgendaHorizonMax = int(agenda.MaxHorizon / time.Hour)

	DefaultRankLimit    = 15 // contacts and companies
	DefaultMessageLimit = 10
	DefaultAgendaLimit  = 20
	DefaultTimeLimit    = 12
)

// PromptSchema is the model-facing statement of the plan bounds, generated
// from the validator's own constants. It is one line pair in the planner
// prompt, never a second copy of the numbers.
func PromptSchema() string {
	return fmt.Sprintf(
		"- limit: 1..%d for contacts, 1..%d for companies, 1..%d for message lists, 1..%d for agenda, 1..%d for time (defaults %d, %d, %d, %d, %d)\n"+
			"- horizon_hours: agenda only, 0..%d (0 uses the owner's default; \"next 3 hours\" means 3)",
		MaxContactLimit, MaxCompanyLimit, MaxMessageListLimit, MaxAgendaLimit, MaxTimeLimit,
		DefaultRankLimit, DefaultRankLimit, DefaultMessageLimit, DefaultAgendaLimit, DefaultTimeLimit,
		AgendaHorizonMax)
}

// Normalize applies the defaults and trims user-facing text. It does not
// validate; call Validate after Normalize.
func (p Plan) Normalize() Plan {
	p.Kind = Kind(strings.ToLower(strings.TrimSpace(string(p.Kind))))
	p.Action = Action(strings.ToLower(strings.TrimSpace(string(p.Action))))
	p.Direction = Direction(strings.ToLower(strings.TrimSpace(string(p.Direction))))
	p.Metric = Metric(strings.ToLower(strings.TrimSpace(string(p.Metric))))
	p.GroupBy = GroupBy(strings.ToLower(strings.TrimSpace(string(p.GroupBy))))
	p.TimeRange = TimeRange(strings.ToLower(strings.TrimSpace(string(p.TimeRange))))
	p.Order = Order(strings.ToLower(strings.TrimSpace(string(p.Order))))
	p.Since = strings.TrimSpace(p.Since)
	p.Until = strings.TrimSpace(p.Until)
	p.Contact = strings.TrimSpace(p.Contact)
	p.Text = strings.TrimSpace(p.Text)
	p.ContactAddresses = normalizeAddresses(p.ContactAddresses)

	if p.TimeRange == "" {
		p.TimeRange = TimeAll
	}
	if p.Order == "" {
		p.Order = OrderDesc
	}
	switch p.Kind {
	case KindContacts, KindCompanies:
		if p.Action == "" {
			p.Action = ActionRank
		}
		if p.Direction == "" {
			p.Direction = DirectionBoth
		}
		if p.Metric == "" {
			p.Metric = MetricMessages
		}
		if p.Limit == 0 {
			p.Limit = DefaultRankLimit
		}
	case KindMessages:
		if p.Action == "" {
			p.Action = ActionCount
		}
		if p.Direction == "" {
			p.Direction = DirectionBoth
		}
		if p.Limit == 0 {
			p.Limit = DefaultMessageLimit
		}
	case KindAgenda:
		if p.Action == "" {
			p.Action = ActionList
		}
		if p.Limit == 0 {
			p.Limit = DefaultAgendaLimit
		}
	case KindTime:
		if p.Action == "" {
			p.Action = ActionList
		}
		if p.Direction == "" {
			p.Direction = DirectionBoth
		}
		if p.GroupBy == "" {
			p.GroupBy = GroupMonth
		}
		if p.Limit == 0 {
			p.Limit = DefaultTimeLimit
		}
	}
	return p
}

// Validate checks every field and the cross-field invariants. It is the
// trust boundary between a model's plan and the executor.
func (p Plan) Validate() error {
	bad := planError
	switch p.Kind {
	case KindContacts:
		if err := p.validateRankedEntity("contact", "contacts", MaxContactLimit); err != nil {
			return err
		}
	case KindCompanies:
		if err := p.validateRankedEntity("company", "companies", MaxCompanyLimit); err != nil {
			return err
		}
	case KindMessages:
		if p.Action != ActionCount && p.Action != ActionList {
			return bad("messages need action count or list, got %q", p.Action)
		}
		if !p.validDirection() {
			return bad("invalid direction %q", p.Direction)
		}
		if p.Metric != "" || p.GroupBy != "" {
			return bad("messages cannot carry a contact metric or time group")
		}
		if p.Action == ActionList && (p.Limit < 1 || p.Limit > MaxMessageListLimit) {
			return bad("message list limit must be 1..%d, got %d", MaxMessageListLimit, p.Limit)
		}
		if p.Action == ActionCount && p.Limit != 0 && (p.Limit < 1 || p.Limit > MaxMessageListLimit) {
			return bad("message count limit must be 0..%d, got %d", MaxMessageListLimit, p.Limit)
		}
	case KindAgenda:
		if p.Action != ActionList {
			return bad("agenda needs action list, got %q", p.Action)
		}
		if p.Direction != "" || p.Metric != "" || p.GroupBy != "" || p.Contact != "" || p.Text != "" ||
			len(p.ContactAddresses) > 0 || p.Unread != nil || p.HasAttachments != nil {
			return bad("agenda only takes a horizon and a limit")
		}
		if p.HorizonHours < 0 || p.HorizonHours > float64(AgendaHorizonMax) {
			return bad("agenda horizon must be 0..%d hours, got %g", AgendaHorizonMax, p.HorizonHours)
		}
		if p.Limit < 1 || p.Limit > MaxAgendaLimit {
			return bad("agenda limit must be 1..%d, got %d", MaxAgendaLimit, p.Limit)
		}
	case KindTime:
		if p.Action != ActionList {
			return bad("time needs action list, got %q", p.Action)
		}
		if !p.validDirection() {
			return bad("invalid direction %q", p.Direction)
		}
		switch p.GroupBy {
		case GroupDay, GroupWeek, GroupMonth, GroupWeekday:
		default:
			return bad("invalid time group %q", p.GroupBy)
		}
		if p.Metric != "" {
			return bad("time cannot carry a contact metric")
		}
		if p.Limit < 1 || p.Limit > MaxTimeLimit {
			return bad("time limit must be 1..%d, got %d", MaxTimeLimit, p.Limit)
		}
	default:
		return bad("unknown kind %q", p.Kind)
	}
	if p.Order != OrderDesc && p.Order != OrderAsc {
		return bad("invalid order %q", p.Order)
	}
	if len(p.Contact) > 200 || len(p.Text) > 200 {
		return bad("contact and text filters must be at most 200 characters")
	}
	if mail.HasControl(p.Contact) || mail.HasControl(p.Text) {
		return bad("contact and text filters must not contain control characters")
	}
	if len(p.ContactAddresses) > MaxContactAddresses {
		return bad("contact_addresses must contain at most %d addresses", MaxContactAddresses)
	}
	for _, address := range p.ContactAddresses {
		if utf8.RuneCountInString(address) > MaxContactAddressChars {
			return bad("contact address is longer than %d characters", MaxContactAddressChars)
		}
		if !validPlanAddress(address) {
			return bad("invalid contact address %q", address)
		}
	}
	switch p.TimeRange {
	case TimeAll, TimeToday, TimeYesterday, TimeLast7Days, TimeLast30Days, TimeThisMonth, TimeLastMonth, TimeThisYear, TimeLastYear:
	default:
		return bad("invalid time range %q", p.TimeRange)
	}
	since, sinceFull, err := parseBound(p.Since)
	if err != nil {
		return err
	}
	until, untilFull, err := parseBound(p.Until)
	if err != nil {
		return err
	}
	if err := windowError(since, until, sinceFull, untilFull); err != nil {
		return err
	}
	return nil
}

// planError is the one trust-boundary error of a plan.
func planError(format string, args ...any) error {
	return fmt.Errorf("mailquery: plan: "+format, args...)
}

// validDirection reports whether Direction is one of the three values the
// executor understands. Contacts, companies, messages and time all read it.
func (p Plan) validDirection() bool {
	switch p.Direction {
	case DirectionBoth, DirectionSent, DirectionReceived:
		return true
	}
	return false
}

// validateRankedEntity is the shared rule of the two ranked kinds: rank action,
// a direction and metric the executor reads, no time grouping, and the kind's
// own limit. Only the noun and the bound differ.
func (p Plan) validateRankedEntity(singular, plural string, maxLimit int) error {
	if p.Action != ActionRank {
		return planError("%s need action rank, got %q", plural, p.Action)
	}
	if !p.validDirection() {
		return planError("invalid direction %q", p.Direction)
	}
	switch p.Metric {
	case MetricMessages, MetricThreads, MetricExchanges:
	default:
		return planError("invalid %s metric %q", singular, p.Metric)
	}
	if p.GroupBy != "" {
		return planError("%s cannot group by time", plural)
	}
	if p.Limit < 1 || p.Limit > maxLimit {
		return planError("%s limit must be 1..%d, got %d", singular, maxLimit, p.Limit)
	}
	return nil
}

// windowError is the one non-empty-window rule: a pair of bounds must not run
// backwards, and two full timestamps must not name the same instant (a
// date-only pair is inclusive, so equality there still covers a whole day).
func windowError(since, until time.Time, sinceFull, untilFull bool) error {
	if since.IsZero() || until.IsZero() {
		return nil
	}
	if since.After(until) {
		return planError("since must not be after until")
	}
	if since.Equal(until) && sinceFull && untilFull {
		return planError("since and until must describe a non-empty window")
	}
	return nil
}

// Resolve turns the relative time range into a UTC half-open window in loc.
// It returns the plan with Since/Until filled for the executor; the cache
// keeps the original relative plan. A nil loc means UTC, and a missing time
// zone is reported by the caller, never silently guessed.
func (p Plan) Resolve(now time.Time, loc *time.Location) (Plan, Window, error) {
	if loc == nil {
		loc = time.UTC
	}
	now = now.In(loc)
	var since, until time.Time
	var err error
	if p.Since != "" || p.Until != "" {
		var sinceFull, untilFull bool
		since, sinceFull, err = parseBound(p.Since)
		if err != nil {
			return Plan{}, Window{}, err
		}
		until, untilFull, err = parseBound(p.Until)
		if err != nil {
			return Plan{}, Window{}, err
		}
		// A date-only bound is inclusive in the owner's zone: "through
		// 26 October" includes that whole day, so the executor gets the next
		// local midnight. A full timestamp is already resolved and passes
		// through unchanged.
		if !since.IsZero() && !sinceFull {
			since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, loc)
		}
		if !until.IsZero() && !untilFull {
			until = time.Date(until.Year(), until.Month(), until.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
		if err := windowError(since, until, true, true); err != nil {
			return Plan{}, Window{}, err
		}
	} else {
		since, until = rangeBounds(p.TimeRange, now)
	}
	if !since.IsZero() {
		p.Since = since.UTC().Format(mail.TimeLayout)
	}
	if !until.IsZero() {
		p.Until = until.UTC().Format(mail.TimeLayout)
	}
	return p, Window{Since: p.Since, Until: p.Until}, nil
}

// rangeBounds returns the half-open UTC range of a relative range in loc.
// A zero value means unbounded on that side.
func rangeBounds(r TimeRange, now time.Time) (since, until time.Time) {
	loc := now.Location()
	startOfDay := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	today := startOfDay(now)
	switch r {
	case TimeToday:
		return today, today.AddDate(0, 0, 1)
	case TimeYesterday:
		return today.AddDate(0, 0, -1), today
	case TimeLast7Days:
		return today.AddDate(0, 0, -6), today.AddDate(0, 0, 1)
	case TimeLast30Days:
		return today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
	case TimeThisMonth:
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return first, first.AddDate(0, 1, 0)
	case TimeLastMonth:
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return first.AddDate(0, -1, 0), first
	case TimeThisYear:
		first := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc)
		return first, first.AddDate(1, 0, 0)
	case TimeLastYear:
		first := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc)
		return first.AddDate(-1, 0, 0), first
	case TimeAll:
		return time.Time{}, time.Time{}
	default:
		return time.Time{}, time.Time{}
	}
}

// parseBound accepts an unresolved date (YYYY-MM-DD) or a resolved UTC
// timestamp (mail.TimeLayout). The bool reports which form was read.
func parseBound(s string) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	if strings.Contains(s, "T") {
		t, err := time.Parse(mail.TimeLayout, s)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("mailquery: plan: invalid timestamp %q", s)
		}
		return t, true, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("mailquery: plan: invalid date %q (want YYYY-MM-DD)", s)
	}
	return t, false, nil
}

// normalizeAddresses trims, lowercases and de-duplicates an exact address
// scope. Empty entries are preserved for Validate to reject; they are never
// silently dropped at the trust boundary.
func normalizeAddresses(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, address := range in {
		address = strings.ToLower(strings.TrimSpace(address))
		if seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out
}

// validPlanAddress reports one normalized address the executor may compare
// exactly: one @, non-empty local part and domain, no whitespace or control
// characters. It never rewrites the value.
func validPlanAddress(address string) bool {
	if address == "" || address != strings.TrimSpace(address) || mail.HasControl(address) {
		return false
	}
	if strings.ContainsFunc(address, unicode.IsSpace) {
		return false
	}
	local, domain, ok := strings.Cut(address, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@")
}
