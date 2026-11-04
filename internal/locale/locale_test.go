package locale

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// The generated files are exactly what the pinned sources gave
// (scripts/update-locale-data.sh): an edit by hand fails here.
func TestShippedDataMatchesThePin(t *testing.T) {
	raw, err := os.ReadFile("pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Outputs map[string]string `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil || len(pin.Outputs) != 5 {
		t.Fatalf("pin.json: %v %v", err, pin.Outputs)
	}
	for name, want := range pin.Outputs {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
			t.Errorf("%s differs from pin.json: re-run scripts/update-locale-data.sh", name)
		}
	}
}

func TestDataLoadsAndEveryLocaleCompiles(t *testing.T) {
	langs := Languages()
	if len(langs) < 90 || !slices.Contains(langs, "fr") || !slices.Contains(langs, "ja") {
		t.Fatalf("languages: %d %v", len(langs), langs)
	}
	for _, tag := range append(langs, Undetermined) {
		l, ok := LocaleOf(tag)
		if !ok || l.dates == nil || l.times == nil {
			t.Errorf("%s: no compiled locale (%v)", tag, ok)
		}
	}
	if _, ok := LocaleOf("xx-Invalid"); ok {
		t.Error("an unknown tag must not resolve")
	}
	if len(currencyData().Codes) < 100 || len(confusableMap()) < 5000 {
		t.Errorf("currency codes %d, confusables %d", len(currencyData().Codes), len(confusableMap()))
	}
}

// Every language of the synthetic corpus is recognised from a short
// mail; text without enough letters stays undetermined.
func TestLanguageOfTheCorpus(t *testing.T) {
	for _, s := range mailtest.Corpus() {
		g := Language(s.Subject+"\n"+s.Body, 10)
		if g.Tag != s.Lang || g.Script != s.Script {
			t.Errorf("%s: got %+v", s.Lang, g)
		}
	}
	for _, text := range []string{"", "12:30", "ok", "+33 1 23 45 67 89"} {
		if g := Language(text, 10); g.Tag != Undetermined || g.Margin != 0 {
			t.Errorf("%q: %+v", text, g)
		}
	}
}

// Katakana and Hiragana are the two scripts of one writing system: a
// katakana-only Japanese mail used to be undetermined (Japanese's shipped
// scripts are Han and Hiragana, and no language was a candidate for the
// Katakana script). The dominant script still reports Katakana, and the kana
// rule is evidence of Japanese only: the Han-only and Hangul-only languages
// stay out of the katakana candidates, and Chinese/Korean/Latin keep their
// answers.
func TestLanguageOfKanaScripts(t *testing.T) {
	const katakanaOnly = "カタカナデカカレタブンショウデス"
	cases := []struct{ name, text, tag, script string }{
		{"katakana-only", katakanaOnly, "ja", "Katakana"},
		{"hiragana-only", "ひらがなだけでかかれたぶんしょうです", "ja", "Hiragana"},
		{"kanji-and-katakana", "会議議事録ハ金曜日マデニ送付シテクダサイ", "ja", "Han"},
		{"Han-only", "会议纪要请在星期五之前发送", "zh", "Han"},
		{"Hangul-only", "회의록을금요일까지보내주세요", "ko", "Hangul"},
		{"Latin", "Please send the revised budget by Friday", "en", "Latin"},
	}
	for _, c := range cases {
		if g := Language(c.text, 10); g.Tag != c.tag || g.Script != c.script {
			t.Errorf("%s: got %+v, want %s/%s", c.name, g, c.tag, c.script)
		}
	}
	p := mail.Scripts(katakanaOnly)
	m := langModel()
	var tags []string
	for _, i := range m.candidates(p.Dominant(), p) {
		tags = append(tags, m.tags[i])
	}
	if p.Dominant() != "Katakana" || !slices.Equal(tags, []string{"ja"}) {
		t.Errorf("katakana candidates: dominant %s, languages %v", p.Dominant(), tags)
	}
}

func TestDatesInEveryShape(t *testing.T) {
	ref := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) // a Thursday
	cases := []struct {
		tag, text string
		days      []int
		times     int
	}{
		{"fr", "Réunion le 20 octobre 2026 à 14:30, sinon lundi.", []int{5, 4}, 1},
		{"fr", "Livraison prévue le 03/11 ; facture du 12/09/2026.", []int{-33}, 0}, // 03/11: fr_CA writes MM-dd (disputed)
		{"fr", "rendez-vous jeudi 22 OCTOBRE", []int{7}, 0},
		{"fr", "avant le 2 fevr.", []int{110}, 0}, // diacritics and the abbreviation's dot never matter
		{"en", "Please reply by October 20, 2026 or Monday at 3:15 PM.", []int{5, 4}, 1},
		{"en", "Due 11/3/2026 or 11/23/26", []int{39}, 0}, // 11/3 is ambiguous across the English regions; 11/23 is not
		{"de", "Bitte bis zum 20. Oktober prüfen, 9:05 Uhr.", []int{5}, 1},
		{"ja", "会議は2026年10月20日です。", []int{5}, 0},
		{"zh", "请在10月20日之前发送。", []int{5}, 0},
		{"ar", "الموعد ٢٠٢٦-١٠-٢٠", []int{5}, 0}, // ISO 8601 (root) in Arabic-Indic digits
		{Undetermined, "build 2026-10-20 at 07:45", []int{5}, 1},
		{"en", "version 3.14.2026 and 99/99/2026 and room 12/45", nil, 0},
		{"en", "a20/10/2026b", []int{5}, 0},                   // a letter next to a digit is a boundary (review L0: prefix letters)
		{"he", "הפגישה ב20 באוקטובר", []int{5}, 0},            // a Hebrew prefix letter glued to the day
		{Undetermined, "build 2026-10-20T07:45", []int{5}, 1}, // ISO 8601 date and time
		{"fr", "le 20 octobrex", nil, 0},                      // a word glued to a word is no name
		{"fr", "le 30 février", nil, 0},
		{"vi", "Hạn chót: 20 tháng 10, 2026", []int{5}, 0},       // a month name of two tokens
		{"zh", "我们星期五见", []int{1}, 0},                            // a weekday named alone, inside spaceless text
		{"ja", "午後2:30に会いましょう", nil, 1},                          // a day period before the time
		{"en", "Room 12:75 and 25:00 and 3:5 and 24:00", nil, 1}, // 24:00 at most; 3:5 has one minute digit
		{"de", "am 3. Okt. treffen", []int{-12}, 0},              // an abbreviation with its dot
	}
	for _, c := range cases {
		l, ok := LocaleOf(c.tag)
		if !ok {
			t.Fatal(c.tag)
		}
		got := l.Find(c.text)
		var days []int
		for _, m := range got.Dates {
			if d, _, ok := l.Resolve(m, ref); ok {
				days = append(days, Days(d, ref))
			}
		}
		if !slices.Equal(days, c.days) || got.Times != c.times {
			t.Errorf("%s %q: days %v times %d, want %v %d (%+v)", c.tag, c.text, days, got.Times, c.days, c.times, got.Dates)
		}
	}
}

func TestResolveMisuse(t *testing.T) {
	ref := time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)
	en, _ := LocaleOf("en")
	if d, _, ok := en.Resolve(DateMention{Month: 1, Day: 2, Weekday: -1}, ref); !ok || Days(d, ref) != 3 {
		t.Errorf("a January date seen at year end is next year's: %v", d)
	}
	if _, why, ok := en.Resolve(DateMention{Month: 2, Day: 30, Weekday: -1, Year: 2026}, ref); ok || why != ReasonNoSuchDate {
		t.Error("30 February must not resolve")
	}
	if _, _, ok := en.Resolve(DateMention{Weekday: 9}, ref); ok {
		t.Error("an impossible weekday must not resolve")
	}
	if d, _, ok := en.Resolve(DateMention{Month: 3, Day: 1, Weekday: -1, Year: -1 - 99}, ref); !ok || d.Year() != 2099 && d.Year() != 1999 {
		t.Errorf("two-digit year: %v", d)
	}
}

func TestAmounts(t *testing.T) {
	fr, _ := LocaleOf("fr")
	en, _ := LocaleOf("en")
	und, _ := LocaleOf(Undetermined)
	cases := []struct {
		l     *Locale
		text  string
		codes [][]string
		vals  []float64
		known []bool
	}{
		{fr, "Total : 1 234,50 € HT, soit 1 481,40 EUR TTC", [][]string{{"EUR"}, {"EUR"}}, []float64{1234.5, 1481.4}, []bool{true, true}},
		{en, "It costs $1,250.00 or ¥3000", [][]string{nil, {"CNY", "JPY"}}, []float64{1250, 3000}, []bool{true, true}},
		{und, "USD 1.234 and 2,5 CHF", [][]string{{"USD"}, {"CHF"}}, []float64{0, 2.5}, []bool{false, true}},
		{en, "EURO 12 and 12 EURO and XEUR 5", nil, nil, nil},
		{en, "１２３,５０ ＥＵＲ", [][]string{{"EUR"}}, []float64{123.5}, []bool{true}},
	}
	for _, c := range cases {
		got := c.l.Amounts(c.text)
		if len(got) != len(c.vals) {
			t.Errorf("%q: %+v", c.text, got)
			continue
		}
		for i, a := range got {
			if c.codes[i] != nil && !slices.Equal(a.Codes, c.codes[i]) || c.codes[i] == nil && len(a.Codes) < 2 ||
				a.Known != c.known[i] || a.Known && a.Value != c.vals[i] {
				t.Errorf("%q #%d: %+v", c.text, i, a)
			}
		}
	}
}

func TestSkeleton(t *testing.T) {
	if Skeleton("раураl") != Skeleton("paypal") || Skeleton("examp1e") != Skeleton("example") {
		t.Error("Cyrillic look-alikes and 1/l must share a skeleton")
	}
	if Skeleton("example") == Skeleton("exemple") || Skeleton("") != "" {
		t.Error("different letters must keep different skeletons")
	}
}
