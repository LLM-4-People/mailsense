package world

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// The cadence trend compares two equal windows of complete local calendar
// months taken from the profile's own zero-filled month buckets. The default
// window is trendWindowMonths complete months per side, so a profile with at
// least ten complete months compares its newest five against the five before
// them. This is a calendar-month comparison, not
// a trailing-day one: it is not "the last 90 days against the previous 90".
// The anchor month, the bucket holding the profile's build instant, is
// excluded because it is partial: a month counted up to the build instant
// against a full month would make every recent window read low by the day of
// the month. When the profile carries less history, both windows shrink to
// the same length (half the complete buckets, capped at trendWindowMonths):
// exactly two complete months form one-month windows, fewer than two form
// none, and older complete months beyond the compared span are not compared.
// The evidence gates below then decide whether that span is deep enough for a
// status of "ok".
const (
	// trendWindowMonths is the full comparison window size: the newest
	// complete calendar months per side, against the complete months before
	// them.
	trendWindowMonths = 5
	// trendMinActiveMonths is the evidence floor of a comparison: at least
	// this many months of the compared span must carry mail, so a brand-new
	// correspondent's pre-history zeros are never read as silence.
	trendMinActiveMonths = 6
	// trendMinBaselineMessages is the second evidence floor: the earlier
	// window must carry real volume, not a single burst or a one-off reply.
	trendMinBaselineMessages = 12
)

// TrendStatus is the trend's evidence state, never a judgement about the
// organization. "ok" means the compared windows passed every evidence gate;
// "insufficient_history" means at least one gate failed, or no equal window
// could be formed, and the reported counts say what exists. No consumer may
// turn this field alone into a verdict (cooling, ignoring): the counts and the
// two-way flag are the evidence, and the wording is a presentation decision.
type TrendStatus string

const (
	// TrendOK: both windows exist, the compared span carries at least
	// trendMinActiveMonths active months, the earlier window carries at
	// least trendMinBaselineMessages messages, and both directions
	// exchanged mail inside the compared span.
	TrendOK TrendStatus = "ok"
	// TrendInsufficientHistory: one or more evidence gates failed, or no
	// equal window could be formed. The window counts and the days-since
	// values are still reported.
	TrendInsufficientHistory TrendStatus = "insufficient_history"
)

// TrendWindow is one equal-length side of the cadence comparison: Recent is
// the newest complete calendar months, Baseline the complete months before
// them. Months is the window's length in calendar months; both windows of one
// trend always carry the same value. ActiveMonths is the number of them with
// at least one message: the profile's months are zero-filled, so a zero month
// is a real inactive month that stays inside the window, never a missing one.
// Months is 0 only when fewer than two complete months were available and no
// equal window could be formed; exactly two complete months form one-month
// windows. A zero window reports zero counts.
type TrendWindow struct {
	Months       int `json:"months"`
	Messages     int `json:"messages"`
	Sent         int `json:"sent"`
	Received     int `json:"received"`
	ActiveMonths int `json:"active_months"`
}

// ProfileTrend is the organization's cadence evidence, derived from the
// values already in a ProfileView: the zero-filled month buckets, the
// direction instants and the all-time totals. It performs no mail read of its
// own and calls no provider; the comparison is deliberately neutral numbers,
// not a direction word. Recent and Baseline are equal windows of complete
// calendar months (five per side by default); the current partial month is
// never compared.
//
// TwoWay says both directions exchanged mail inside the compared span, so a
// newsletter or a one-way correspondent can never read as a cadence change.
// Status is "ok" only when every evidence gate holds and
// "insufficient_history" otherwise; it is never a verdict. The counts and the
// days-since values stay present in both states. A nil Trend means the
// organization has no mail at all.
type ProfileTrend struct {
	// Status is "ok" or "insufficient_history": evidence depth, never a
	// judgement about the organization.
	Status TrendStatus `json:"status"`
	// TwoWay is true when the compared span carries at least one sent and
	// one received message.
	TwoWay bool `json:"two_way"`
	// Recent is the newest complete-months window; Baseline is the equal
	// window immediately before it.
	Recent   TrendWindow `json:"recent"`
	Baseline TrendWindow `json:"baseline"`

	// DaysSinceInbound and DaysSinceOutbound are whole days between the
	// profile's build instant and the newest message of that direction.
	// They are omitted when the direction has no dated message; an instant
	// in the future (clock skew) reads zero.
	DaysSinceInbound  *int `json:"days_since_inbound,omitempty"`
	DaysSinceOutbound *int `json:"days_since_outbound,omitempty"`
}

// companyTrend derives the trend from data already in the view; now is the
// instant the view was built at, the same one behind its generated_at and its
// month anchor. The month buckets are the store's zero-filled local calendar
// months, ascending, ending with now's month, so the last bucket is partial
// by construction and the complete months before it are the compared span.
// The comparison uses two equal windows of at least one month each: two
// complete months are enough for one-month windows, and fewer form none. A
// view with no mail returns no trend.
func companyTrend(view ProfileView, now time.Time) *ProfileTrend {
	if view.Messages == 0 {
		return nil
	}
	trend := &ProfileTrend{
		Status:            TrendInsufficientHistory,
		DaysSinceInbound:  daysSince(view.LastInboundAt, now),
		DaysSinceOutbound: daysSince(view.LastOutboundAt, now),
	}
	complete := view.Months
	if len(complete) > 0 {
		complete = complete[:len(complete)-1]
	}
	window := min(trendWindowMonths, len(complete)/2)
	if window == 0 {
		return trend
	}
	trend.Recent = trendWindowOf(complete[len(complete)-window:])
	trend.Baseline = trendWindowOf(complete[len(complete)-2*window : len(complete)-window])
	// A cadence comparison is only meaningful for a two-way exchange: a
	// newsletter, a notification feed or an owner-only outreach has no
	// counterpart reply cadence to compare. The flag is measured over the
	// compared span, so an old two-way exchange cannot qualify today's
	// one-way traffic.
	trend.TwoWay = trend.Recent.Sent+trend.Baseline.Sent > 0 &&
		trend.Recent.Received+trend.Baseline.Received > 0
	if trend.TwoWay &&
		trend.Recent.ActiveMonths+trend.Baseline.ActiveMonths >= trendMinActiveMonths &&
		trend.Baseline.Messages >= trendMinBaselineMessages {
		trend.Status = TrendOK
	}
	return trend
}

// trendWindowOf sums one window's calendar months. Its one rule is that a
// month with zero messages is counted as an inactive month, not skipped.
func trendWindowOf(months []CompanyMonth) TrendWindow {
	window := TrendWindow{Months: len(months)}
	for _, month := range months {
		window.Messages += month.Messages
		window.Sent += month.Sent
		window.Received += month.Received
		if month.Messages > 0 {
			window.ActiveMonths++
		}
	}
	return window
}

// daysSince is the whole days between a stored instant and now. An absent or
// unparseable instant has no age; a future instant is zero, not negative.
func daysSince(instant string, now time.Time) *int {
	at, ok := mail.Parse(instant)
	if !ok {
		return nil
	}
	days := int(now.Sub(at).Hours() / 24)
	if days < 0 {
		days = 0
	}
	return &days
}
