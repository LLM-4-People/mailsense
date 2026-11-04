package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// supplemental is what the generator reads from the supplemental data.
type supplemental struct {
	parents    map[string]string // locale → parent (parentLocales)
	likely     map[string]string // language → its likely full tag (und_ forms excluded)
	aliases    map[string]string // deprecated or individual language → its CLDR language (languageAlias)
	currencies []string          // ISO 4217 codes in use as tender in some region
	// official are, per language id (as territoryInfo writes it: fr,
	// zh_Hant), the territories and the status it has there
	official map[string][]regional
	// calendarPrefs are the calendars in use per territory, preferred
	// first (calendarPreferenceData; "001" for any other territory)
	calendarPrefs map[string][]string
	calendars     map[string]calendarInfo // calendarData by type
}

// regional is one territory where a language has an official status.
type regional struct{ territory, status string }

// calendarInfo is one calendar of calendarData: its system (solar, lunar,
// lunisolar, other) and the start of its current era (the latest one
// with a start date: the era years are counted in).
type calendarInfo struct {
	System   string `json:"system"`
	EraStart string `json:"era_start"`
}

func readSupplemental(src string) (*supplemental, error) {
	var doc struct {
		Parents []struct {
			Parent  string `xml:"parent,attr"`
			Locales string `xml:"locales,attr"`
		} `xml:"parentLocales>parentLocale"`
		Regions []struct {
			Currencies []struct {
				Code   string `xml:"iso4217,attr"`
				To     string `xml:"to,attr"`
				Tender string `xml:"tender,attr"`
			} `xml:"currency"`
		} `xml:"currencyData>region"`
		Territories []struct {
			Type      string `xml:"type,attr"`
			Languages []struct {
				Type   string `xml:"type,attr"`
				Status string `xml:"officialStatus,attr"`
			} `xml:"languagePopulation"`
		} `xml:"territoryInfo>territory"`
		Preferences []struct {
			Territories string `xml:"territories,attr"`
			Ordering    string `xml:"ordering,attr"`
		} `xml:"calendarPreferenceData>calendarPreference"`
		Calendars []struct {
			Type   string `xml:"type,attr"`
			System struct {
				Type string `xml:"type,attr"`
			} `xml:"calendarSystem"`
			Eras []struct {
				Start string `xml:"start,attr"`
			} `xml:"eras>era"`
		} `xml:"calendarData>calendar"`
	}
	if err := decodeXML(filepath.Join(src, cldrSupplemental), &doc); err != nil {
		return nil, err
	}
	s := &supplemental{parents: map[string]string{}, likely: map[string]string{}, aliases: map[string]string{},
		official: map[string][]regional{}, calendarPrefs: map[string][]string{}, calendars: map[string]calendarInfo{}}
	for _, t := range doc.Territories {
		for _, l := range t.Languages {
			if l.Status != "" {
				s.official[l.Type] = append(s.official[l.Type], regional{territory: t.Type, status: l.Status})
			}
		}
	}
	for _, p := range doc.Preferences {
		for _, t := range strings.Fields(p.Territories) {
			s.calendarPrefs[t] = strings.Fields(p.Ordering)
		}
	}
	for _, c := range doc.Calendars {
		info := s.calendars[c.Type] // japanese appears twice: its eras and its system merge
		if c.System.Type != "" {
			info.System = c.System.Type
		}
		for _, e := range c.Eras {
			if e.Start != "" && laterDate(e.Start, info.EraStart) {
				info.EraStart = e.Start
			}
		}
		s.calendars[c.Type] = info
	}
	for _, p := range doc.Parents {
		for _, l := range strings.Fields(p.Locales) {
			s.parents[l] = p.Parent
		}
	}
	for _, r := range doc.Regions {
		for _, c := range r.Currencies {
			if c.To == "" && c.Tender != "false" && !slices.Contains(s.currencies, c.Code) {
				s.currencies = append(s.currencies, c.Code)
			}
		}
	}
	slices.Sort(s.currencies)
	if err := s.readLikely(src); err != nil {
		return nil, err
	}
	return s, s.readAliases(src)
}

// readLikely reads the likely subtags of the languages.
func (s *supplemental) readLikely(src string) error {
	var likely struct {
		Tags []struct {
			From string `xml:"from,attr"`
			To   string `xml:"to,attr"`
		} `xml:"likelySubtags>likelySubtag"`
	}
	if err := decodeXML(filepath.Join(src, cldrLikely), &likely); err != nil {
		return err
	}
	for _, t := range likely.Tags {
		if !strings.HasPrefix(t.From, "und") {
			s.likely[t.From] = t.To
		}
	}
	return nil
}

// readAliases reads the language aliases that replace one language by one.
func (s *supplemental) readAliases(src string) error {
	var meta struct {
		Aliases []struct {
			Type        string `xml:"type,attr"`
			Replacement string `xml:"replacement,attr"`
		} `xml:"metadata>alias>languageAlias"`
	}
	if err := decodeXML(filepath.Join(src, cldrMetadata), &meta); err != nil {
		return err
	}
	for _, a := range meta.Aliases {
		if !strings.ContainsAny(a.Type, "_-") && a.Replacement != "" && !strings.ContainsAny(a.Replacement, "_- ") {
			s.aliases[a.Type] = a.Replacement
		}
	}
	return nil
}

func decodeXML(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// currencySymbols keeps the symbols of codes in use, each with its codes
// sorted; a symbol of ASCII letters only is left out when the parameters
// say so (its code is matched instead), and so is a symbol equal to a code.
func currencySymbols(symbols map[string][]string, codes []string, excludeASCII bool) map[string][]string {
	out := map[string][]string{}
	for sym, cs := range symbols {
		if slices.Contains(codes, sym) || (excludeASCII && onlyASCIILetters(sym)) {
			continue
		}
		var keep []string
		for _, c := range cs {
			if slices.Contains(codes, c) {
				keep = append(keep, c)
			}
		}
		if len(keep) > 0 {
			slices.Sort(keep)
			out[sym] = keep
		}
	}
	return out
}

func onlyASCIILetters(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.') {
			return false
		}
	}
	return true
}

// laterDate compares CLDR era dates (y-m-d, y may be negative): a is later than b ("" is the earliest).
func laterDate(a, b string) bool {
	if b == "" {
		return true
	}
	pa, pb := datePartsOf(a), datePartsOf(b)
	return slices.Compare(pa, pb) > 0
}

// datePartsOf reads a CLDR era date "-542-01-01" / "1989-1-8" as [y m d].
func datePartsOf(d string) []int {
	neg := strings.HasPrefix(d, "-")
	f := strings.Split(strings.TrimPrefix(d, "-"), "-")
	out := make([]int, 3)
	for i := 0; i < len(f) && i < 3; i++ {
		out[i], _ = strconv.Atoi(f[i])
	}
	if neg {
		out[0] = -out[0]
	}
	return out
}
