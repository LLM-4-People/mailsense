package locale

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Token kinds of the date, time and amount matchers.
const (
	tokNumber = iota + 1 // a run of decimal digits (any script)
	tokWord              // a run of letters and marks of a script written with spaces, or ONE letter of a spaceless script
	tokSpace             // a run of white space
	tokMark              // one other character (punctuation, a symbol)
)

type token struct {
	kind     int
	text     string
	from, to int // byte offsets in the text
}

// tokens splits a text for the matchers. Spaceless scripts (Han, Kana,
// Thai…) give one token per letter, so 2026年10月20日 is number, 年,
// number, 月, number, 日.
func tokens(t string) []token {
	var out []token
	for i := 0; i < len(t); {
		r, n := utf8.DecodeRuneInString(t[i:])
		kind, j := tokMark, i+n
		switch {
		case unicode.IsDigit(r):
			kind, j = tokNumber, runWhile(t, j, unicode.IsDigit)
		case mail.IsSpace(r):
			kind, j = tokSpace, runWhile(t, j, mail.IsSpace)
		case (unicode.IsLetter(r) || unicode.IsMark(r)) && mail.Spaceless(mail.ScriptOf(r)):
			kind = tokWord
		case unicode.IsLetter(r) || unicode.IsMark(r):
			kind, j = tokWord, runWhile(t, j, func(r rune) bool {
				return (unicode.IsLetter(r) || unicode.IsMark(r)) && !mail.Spaceless(mail.ScriptOf(r))
			})
		}
		out = append(out, token{kind: kind, text: t[i:j], from: i, to: j})
		i = j
	}
	return out
}

func runWhile(t string, i int, ok func(rune) bool) int {
	for i < len(t) {
		r, n := utf8.DecodeRuneInString(t[i:])
		if !ok(r) {
			break
		}
		i += n
	}
	return i
}

// space skips one white-space token of exactly one character. A run of two
// spaces is one token of two characters, so it is not the single separator a
// pattern or an amount allows: "5 Feb" matches, "5  Feb" does not.
func space(tks []token, i int) int {
	if i < len(tks) && tks[i].kind == tokSpace && mail.Runes(tks[i].text) == 1 {
		return i + 1
	}
	return i
}

// joins reports whether a token glued to a match takes it out of
// standalone use: a number, or a word of a script written with spaces
// (a date inside a Japanese sentence touches kana; one inside a Latin
// word does not stand alone).
func joins(tk token) bool {
	if tk.kind == tokNumber {
		return true
	}
	if tk.kind != tokWord {
		return false
	}
	r, _ := utf8.DecodeRuneInString(tk.text)
	return !mail.Spaceless(mail.ScriptOf(r))
}

// standaloneAt: the tokens right before i and right after j (exclusive)
// do not join a date or a time. A letter next to a digit is a token
// boundary in every script (a prefix letter glued to a number: ב20,
// 2026-10-20T08:00, 20h): only a word glued to a word of the match joins it.
func standaloneAt(tks []token, i, j int) bool {
	return (i == 0 || !gluedTo(tks[i-1], tks[i])) && (j >= len(tks) || !gluedTo(tks[j], tks[j-1]))
}

// isolatedAt: no number or word of a spaced script touches an amount
// (EUR inside a word is no code; a number glued to letters is part of a
// reference, measured on the fixture: the letter–digit boundary of dates
// would add amounts that are none).
func isolatedAt(tks []token, i, j int) bool {
	return (i == 0 || !joins(tks[i-1])) && (j >= len(tks) || !joins(tks[j]))
}

// gluedTo reports whether an outer token takes the match's edge token out
// of standalone use: a word of a script written with spaces glued to a
// word (a number glued to a number is one token).
func gluedTo(outer, edge token) bool {
	return joins(outer) && edge.kind != tokNumber && outer.kind != tokNumber
}

// seqKey is the key of a token sequence (names of several tokens: 星期一,
// tháng 10): the texts without the white space, joined.
func seqKey(tks []token) string {
	var parts []string
	for _, tk := range tks {
		if tk.kind != tokSpace {
			parts = append(parts, tk.text)
		}
	}
	return strings.Join(parts, "\x00")
}

// phrases maps token sequences (names, symbols) to their values.
type phrases struct {
	byKey   map[string]int
	longest int             // tokens of the longest sequence, white space included
	first   map[string]bool // the first token's text of every sequence
}

func newPhrases() *phrases { return &phrases{byKey: map[string]int{}, first: map[string]bool{}} }

func (p *phrases) add(text string, v int) {
	tks := tokens(text)
	if len(tks) == 0 {
		return
	}
	p.byKey[seqKey(tks)] = v
	p.longest = max(p.longest, len(tks))
	p.first[tks[0].text] = true
}

// prefixed is the set with a literal glued before every sequence's first
// token (a word written right before the names, without white space).
func (p *phrases) prefixed(lit string) *phrases {
	out := &phrases{byKey: make(map[string]int, len(p.byKey)), longest: p.longest, first: map[string]bool{}}
	for k, v := range p.byKey {
		out.byKey[lit+k] = v
	}
	for f := range p.first {
		out.first[lit+f] = true
	}
	return out
}

// at matches the longest sequence starting at token i: its value and the
// token after it.
func (p *phrases) at(tks []token, i int) (value, end int, ok bool) {
	if i >= len(tks) || !p.first[tks[i].text] {
		return 0, 0, false
	}
	for n := min(p.longest, len(tks)-i); n >= 1; n-- {
		if tks[i+n-1].kind == tokSpace {
			continue
		}
		if v, found := p.byKey[seqKey(tks[i:i+n])]; found {
			return v, i + n, true
		}
	}
	return 0, 0, false
}
