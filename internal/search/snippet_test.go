package search

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func oneTerm(term string, mode store.TermMode) []snippetTerm {
	return snippetPlan([]store.TermMatch{{Term: term, Mode: mode}})
}

func wholeToken(text string, words ...string) bool {
	have := tokenizeFTS(mail.Fold(text))
	want := append([]string(nil), words...)
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(have); i++ {
		ok := true
		for j := range want {
			if have[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func TestSnippetWordModeWholeToken(t *testing.T) {
	body := strings.Repeat("communication ", 20) + strings.Repeat("filler ", 30) + "com " + strings.Repeat("tail ", 30)
	s, ok := snippet(body, oneTerm("com", store.ModeWord), 60)
	if !ok {
		t.Fatalf("a standalone com must anchor: %q", s)
	}
	if !wholeToken(s, "com") {
		t.Fatalf("the snippet has no whole token com: %q", s)
	}
	if strings.Contains(s, "communication") {
		t.Fatalf("the snippet anchored the substring inside communication: %q", s)
	}
}

func TestSnippetOneRuneWordTerms(t *testing.T) {
	for _, term := range []string{"a", "x"} {
		body := strings.Repeat("filler ", 100) + term + " " + strings.Repeat("tail ", 100)
		s, ok := snippet(body, oneTerm(term, store.ModeWord), 40)
		if !ok || !strings.HasPrefix(s, ellipsis) || !wholeToken(s, term) {
			t.Fatalf("%q: a one-rune word term must anchor its whole token: %q (ok %v)", term, s, ok)
		}
	}
}

func TestSnippetPhraseTokens(t *testing.T) {
	body := "mailer " + strings.Repeat("filler ", 60) + "mailer-daemon bounce"
	s, ok := snippet(body, oneTerm("mailer-daemon", store.ModeWord), 60)
	if !ok || !strings.HasPrefix(s, ellipsis) {
		t.Fatalf("the later adjacent phrase must anchor: %q (ok %v)", s, ok)
	}
	if !strings.Contains(s, "mailer-daemon") {
		t.Fatalf("the snippet anchors the first isolated mailer, not the phrase: %q", s)
	}
}

func TestSnippetUnderscoreAndPunctuationBoundaries(t *testing.T) {
	for _, body := range []string{"prefix com_value suffix", "prefix _com_ suffix", "prefix com. suffix"} {
		s, ok := snippet(body, oneTerm("com", store.ModeWord), 40)
		if !ok || !wholeToken(s, "com") {
			t.Fatalf("%q: separators around com must not hide the token: %q (ok %v)", body, s, ok)
		}
	}
	if s, ok := snippet("communication only", oneTerm("com", store.ModeWord), 40); ok || s != "" {
		t.Fatalf("communication must not match the whole token com: %q (ok %v)", s, ok)
	}
	if s, ok := snippet("prefix com_value suffix", oneTerm("com_value", store.ModeWord), 40); !ok || !strings.Contains(s, "com_value") {
		t.Fatalf("a multi-token term must match adjacent index tokens: %q (ok %v)", s, ok)
	}
}

func TestSnippetSubstringAndScanModes(t *testing.T) {
	body := strings.Repeat("x ", 60) + "communication happens"
	s, ok := snippet(body, oneTerm("com", store.ModeSubstring), 40)
	if !ok || !strings.Contains(s, "communication") {
		t.Fatalf("substring mode must anchor inside communication: %q (ok %v)", s, ok)
	}
	cjk := strings.Repeat("前 ", 60) + "会议"
	s, ok = snippet(cjk, oneTerm("会", store.ModeScan), 40)
	if !ok || !strings.Contains(s, "会") {
		t.Fatalf("a single CJK scan rune must anchor: %q (ok %v)", s, ok)
	}
}

func TestSnippetShortScanTermNoise(t *testing.T) {
	body := strings.Repeat("maison ", 60) + "会议 " + strings.Repeat("tail ", 60)
	plan := snippetPlan([]store.TermMatch{{Term: "a", Mode: store.ModeScan}, {Term: "会议", Mode: store.ModeScan}})
	s, ok := snippet(body, plan, 60)
	if !ok || !strings.Contains(s, "会议") || !strings.HasPrefix(s, ellipsis) {
		t.Fatalf("a one-rune scan term must not outrank the longer term: %q (ok %v)", s, ok)
	}
	s, ok = snippet(body, oneTerm("a", store.ModeScan), 40)
	if !ok || !strings.Contains(s, "maison") {
		t.Fatalf("a lone one-rune scan term still anchors: %q (ok %v)", s, ok)
	}
}

func TestSnippetCombiningMarksAndCJK(t *testing.T) {
	body := strings.Repeat("filler ", 40) + "re\u0301union " + strings.Repeat("tail ", 40)
	s, ok := snippet(body, oneTerm("reunion", store.ModeWord), 40)
	if !ok || !strings.Contains(mail.Fold(s), "reunion") || !strings.HasPrefix(s, ellipsis) {
		t.Fatalf("NFD reunion must match the folded word: %q (ok %v)", s, ok)
	}
	body = strings.Repeat("filler ", 40) + "a\u0301b " + strings.Repeat("tail ", 40)
	if s, ok := snippet(body, oneTerm("a", store.ModeWord), 40); ok || s != "" {
		t.Fatalf("a combining mark must join the token ab, not expose a: %q (ok %v)", s, ok)
	}
	if s, ok := snippet(body, oneTerm("ab", store.ModeWord), 40); !ok || !strings.Contains(mail.Fold(s), "ab") {
		t.Fatalf("the folded token ab must anchor: %q (ok %v)", s, ok)
	}
	cjk := strings.Repeat("文字", 80) + "会议" + strings.Repeat("後", 80)
	s, ok = snippet(cjk, oneTerm("会议", store.ModeScan), 20)
	if !ok || utf8.RuneCountInString(s) > 22 || !strings.Contains(s, "会议") {
		t.Fatalf("a CJK snippet stays within the budget: %d %q (ok %v)", utf8.RuneCountInString(s), s, ok)
	}
}

func TestSnippetBudgetEllipsesWhitespace(t *testing.T) {
	for _, n := range []int{20, 60, 240} {
		body := "start " + strings.Repeat("filler\twords\n", 80) + "needle " + strings.Repeat("tail ", 80)
		s, ok := snippet(body, oneTerm("needle", store.ModeWord), n)
		if !ok || utf8.RuneCountInString(s) > n+2 {
			t.Fatalf("n=%d: budget: %d %q (ok %v)", n, utf8.RuneCountInString(s), s, ok)
		}
		if !strings.HasPrefix(s, ellipsis) || !strings.HasSuffix(s, ellipsis) {
			t.Fatalf("n=%d: ellipses missing: %q", n, s)
		}
		if strings.ContainsAny(s, "\t\n\r") {
			t.Fatalf("n=%d: whitespace is not collapsed: %q", n, s)
		}
	}
	if s, ok := snippet("", oneTerm("x", store.ModeWord), 100); ok || s != "" {
		t.Fatalf("an empty body has no match: %q (ok %v)", s, ok)
	}
}

func TestSnippetEmptyBodyAndNoMatch(t *testing.T) {
	if s, ok := snippet("", oneTerm("com", store.ModeWord), 240); ok || s != "" {
		t.Fatalf("empty body: %q (ok %v)", s, ok)
	}
	if s, ok := snippet("communication only", oneTerm("com", store.ModeWord), 240); ok || s != "" {
		t.Fatalf("no whole-token match: %q (ok %v)", s, ok)
	}
	if s := preview("Hello there.\n\n> quoted\n-- \nsig", 100); s != "Hello there." {
		t.Fatalf("preview is the cleaned beginning: %q", s)
	}
	if s := preview(" \n\t ", 100); s != "" {
		t.Fatalf("preview of an empty cleaned body: %q", s)
	}
}

// TestSnippetMarkRetainingScript pins the shipped unicode61 contract: its
// token characters are L*, N* and Co only, so a combining mark (Devanagari
// matra) separates tokens instead of joining them. mail.Fold keeps marks in
// this script, so the snippet tokenizer must drop them at token boundaries.
func TestSnippetMarkRetainingScript(t *testing.T) {
	for _, term := range []string{"क", "कि"} {
		body := "कि"
		s, ok := snippet(body, oneTerm(term, store.ModeWord), 40)
		if !ok || !strings.Contains(s, "क") {
			t.Fatalf("query %q over %q: a genuine body hit must anchor at the unicode61 base token: %q (ok %v)", term, body, s, ok)
		}
	}
	// The reverse proves the matra split: the query's two code points are one
	// unicode61 token, exactly like the body's base character.
	if s, ok := snippet("क", oneTerm("कि", store.ModeWord), 40); !ok || !strings.Contains(s, "क") {
		t.Fatalf("the matra must separate, so query कि and body क share one token: %q (ok %v)", s, ok)
	}
	for _, body := range []string{"की", "कु", "कू"} {
		s, ok := snippet(body, oneTerm("क", store.ModeWord), 40)
		if !ok || !strings.Contains(s, "क") {
			t.Fatalf("query क over %q: a mark-retaining body must anchor: %q (ok %v)", body, s, ok)
		}
	}
}

// Round 30: once the matra-based Indic scripts are substring-searched, the
// snippet plan keeps the matra. A scan term anchors the exact sign with
// strings.Index on the folded text; the word mode would tokenize कि to क
// and anchor the first base consonant (here की), the wrong excerpt.
func TestSnippetIndicMatras(t *testing.T) {
	body := strings.Repeat("की ", 200) + strings.Repeat("tail ", 200) + "कि"
	s, ok := snippet(body, oneTerm("कि", store.ModeScan), 40)
	if !ok || !strings.Contains(s, "कि") || strings.Contains(s, "की") {
		t.Fatalf("scan mode must anchor the exact matra: %q (ok %v)", s, ok)
	}
	// Control: the same query in word mode anchors the first base token.
	w, ok := snippet(body, oneTerm("कि", store.ModeWord), 40)
	if !ok || !strings.Contains(w, "की") || !strings.HasPrefix(w, "की") {
		t.Fatalf("word mode cannot tell the matras apart (control): %q (ok %v)", w, ok)
	}
	// A four-rune substring term anchors its exact word far from the body
	// beginning (कमला also contains कमल, the documented residual).
	body = strings.Repeat("tail ", 200) + "कमला"
	s, ok = snippet(body, oneTerm("कमला", store.ModeSubstring), 40)
	if !ok || !strings.Contains(s, "कमला") || !strings.HasPrefix(s, ellipsis) {
		t.Fatalf("substring mode must anchor the exact word: %q (ok %v)", s, ok)
	}
}
