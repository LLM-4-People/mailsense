package locale

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Field kinds a compiled pattern captures (UTS #35 date field symbols,
// grouped by what the matcher reads).
const (
	fieldYear      = "y"  // four-digit year
	fieldYear2     = "yy" // two-digit year
	fieldMonth     = "M"  // numeric month
	fieldMonthName = "N"  // month name (MMM, MMMM, LLL…)
	fieldDay       = "d"
	fieldWeekday   = "E"
	fieldHour      = "H"
	fieldMinute    = "m"
	fieldSecond    = "s"
	fieldPeriod    = "a"
)

// element is one step of a compiled pattern: a field (a number of
// digits min..max, or a name), a literal (the folded text of one or more
// tokens) or white space (one optional white-space token).
type element struct {
	field    string
	min, max int      // digits of a numeric field
	names    *phrases // a name field's names
	literal  string
	space    bool
}

// compiled is one CLDR pattern as elements. disputed marks a year-less
// numeric date pattern whose day/month order another pattern of the
// language (of any of its regions) reverses (disputeOrders).
type compiled struct {
	pattern  string
	elements []element
	disputed bool
}

// numericOrder is the day/month order an all-numeric date pattern claims
// ("dM" or "Md"), and whether it has no year. "" for any other pattern:
// one with a month name, without a day or a month, with its year first
// (ISO 8601's y-MM-dd is read by its year, never by a convention), or
// whose literals hold a letter — a literal that names the fields
// ("M月d日": 月 month, 日 day) leaves no order to guess.
func (c *compiled) numericOrder() (order string, yearless bool) {
	day, month, year := -1, -1, -1
	for i, e := range c.elements {
		if strings.IndexFunc(e.literal, unicode.IsLetter) >= 0 {
			return "", false
		}
		switch e.field {
		case fieldDay:
			day = i
		case fieldMonth:
			month = i
		case fieldYear, fieldYear2:
			year = i
		case fieldMonthName:
			return "", false
		}
	}
	switch {
	case day < 0 || month < 0 || year >= 0 && year < min(day, month):
		return "", false
	case day < month:
		return "dM", year < 0
	}
	return "Md", year < 0
}

// disputeOrders marks the year-less numeric date patterns whose order
// another pattern of the same language reverses. The patterns are the
// union of the language's regions, so this is the rule "a year-less
// numeric date is a resolved date only if every region writing that form
// reads it in the same order and no region of the language writes its
// numeric dates the other way" (en: en_CA's MM-dd against en_GB's dd/MM;
// fr: fr_CA's MM-dd against fr_FR's dd/MM): a disputed form is still a
// date mention, of an unknown order (DateMention.Ambiguous).
func disputeOrders(patterns []compiled) {
	claims := map[string]bool{}
	for i := range patterns {
		if o, _ := patterns[i].numericOrder(); o != "" {
			claims[o] = true
		}
	}
	for i := range patterns {
		o, yearless := patterns[i].numericOrder()
		patterns[i].disputed = yearless && claims[map[string]string{"dM": "Md", "Md": "dM"}[o]]
	}
}

// newNames folds a list of names per index (index + base is the value);
// a name ending in "." is also known without it (abbreviations are
// written both ways).
func newNames(lists [][]string, base int) *phrases {
	p := newPhrases()
	for i, list := range lists {
		for _, v := range list {
			f := mail.Fold(v)
			if strings.TrimSpace(f) == "" {
				continue
			}
			p.add(f, i+base)
			if t := strings.TrimSuffix(f, "."); t != f && strings.TrimSpace(t) != "" {
				p.add(t, i+base)
			}
		}
	}
	return p
}

// compile turns a CLDR pattern into elements. It fails on a field the
// matcher does not read and on a name field whose locale has no names.
func compile(pattern string, months, weekdays, periods *phrases) (compiled, error) {
	c := compiled{pattern: pattern}
	rs := []rune(pattern)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			lit := string(rs[i+1 : min(j, len(rs))])
			if j == i+1 && j < len(rs) { // '' is a quote
				lit = "'"
			}
			c.literal(lit)
			i = j + 1
		case isFieldLetter(r):
			j := i
			for j < len(rs) && rs[j] == r {
				j++
			}
			e, err := field(r, j-i, months, weekdays, periods)
			if err != nil {
				return compiled{}, fmt.Errorf("pattern %q: %w", pattern, err)
			}
			c.add(e)
			i = j
		default:
			j := i
			for j < len(rs) && rs[j] != '\'' && !isFieldLetter(rs[j]) {
				j++
			}
			c.literal(string(rs[i:j]))
			i = j
		}
	}
	return c, nil
}

func isFieldLetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// add appends a field. A name written right after a literal, without
// white space ("d בMMMM": Hebrew's ב before the month), is one word in a
// text: the literal joins the names.
func (c *compiled) add(e element) {
	if n := len(c.elements); e.names != nil && n > 0 && c.elements[n-1].literal != "" {
		e.names = e.names.prefixed(c.elements[n-1].literal)
		c.elements = c.elements[:n-1]
	}
	c.elements = append(c.elements, e)
}

// literal appends a literal in the folded form of the text: its white
// space as optional white space, the rest as literal elements.
func (c *compiled) literal(s string) {
	for _, tk := range tokens(mail.Fold(s)) {
		if tk.kind == tokSpace {
			c.elements = append(c.elements, element{space: true})
			continue
		}
		c.elements = append(c.elements, element{literal: tk.text})
	}
}

func field(r rune, n int, months, weekdays, periods *phrases) (element, error) {
	byName := func(kind string, set *phrases) (element, error) {
		if len(set.byKey) == 0 {
			return element{}, fmt.Errorf("field %c: the locale has no names", r)
		}
		return element{field: kind, names: set}, nil
	}
	switch {
	case r == 'y' && n == 2:
		return element{field: fieldYear2, min: 2, max: 2}, nil
	case r == 'y':
		return element{field: fieldYear, min: 4, max: 4}, nil
	case (r == 'M' || r == 'L') && n <= 2:
		return element{field: fieldMonth, min: n, max: 2}, nil // UTS #35 widths: MM is exactly two digits, M one or two
	case r == 'M' || r == 'L':
		return byName(fieldMonthName, months)
	case r == 'd':
		return element{field: fieldDay, min: min(n, 2), max: 2}, nil
	case r == 'E' || r == 'c' && n >= 3:
		return byName(fieldWeekday, weekdays)
	case r == 'h' || r == 'H' || r == 'k' || r == 'K':
		return element{field: fieldHour, min: 1, max: 2}, nil
	case r == 'm':
		return element{field: fieldMinute, min: 2, max: 2}, nil
	case r == 's':
		return element{field: fieldSecond, min: 2, max: 2}, nil
	case r == 'a' || r == 'b' || r == 'B':
		return byName(fieldPeriod, periods)
	}
	return element{}, fmt.Errorf("field %c×%d is not read", r, n)
}

// match matches the pattern at token i: the token after the match and
// the fields read (numbers as their digits' text, names as their value).
func (c *compiled) match(tks []token, i int) (int, map[string]int, bool) {
	got := map[string]int{}
	for _, e := range c.elements {
		switch {
		case e.space:
			i = space(tks, i)
		case e.literal != "":
			if j, ok := literalAt(tks, i, e.literal); ok {
				i = j
				continue
			}
			return 0, nil, false
		case e.names != nil:
			v, j, ok := e.names.at(tks, i)
			if !ok {
				return 0, nil, false
			}
			got[e.field], i = v, j
		default:
			if i >= len(tks) || tks[i].kind != tokNumber {
				return 0, nil, false
			}
			if d := mail.Runes(tks[i].text); d < e.min || d > e.max {
				return 0, nil, false
			}
			got[e.field] = number(tks[i].text)
			i++
		}
	}
	return i, got, true
}

// literalAt consumes the tokens whose texts make up lit exactly.
func literalAt(tks []token, i int, lit string) (int, bool) {
	rest := lit
	for rest != "" {
		if i >= len(tks) || !strings.HasPrefix(rest, tks[i].text) {
			return 0, false
		}
		rest = rest[len(tks[i].text):]
		i++
	}
	return i, true
}
