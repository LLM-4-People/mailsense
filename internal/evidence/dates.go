package evidence

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/locale"
)

// dateSettings are the settings the date signals read.
var dateSettings = []string{clock.SettingZone, settingMinLetters, settingLangChars, settingMaxDates}

// dateDays are the dates a message's own text names (full dates or, with
// weekdays, the weekdays named alone), each placed from the author's day
// (Input.AuthorDay: the civil date the author meant) and counted in days
// from the message's day in the owner's zone (time.zone): the resolved
// distances — at most evidence.dates.max of them (the nearest), sorted —
// and the reasons of the mentions that have no exact date (an ambiguous
// numeric order, a calendar without an exact conversion: locale.Resolve);
// ok is false for an undated message.
func dateDays(in *Input, weekdays bool) (dates, bool) {
	day, ok := in.Day()
	if !ok {
		return dates{}, false
	}
	var days []int
	var unresolved []string
	author, _ := in.AuthorDay()
	l := in.Locale()
	for _, m := range in.Mentions().Dates {
		if (m.Month == 0) != weekdays {
			continue
		}
		d, why, ok := l.Resolve(m, author)
		if !ok {
			unresolved = append(unresolved, why)
			continue
		}
		days = append(days, locale.Days(d, day))
	}
	slices.SortFunc(days, func(a, b int) int {
		if abs(a) != abs(b) {
			return abs(a) - abs(b)
		}
		return a - b
	})
	out := dates{all: len(days), unresolved: unresolved}
	out.days = days[:min(len(days), in.env.set.MaxDates)]
	slices.Sort(out.days)
	return out, true
}

// dates are the date mentions of a message's own text: the distances of
// the resolved ones (the nearest evidence.dates.max, sorted), how many
// resolved before that cap, and the reasons of the unresolved ones.
type dates struct {
	days       []int
	all        int
	unresolved []string
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// datesOf answers a date signal over the full dates (weekdays: the
// weekdays named alone): NA for an undated message.
func datesOf(in *Input, weekdays bool, fn func(dates) Value) Value {
	ds, ok := dateDays(in, weekdays)
	if !ok {
		return NotApplicable(ReasonNoDate)
	}
	return fn(ds).Cut(in.BodyCut())
}
