package outlook

import (
	"errors"
	"fmt"
	"math/bits"
	"time"
)

// The recurrence rule of one master, as the Outlook object model exposes it
// (RecurrencePattern). All times are the owner's local wall clock, held in a
// UTC time.Time whose calendar fields are the wall clock: Outlook's built-in
// date properties are local and have no zone (date.go), so civil arithmetic
// must never go through a zone conversion.
//
// This file owns the recurrence math; calendar.go owns the COM reads and the
// single GetOccurrence call per candidate. IncludeRecurrences is never used:
// Outlook documents it as slow and it has no window bound.
type recurrencePattern struct {
	kind     int64 // OlRecurrenceType; -1 when it could not be read
	interval int   // units of the pattern (days, weeks, months or years)
	start    time.Time
	end      time.Time // last allowed occurrence date; zero when unbounded
	count    int       // Occurrences; 0 when the series has no count bound
	mask     int       // OlDaysOfWeek bits
	day      int       // DayOfMonth
	instance int       // Instance (1-4, 5 = last)
	month    int       // MonthOfYear
}

// OlRecurrenceType values (Outlook object model). Value 4 is not defined.
const (
	recurUnknown  int64 = -1
	recurDaily    int64 = 0
	recurWeekly   int64 = 1
	recurMonthly  int64 = 2
	recurMonthNth int64 = 3
	recurYearly   int64 = 5
	recurYearNth  int64 = 6
)

// OlDaysOfWeek bits: Sunday is bit 0, Saturday bit 6.
const (
	daySunday = 1 << iota
	dayMonday
	dayTuesday
	dayWednesday
	dayThursday
	dayFriday
	daySaturday
	allDays = daySunday | dayMonday | dayTuesday | dayWednesday | dayThursday | dayFriday | daySaturday
)

// maxOccurrencesPerSeries is the hard per-series cap on GetOccurrence calls
// for one window. The largest window the syncer can ask for is 365 past +
// 730 future days (config schema), whose densest legal pattern (every day)
// is 1,095 occurrences, so 2,000 leaves a wide margin while still bounding a
// pathological or corrupt pattern. Reaching it skips the series with one
// warning instead of flooding the COM thread.
const maxOccurrencesPerSeries = 2000

// noEndDateYear is the year Outlook puts in PatternEndDate for a pattern
// that never ends (the same "no date" convention as mail.Times).
const noEndDateYear = 4500

// errRecurrenceUnsupported marks a pattern this connector will not expand:
// an unknown RecurrenceType, an invalid field, or a series denser than
// maxOccurrencesPerSeries. The caller logs one warning and skips the series.
var errRecurrenceUnsupported = errors.New("outlook: recurrence pattern cannot be enumerated")

func unsupportedPattern(pattern, format string, args ...any) error {
	return fmt.Errorf("%w: %s: %s", errRecurrenceUnsupported, pattern, fmt.Sprintf(format, args...))
}

// name is the log label of the pattern type. It is never owner content.
func (p recurrencePattern) name() string {
	switch p.kind {
	case recurDaily:
		return "daily"
	case recurWeekly:
		return "weekly"
	case recurMonthly:
		return "monthly"
	case recurMonthNth:
		return "monthly-nth"
	case recurYearly:
		return "yearly"
	case recurYearNth:
		return "yearly-nth"
	case recurUnknown:
		return "unknown"
	}
	return fmt.Sprintf("type %d", p.kind)
}

// check rejects a pattern whose fields cannot describe occurrences safely.
func (p recurrencePattern) check() error {
	if p.start.IsZero() {
		return unsupportedPattern(p.name(), "no pattern start date")
	}
	if p.interval < 1 {
		return unsupportedPattern(p.name(), "interval %d", p.interval)
	}
	switch p.kind {
	case recurDaily:
		if p.mask&^allDays != 0 {
			return unsupportedPattern(p.name(), "day-of-week mask %#x", p.mask)
		}
		// Outlook's "every weekday" daily pattern carries the Monday-Friday
		// mask with interval 1. A weekday mask on a multi-day interval has no
		// documented meaning, so it is refused rather than guessed.
		if p.mask != 0 && p.mask != allDays && p.interval != 1 {
			return unsupportedPattern(p.name(), "weekday mask with interval %d", p.interval)
		}
	case recurWeekly:
		if p.mask == 0 || p.mask&^allDays != 0 {
			return unsupportedPattern(p.name(), "day-of-week mask %#x", p.mask)
		}
	case recurMonthly:
		if p.day < 1 || p.day > 31 {
			return unsupportedPattern(p.name(), "day of month %d", p.day)
		}
	case recurMonthNth:
		if p.instance < 1 || p.instance > 5 {
			return unsupportedPattern(p.name(), "instance %d", p.instance)
		}
		// Outlook accepts a single weekday only for monthly/yearly patterns
		// (RecurrencePattern.DayOfWeekMask remarks).
		if p.mask == 0 || p.mask&^allDays != 0 || bits.OnesCount(uint(p.mask)) != 1 {
			return unsupportedPattern(p.name(), "day-of-week mask %#x", p.mask)
		}
	case recurYearly:
		if p.month < 1 || p.month > 12 {
			return unsupportedPattern(p.name(), "month of year %d", p.month)
		}
		if p.day < 1 || p.day > 31 {
			return unsupportedPattern(p.name(), "day of month %d", p.day)
		}
	case recurYearNth:
		if p.month < 1 || p.month > 12 {
			return unsupportedPattern(p.name(), "month of year %d", p.month)
		}
		if p.instance < 1 || p.instance > 5 {
			return unsupportedPattern(p.name(), "instance %d", p.instance)
		}
		if p.mask == 0 || p.mask&^allDays != 0 || bits.OnesCount(uint(p.mask)) != 1 {
			return unsupportedPattern(p.name(), "day-of-week mask %#x", p.mask)
		}
	default:
		return unsupportedPattern(p.name(), "unsupported recurrence type %d", p.kind)
	}
	return nil
}

// dates returns the occurrence starts in [since, until), ascending. since and
// until are wall-clock values too (see wallClock), so the whole computation is
// calendar arithmetic in the owner's time.
func (p recurrencePattern) dates(since, until time.Time) ([]time.Time, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if !since.Before(until) {
		return nil, nil
	}
	startDay := dateOnly(p.start)
	if startDay.After(dateOnly(until)) {
		return nil, nil
	}
	if !p.end.IsZero() && startDay.After(p.end) {
		return nil, nil
	}

	first := p.firstPeriod(since)
	seen := p.occurrencesBefore(first)
	out := make([]time.Time, 0, 8)
	for k := first; ; k++ {
		anchor := p.periodAnchor(k)
		if !p.end.IsZero() && anchor.After(p.end) {
			break
		}
		if !anchor.Before(until) {
			break
		}
		for _, d := range p.period(k) {
			if d.Before(startDay) {
				continue // the first period may begin before the series does
			}
			seen++
			if p.count > 0 && seen > p.count {
				return out, nil
			}
			if d.Before(since) {
				continue
			}
			if !p.end.IsZero() && dateOnly(d).After(p.end) {
				return out, nil
			}
			if !d.Before(until) {
				return out, nil
			}
			if len(out) == maxOccurrencesPerSeries {
				return nil, unsupportedPattern(p.name(), "more than %d occurrences in the requested window",
					maxOccurrencesPerSeries)
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// firstPeriod is the first period whose dates can reach since. Period k is
// the k-th repetition of the pattern (the k-th day/week/month/year group).
func (p recurrencePattern) firstPeriod(since time.Time) int {
	d := dateOnly(since)
	var n int
	switch p.kind {
	case recurDaily:
		n = daysBetween(dateOnly(p.start), d)
	case recurWeekly:
		n = daysBetween(weekStart(dateOnly(p.start)), weekStart(d)) / 7
	case recurMonthly, recurMonthNth:
		n = monthsBetween(dateOnly(p.start), d)
	case recurYearly, recurYearNth:
		n = d.Year() - p.start.Year()
	}
	if n < 0 {
		return 0
	}
	return n / p.interval
}

// occurrencesBefore counts the occurrences in periods [0, k).
func (p recurrencePattern) occurrencesBefore(k int) int {
	if k <= 0 {
		return 0
	}
	switch p.kind {
	case recurDaily:
		if p.mask == 0 || p.mask == allDays {
			return k
		}
		// Every-weekday daily patterns are interval 1 (check refuses the
		// rest), so the masked days of the k days before the period repeat
		// every seven days.
		startDay := dateOnly(p.start)
		n := (k / 7) * bits.OnesCount(uint(p.mask))
		for i := range k % 7 {
			if p.mask&weekdayBit(startDay.AddDate(0, 0, i)) != 0 {
				n++
			}
		}
		return n
	case recurWeekly:
		return p.firstWeekCount() + (k-1)*bits.OnesCount(uint(p.mask))
	default:
		if p.firstPeriodYields() {
			return k
		}
		return k - 1
	}
}

// firstWeekCount is the number of masked days in the start week that fall on
// or after PatternStartDate (the week's earlier days are not occurrences).
func (p recurrencePattern) firstWeekCount() int {
	n := 0
	startDay := dateOnly(p.start)
	for _, d := range p.period(0) {
		if !d.Before(startDay) {
			n++
		}
	}
	return n
}

// firstPeriodYields reports whether period 0 contains an occurrence at or
// after PatternStartDate. It is always true for a well-formed Outlook
// pattern; the false branch keeps the count exact for a corrupt one.
func (p recurrencePattern) firstPeriodYields() bool {
	startDay := dateOnly(p.start)
	for _, d := range p.period(0) {
		if !d.Before(startDay) {
			return true
		}
	}
	return false
}

// periodAnchor is the first calendar day of period k, used to stop the loop
// as soon as a whole period lies beyond the window or the pattern's end.
func (p recurrencePattern) periodAnchor(k int) time.Time {
	startDay := dateOnly(p.start)
	switch p.kind {
	case recurDaily:
		return startDay.AddDate(0, 0, k*p.interval)
	case recurWeekly:
		return weekStart(startDay).AddDate(0, 0, k*p.interval*7)
	case recurMonthly, recurMonthNth:
		return monthStart(startDay).AddDate(0, k*p.interval, 0)
	default: // yearly, yearly-nth
		return time.Date(startDay.Year()+k*p.interval, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
}

// period returns the occurrence starts of period k, before the window and
// start-date filters of dates. Every returned time carries the pattern's
// StartTime; days outside the pattern are absent.
func (p recurrencePattern) period(k int) []time.Time {
	startDay := dateOnly(p.start)
	switch p.kind {
	case recurDaily:
		d := startDay.AddDate(0, 0, k*p.interval)
		if p.mask != 0 && p.mask != allDays && p.mask&weekdayBit(d) == 0 {
			return nil
		}
		return []time.Time{p.at(d)}
	case recurWeekly:
		week := weekStart(startDay).AddDate(0, 0, k*p.interval*7)
		out := make([]time.Time, 0, bits.OnesCount(uint(p.mask)))
		for i := range 7 {
			d := week.AddDate(0, 0, i)
			if p.mask&weekdayBit(d) != 0 {
				out = append(out, p.at(d))
			}
		}
		return out
	case recurMonthly, recurMonthNth:
		m := monthStart(startDay).AddDate(0, k*p.interval, 0)
		if p.kind == recurMonthly {
			// Outlook clamps a monthly day 29-31 to the month's last day
			// (MS-OXCICAL 2.1.3.2.3, End-of-Month Concerns).
			return []time.Time{p.at(time.Date(m.Year(), m.Month(), min(p.day, daysInMonth(m.Year(), m.Month())),
				0, 0, 0, 0, time.UTC))}
		}
		if d := nthWeekday(m.Year(), m.Month(), p.instance, p.mask); !d.IsZero() {
			return []time.Time{p.at(d)}
		}
		return nil
	default: // yearly, yearly-nth
		year := startDay.Year() + k*p.interval
		month := time.Month(p.month)
		if p.kind == recurYearly {
			return []time.Time{p.at(time.Date(year, month, min(p.day, daysInMonth(year, month)),
				0, 0, 0, 0, time.UTC))}
		}
		if d := nthWeekday(year, month, p.instance, p.mask); !d.IsZero() {
			return []time.Time{p.at(d)}
		}
		return nil
	}
}

// at applies the pattern's StartTime to a calendar day.
func (p recurrencePattern) at(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), p.start.Hour(), p.start.Minute(), p.start.Second(),
		0, time.UTC)
}

// wallClock renders an instant as the owner's local wall clock, the form
// Outlook's built-in date properties and its Restrict filter use (see
// restrictCalendar). The result is a UTC time.Time carrying those fields so
// calendar arithmetic never crosses a zone.
func wallClock(t time.Time) time.Time {
	l := t.In(time.Local)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func weekStart(t time.Time) time.Time {
	return dateOnly(t).AddDate(0, 0, -int(t.Weekday()))
}

// weekdayBit maps Go's Sunday=0 week to Outlook's OlDaysOfWeek bits.
func weekdayBit(t time.Time) int { return 1 << uint(t.Weekday()) }

func daysBetween(a, b time.Time) int {
	// Unix seconds, not Duration: a series may start before 1601 + 292
	// years, where time.Time.Sub saturates.
	return int((dateOnly(b).Unix() - dateOnly(a).Unix()) / 86400)
}

func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()-a.Month())
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nthWeekday returns the instance-th weekday of a month (instance 5: the
// last one), or the zero time when the month has none.
func nthWeekday(year int, month time.Month, instance, mask int) time.Time {
	if instance == 5 {
		for d := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC); d.Month() == month; d = d.AddDate(0, 0, -1) {
			if weekdayBit(d) == mask {
				return d
			}
		}
		return time.Time{}
	}
	n := 0
	for d := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC); d.Month() == month; d = d.AddDate(0, 0, 1) {
		if weekdayBit(d) == mask {
			n++
			if n == instance {
				return d
			}
		}
	}
	return time.Time{}
}
