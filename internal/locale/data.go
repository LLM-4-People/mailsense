package locale

import (
	_ "embed"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

var (
	//go:embed langid.json
	langidJSON []byte
	//go:embed locales.json
	localesJSON []byte
	//go:embed currency.json
	currencyJSON []byte
	//go:embed confusables.json
	confusablesJSON []byte
)

// Undetermined is the language tag of text whose language is not known
// (BCP 47 "und"); its locale holds only the numeric patterns of CLDR root.
const Undetermined = "und"

// load decodes one embedded file strictly; a broken file is a build
// defect (TestDataLoads).
func load[T any](name string, raw []byte) func() *T {
	return sync.OnceValue(func() *T {
		v := new(T)
		if err := jsonx.DecodeStrict(raw, v); err != nil {
			panic("locale: " + name + ": " + err.Error())
		}
		return v
	})
}

type langidDoc struct {
	Doc         string  `json:"_doc"`
	Source      string  `json:"_source"`
	NgramMax    int     `json:"ngram_max"`
	ProfileSize int     `json:"profile_size"`
	AuxWeight   float64 `json:"exemplar_auxiliary_weight"`
	OutWeight   float64 `json:"exemplar_outside_weight"`
	Languages   []struct {
		Tag       string   `json:"tag"`
		Locale    string   `json:"locale"`
		Texts     []string `json:"udhr"`
		Scripts   []string `json:"scripts"`
		Letters   string   `json:"letters"`
		Auxiliary string   `json:"auxiliary"`
		Profile   []string `json:"profile"`
	} `json:"languages"`
}

type localeDoc struct {
	Months       [12][]string `json:"months"`
	Weekdays     [7][]string  `json:"weekdays"`
	WeekdaysWide [7][]string  `json:"weekdays_wide"`
	DayPeriods   [2][]string  `json:"day_periods"`
	DatePatterns []string     `json:"date_patterns"`
	TimePatterns []string     `json:"time_patterns"`
	Decimals     []string     `json:"decimals"`
	Groups       []string     `json:"groups"`
	Calendars    []string     `json:"calendars"`
}

type calendarDoc struct {
	System          string `json:"system"`
	EraStart        string `json:"era_start"`
	GregorianMonths bool   `json:"gregorian_months"`
}

type localesDoc struct {
	Doc       string                 `json:"_doc"`
	Source    string                 `json:"_source"`
	Calendars map[string]calendarDoc `json:"calendars"`
	Locales   map[string]localeDoc   `json:"locales"`
}

type currencyDoc struct {
	Doc     string              `json:"_doc"`
	Source  string              `json:"_source"`
	Codes   []string            `json:"codes"`
	Symbols map[string][]string `json:"symbols"`
}

type confusablesDoc struct {
	Doc    string            `json:"_doc"`
	Source string            `json:"_source"`
	Map    map[string]string `json:"map"`
}

var (
	langidData      = load[langidDoc]("langid.json", langidJSON)
	localesData     = load[localesDoc]("locales.json", localesJSON)
	currencyData    = load[currencyDoc]("currency.json", currencyJSON)
	confusablesData = load[confusablesDoc]("confusables.json", confusablesJSON)
)

// DataDigest is the digest of what the shipped locale data means
// (digest.Content: every file, in a fixed order; documentation, provenance
// and formatting aside): views computed from it (evidence values) are
// versioned with it, so regenerated data recomputes them and a doc edit
// changes nothing.
var DataDigest = sync.OnceValue(func() string {
	return digest.Content(langidJSON, localesJSON, currencyJSON, confusablesJSON)
})
