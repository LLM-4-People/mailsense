package feedback

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Volume is when mail arrives and leaves, in the owner's zone. Per-month
// counts are the behaviour profile's (threads.Profile.Months).
type Volume struct {
	Weekday     map[string]int `json:"weekday"`       // all mail
	WeekdayIn   map[string]int `json:"weekday_in"`    // received only
	Hour        map[int]int    `json:"hour"`          // received, local hour
	WeekOfMonth map[string]int `json:"week_of_month"` // W1..W5
	Month       map[string]int `json:"month"`
	// Season counts by meteorological season of the owner's hemisphere
	// (calendar.hemisphere); nil when it is not set: seasons are never
	// assumed (the tropics have none, the south's are shifted).
	Season map[string]int `json:"season"`
	// YearOverYear compares received mail on the months present in both
	// years only (an absent month is "no data yet", not zero).
	YearOverYear []YearChange `json:"year_over_year"`
	// Fluctuation of monthly received mail (needs ≥ 2 months).
	StdDev *float64 `json:"stddev,omitempty"`
	CV     *float64 `json:"cv,omitempty"`
}

// YearChange is received mail of one year against the previous one.
type YearChange struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Months     int      `json:"months"`
	Previous   int      `json:"previous"`
	Current    int      `json:"current"`
	ChangeRate *float64 `json:"change_rate,omitempty"`
}

// northernSeasons are the meteorological seasons by month in the
// northern hemisphere; the southern ones are six months apart.
var northernSeasons = [12]string{"winter", "winter", "spring", "spring", "spring", "summer", "summer", "summer", "autumn", "autumn", "autumn", "winter"}

// Hemispheres (calendar.hemisphere values).
const (
	HemisphereNorth = "north"
	HemisphereSouth = "south"
)

func season(m time.Month, hemisphere string) string {
	i := int(m) - 1
	if hemisphere == HemisphereSouth {
		i = (i + 6) % 12
	}
	return northernSeasons[i]
}

// Patterns computes the volume patterns of the analysed mail; seasons
// only for a known hemisphere ("" leaves them out).
func Patterns(a *threads.Analysis, b *threads.Profile, zone *time.Location, hemisphere string) Volume {
	v := Volume{Weekday: map[string]int{}, WeekdayIn: map[string]int{}, Hour: map[int]int{}, WeekOfMonth: map[string]int{},
		Month: map[string]int{}}
	if hemisphere != "" {
		v.Season = map[string]int{}
	}
	for i := range a.Messages {
		m := &a.Messages[i]
		if !m.Dated() {
			continue
		}
		at := m.At.In(zone)
		v.Weekday[at.Weekday().String()]++
		v.WeekOfMonth["W"+strconv.Itoa((at.Day()-1)/7+1)]++
		v.Month[at.Month().String()]++
		if v.Season != nil {
			v.Season[season(at.Month(), hemisphere)]++
		}
		if m.Role == mail.RoleInbox {
			v.WeekdayIn[at.Weekday().String()]++
			v.Hour[at.Hour()]++
		}
	}
	v.yearOverYear(b.Months)
	v.fluctuation(b.Months)
	return v
}

// volumePlaces is how the monthly-volume dispersion is rounded (half-up). It
// is a count statistic, not a reply latency: it deliberately does not borrow
// threads.MinutePlaces.
const volumePlaces = 1

func (v *Volume) yearOverYear(months map[string]threads.Volume) {
	byYear := map[string]map[string]int{}
	for month, c := range months {
		y, m := mail.SplitMonth(month)
		if byYear[y] == nil {
			byYear[y] = map[string]int{}
		}
		byYear[y][m] += c.Received
	}
	years := slices.Sorted(maps.Keys(byYear))
	for i := 1; i < len(years); i++ {
		ch := YearChange{From: years[i-1], To: years[i]}
		for m, cur := range byYear[years[i]] {
			if prev, ok := byYear[years[i-1]][m]; ok {
				ch.Months++
				ch.Previous += prev
				ch.Current += cur
			}
		}
		if ch.Previous > 0 {
			ch.ChangeRate = ptr(round3(float64(ch.Current-ch.Previous) / float64(ch.Previous)))
		}
		if ch.Months > 0 {
			v.YearOverYear = append(v.YearOverYear, ch)
		}
	}
}

func (v *Volume) fluctuation(months map[string]threads.Volume) {
	var monthly []float64
	for _, c := range months {
		monthly = append(monthly, float64(c.Received))
	}
	if len(monthly) < 2 {
		return
	}
	sd := mathx.PStdev(monthly)
	v.StdDev = ptr(mathx.Round(sd, volumePlaces))
	if mean, _ := mathx.Mean(monthly); mean > 0 {
		v.CV = ptr(round3(sd / mean))
	}
}
