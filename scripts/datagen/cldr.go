package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// inherit is CLDR's marker for "same as the inherited value".
const inherit = "↑↑↑"

// ldml is the part of a CLDR main/<locale>.xml file the generator reads.
type ldml struct {
	Exemplars []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"characters>exemplarCharacters"`
	Calendars       []calendar `xml:"dates>calendars>calendar"`
	NumberingSystem string     `xml:"numbers>defaultNumberingSystem"`
	Symbols         []struct {
		System  string `xml:"numberSystem,attr"`
		Decimal string `xml:"decimal"`
		Group   string `xml:"group"`
		Alias   struct {
			Path string `xml:"path,attr"`
		} `xml:"alias"` // root: a native numbering system's symbols are latn's
	} `xml:"numbers>symbols"`
	Currencies []struct {
		Type    string `xml:"type,attr"`
		Symbols []struct {
			Alt   string `xml:"alt,attr"`
			Value string `xml:",chardata"`
		} `xml:"symbol"`
	} `xml:"numbers>currencies>currency"`
}

type calendar struct {
	Type        string `xml:"type,attr"`
	MonthsAlias struct {
		Path string `xml:"path,attr"`
	} `xml:"months>alias"` // root: a calendar whose months are another's (buddhist → gregorian)
	Months     []context `xml:"months>monthContext"`
	Days       []context `xml:"days>dayContext"`
	DayPeriods []context `xml:"dayPeriods>dayPeriodContext"`
	Dates      []length  `xml:"dateFormats>dateFormatLength"`
	Times      []length  `xml:"timeFormats>timeFormatLength"`
	Items      []struct {
		ID    string `xml:"id,attr"`
		Alt   string `xml:"alt,attr"`
		Value string `xml:",chardata"`
	} `xml:"dateTimeFormats>availableFormats>dateFormatItem"`
}

// context is a monthContext, dayContext or dayPeriodContext.
type context struct {
	Type   string `xml:"type,attr"`
	Widths []struct {
		Type  string `xml:"type,attr"`
		Items []struct {
			Type  string `xml:"type,attr"`
			Alt   string `xml:"alt,attr"`
			Value string `xml:",chardata"`
		} `xml:",any"`
	} `xml:",any"`
}

// length is a dateFormatLength or timeFormatLength.
type length struct {
	Type string   `xml:"type,attr"`
	Date []string `xml:"dateFormat>pattern"`
	Time []string `xml:"timeFormat>pattern"`
}

// cldrLocale is one shipped locale of locales.json: a language with its
// regional locales joined (patterns, names and separators of all of them).
type cldrLocale struct {
	Months       [12][]string `json:"months"`        // month 1..12: its names (format and stand-alone, abbreviated and wide)
	Weekdays     [7][]string  `json:"weekdays"`      // Sunday..Saturday
	WeekdaysWide [7][]string  `json:"weekdays_wide"` // the wide names only (a weekday named alone in a text)
	DayPeriods   [2][]string  `json:"day_periods"`   // am, pm
	DatePatterns []string     `json:"date_patterns"`
	TimePatterns []string     `json:"time_patterns"`
	Decimals     []string     `json:"decimals"`  // the decimal separators of the language and its regional locales (the language's first)
	Groups       []string     `json:"groups"`    // their grouping separators (the language's first)
	Calendars    []string     `json:"calendars"` // the calendars of its likely territory, preferred first
}

var (
	dayTypes    = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	periodTypes = []string{"am", "pm"}
	names       = []string{"abbreviated", "wide"} // narrow forms are single letters, never names
)

// readLDML decodes main/<id>.xml.
func readLDML(dir, id string) (*ldml, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "main", id+".xml"))
	if err != nil {
		return nil, err
	}
	var l ldml
	if err := xml.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("%s.xml: %w", id, err)
	}
	return &l, nil
}

// chain is the inheritance chain of a locale: itself, its parents
// (parentLocales, else the id truncated), root.
func chain(id string, parents map[string]string) []string {
	out := []string{id}
	for id != "root" {
		if p, ok := parents[id]; ok {
			id = p
		} else if i := strings.LastIndexByte(id, '_'); i > 0 {
			id = id[:i]
		} else {
			id = "root"
		}
		out = append(out, id)
	}
	return out
}

// buildLocale resolves a locale through its chain (CLDR inheritance per
// item: the first value found wins; the inheritance marker is no value).
func buildLocale(files []*ldml, p localeParams) cldrLocale {
	var out cldrLocale
	dates, times := map[string]string{}, map[string]string{}
	for _, l := range files {
		g := gregorian(l)
		if g == nil {
			continue
		}
		day := func(t string) int { return slices.Index(dayTypes, t) }
		addNames(out.Months[:], g.Months, func(t string) int { n, _ := strconv.Atoi(t); return n - 1 }, names...)
		addNames(out.Weekdays[:], g.Days, day, names...)
		addNames(out.WeekdaysWide[:], g.Days, day, "wide")
		addNames(out.DayPeriods[:], g.DayPeriods, func(t string) int { return slices.Index(periodTypes, t) }, names...)
		addPatterns(dates, g.Dates, func(x length) []string { return x.Date }, p.DateLengths)
		addPatterns(times, g.Times, func(x length) []string { return x.Time }, p.TimeLengths)
		for _, it := range g.Items {
			v := strings.TrimSpace(it.Value)
			key := "item:" + it.ID
			if it.Alt != "" || v == "" || v == inherit {
				continue
			}
			if slices.Contains(p.DateSkeletons, it.ID) && dates[key] == "" {
				dates[key] = v
			}
			if slices.Contains(p.TimeSkeletons, it.ID) && times[key] == "" {
				times[key] = v
			}
		}
	}
	out.DatePatterns, out.TimePatterns = shippable(dates), shippable(times)
	d, g := separators(files)
	out.Decimals, out.Groups = nonEmpty(d), nonEmpty(g)
	return out
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// join adds a regional locale to its language: its patterns, the names
// the language lacks and its separators.
func (l *cldrLocale) join(r cldrLocale) {
	l.DatePatterns = slices.Sorted(slices.Values(union(l.DatePatterns, r.DatePatterns)))
	l.TimePatterns = slices.Sorted(slices.Values(union(l.TimePatterns, r.TimePatterns)))
	for i := range l.Months {
		l.Months[i] = union(l.Months[i], r.Months[i])
	}
	for i := range l.Weekdays {
		l.Weekdays[i] = union(l.Weekdays[i], r.Weekdays[i])
		l.WeekdaysWide[i] = union(l.WeekdaysWide[i], r.WeekdaysWide[i])
	}
	for i := range l.DayPeriods {
		l.DayPeriods[i] = union(l.DayPeriods[i], r.DayPeriods[i])
	}
	l.Decimals, l.Groups = union(l.Decimals, r.Decimals), union(l.Groups, r.Groups)
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

func gregorian(l *ldml) *calendar {
	for i := range l.Calendars {
		if l.Calendars[i].Type == "gregorian" {
			return &l.Calendars[i]
		}
	}
	return nil
}

// addNames fills the months (weekdays, day periods) that have no names
// yet with this file's names of the given widths (format and
// stand-alone): CLDR inheritance per item, so root's placeholders never
// join a locale's own names.
func addNames(dst [][]string, ctxs []context, index func(string) int, widths ...string) {
	own := make([][]string, len(dst))
	for _, c := range ctxs {
		for _, w := range c.Widths {
			if !slices.Contains(widths, w.Type) {
				continue
			}
			for _, it := range w.Items {
				i, v := index(it.Type), strings.TrimSpace(it.Value)
				if i < 0 || i >= len(dst) || v == "" || v == inherit || slices.Contains(own[i], v) {
					continue
				}
				own[i] = append(own[i], v)
			}
		}
	}
	for i := range dst {
		if len(dst[i]) == 0 {
			dst[i] = own[i]
		}
	}
}

// separators are the decimal and grouping separators of the default
// numbering system, through the chain; a system whose symbols are an
// alias of another's (root: beng, deva, mymr → latn) is read as that one.
func separators(files []*ldml) (decimal, group string) {
	system := ""
	for _, l := range files {
		if system == "" && l.NumberingSystem != "" && l.NumberingSystem != inherit {
			system = l.NumberingSystem
		}
	}
	for seen := map[string]bool{}; system != "" && !seen[system]; {
		seen[system] = true
		next := ""
		for _, l := range files {
			for _, s := range l.Symbols {
				if s.System != system {
					continue
				}
				if decimal == "" && s.Decimal != "" && s.Decimal != inherit {
					decimal = s.Decimal
				}
				if group == "" && s.Group != "" && s.Group != inherit {
					group = s.Group
				}
				if next == "" {
					next = aliasedSystem(s.Alias.Path)
				}
			}
		}
		if decimal != "" && group != "" {
			break
		}
		system = next
	}
	return decimal, group
}

// aliasedSystem is the numbering system a symbols alias names
// ("../symbols[@numberSystem='latn']" → latn).
func aliasedSystem(path string) string {
	const key = "@numberSystem='"
	i := strings.Index(path, key)
	if i < 0 {
		return ""
	}
	rest := path[i+len(key):]
	if j := strings.IndexByte(rest, '\''); j >= 0 {
		return rest[:j]
	}
	return ""
}

// exemplars are a locale's main and auxiliary exemplar letters (the
// first set found through the chain), lower-case single characters.
func exemplars(files []*ldml) (main, aux string) {
	for _, l := range files {
		for _, e := range l.Exemplars {
			v := strings.TrimSpace(e.Value)
			if v == "" || v == inherit {
				continue
			}
			switch {
			case e.Type == "" && main == "":
				main = unicodeSet(v)
			case e.Type == "auxiliary" && aux == "":
				aux = unicodeSet(v)
			}
		}
	}
	return main, aux
}

// unicodeSet lists the single characters of a CLDR exemplar UnicodeSet
// ("[a b c-e {ch} \\-]"): ranges expanded, escapes read, multi-character
// sequences in braces left out (they never decide a single letter).
func unicodeSet(v string) string {
	rs := []rune(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"))
	var out []rune
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; {
		case r == '{':
			for i < len(rs) && rs[i] != '}' {
				i++
			}
		case r == ' ':
		case r == '\\' && i+1 < len(rs):
			i++
			out = append(out, rs[i])
		case r == '-' && len(out) > 0 && i+1 < len(rs) && rs[i+1] != ' ':
			for c := out[len(out)-1] + 1; c <= rs[i+1]; c++ {
				out = append(out, c)
			}
			i++
		default:
			out = append(out, r)
		}
	}
	var b strings.Builder
	for _, r := range out {
		if unicode.IsLetter(r) || unicode.IsMark(r) {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}
