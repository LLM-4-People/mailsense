package locale

import (
	"math"
	"slices"
	"sync"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/locale/ngram"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Guess is the language of a text.
type Guess struct {
	Tag    string `json:"tag"`    // BCP 47 tag of a shipped language, or Undetermined
	Script string `json:"script"` // the dominant script of the text's letters ("" without letters)
	// Margin is how clearly the best language won: the score gap to the
	// runner-up per n-gram, over the largest possible gap (0..1); 1 when
	// it was the only language of its scripts; 0 when undetermined.
	Margin float64 `json:"margin"`
}

// Language identifies the language of text (statistical: the
// likelihood of the text's character n-grams under each language's
// ranked profile, among the languages written in the text's scripts;
// Hiragana and Katakana are the two scripts of one writing system, so
// either is evidence of a language written in the other). Text with fewer
// than minLetters letters, or in a script no shipped language uses, is
// Undetermined.
func Language(text string, minLetters int) Guess {
	p := mail.Scripts(text)
	g := Guess{Tag: Undetermined, Script: p.Dominant()}
	if p.Letters < max(minLetters, 1) {
		return g
	}
	m := langModel()
	cands := m.candidates(g.Script, p)
	switch len(cands) {
	case 0:
		return g
	case 1:
		g.Tag, g.Margin = m.tags[cands[0]], 1
		return g
	}
	norm := ngram.Normalize(text)
	counts := ngram.Count(norm, m.maxN)
	if len(counts) == 0 {
		return g
	}
	// Score: the log-likelihood of the text's n-grams under each profile,
	// a profile's n-gram of rank r having the Zipf weight 1/(r+1) and an
	// absent one the weight of the rank past the profile's end.
	score, cand := make([]float64, len(m.tags)), make([]bool, len(m.tags))
	absent := math.Log(1 / float64(m.size+1))
	total := 0
	for _, c := range counts {
		total += c
	}
	for _, c := range cands {
		score[c], cand[c] = absent*float64(total), true
	}
	for g, c := range counts {
		for _, e := range m.index[g] {
			if cand[e.lang] {
				score[e.lang] += float64(c) * (math.Log(1/float64(e.rank+1)) - absent)
			}
		}
	}
	m.weighLetters(norm, cands, score, absent)
	best, second := -1, -1
	for _, c := range cands {
		switch {
		case best < 0 || score[c] > score[best] || score[c] == score[best] && m.tags[c] < m.tags[best]:
			best, second = c, best
		case second < 0 || score[c] > score[second]:
			second = c
		}
	}
	g.Tag = m.tags[best]
	g.Margin = (score[best] - score[second]) / (-absent * float64(total))
	return g
}

// Languages lists the shipped language tags, sorted.
func Languages() []string {
	out := slices.Clone(langModel().tags)
	slices.Sort(out)
	return out
}

// model is langid.json indexed for scoring.
type model struct {
	size                 int
	maxN                 int
	tags                 []string
	scripts              [][]string
	letters              []map[rune]int8 // per language: letter → exemplar kind
	auxWeight, outWeight float64
	byScript             map[string][]int
	index                map[string][]entry // trigram → the languages that rank it
}

type entry struct{ lang, rank int }

// Exemplar kinds of a letter for a language (CLDR exemplar sets).
const (
	letterOutside = iota
	letterMain
	letterAuxiliary
)

// weighLetters adds the exemplar evidence: a letter outside a language's
// main exemplar set weighs against it (auxiliary: auxWeight absent
// n-grams; outside both: outWeight).
func (m *model) weighLetters(norm string, cands []int, score []float64, absent float64) {
	letters := map[rune]int{}
	for _, r := range norm {
		if unicode.IsLetter(r) {
			letters[r]++
		}
	}
	for r, n := range letters {
		for _, c := range cands {
			switch m.letters[c][r] {
			case letterAuxiliary:
				score[c] += float64(n) * m.auxWeight * absent
			case letterOutside:
				score[c] += float64(n) * m.outWeight * absent
			}
		}
	}
}

// The kana scripts and Han are the scripts of Japanese writing. Hiragana
// and Katakana are two scripts of one writing system (kana) and Japanese
// uses both; Han is shared with the Han-only languages (Chinese).
const (
	han      = "Han"
	hiragana = "Hiragana"
	katakana = "Katakana"
)

// kanaPeer is the other script of the kana writing system ("" for a
// non-kana script).
func kanaPeer(script string) string {
	switch script {
	case hiragana:
		return katakana
	case katakana:
		return hiragana
	}
	return ""
}

// candidates are the languages written in the text's dominant script whose
// scripts the text supports. Kana is one writing system in two scripts: a
// language written in Hiragana (Japanese) is also a candidate for Katakana
// text, and either kana satisfies the other, so katakana-only mail is not
// left undetermined. Han is written with kana in Japanese but is shared with
// the Han-only languages (Chinese); it is no evidence of a language by
// itself, so a kana language is identified by its kana (a bare Han text
// never makes it a candidate) and Han is required only of the languages
// written in Han alone. The shipped script list holds what the training
// text's letter shares show (Japanese's Katakana misses min_script_share);
// this is a matching rule of language identification, not training data.
func (m *model) candidates(dominant string, p mail.ScriptProfile) []int {
	present := func(s string) bool {
		if p.ByScript[s] > 0 {
			return true
		}
		peer := kanaPeer(s)
		return peer != "" && p.ByScript[peer] > 0
	}
	var out []int
	for _, i := range m.byScript[dominant] {
		kana := slices.ContainsFunc(m.scripts[i], func(s string) bool { return kanaPeer(s) != "" })
		ok := true
		for _, s := range m.scripts[i] {
			if s == han && kana {
				continue
			}
			ok = ok && present(s)
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

var langModel = sync.OnceValue(func() *model {
	d := langidData()
	m := &model{size: d.ProfileSize, maxN: d.NgramMax, auxWeight: d.AuxWeight, outWeight: d.OutWeight, byScript: map[string][]int{}, index: map[string][]entry{}}
	for i, l := range d.Languages {
		m.tags, m.scripts = append(m.tags, l.Tag), append(m.scripts, l.Scripts)
		set := map[rune]int8{}
		for _, r := range l.Auxiliary {
			set[r] = letterAuxiliary
		}
		for _, r := range l.Letters {
			set[r] = letterMain
		}
		m.letters = append(m.letters, set)
		for _, s := range l.Scripts {
			m.byScript[s] = append(m.byScript[s], i)
			// A kana language competes for the other kana script too (the
			// text's dominant script stays the text's own; only the
			// candidate lookup is shared).
			if peer := kanaPeer(s); peer != "" && !slices.Contains(l.Scripts, peer) {
				m.byScript[peer] = append(m.byScript[peer], i)
			}
		}
		for r, t := range l.Profile {
			m.index[t] = append(m.index[t], entry{lang: i, rank: r})
		}
	}
	return m
})
