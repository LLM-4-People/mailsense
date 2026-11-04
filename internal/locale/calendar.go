package locale

import (
	"strconv"
	"strings"
	"time"
)

// Reasons a date mention has no exact civil date (Resolve).
const (
	ReasonNoSuchDate = "no_such_date"    // 30 February, month 13
	ReasonAmbiguous  = "ambiguous_order" // the language's regions read its numbers in different orders
	ReasonCalendar   = "calendar:"       // + the calendar the date is written in, which has no exact conversion here
)

// calendar is one calendar a language writes dates in (locales.json
// calendars: CLDR calendarData and root's month aliases).
type calendar struct {
	name            string
	lunar           bool // a lunar calendar (CLDR system "lunar": its years are shorter)
	eraYear         int  // the (proleptic Gregorian) year its current era starts in
	gregorianMonths bool // its months and days are the Gregorian ones
}

const (
	gregorian = "gregorian"
	// tabularYear is the mean year of the tabular Islamic calendar in days
	// (10,631 days per 30-year cycle): how many lunar years a solar one holds.
	tabularYear = 10631.0 / 30
	solarYear   = 365.2425 // the Gregorian mean year in days
)

// newCalendar reads a calendar of locales.json.
func newCalendar(name string, d calendarDoc) calendar {
	y, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(d.EraStart, "-"), "-")[0])
	if strings.HasPrefix(d.EraStart, "-") {
		y = -y
	}
	return calendar{name: name, lunar: d.System == "lunar", eraYear: y, gregorianMonths: d.GregorianMonths}
}

// approxYear is the Gregorian year a year of the calendar mostly falls in
// (enough to tell which calendar a written year is nearest in).
func (c calendar) approxYear(y int) float64 {
	if c.lunar {
		return float64(c.eraYear) + float64(y-1)*tabularYear/solarYear
	}
	return float64(c.eraYear + y - 1)
}

// yearOf is the calendar's year that contains a Gregorian year (approximately).
func (c calendar) yearOf(gy int) int {
	if c.lunar {
		return int(float64(gy-c.eraYear)*solarYear/tabularYear) + 1
	}
	return gy - c.eraYear + 1
}

// exact converts a date of the calendar to its civil (Gregorian) date when
// the conversion is exact: the Gregorian calendar itself; a calendar with
// the Gregorian months that only counts years from another era (buddhist,
// roc: CE = year + era start − 1); the Persian (Solar Hijri) calendar over
// the years its arithmetic is known to agree with the official
// astronomical one. Any other calendar (Islamic: which variant — civil,
// Umm al-Qura, observed — a writer used is not in the date) is not exact.
func (c calendar) exact(y, m, d int) (time.Time, bool) {
	switch {
	case c.name == "buddhist" && y < buddhistSolarFrom:
		return time.Time{}, false
	case c.gregorianMonths:
		return civilDate(y+c.eraYear-1, m, d)
	case c.name == "persian":
		return persianDate(y, m, d)
	}
	return time.Time{}, false
}

// buddhistSolarFrom: the Thai solar calendar starts its years on
// 1 January since 2484 BE (1941); before, a year began on 1 April, so a
// January–March date of an earlier year is no fixed offset.
const buddhistSolarFrom = 2484

// civilDate is a proleptic Gregorian date; false when it does not exist.
func civilDate(y, m, d int) (time.Time, bool) {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t, m >= 1 && m <= 12 && t.Day() == d && t.Year() == y
}

// civilDays is the number of days from 1970-01-01 to a civil date
// (proleptic Gregorian, any year: no Duration overflow).
func civilDays(y, m, d int) int64 {
	yy := int64(y)
	if m <= 2 {
		yy--
	}
	era := yy / 400
	if yy < 0 && yy%400 != 0 {
		era--
	}
	yoe := yy - era*400
	mp := (int64(m) + 9) % 12
	doy := (153*mp+2)/5 + int64(d) - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

// Persian (Solar Hijri) arithmetic: 1 Farvardin of year y is Julian day
// 1948320 + 365(y−1) + ⌊(8y+21)/33⌋ (the 33-year rule, leap when
// (25y+11) mod 33 < 8; epoch 1 Farvardin 1 = 21 March 622, CLDR's era
// start). It agrees with the official calendar of the University of
// Tehran's Calendar Center, and with ICU4X's corrected rule, over the
// years below; outside them it is not exact.
const (
	persianEpochJD  = 1948320
	persianExactMin = 1300
	persianExactMax = 1498
	unixEpochJD     = 2440588 // the Julian day of 1970-01-01
)

func persianNewYear(y int) int64 { return persianEpochJD + 365*int64(y-1) + int64((8*y+21)/33) }

func persianDate(y, m, d int) (time.Time, bool) {
	if y < persianExactMin || y > persianExactMax || m < 1 || m > 12 || d < 1 {
		return time.Time{}, false
	}
	days := 31
	switch {
	case m == 12:
		days = int(persianNewYear(y+1) - persianNewYear(y) - 336) // Esfand: 29, 30 in a leap year
	case m > 6:
		days = 30
	}
	if d > days {
		return time.Time{}, false
	}
	offset := int64(d - 1)
	if m <= 6 {
		offset += int64((m - 1) * 31)
	} else {
		offset += int64(186 + (m-7)*30)
	}
	unix := persianNewYear(y) + offset - unixEpochJD
	return time.Unix(unix*86400, 0).UTC(), true
}
