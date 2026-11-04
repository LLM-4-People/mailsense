package mail

import (
	"fmt"
	netmail "net/mail"
	"strings"
	"time"
)

// TimeLayout is the one stored timestamp form: UTC, second precision, Z.
const TimeLayout = "2006-01-02T15:04:05Z"

// Times normalizes timestamps. Outlook marks "no date" with far-past or
// far-future values (1/1/4501); those become "".
type Times struct {
	NoDateYearAtMost  int // a year ≤ this means "no date"
	NoDateYearAtLeast int // a year ≥ this means "no date"
}

// Format renders t in the stored form, or "" for a no-date sentinel.
func (r Times) Format(t time.Time) string {
	if t.IsZero() || r.isSentinel(t) {
		return ""
	}
	return t.UTC().Format(TimeLayout)
}

// Stamp renders a derived instant for the wire: the stored form (UTC, second
// precision, Z) or "" for the zero instant. Every generated_at, last_at and
// sync stamp of a response uses it, so the JSON the UI reads is one form and
// mail.Parse can read it back.
func Stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(TimeLayout)
}

// monthLayout is the one month bucket key. A behaviour volume month, a company
// activity month and a time-plan month bucket are the same wire key, so its
// format has one owner.
const monthLayout = "2006-01"

// MonthKey is the month bucket of an instant: the caller passes the instant
// already in the zone that names the buckets.
func MonthKey(t time.Time) string { return t.Format(monthLayout) }

// SplitMonth splits a MonthKey into its year and month digits. A key that is
// not a month renders ("", ""), so a caller never slices past its end and junk
// never becomes a year bucket.
func SplitMonth(key string) (year, month string) {
	at, err := time.Parse(monthLayout, key)
	if err != nil {
		return "", ""
	}
	return at.Format("2006"), at.Format("01")
}

// Normalize parses an RFC3339 value (any offset, optional fraction) and
// returns the stored form. "" stays "". Unparseable input is an error.
func (r Times) Normalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", fmt.Errorf("mail: not an RFC3339 timestamp: %q", s)
	}
	return r.Format(t), nil
}

// Parse reads a stored timestamp; ok is false for "" or junk.
func Parse(s string) (time.Time, bool) {
	t, err := time.Parse(TimeLayout, s)
	return t, err == nil
}

func (r Times) isSentinel(t time.Time) bool {
	y := t.UTC().Year()
	return y <= r.NoDateYearAtMost || y >= r.NoDateYearAtLeast
}

// Instant is when a message happened for its owner — the one timing rule
// of threads, triage states and labels: a sent item when it left
// (sent_on, else received_at), any other message when it arrived.
func Instant(role FolderRole, receivedAt, sentOn string) (time.Time, bool) {
	if role == RoleSent && sentOn != "" {
		return Parse(sentOn)
	}
	return Parse(receivedAt)
}

// Instant is Instant of the message.
func (m *Message) Instant() (time.Time, bool) { return Instant(m.FolderRole, m.ReceivedAt, m.SentOn) }

// AuthorInstant is the message's instant in its author's own UTC offset,
// the one its Date header carries (RFC 5322 §3.3): the day and hour the
// author wrote it in, causal (the message carries it) and independent of
// any setting. false without a usable instant or Date header (the
// owner's own copy carries no transport headers).
func (m *Message) AuthorInstant() (time.Time, bool) {
	at, dated := m.Instant()
	if !dated {
		return time.Time{}, false
	}
	sent, err := netmail.ParseDate(HeaderValue(m.Headers, "Date"))
	if err != nil {
		return time.Time{}, false
	}
	return at.In(sent.Location()), true
}
