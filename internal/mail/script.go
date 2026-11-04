package mail

import (
	_ "embed"
	"maps"
	"slices"
	"sort"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed scripts.json
var scriptsJSON []byte

type scriptRules struct {
	Doc            string   `json:"_doc"`
	Source         string   `json:"_source"`
	Spaceless      []string `json:"spaceless"`
	SubstringExtra []string `json:"substring_extra"`
}

// scripts is the script data (scripts.json) and a per-rune lookup of the
// Basic Multilingual Plane, built once (TestScriptRulesAreValid guards the
// file; a broken file is a build defect).
var scripts = sync.OnceValue(func() *scriptData {
	var r scriptRules
	if err := jsonx.DecodeStrict(scriptsJSON, &r); err != nil {
		panic("mail: scripts.json: " + err.Error())
	}
	d := &scriptData{spaceless: map[string]bool{}, substring: map[string]bool{}}
	for _, s := range r.Spaceless {
		d.spaceless[s], d.substring[s] = true, true
	}
	for _, s := range r.SubstringExtra {
		d.substring[s] = true
	}
	for name := range unicode.Scripts {
		d.names = append(d.names, name)
	}
	sort.Strings(d.names)
	d.names = append([]string{""}, d.names...) // index 0: no script
	for r := rune(0); r <= 0xFFFF; r++ {
		d.bmp[r] = d.slowIndex(r)
	}
	return d
})

type scriptData struct {
	spaceless map[string]bool
	substring map[string]bool
	names     []string
	bmp       [0x10000]uint8
}

func (d *scriptData) slowIndex(r rune) uint8 {
	for i, name := range d.names[1:] {
		if unicode.Is(unicode.Scripts[name], r) {
			return uint8(i + 1)
		}
	}
	return 0
}

// ScriptOf is the Unicode script of r ("" for none), e.g. "Latin", "Han".
func ScriptOf(r rune) string {
	d := scripts()
	if r >= 0 && r <= 0xFFFF {
		return d.names[d.bmp[r]]
	}
	return d.names[d.slowIndex(r)]
}

// Spaceless reports whether a script writes words without spaces.
func Spaceless(script string) bool { return scripts().spaceless[script] }

// NeedsSubstringSearch reports whether s has a letter of a script that
// is searched by substring (scripts.json: the spaceless scripts and
// substring_extra).
func NeedsSubstringSearch(s string) bool {
	d := scripts()
	for _, r := range s {
		if r >= utf8.RuneSelf && unicode.IsLetter(r) && d.substring[ScriptOf(r)] {
			return true
		}
	}
	return false
}

// ScriptProfile counts the letters of a text per script.
type ScriptProfile struct {
	Letters  int            `json:"letters"`
	Cased    int            `json:"cased"` // letters with an upper/lower case distinction
	ByScript map[string]int `json:"by_script"`
}

// Scripts profiles the letters of s.
func Scripts(s string) ScriptProfile {
	p := ScriptProfile{ByScript: map[string]int{}}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		p.Letters++
		if unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) {
			p.Cased++
		}
		p.ByScript[ScriptOf(r)]++
	}
	return p
}

// Dominant is the script with the most letters ("" without letters; ties
// break on the name, so the result never depends on map order).
func (p ScriptProfile) Dominant() string {
	best, n := "", 0
	for _, name := range slices.Sorted(maps.Keys(p.ByScript)) {
		if p.ByScript[name] > n {
			best, n = name, p.ByScript[name]
		}
	}
	return best
}

// SpacelessShare is the share of letters in spaceless scripts.
func (p ScriptProfile) SpacelessShare() float64 {
	if p.Letters == 0 {
		return 0
	}
	n := 0
	for name, c := range p.ByScript {
		if Spaceless(name) {
			n += c
		}
	}
	return float64(n) / float64(p.Letters)
}
