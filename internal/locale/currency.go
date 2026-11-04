package locale

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Amount is one amount of money written in a text.
type Amount struct {
	// Codes are the ISO 4217 codes the currency can be: one for a code or
	// an unambiguous symbol, several for a shared symbol ($, kr…).
	Codes []string `json:"codes"`
	// Value is the amount read with the locale's separators; Known is
	// false when the separators leave it ambiguous (1.234 without a
	// language: one thousand or one point two).
	Value float64 `json:"value"`
	Known bool    `json:"known"`
}

// currencies is currency.json compiled for the token matcher.
type currencies struct {
	codes   map[string]bool
	symbols *phrases   // a symbol (its compatibility view) → its index in codesOf
	codesOf [][]string // per symbol, its ISO codes
}

var currencyModel = sync.OnceValue(func() *currencies {
	d := currencyData()
	c := &currencies{codes: map[string]bool{}, symbols: newPhrases()}
	for _, code := range d.Codes {
		c.codes[code] = true
	}
	syms := make([]string, 0, len(d.Symbols))
	for sym := range d.Symbols {
		syms = append(syms, sym)
	}
	slices.Sort(syms)
	for _, sym := range syms {
		c.symbols.add(mail.CompatView(sym), len(c.codesOf))
		c.codesOf = append(c.codesOf, slices.Clone(d.Symbols[sym]))
	}
	return c
})

// currency matches a code or a symbol at token i: its codes, whether it
// was a code (a word) and the token after it.
func (c *currencies) currency(tks []token, i int) ([]string, bool, int, bool) {
	if i < len(tks) && tks[i].kind == tokWord && c.codes[tks[i].text] {
		return []string{tks[i].text}, true, i + 1, true
	}
	if v, j, ok := c.symbols.at(tks, i); ok {
		return c.codesOf[v], false, j, true
	}
	return nil, false, 0, false
}

// separators are the decimal and grouping separators of every shipped
// locale (CLDR, in their compatibility view; white space apart): the
// characters an amount may split its digits with.
type separators struct{ decimals, groups map[string]bool }

var separatorSet = sync.OnceValue(func() separators {
	s := separators{decimals: map[string]bool{}, groups: map[string]bool{}}
	add := func(set map[string]bool, list []string) {
		for _, v := range list {
			if c := mail.CompatView(v); c != "" && !isSpaceText(c) {
				set[c] = true
			}
		}
	}
	for _, l := range localesData().Locales {
		add(s.decimals, l.Decimals)
		add(s.groups, l.Groups)
	}
	return s
})

func isSpaceText(s string) bool {
	for _, r := range s {
		if !mail.IsSpace(r) {
			return false
		}
	}
	return s != ""
}

// minus reports whether a token is a minus sign (U+002D HYPHEN-MINUS,
// U+2212 MINUS SIGN: the characters Unicode defines for it).
func minus(tk token) bool { return tk.kind == tokMark && (tk.text == "-" || tk.text == "\u2212") }

// amount matches a number at token i (a minus sign glued before it makes
// it negative): digits, then a separator (separatorSet) and digits, one at
// a time; or one white-space character and exactly three digits when the
// language groups digits with a space (1 234 in French; never two numbers
// side by side in English). It returns the number's text, whether it is
// negative and the token after it.
func (l *Locale) amount(tks []token, i int) (string, bool, int, bool) {
	neg := false
	if i+1 < len(tks) && minus(tks[i]) && tks[i+1].kind == tokNumber {
		neg, i = true, i+1
	}
	if i >= len(tks) || tks[i].kind != tokNumber {
		return "", false, 0, false
	}
	seps, spaced := separatorSet(), l.groupsWithSpace()
	var b strings.Builder
	b.WriteString(tks[i].text)
	j := i + 1
	for j+1 < len(tks) && tks[j+1].kind == tokNumber {
		sep := tks[j].kind == tokMark && (seps.decimals[tks[j].text] || seps.groups[tks[j].text])
		space := spaced && tks[j].kind == tokSpace && mail.Runes(tks[j].text) == 1 && mail.Runes(tks[j+1].text) == 3
		if !sep && !space {
			break
		}
		b.WriteString(tks[j].text + tks[j+1].text)
		j += 2
	}
	return b.String(), neg, j, true
}

// groupsWithSpace reports whether the language (or a region of it) groups
// digits with white space.
func (l *Locale) groupsWithSpace() bool {
	return slices.ContainsFunc(l.Groups, func(g string) bool { return isSpaceText(mail.CompatView(g)) })
}

// Amounts lists the amounts of money of a text (its compatibility view:
// full-width digits and signs read as plain ones): a number right before
// or after an ISO 4217 code or a CLDR currency symbol (one white-space
// character between them at most), read with this language's separators.
// An amount must stand alone: no letter or number glued to it (EUR inside
// a word is no code). A code is also a word in some languages ("ALL 5",
// "TOP 10"): a code next to a plain integer is an amount whose value is
// not known (Known false), never a confident one.
func (l *Locale) Amounts(text string) []Amount {
	c := currencyModel()
	tks := tokens(mail.CompatView(text))
	var out []Amount
	for i := 0; i < len(tks); i++ {
		var codes []string
		var num string
		neg, byCode, j, ok := false, false, 0, false
		if codes, byCode, j, ok = c.currency(tks, i); ok {
			num, neg, j, ok = l.amount(tks, space(tks, j))
		} else if num, neg, j, ok = l.amount(tks, i); ok {
			codes, byCode, j, ok = c.currency(tks, space(tks, j))
		}
		if !ok || !isolatedAt(tks, i, j) {
			continue
		}
		a := Amount{Codes: codes}
		a.Value, a.Known = l.parseAmount(num)
		if neg {
			a.Value = -a.Value
		}
		if byCode && plainInteger(num) {
			a.Known = false
		}
		out = append(out, a)
		i = j - 1
	}
	return out
}

// plainInteger reports whether a number's text is digits only.
func plainInteger(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// parseAmount reads a number's text (white space in it groups digits).
// Two different separators: the last is the decimal one. One separator,
// more than once: it groups. One separator once: the decimal one when
// the language's separators (all its regions) say so, a group when they
// say so — and when they say both (or it is none of the language's) by
// its structure: not followed by exactly three digits it is decimal,
// else the number is ambiguous (1.234: one thousand or one point two).
func (l *Locale) parseAmount(s string) (float64, bool) {
	var digits strings.Builder
	var seps []string // the separators in order
	for _, r := range s {
		if d, ok := digitValue(r); ok {
			digits.WriteByte(byte('0' + d))
			continue
		}
		if mail.IsSpace(r) {
			continue // a group of three digits (amount)
		}
		seps = append(seps, string(r))
		digits.WriteByte(0) // a separator's place
	}
	text := digits.String()
	dec, ok := l.decimalOf(text, seps)
	if !ok {
		return 0, false
	}
	var b strings.Builder
	k := 0
	for i := 0; i < len(text); i++ {
		if text[i] != 0 {
			b.WriteByte(text[i])
			continue
		}
		if k == dec {
			b.WriteByte('.')
		}
		k++
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	return v, err == nil && !math.IsInf(v, 0)
}

// decimalOf is the index (among the separators) of the decimal one, -1
// for none; false when the number cannot be read for sure.
func (l *Locale) decimalOf(text string, seps []string) (int, bool) {
	if len(seps) == 0 {
		return -1, true
	}
	last := seps[len(seps)-1]
	for _, s := range seps[:len(seps)-1] {
		if s != last {
			return len(seps) - 1, l.canDecimal(last) // two separators: the last one is the decimal one
		}
	}
	if len(seps) > 1 {
		return -1, l.canGroup(last) // one separator repeated: groups
	}
	after := len(text) - strings.LastIndexByte(text, 0) - 1
	canDec, canGroup := l.canDecimal(last), l.canGroup(last) && after == 3
	if !canDec && !canGroup { // the language's reading is impossible (a "group" of two digits): any locale's
		canDec, canGroup = separatorSet().decimals[last], separatorSet().groups[last] && after == 3
	}
	switch {
	case canDec && !canGroup:
		return 0, true
	case canGroup && !canDec:
		return -1, true
	}
	return 0, false
}

// canDecimal and canGroup tell what a separator can be in this language
// (any locale's use for one the language does not have, or without a
// language).
func (l *Locale) canDecimal(sep string) bool {
	if l.Tag != Undetermined && l.has(sep) {
		return slices.ContainsFunc(l.Decimals, func(d string) bool { return mail.CompatView(d) == sep })
	}
	return separatorSet().decimals[sep]
}

func (l *Locale) canGroup(sep string) bool {
	if l.Tag != Undetermined && l.has(sep) {
		return slices.ContainsFunc(l.Groups, func(g string) bool { return mail.CompatView(g) == sep })
	}
	return separatorSet().groups[sep]
}

// has reports whether a separator is one of the language's own.
func (l *Locale) has(sep string) bool {
	same := func(x string) bool { return mail.CompatView(x) == sep }
	return slices.ContainsFunc(l.Decimals, same) || slices.ContainsFunc(l.Groups, same)
}
