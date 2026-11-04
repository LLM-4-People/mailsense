package locale

import (
	"slices"
	"testing"
	"time"
)

var october15 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) // a Thursday

// days resolves every date mention of a text: the resolved distances and
// the reasons of the others.
func days(t *testing.T, tag, text string) ([]int, []string) {
	t.Helper()
	l, ok := LocaleOf(tag)
	if !ok {
		t.Fatalf("no locale %s", tag)
	}
	var out []int
	var why []string
	for _, m := range l.Find(text).Dates {
		if d, reason, ok := l.Resolve(m, october15); ok {
			out = append(out, Days(d, october15))
		} else {
			why = append(why, reason)
		}
	}
	return out, why
}

// Regression (review L0, bugs #9): UTS #35 widths — MM and dd are exactly
// two digits — so ranges and scores are no dates in any language.
func TestRangesAreNoDates(t *testing.T) {
	for _, tag := range []string{"en", "fr", "de", Undetermined} {
		for _, text := range []string{"aged 5-7", "score 2-1", "chapters 3-11", "pages 10-12"} {
			if text == "pages 10-12" && (tag == "en" || tag == "fr") {
				continue // below
			}
			if d, why := days(t, tag, text); len(d)+len(why) != 0 {
				t.Errorf("%s %q: %v %v", tag, text, d, why)
			}
		}
	}
	if d, _ := days(t, Undetermined, "on 2026-10-20"); !slices.Equal(d, []int{5}) {
		t.Errorf("ISO 8601 still reads: %v", d)
	}
}

// Lead decision 2026-10-15 (open issue of review L0: "10-12" read as a
// date): a year-less numeric form is a resolved date only if no region of
// the language writes its numeric dates in the other order. en_CA and
// fr_CA write year-less MM-dd, en_GB and fr_FR the day first: in English
// and French such a form is a date of unknown order (ambiguous_order);
// every German region writes the day first, so "20.10." stays a date.
// The rule is the data's (disputeOrders), no language is named in code.
func TestYearlessNumericDatesNeedAgreeingRegions(t *testing.T) {
	cases := []struct {
		tag, text string
		days      []int
		why       []string
	}{
		{"en", "pages 10-12", nil, []string{ReasonAmbiguous}},
		{"en", "on 10-20", nil, []string{ReasonAmbiguous}},
		{"en", "by 10/23", nil, []string{ReasonAmbiguous}}, // a field above 12 no longer decides a disputed form
		{"fr", "pages 10-12", nil, []string{ReasonAmbiguous}},
		{"fr", "le 03/11", nil, []string{ReasonAmbiguous}},
		{"fr", "le 03/11/2026", []int{19}, nil}, // with a year every French region writes the day first
		{"en", "due 10/20/2026", []int{5}, nil},
		{"de", "bis 20.10. bitte", []int{5}, nil},
		{"de", "bis 20.10.2026", []int{5}, nil},
		{"zh-Hant", "請在10月20日前回覆", []int{5}, nil}, // literals naming the fields leave no order to guess
	}
	for _, c := range cases {
		d, why := days(t, c.tag, c.text)
		if !slices.Equal(d, c.days) || !slices.Equal(why, c.why) {
			t.Errorf("%s %q: %v %v, want %v %v", c.tag, c.text, d, why, c.days, c.why)
		}
	}
	// The pattern-level rule itself.
	l, _ := LocaleOf("de")
	for _, c := range l.dates {
		if c.disputed {
			t.Errorf("de: %q disputed, but every German region writes the day first", c.pattern)
		}
	}
}

// Regression (review L0, rules #8, bugs plausible: US-only English): a
// language reads the patterns of all its regions; a numeric date their
// orders read differently is ambiguous — a date, never a confident wrong
// one — unless a field above 12 decides.
func TestRegionalPatternsAndAmbiguousOrder(t *testing.T) {
	cases := []struct {
		tag, text string
		days      []int
		why       []string
	}{
		{"en", "Please reply by 2 January 2027", []int{79}, nil},
		{"en", "Friday 13 March", []int{-216}, nil}, // a full date (13 March 2026: the year whose 13 March is a Friday)
		{"en", "due 20/10/2026", []int{5}, nil},
		{"en", "due 10/20/2026", []int{5}, nil},
		{"en", "due 03/04/2026", nil, []string{ReasonAmbiguous}},
		{"en", "1/2 cup of flour", nil, []string{ReasonAmbiguous}},
		{"en", "Due 11/3/26", nil, []string{ReasonAmbiguous}},
		{"fr", "le 03/11/2026", []int{19}, nil}, // every French region writes the day first
	}
	for _, c := range cases {
		d, why := days(t, c.tag, c.text)
		if !slices.Equal(d, c.days) || !slices.Equal(why, c.why) {
			t.Errorf("%s %q: %v %v, want %v %v", c.tag, c.text, d, why, c.days, c.why)
		}
	}
}

// Regression (review L0, rules #7): a year written in another calendar of
// the language's territory is read in the calendar that brings it nearest:
// converted when that is exact (Buddhist era, Persian arithmetic within
// its agreed years), else not a date with a disclosed reason — never a
// date centuries away.
func TestCalendars(t *testing.T) {
	cases := []struct {
		tag, text string
		days      []int
		why       []string
	}{
		{"th", "ส่งภายใน 15/10/2569", []int{0}, nil},           // Buddhist era 2569 = 2026
		{"th", "ส่งภายใน 20/10/69", []int{5}, nil},             // a two-digit Buddhist year
		{"th", "ส่งภายใน 15/10/2026", []int{0}, nil},           // the Gregorian year is nearer
		{"th", "1/1/2480", nil, []string{"calendar:buddhist"}}, // before the 1941 reform: no fixed offset
		{"fa", "تا ۱۴۰۵/۰۷/۲۵", []int{2}, nil},                 // Solar Hijri 1405-07-25 = 2026-10-17
		{"fa", "تا ۲۰۲۶/۱۰/۲۰", []int{5}, nil},                 // a Gregorian year in Persian text
		{"fa", "تا ۷/۲۵", nil, []string{"calendar:persian"}},   // a month and day of the Persian months
		{"ar", "الموعد ٢٣/٤/١٤٤٧", nil, []string{"calendar:islamic"}},
	}
	for _, c := range cases {
		d, why := days(t, c.tag, c.text)
		if !slices.Equal(d, c.days) || !slices.Equal(why, c.why) {
			t.Errorf("%s %q: %v %v, want %v %v", c.tag, c.text, d, why, c.days, c.why)
		}
	}
}

// Persian arithmetic against published dates (Calendar Center of the
// University of Tehran; ICU): Nowruz 1403–1405 and today.
func TestPersianArithmetic(t *testing.T) {
	for _, c := range []struct {
		y, m, d int
		want    string
	}{
		{1403, 1, 1, "2024-03-20"}, {1403, 12, 30, "2025-03-20"}, {1404, 1, 1, "2025-03-21"}, {1405, 1, 1, "2026-03-21"},
		{1405, 7, 23, "2026-10-15"}, {1405, 7, 25, "2026-10-17"},
	} {
		got, ok := persianDate(c.y, c.m, c.d)
		if !ok || got.Format("2006-01-02") != c.want {
			t.Errorf("%d-%d-%d: %v %v, want %s", c.y, c.m, c.d, got, ok, c.want)
		}
	}
	for _, bad := range [][3]int{{1404, 12, 30}, {1405, 7, 31}, {1405, 13, 1}, {1299, 1, 1}, {1499, 1, 1}} {
		if _, ok := persianDate(bad[0], bad[1], bad[2]); ok {
			t.Errorf("%v must not resolve (no such day, or outside the exact years)", bad)
		}
	}
}

// Regression (review L0, bugs #13, rules #6): distances are counted in
// civil day numbers, never a saturating Duration.
func TestDaysAcrossCenturies(t *testing.T) {
	far := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	if d := Days(far, october15); d != 2912155 {
		t.Fatalf("9999-12-31: %d days", d)
	}
	if d := Days(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), october15); d != -739903 {
		t.Fatalf("0001-01-01: %d days", d)
	}
	if d := Days(time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC), october15); d != 1 {
		t.Fatalf("the next day: %d", d)
	}
}

// Regression (review L0, bugs #14): the digits of adjacent blocks (the
// mathematical digits) keep their values.
func TestDigitValuesOfAdjacentBlocks(t *testing.T) {
	for r, want := range map[rune]int{'\U0001D7D8': 0, '\U0001D7E1': 9, '\U0001D7CE': 0, '\U0001D7FF': 9, '٣': 3, '9': 9} {
		if got, ok := digitValue(r); !ok || got != want {
			t.Errorf("%U: %d %v, want %d", r, got, ok, want)
		}
	}
}

// Regression (review L0, bugs #11, rules #9/#10, bugs plausible: negative
// amounts): separators come from every locale's CLDR data, numbers join
// across a space only in exactly-three-digit groups of a language that
// groups with a space, a code next to a plain integer is an amount of
// unknown value, and a minus sign makes it negative.
func TestAmountsReadWithTheLanguagesSeparators(t *testing.T) {
	type want struct {
		value float64
		known bool
	}
	cases := []struct {
		tag, text string
		want      []want
	}{
		{"en", "USD 100 2026", []want{{100, false}}},   // never 1,002,026; a plain integer next to a code: value unknown
		{"en", "3 100 EUR", []want{{100, false}}},      // English never groups with a space
		{"fr", "3 100,50 EUR", []want{{3100.5, true}}}, // French does
		{"fr", "12 3456 EUR", []want{{3456, false}}},   // a group is exactly three digits
		{"en", "ALL 5 of them", []want{{5, false}}},    // a word that is a code: never a confident value
		{"en", "50 CUP", []want{{50, false}}},
		{"fa", "مبلغ ۱۲٬۵۰۰٫۷۵ ریال", []want{{12500.75, true}}}, // Persian separators (CLDR arabext)
		{"bn", "মূল্য ১,২৩৪ BDT", []want{{1234, true}}},         // Bengali: beng's separators are latn's (CLDR alias)
		{"fr", "remise de -50,00 EUR", []want{{-50, true}}},
		{"de", "Saldo EUR −1.234,50", []want{{-1234.5, true}}},
		{"en", "pages 10-12 EUR", []want{{12, false}}}, // a range's dash is no minus sign
		{"es", "total 1.234 EUR", []want{{0, false}}},  // Spanish regions read 1.234 both ways
	}
	for _, c := range cases {
		l, _ := LocaleOf(c.tag)
		got := l.Amounts(c.text)
		var have []want
		for _, a := range got {
			w := want{known: a.Known}
			if a.Known || a.Value != 0 && !a.Known {
				w.value = a.Value
			}
			have = append(have, w)
		}
		if len(have) != len(c.want) {
			t.Errorf("%s %q: %+v", c.tag, c.text, got)
			continue
		}
		for i := range have {
			if have[i].known != c.want[i].known || c.want[i].known && have[i].value != c.want[i].value {
				t.Errorf("%s %q #%d: %+v, want %+v", c.tag, c.text, i, got[i], c.want[i])
			}
		}
	}
}

// Regression (review L0, rules #10): every shipped locale has a decimal
// and a grouping separator (a native numbering system's alias followed).
func TestEveryLocaleHasSeparators(t *testing.T) {
	for tag, l := range localesData().Locales {
		if len(l.Decimals) == 0 || len(l.Groups) == 0 {
			t.Errorf("%s: decimals %q groups %q", tag, l.Decimals, l.Groups)
		}
	}
}

// Review of the crash-safety round, #10 (failed first: a date 5 days
// ahead): a written year 0000 (a form's placeholder, "0000-10-20") read as
// "no year" and resolved to the nearest October 20. No calendar a mail is
// dated in has a year 0 written so: such a match is no date.
func TestAWrittenYearZeroIsNoDate(t *testing.T) {
	for _, c := range []struct{ tag, text string }{
		{Undetermined, "on 0000-10-20"}, {"en", "on 0000-10-20"}, {"de", "am 20.10.0000"}, {"fr", "le 20/10/0000"},
	} {
		if d, why := days(t, c.tag, c.text); len(d) != 0 {
			t.Errorf("%s %q: resolved to %v (%v)", c.tag, c.text, d, why)
		}
	}
	if d, _ := days(t, "de", "am 20.10.2026"); !slices.Equal(d, []int{5}) {
		t.Errorf("a real year still reads: %v", d)
	}
}
