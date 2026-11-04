package locale

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Locale is one language's compiled calendar and number data (its CLDR
// locale joined with its regional locales).
type Locale struct {
	Tag      string
	dates    []compiled // every date pattern
	times    []compiled
	months   *phrases
	weekdays *phrases
	wide     *phrases // the wide weekday names (a weekday named alone)
	// Decimals and Groups are the separators of the language and its
	// regional locales (the language's first): a number they read
	// differently is ambiguous.
	Decimals  []string
	Groups    []string
	calendars []calendar // the calendars its dates may be written in, preferred first (the Gregorian one always among them)
}

var locales sync.Map // tag → *Locale (compiled on first use)

// LocaleOf returns the compiled locale of a shipped language tag
// (Language's tags and Undetermined); ok is false for any other tag.
func LocaleOf(tag string) (*Locale, bool) {
	if l, ok := locales.Load(tag); ok {
		return l.(*Locale), true
	}
	doc, ok := localesData().Locales[tag]
	if !ok {
		return nil, false
	}
	l, err := compileLocale(tag, doc)
	if err != nil {
		panic("locale: locales.json " + tag + ": " + err.Error()) // TestDataLoadsAndEveryLocaleCompiles guards the shipped data
	}
	v, _ := locales.LoadOrStore(tag, l)
	return v.(*Locale), true
}

// compileLocale compiles a locale's patterns plus the root's numeric ones
// (Undetermined: ISO 8601 order), which every language reads.
func compileLocale(tag string, d localeDoc) (*Locale, error) {
	if tag != Undetermined {
		root := localesData().Locales[Undetermined]
		d.DatePatterns = union(d.DatePatterns, root.DatePatterns)
		d.TimePatterns = union(d.TimePatterns, root.TimePatterns)
	}
	l := &Locale{Tag: tag, months: newNames(d.Months[:], 1), weekdays: newNames(d.Weekdays[:], 0), wide: newNames(d.WeekdaysWide[:], 0),
		Decimals: d.Decimals, Groups: d.Groups}
	cals := localesData().Calendars
	for _, c := range union(d.Calendars, []string{gregorian}) {
		doc, ok := cals[c]
		if !ok {
			return nil, fmt.Errorf("calendar %q is not described", c)
		}
		l.calendars = append(l.calendars, newCalendar(c, doc))
	}
	periods := newNames(d.DayPeriods[:], 0)
	build := func(patterns []string) ([]compiled, error) {
		var out []compiled
		for _, p := range patterns {
			c, err := compile(p, l.months, l.weekdays, periods)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}
	var err error
	if l.dates, err = build(d.DatePatterns); err != nil {
		return nil, err
	}
	disputeOrders(l.dates)
	l.times, err = build(d.TimePatterns)
	return l, err
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// DateMention is one date written in a text: Year 0 when the text gave
// none, Weekday -1 when none was written; Named when the month or the
// weekday was a name; Ambiguous when the language's patterns read its
// numbers as different dates (03/04 is 4 March in one region, 3 April in
// another) and no field above 12 decides, or when it is a year-less
// numeric form whose order the language's regions dispute (en "10-12":
// en_CA's MM-dd, but en_GB writes the day first): it is a date, but
// which one is not known (Resolve refuses it).
type DateMention struct {
	Year      int  `json:"year,omitempty"`
	Month     int  `json:"month"` // 0 for a weekday named alone
	Day       int  `json:"day"`   // 0 for a weekday named alone
	Weekday   int  `json:"weekday"`
	Named     bool `json:"named"`
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Mentions are the dates, times and lone weekdays of a text.
type Mentions struct {
	Dates []DateMention `json:"dates"` // full dates and lone weekdays (Month 0)
	Times int           `json:"times"`
}

// Find reads the dates (the locale's date patterns), the times (its time
// patterns) and the weekdays named alone (wide names outside a date) of
// a text. Everything is matched on the folded text (mail.Fold), so case,
// width and Latin diacritics never matter; at each position the longest
// match wins; a match must stand alone (standaloneAt).
func (l *Locale) Find(text string) Mentions {
	tks := tokens(mail.Fold(text))
	var out Mentions
	inDate := make([]bool, len(tks))
	scan(tks, l.dates, l.months, l.weekdays, func(i, j int, readings []reading) {
		if m, ok := readDate(readings); ok {
			out.Dates = append(out.Dates, m)
			for k := i; k < j; k++ {
				inDate[k] = true
			}
		}
	})
	scan(tks, l.times, nil, nil, func(_, _ int, readings []reading) {
		if slices.ContainsFunc(readings, func(r reading) bool { return validTime(r.fields) }) {
			out.Times++
		}
	})
	for i := 0; i < len(tks); i++ {
		if wd, j, ok := l.wide.at(tks, i); ok && !inDate[i] && standaloneAt(tks, i, j) {
			out.Dates = append(out.Dates, DateMention{Weekday: wd, Named: true})
			i = j - 1
		}
	}
	return out
}

// reading is what one pattern read of a match: its fields, and whether
// the pattern is a disputed year-less numeric form (disputeOrders).
type reading struct {
	fields   map[string]int
	disputed bool
}

// scan finds the longest standalone match of the patterns at each
// position that can start one (a number or the first token of a name),
// left to right, without overlaps; fn gets what every pattern of that
// longest match read.
func scan(tks []token, patterns []compiled, months, weekdays *phrases, fn func(i, j int, readings []reading)) {
	for i := 0; i < len(tks); i++ {
		tk := tks[i]
		if tk.kind != tokNumber && (months == nil || !months.first[tk.text]) && (weekdays == nil || !weekdays.first[tk.text]) {
			continue
		}
		best, readings := -1, []reading(nil)
		for k := range patterns {
			j, got, ok := patterns[k].match(tks, i)
			if !ok || j < best || !standaloneAt(tks, i, j) {
				continue
			}
			if j > best {
				best, readings = j, nil
			}
			readings = append(readings, reading{fields: got, disputed: patterns[k].disputed})
		}
		if best > i {
			fn(i, best, readings)
			i = best - 1
		}
	}
}

// readDate is the date the readings of one match agree on: the valid
// ones (dateOf), one date, or the first marked Ambiguous when they name
// different dates or one of them comes from a disputed year-less numeric
// form (its order is not known: disputeOrders).
func readDate(readings []reading) (DateMention, bool) {
	var valid []DateMention
	disputed := false
	for _, r := range readings {
		if m, ok := dateOf(r.fields); ok {
			disputed = disputed || r.disputed
			if !slices.Contains(valid, m) {
				valid = append(valid, m)
			}
		}
	}
	if len(valid) == 0 {
		return DateMention{}, false
	}
	m := valid[0]
	m.Ambiguous = len(valid) > 1 || disputed
	return m, true
}

// dateOf reads a date match; impossible dates (month 13, day 32) are no
// dates, and neither is a written year 0 (a form's placeholder, 0000:
// Year 0 means "no year written").
func dateOf(got map[string]int) (DateMention, bool) {
	m := DateMention{Weekday: -1}
	if v, ok := got[fieldYear]; ok {
		if v == 0 {
			return m, false
		}
		m.Year = v
	} else if v, ok := got[fieldYear2]; ok {
		m.Year = -1 - v // two digits: the century is chosen against the reference (Resolve)
	}
	if v, ok := got[fieldMonthName]; ok {
		m.Month, m.Named = v, true
	} else {
		m.Month = got[fieldMonth]
	}
	if v, ok := got[fieldWeekday]; ok {
		m.Weekday, m.Named = v, true
	}
	m.Day = got[fieldDay]
	return m, m.Month >= 1 && m.Month <= 12 && m.Day >= 1 && m.Day <= 31
}

func validTime(got map[string]int) bool {
	s, hasS := got[fieldSecond]
	return got[fieldHour] >= 0 && got[fieldHour] <= 24 && got[fieldMinute] >= 0 && got[fieldMinute] <= 59 && (!hasS || s <= 59)
}

// number reads decimal digits of any script (-1 when s holds none or
// something else).
func number(s string) int {
	if s == "" {
		return -1
	}
	var b strings.Builder
	for _, r := range s {
		d, ok := digitValue(r)
		if !ok {
			return -1
		}
		b.WriteByte(byte('0' + d))
	}
	n, err := strconv.Atoi(b.String())
	if err != nil {
		return -1
	}
	return n
}

// digitValue is the value of a decimal digit of any script: Unicode
// encodes the Nd digits in runs of whole blocks of ten, each starting at
// its zero (adjacent blocks too: the mathematical digits), so the value
// is the distance from the start of the run, modulo ten.
func digitValue(r rune) (int, bool) {
	if !unicode.IsDigit(r) {
		return 0, false
	}
	start := r
	for unicode.IsDigit(start - 1) {
		start--
	}
	return int(r-start) % 10, true
}

// Resolve dates a mention against the reference date (the mail's date)
// in this language's calendars: a year-less date is put in the year
// (reference year -1, +0, +1) that brings it nearest, one that also fits a
// written weekday first; a written year is read in the calendar (of the
// language's, preferred first) that brings it nearest the reference — a
// two-digit year in the century nearest it — and converted when that is
// exact (calendar.exact); a weekday named alone is its next occurrence
// (0..6 days ahead). It returns the civil date (midnight UTC), or false
// with a reason: ReasonNoSuchDate, ReasonAmbiguous, ReasonCalendar+name
// (never a guessed date).
func (l *Locale) Resolve(m DateMention, ref time.Time) (time.Time, string, bool) {
	day := time.Date(ref.Year(), ref.Month(), ref.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case m.Month == 0:
		if m.Weekday < 0 || m.Weekday > 6 {
			return time.Time{}, ReasonNoSuchDate, false
		}
		return day.AddDate(0, 0, (m.Weekday-int(day.Weekday())+7)%7), "", true
	case m.Ambiguous:
		return time.Time{}, ReasonAmbiguous, false
	case m.Year == 0:
		if first := l.calendars[0]; !first.gregorianMonths && !m.Named {
			return time.Time{}, ReasonCalendar + first.name, false // a numeric day and month of a calendar with its own months
		}
		return gregorianDate(m, day)
	}
	c, y := l.calendarOf(m, day)
	if c.name == gregorian {
		return gregorianDate(m, day)
	}
	t, ok := c.exact(y, m.Month, m.Day)
	if !ok {
		return time.Time{}, ReasonCalendar + c.name, false
	}
	return t, "", true
}

// calendarOf is the calendar a written year is nearest the reference in,
// and the year read in it (a two-digit year: in the century nearest the
// reference's year of that calendar). A month written as a name is a
// Gregorian month: only calendars with those months read it.
func (l *Locale) calendarOf(m DateMention, day time.Time) (calendar, int) {
	best, bestYear, bestDist := calendar{}, 0, 0.0
	for _, c := range l.calendars {
		if m.Named && !c.gregorianMonths {
			continue
		}
		y := m.Year
		if y < 0 {
			y = nearestCentury(-1-m.Year, c.yearOf(day.Year()))
		}
		d := c.approxYear(y) - float64(day.Year())
		if d < 0 {
			d = -d
		}
		if best.name == "" || d < bestDist {
			best, bestYear, bestDist = c, y, d
		}
	}
	return best, bestYear
}

// nearestCentury puts a two-digit year in the century that brings it
// nearest a reference year.
func nearestCentury(yy, ref int) int {
	c := ref / 100 * 100
	best := c + yy
	for _, y := range []int{c - 100 + yy, c + 100 + yy} {
		if absInt(y-ref) < absInt(best-ref) {
			best = y
		}
	}
	return best
}

// gregorianDate resolves a mention in the Gregorian calendar (see Resolve).
func gregorianDate(m DateMention, day time.Time) (time.Time, string, bool) {
	years := []int{m.Year}
	switch {
	case m.Year == 0:
		years = []int{day.Year() - 1, day.Year(), day.Year() + 1}
	case m.Year < 0:
		yy := -1 - m.Year
		c := day.Year() / 100 * 100
		years = []int{c - 100 + yy, c + yy, c + 100 + yy}
	}
	var best time.Time
	bestFits := false
	for _, y := range years {
		d, ok := civilDate(y, m.Month, m.Day)
		if !ok {
			continue // the day does not exist in that month
		}
		fits := m.Weekday < 0 || int(d.Weekday()) == m.Weekday
		if best.IsZero() || fits && !bestFits || fits == bestFits && absInt(Days(d, day)) < absInt(Days(best, day)) {
			best, bestFits = d, fits
		}
	}
	if best.IsZero() {
		return time.Time{}, ReasonNoSuchDate, false
	}
	return best, "", true
}

// Days is the number of civil days from ref's date to d's date (both
// read as calendar dates in their own location), for any years.
func Days(d, ref time.Time) int {
	return int(civilDays(d.Year(), int(d.Month()), d.Day()) - civilDays(ref.Year(), int(ref.Month()), ref.Day()))
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
