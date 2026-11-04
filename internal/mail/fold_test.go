package mail

import (
	"strings"
	"testing"
)

// Review L0 bugs #2 / rules §1.3 (each case failed first on the per-rune
// fold): the whole string is NFKC-normalized, case-folded, and the marks
// of a character go when its BASE is of a strip script.
func TestFoldWholeStringNormalization(t *testing.T) {
	for _, c := range []struct{ name, a, b string }{
		{"Arabic harakat", "اَلْاِجْتِمَاعُ", "الاجتماع"},
		{"Arabic tanween", "شكرًا", "شكرا"},
		{"NFD Latin (macOS)", "réunion", "réunion"},
		{"half-width katakana", "ｶﾀﾛｸﾞ", "カタログ"},
		{"conjoining Hangul jamo", "견적서", "견적서"},
		{"Greek final sigma", "ΛΟΓΟΣ", "λόγος"},
		{"Turkish dotted capital", "İSTANBUL", "istanbul"},
		{"combining dot after a Latin base", "i̇", "i"},
		{"bidi controls", "fac‎ture‫", "facture"},
		{"variation selector", "ok️", "ok"},
	} {
		if fa, fb := Fold(c.a), Fold(c.b); fa != fb {
			t.Errorf("%s: Fold(%q) = %q, Fold(%q) = %q", c.name, c.a, fa, c.b, fb)
		}
	}
}

// Marks that belong to the letter stay: Indic and Thai vowel signs,
// Japanese voicing, Cyrillic й.
func TestFoldKeepsMarksOfOtherScripts(t *testing.T) {
	for in, want := range map[string]string{
		"हिन्दी": "हिन्दी",
		"हन्द":   "हन्द",
		"ประชุม": "ประชุม",
		"ガ":      "ガ",
		"й":      "й",
		"ָx":     "x", // a lone Hebrew mark is Hebrew: it goes
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	if Fold("हिन्दी") == Fold("हन्द") {
		t.Error("Devanagari vowel signs are part of the word")
	}
}

// Misuse: bytes that are not UTF-8 are kept as they are (never replaced),
// and nothing panics; the offset map covers every folded byte.
func TestFoldMisuse(t *testing.T) {
	for _, in := range []string{"x\xffy", "\xe2\x82", "́", "́́a", strings.Repeat("é", 1000), "a‎", ""} {
		got, offsets := FoldMap(in)
		if len(offsets) != len(got) {
			t.Errorf("FoldMap(%q): %d offsets for %d bytes", in, len(offsets), len(got))
		}
		for i, o := range offsets {
			if o < 0 || o >= max(len(in), 1) || (i > 0 && o < offsets[i-1]) {
				t.Fatalf("FoldMap(%q): offset %d = %d", in, i, o)
			}
		}
	}
	if got := Fold("x\xffy"); got != "x\xffy" {
		t.Errorf("invalid bytes changed: %q", got)
	}
}

// The offset map points at the original character of every folded byte,
// also across compositions and half-width forms.
func TestFoldMapOffsets(t *testing.T) {
	in := "AéｸﾞZ"
	got, offsets := FoldMap(in)
	if got != "aeグz" {
		t.Fatalf("%q", got)
	}
	want := []int{0, 1, 4, 4, 4, 10}
	if !equalInts(offsets, want) {
		t.Fatalf("offsets %v, want %v", offsets, want)
	}
}

// BenchmarkFold: mail text with accents (the import folds every
// subject, body, sender and recipient list).
func BenchmarkFold(b *testing.B) {
	text := strings.Repeat("Bonjour, la réunion est déplacée à 14 h. Merci de confirmer l'ébauche déjà prête. ", 200)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		_ = Fold(text)
	}
}

func TestCompactFoldKeepsOnlyLettersAndDigits(t *testing.T) {
	cases := map[string]string{
		"Élodie O'Brien": "elodieobrien",
		"ＡＣＭＥ 42":        "acme42",
		"A/B-C.D":        "abcd",
		"  spaced  ":     "spaced",
	}
	for in, want := range cases {
		if got := CompactFold(in); got != want {
			t.Errorf("CompactFold(%q) = %q, want %q", in, got, want)
		}
	}
}
