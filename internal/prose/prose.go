// Package prose owns the public-copy style rules.
//
// The rules exist so a reader meets the same plain voice everywhere: no em
// dash, no spaced en dash used as punctuation, no double hyphen used as
// punctuation, and no stock phrase that makes a page read like generated
// marketing. The documentation test and
// the UI catalog test both call this package, so the rule has one owner.
//
// The check is deliberately narrow. It runs on public prose, never on code,
// legal text, or engineering logs.
package prose

import (
	"fmt"
	"regexp"
	"strings"
)

// Rule names returned by Check.
const (
	RuleEmDash      = "em dash"
	RuleEnDash      = "en dash"
	RuleDoubleDash  = "double hyphen"
	RuleStockPhrase = "stock phrase"
)

// stockPhrases are the tells this project refuses in public copy. Add a phrase
// only when it is a reliable signal, not a word that technical writing needs.
var stockPhrases = []string{
	"delve",
	"seamless",
	"unleash",
	"elevate",
	"empower",
	"leverage",
	"game-changer",
	"cutting-edge",
	"state-of-the-art",
	"revolutioniz",
	"transformative",
	"tapestry",
	"in today's",
	"look no further",
	"deep dive",
	"whether you're",
	"it's not just",
	"more than just",
	"in conclusion",
	"furthermore",
	"moreover",
	"plongez",
	"véritable",
	"incontournable",
	"révolutionnaire",
	"clé en main",
	"dans un monde où",
	"à l'ère du",
	"il est important de noter",
	"en conclusion",
	"par ailleurs",
}

// spacedEnDashRE finds an en dash surrounded by spaces, the punctuation form.
// A numeric or token range such as 1,3–1,8x or {from}–{to} is a range,
// not punctuation, and stays allowed.
var spacedEnDashRE = regexp.MustCompile(`\s\x{2013}\s`)

// doubleDashRE finds a double hyphen used as punctuation: surrounded by a
// space, a quote, or a bracket, or between two letters. A command flag such as
// --ignore-missing and a Markdown table separator are not punctuation and do
// not match.
var doubleDashRE = regexp.MustCompile(`(^|[\s"“(\[])--([\s"”)\]]|$)|\p{L}--\p{L}`)

// Check returns the rule names violated by one piece of prose, in a stable
// order with duplicates removed. It operates on a single line or a single
// catalog value; callers strip code before calling it.
func Check(text string) []string {
	var rules []string
	add := func(rule string) {
		for _, existing := range rules {
			if existing == rule {
				return
			}
		}
		rules = append(rules, rule)
	}
	if strings.ContainsRune(text, '\u2014') {
		add(RuleEmDash)
	}
	if spacedEnDashRE.MatchString(text) {
		add(RuleEnDash)
	}
	if doubleDashRE.MatchString(text) {
		add(RuleDoubleDash)
	}
	lower := strings.ToLower(text)
	for _, phrase := range stockPhrases {
		if strings.Contains(lower, phrase) {
			add(RuleStockPhrase)
			break
		}
	}
	return rules
}

// Finding is one violation in one document.
type Finding struct {
	Path string
	Line int
	Rule string
	Text string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", f.Path, f.Line, f.Rule, strings.TrimSpace(f.Text))
}

// CheckDocument checks Markdown or plain prose. Fenced code blocks and inline
// code spans are skipped: a flag, a SQL comment, or a shell example may use
// hyphens freely. The returned findings carry the original line number.
func CheckDocument(path string, body []byte) []Finding {
	var findings []Finding
	inFence := false
	fenceMarker := ""
	for index, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if marker, ok := fenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				continue
			}
		} else {
			if strings.HasPrefix(trimmed, fenceMarker) {
				inFence = false
				fenceMarker = ""
			}
			continue
		}
		proseLine := stripInlineCode(line)
		for _, rule := range Check(proseLine) {
			findings = append(findings, Finding{
				Path: path,
				Line: index + 1,
				Rule: rule,
				Text: strings.TrimSpace(proseLine),
			})
		}
	}
	return findings
}

// fenceOpen recognises a Markdown fenced code block opener and returns the
// marker to look for when it closes.
func fenceOpen(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```", true
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~", true
	default:
		return "", false
	}
}

// stripInlineCode removes `code spans` and leaves the surrounding prose. A
// span that is not closed is left alone: a stray backtick is a copy defect of
// its own, and hiding the rest of the line would hide real prose.
func stripInlineCode(line string) string {
	var out strings.Builder
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			out.WriteString(line)
			return out.String()
		}
		end := strings.IndexByte(line[start+1:], '`')
		if end < 0 {
			out.WriteString(line)
			return out.String()
		}
		out.WriteString(line[:start])
		line = line[start+end+2:]
	}
}
