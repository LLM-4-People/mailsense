package mail_test

import (
	"testing"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Round 30: the matra-based Indic scripts use spaces, so they must not be
// spaceless (word counts and triage facts stay unchanged), but unicode61
// treats their dependent vowel signs as separators. Every letter of such a
// script must therefore route a query to the substring index.
func TestIndicScriptsAreSubstringSearched(t *testing.T) {
	samples := mailtest.IndicMatras()
	if len(samples) != 10 {
		t.Fatalf("the fixture covers %d scripts, want the 10 locale scripts", len(samples))
	}
	for _, sample := range samples {
		if unicode.Scripts[sample.Script] == nil {
			t.Errorf("%q is not a Unicode script name", sample.Script)
		}
		if mail.Spaceless(sample.Script) {
			t.Errorf("%s must stay spaced: word counts and triage facts must not change", sample.Script)
		}
		for _, term := range sample.Terms {
			if got := mail.ScriptOf([]rune(term)[0]); got != sample.Script {
				t.Errorf("%q: base letter script %q, want %q", term, got, sample.Script)
			}
			if !mail.NeedsSubstringSearch(term) {
				t.Errorf("%q (%s) must be searched by substring: its matra is not a unicode61 token character", term, sample.Script)
			}
		}
	}
	// Hangul keeps its hand-kept entry: words carry attached particles.
	if mail.Spaceless("Hangul") {
		t.Error("Hangul must stay spaced")
	}
	if !mail.NeedsSubstringSearch("견적서") {
		t.Error("Hangul must stay substring-searched")
	}
}
