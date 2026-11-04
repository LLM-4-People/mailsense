package mail

import (
	_ "embed"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Fold is the search form of text, in every script, applied to the whole
// string (not rune by rune, so compositions across characters happen):
//   - NFKC: compatibility forms folded (full-width ＡＢＣ is abc, half-width
//     ｸﾞ is グ), decomposed text composed (macOS NFD é, conjoining Hangul
//     jamo 견 is 견);
//   - Unicode default case folding (C+F of CaseFolding.txt: Σ, σ and ς
//     match, ß is ss);
//   - default-ignorable characters removed (bidi and other format
//     controls, variation selectors: DerivedCoreProperties), as NFKC_Casefold does;
//   - in the scripts whose marks are diacritics (fold.json strip_marks),
//     every nonspacing mark of a character whose BASE is of that script
//     removed, after NFD: réunion, Réunion and reunion match, so do
//     Vietnamese, Romanian ș/ț, Greek tonos, Hebrew niqqud and Arabic
//     harakat (شكرًا is شكرا) — the mark's own script (Inherited) never
//     decides; the letters without a decomposition are mapped by fold.json
//     (ø → o, ł → l).
//
// Bytes that are not UTF-8 are kept as they are. The full-text indexes
// store and query this form only (store.writeFTS).
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	foldInto(&b, nil, s)
	return b.String()
}

// CompactFold is Fold reduced to letters and decimal digits: the comparison
// form of names and domains, where punctuation, spacing and separators are
// noise. It is the one owner of that reduction (contacts and the company
// book match on it).
func CompactFold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range Fold(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FoldMap is Fold plus, for every byte of the folded text, the byte offset
// in s of the character (normalization segment) it came from: snippets
// locate matches in the folded text and cut the original.
func FoldMap(s string) (string, []int) {
	var b strings.Builder
	b.Grow(len(s))
	offsets := make([]int, 0, len(s))
	foldInto(&b, &offsets, s)
	return b.String(), offsets
}

// foldInto writes the fold of s. ASCII that no following character can
// combine with is lowered byte by byte, and so is a character that is a
// whole NFKC segment by itself (a precomposed é between ASCII letters);
// anything else goes through NFKC segments (a starter and the marks that
// follow it).
func foldInto(b *strings.Builder, offsets *[]int, s string) {
	t := folding()
	for i := 0; i < len(s); {
		if plainASCII(s, i) {
			c := s[i]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(c)
			mapOffset(offsets, i, 1)
			i++
			continue
		}
		if r, n := aloneRune(s, i); n > 0 {
			f := t.rune(r)
			b.WriteString(f)
			mapOffset(offsets, i, len(f))
			i += n
			continue
		}
		i = foldSegments(b, offsets, s, i)
	}
}

// aloneRune is the character at s[i] when it is an NFKC segment by itself:
// a starter that combines with nothing before it, NFKC-normal on its own,
// with a boundary after it. n is 0 otherwise.
func aloneRune(s string, i int) (rune, int) {
	r, n := utf8.DecodeRuneInString(s[i:])
	if r == utf8.RuneError || r > 0xFFFF || folding().notNFKC[r] || !boundaryAt(s, i+n) ||
		!norm.NFKC.PropertiesString(s[i:i+n]).BoundaryBefore() {
		return 0, 0
	}
	return r, n
}

// plainASCII: s[i] is ASCII and nothing after it combines with it.
func plainASCII(s string, i int) bool { return s[i] < utf8.RuneSelf && boundaryAt(s, i+1) }

// boundaryAt: no character before s[j] combines with what starts there
// (the end, ASCII, or a character with an NFKC boundary before it).
func boundaryAt(s string, j int) bool {
	return j >= len(s) || s[j] < utf8.RuneSelf || norm.NFKC.PropertiesString(s[j:]).BoundaryBefore()
}

// foldSegments folds NFKC segments from s[from:] until plain ASCII starts
// again and returns that position.
func foldSegments(b *strings.Builder, offsets *[]int, s string, from int) int {
	var it norm.Iter
	it.InitString(norm.NFKC, s[from:])
	for !it.Done() {
		pos := from + it.Pos()
		if plainASCII(s, pos) {
			return pos
		}
		n := b.Len()
		foldSegment(b, it.Next())
		mapOffset(offsets, pos, b.Len()-n)
	}
	return len(s)
}

func mapOffset(offsets *[]int, pos, n int) {
	if offsets == nil {
		return
	}
	for range n {
		*offsets = append(*offsets, pos)
	}
}

// foldSegment writes the fold of one NFKC segment: the base character's
// script decides whether the segment's nonspacing marks go.
func foldSegment(b *strings.Builder, seg []byte) {
	t := folding()
	first, size := utf8.DecodeRune(seg)
	if first == utf8.RuneError && size <= 1 {
		b.Write(seg) // not UTF-8: kept as is
		return
	}
	if size == len(seg) {
		b.WriteString(t.rune(first))
		return
	}
	strip := t.strip[ScriptOf(first)]
	for i := 0; i < len(seg); {
		r, n := utf8.DecodeRune(seg[i:])
		if r == utf8.RuneError && n <= 1 {
			b.WriteByte(seg[i])
		} else if !(strip && unicode.Is(unicode.Mn, r)) {
			b.WriteString(t.rune(r))
		}
		i += n
	}
}

//go:embed fold.json
var foldJSON []byte

// foldTable is fold.json plus the fold of every Basic Multilingual Plane
// character that is not its own fold, computed once (immutable).
type foldTable struct {
	letters map[rune]string
	strip   map[string]bool
	bmp     map[rune]string
	notNFKC map[rune]bool // BMP characters NFKC changes even alone (ﬁ, Ａ, ｸ)
}

// rune is the fold of one character standing alone.
func (t *foldTable) rune(r rune) string {
	if r < utf8.RuneSelf {
		return string(unicode.ToLower(r))
	}
	if r <= 0xFFFF {
		if f, ok := t.bmp[r]; ok {
			return f
		}
		return string(r)
	}
	return t.compute(r)
}

// compute folds one character from the Unicode data: case folding, the
// fold.json letters, default ignorables removed, and, when the folded
// character's base is of a strip script, its marks removed after NFD
// (İ folds to i + U+0307, and the dot goes).
func (t *foldTable) compute(r rune) string {
	if defaultIgnorable(r) {
		return ""
	}
	folded := cases.Fold().String(string(r))
	base, _ := utf8.DecodeRuneInString(folded)
	strip := t.strip[ScriptOf(base)]
	var b strings.Builder
	for _, c := range folded {
		if f, ok := t.letters[c]; ok {
			b.WriteString(f)
			continue
		}
		if !strip {
			b.WriteRune(c)
			continue
		}
		for _, d := range norm.NFD.String(string(c)) {
			if !unicode.Is(unicode.Mn, d) {
				b.WriteRune(d)
			}
		}
	}
	return b.String()
}

// defaultIgnorable is Default_Ignorable_Code_Point as DerivedCoreProperties
// derives it: Other_Default_Ignorable_Code_Point + Cf + Variation_Selector,
// minus White_Space, U+FFF9..U+FFFB, U+13430..U+1343F and the
// Prepended_Concatenation_Marks.
func defaultIgnorable(r rune) bool {
	if unicode.Is(unicode.White_Space, r) || (r >= 0xFFF9 && r <= 0xFFFB) || (r >= 0x13430 && r <= 0x1343F) ||
		unicode.Is(unicode.Prepended_Concatenation_Mark, r) {
		return false
	}
	return unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Variation_Selector, r)
}

// folding is fold.json and the BMP table (TestFoldRulesAreValid guards
// the file; a broken file is a build defect).
var folding = sync.OnceValue(func() *foldTable {
	var doc struct {
		Doc        string            `json:"_doc"`
		Source     string            `json:"_source"`
		Letters    map[string]string `json:"letters"`
		StripMarks []string          `json:"strip_marks"`
	}
	if err := jsonx.DecodeStrict(foldJSON, &doc); err != nil {
		panic("mail: fold.json: " + err.Error())
	}
	t := &foldTable{letters: map[rune]string{}, strip: map[string]bool{}, bmp: map[rune]string{}, notNFKC: map[rune]bool{}}
	for k, v := range doc.Letters {
		r, n := utf8.DecodeRuneInString(k)
		if n != len(k) {
			panic("mail: fold.json: letter " + k + " is not one character")
		}
		t.letters[r] = v
	}
	for _, s := range doc.StripMarks {
		t.strip[s] = true
	}
	for r := rune(utf8.RuneSelf); r <= 0xFFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if f := t.compute(r); f != string(r) {
			t.bmp[r] = f
		}
		if !norm.NFKC.IsNormalString(string(r)) {
			t.notNFKC[r] = true
		}
	}
	return t
})
