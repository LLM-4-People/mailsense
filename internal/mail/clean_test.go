package mail

import (
	"strings"
	"testing"
	"unicode"
)

func TestCleanBodyRemovesWhatIsNotTheMessagesOwn(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"outlook fr reply header": {
			in:   "Bonjour,\r\nVoici le devis.\r\n\r\nDe : Paul Martin <paul@x.com>\r\nEnvoyé : 12 mars 2026 10:04\r\nÀ : Moi <me@y.com>\r\nObjet : devis\r\n\r\nAncien texte",
			want: "Bonjour,\nVoici le devis."},
		"outlook en reply header": {
			in:   "Thanks, done.\n\nFrom: Ann <ann@corp.com>\nSent: Monday, March 3, 2026 9:01 AM\nTo: Me\nSubject: RE: task\n\nold",
			want: "Thanks, done."},
		"german header with bold labels": {
			in:   "Danke!\n*Von:* Hans <h@de.de>\n*Gesendet:* 3.3.2026\n*An:* Ich\nalt",
			want: "Danke!"},
		"separator line": {
			in:   "Short answer.\n________________________________\nFrom: someone",
			want: "Short answer."},
		"original message caption": {
			in:   "OK pour moi.\n-----Message d'origine-----\nDe: x",
			want: "OK pour moi."},
		"bottom quote with intro": {
			in:   "Yes.\n\nOn Tue, Mar 3, 2026 at 9:00, Ann <ann@corp.com> wrote:\n> Can you?\n> Thanks",
			want: "Yes."},
		"interleaved quotes keep the answers": {
			in:   "See inline:\n> question one\nanswer one\n> question two\nanswer two",
			want: "See inline:\nanswer one\nanswer two"},
		"rfc 3676 signature": {
			in:   "Body text.\n-- \nJohn\nACME",
			want: "Body text."},
		"contact block": {
			in:   "Merci de votre retour.\n\nJean Dupont\nDirecteur\nTél. : +1 (514) 555-0123\njean@acme.ca\nwww.acme.ca",
			want: "Merci de votre retour."},
		"blank runs collapse": {
			in:   "a\n\n\n\nb   \n",
			want: "a\n\nb"},
	}
	for name, c := range cases {
		if got := CleanBody(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

func TestCleanBodyKeepsContentThatOnlyLooksLikeABoundary(t *testing.T) {
	keep := map[string]string{
		"form fields without address": "Inscription\nNom: Tremblay\nPrénom: Julie\nVille: Laval",
		"label lines at the very top": "From: ann@corp.com\nSent: 2026-03-03\nTo: me@y.com\nSubject: hi\n\nHello",
		"a long closing paragraph":    "Text.\n\n" + strings.Repeat("Call me at 514-555-0123 if anything comes up in the meantime please.\n", 2),
		"a contact block is not all":  "Jean Dupont\n514-555-0123\njean@acme.ca",
		"one contact line only":       "Merci.\n\nJean\n514-555-0123",
		"intro with an answer after":  "Details:\nthe answer",
	}
	for name, in := range keep {
		got := CleanBody(in)
		if name == "label lines at the very top" || name == "a long closing paragraph" || name == "a contact block is not all" {
			if !strings.Contains(got, Strip(strings.Split(in, "\n")[0])) {
				t.Errorf("%s: lost the content: %q", name, got)
			}
			continue
		}
		if got != Strip(in) {
			t.Errorf("%s: changed:\n got %q\nwant %q", name, got, in)
		}
	}
}

func TestCleanBodyNeverReturnsNothingForText(t *testing.T) {
	fwd := "---------- Forwarded message ---------\nFrom: a@b.com\nDate: 3 Mar 2026\nSubject: x\n\nForwarded text"
	if got := CleanBody(fwd); got != Strip(fwd) {
		t.Fatalf("a bare forward keeps its text: %q", got)
	}
	if got := CleanBody("> only quoted\n> lines"); got == "" {
		t.Fatal("only quotes: the original is better than nothing")
	}
	if got := CleanBody(" \n\t"); got != "" {
		t.Fatalf("blank stays blank: %q", got)
	}
}

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Réunion":        "reunion",
		"ÉBAUCHE déjà":   "ebauche deja",
		"Œuvre Straße":   "oeuvre strasse",
		"naïve Ångström": "naive angstrom",
		"日本":             "日本",
		// #5: every script, not only Latin-1/Extended-A (failed first).
		"Tiếng Việt":   "tieng viet",
		"Științe ȚARĂ": "stiinte tara",
		"Συνάντηση":    "συναντηση",
		"שָׁלוֹם":      "שלום",
		"ＦＵＬＬ ｗｉｄｔｈ":   "full width",
		"Йод мой":      "йод мой", // a Cyrillic mark is part of the letter
		"がっこう":         "がっこう",    // so is a Japanese one
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	folded, offsets := FoldMap("aÉœb")
	if folded != "aeoeb" || len(offsets) != len(folded) {
		t.Fatalf("%q %v", folded, offsets)
	}
	if want := []int{0, 1, 3, 3, 5}; !equalInts(offsets, want) {
		t.Fatalf("offsets %v, want %v", offsets, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Review 2b #9 (failed first: each was cut to its first line or emptied
// of its list): an underlined title, a rule under a heading and a status
// list are the message's own text; a rule or header block that starts a
// real reply is still cut. English and French.
func TestCleanBodyKeepsRulesAndListsOfItsOwn(t *testing.T) {
	keep := []string{
		"Quarterly report\n================\nRevenue grew 12% in Q3 thanks to the new contract with Acme.\nCosts were flat.",
		"Rapport trimestriel\n===================\nLe chiffre d'affaires a crû de 12 % au T3 grâce au contrat Acme.\nLes coûts sont stables.",
		"Hi team,\n\nNotes from today:\n----------------------------------------\n1. Budget approved\n2. Hiring freeze lifted",
		"Bonjour l'équipe,\n\nNotes de la réunion :\n----------------------------------------\n1. Budget approuvé\n2. Gel des embauches levé",
		"Status:\nDate: 2026-10-01\nOwner: bob@acme.com\nPhase: 3\nThe migration is done.",
		"Statut :\nDate : 2026-10-01\nResponsable : bob@acme.com\nPhase : 3\nLa migration est terminée.",
	}
	for _, in := range keep {
		if got := CleanBody(in); got != collapseBlank(strings.Split(in, "\n")) {
			t.Errorf("own text was cut:\n in %q\nout %q", in, got)
		}
	}
	cut := map[string]string{
		"Thanks.\n\n________________________________\nFrom: Ann <ann@corp.com>\nSent: 3 March 2026 09:01\nSubject: x\nold": "Thanks.",
		"Merci.\n\n________________________________\nDe : Paul <p@x.com>\nEnvoyé : 3 mars 2026 09:01\nObjet : x\nancien":   "Merci.",
		"Yes.\n================\n> quoted question":                                     "Yes.",
		"Oui.\n\nLe 3 mars 2026 à 09:01, Paul <p@x.com> a écrit :\n> question\n> merci": "Oui.",
	}
	for in, want := range cut {
		if got := CleanBody(in); got != want {
			t.Errorf("reply not cut:\n in %q\ngot %q want %q", in, got, want)
		}
	}
}

func TestCleanLimitsAreValid(t *testing.T) {
	r := cleanLimits()
	if r.RulesVersion < 1 || r.HeaderMinLines < 2 || r.RuleLookahead < 1 || r.IntroMaxRunes < 1 || r.ContactMaxLines < 1 || r.ContactMaxRunes < 1 || r.ContactMinHits < 1 || r.Doc == "" ||
		r.RuleMinChars < 1 || r.CaptionMinRule < 1 || r.CaptionMaxRunes < 1 || r.LabelMaxRunes < 1 || r.PhoneMinChars < 3 {
		t.Fatalf("clean.json: %+v", r)
	}
	// The limits moved from Go patterns to clean.json unchanged.
	for got, want := range map[string]string{
		r.caption.String():   `^\s*[-_=*]{3,}[^-_=*]{1,40}[-_=*]{3,}\s*$`,
		r.rule.String():      `^\s*[-_=*]{8,}\s*$`,
		r.labelLine.String(): `^\s*\*?\p{L}[\p{L}\p{M} .'’-]{0,24}\*?\s?:\s*\S`,
		r.phone.String():     `\+?\(?\p{Nd}[\p{Nd} ().-]{6,}\p{Nd}`,
	} {
		if got != want {
			t.Errorf("pattern %s, want %s", got, want)
		}
	}
	if Links("see https://a.b and www.c.d, http://e") != 3 {
		t.Fatal("links")
	}
}

// Found by the fixture run (panic: slice bounds out of range): a quoted
// line ending in ":" inside the bottom quote is not an intro.
func TestCleanBodyQuotedIntroInsideTheQuote(t *testing.T) {
	for in, want := range map[string]string{
		"Yes.\n\n> On Monday, Ann wrote:\n> the question":           "Yes.",
		"Oui.\n> Le lundi, Paul a écrit :\n>> la question\n> merci": "Oui.",
	} {
		if got := CleanBody(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

// #8: header labels with combining marks (Thai, Hindi), the full-width
// and the NBSP-spaced colon, non-ASCII digits and right-to-left marks no
// longer hide a reply boundary (failed first: each case kept the quote).
func TestCleanBodyI18nBoundaries(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"thai labels (combining marks)": {
			in:   "ขอบคุณครับ\n\nจาก: สมชาย <somchai@example.org>\nส่ง: 3 มีนาคม 2026 09:01\nถึง: amelie@example.com\nเรื่อง: ใบแจ้งหนี้\n\nข้อความเดิม",
			want: "ขอบคุณครับ"},
		"hindi labels": {
			in:   "धन्यवाद\n\nप्रेषक: राहुल <rahul@example.org>\nभेजा गया: 3 मार्च 2026\nप्रति: amelie@example.com\n\nपुराना",
			want: "धन्यवाद"},
		"full-width colon (chinese)": {
			in:   "好的，谢谢。\n\n发件人：李娜 <li.na@example.org>\n发送时间：2026年3月3日 9:01\n收件人：amelie@example.com\n主题：报告\n\n旧内容",
			want: "好的，谢谢。"},
		"nbsp before the colon (french)": {
			in:   "Merci.\n\nDe\u00a0: Paul <paul@example.org>\nEnvoyé\u202f: 12 mars 2026 10:04\nÀ\u00a0: amelie@example.com\n\nAncien",
			want: "Merci."},
		"arabic-indic digits": {
			in:   "شكرا\n\nمن: عمر <omar@example.org>\nأرسلت: ٣ مارس ٢٠٢٦\nإلى: amelie@example.com\n\nقديم",
			want: "شكرا"},
		"rtl mark before the intro and the quote": {
			in:   "תודה.\n\n\u200fב-3 במרץ 2026, נועה <noa@example.org> כתבה:\n\u200f> האם אפשר?\n> תודה",
			want: "תודה."},
		"signature with eastern digits": {
			in:   "Merci de votre retour.\n\nRahul\nफ़ोन: +९१ ९८७६५ ४३२१०\nrahul@example.org",
			want: "Merci de votre retour."},
	}
	for name, c := range cases {
		if got := CleanBody(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

func TestFoldRulesAreValid(t *testing.T) {
	r := folding()
	for l, v := range r.letters {
		if v == "" || Fold(string(l)) != v {
			t.Errorf("fold.json: %q → %q", l, v)
		}
	}
	for s := range r.strip {
		if unicode.Scripts[s] == nil {
			t.Errorf("fold.json: %q is not a script", s)
		}
	}
}

func TestSignatureTextKeepsTheSenderAndDropsTheQuotedOwner(t *testing.T) {
	body := "Bonjour,\n\nVoici les documents demandés.\n\n--\nJulie Roy\nClimatisation Exemple Inc.\nTél. : 819 555-0123\n\nDe : Alex Morgan\nEnvoyé : 28 octobre 2026 09:00\nÀ : Julie Roy\nObjet : Documents\n\n--\nAlex Morgan\nAcme Industries Inc.\nTél. : 819 555-9999\n"
	text := SignatureText(body)
	if text == "" {
		t.Fatal("signature text is empty")
	}
	if !strings.Contains(text, "Julie Roy") || !strings.Contains(text, "Climatisation Exemple Inc.") {
		t.Fatalf("sender signature missing: %q", text)
	}
	if strings.Contains(text, "Acme Industries") || strings.Contains(text, "Alex Morgan") {
		t.Fatalf("quoted owner signature leaked: %q", text)
	}
}

func TestEvidenceTextCutsAMinimalQuotedOwner(t *testing.T) {
	body := "Bonjour,\n\nVoici le suivi demandé.\n\nMerci,\nRobert Mercier\nClimatisation Exemple\nTél. : 819 555-0123\n\nDe : Alex Morgan\nEnvoyé : 28 octobre 2026 09:00\n\n--\nAlex Morgan\nAcme Industries Inc.\nTél. : 819 555-9999"
	evidence := EvidenceText(body)
	if strings.Contains(evidence, "Alex Morgan") || strings.Contains(evidence, "Acme Industries") {
		t.Fatalf("minimal quote leaked into evidence: %q", evidence)
	}
	if !strings.Contains(evidence, "819 555-0123") {
		t.Fatalf("sender evidence missing: %q", evidence)
	}
}

func TestEvidenceTextFallsBackToTheLastLines(t *testing.T) {
	body := "Bonjour,\n\nVoici le suivi demandé.\n\nMerci,\nRobert Mercier\nClimatisation Exemple\nTél. : 819 555-0123\n\nSuivez-nous sur Facebook!\nwww.climatisation-exemple.test\n\nDe : Alex Morgan\nEnvoyé : 28 octobre 2026 09:00\n\n--\nAlex Morgan\nAcme Industries Inc.\nTél. : 819 555-9999"
	text := EvidenceText(body)
	for _, want := range []string{"Robert Mercier", "Climatisation Exemple", "819 555-0123"} {
		if !strings.Contains(text, want) {
			t.Fatalf("evidence fallback %q missing %q", text, want)
		}
	}
	if strings.Contains(text, "Acme Industries") || strings.Contains(text, "Alex Morgan") {
		t.Fatalf("quoted owner signature leaked into evidence: %q", text)
	}
}

func TestSignatureTextStaysStrictWithoutASignature(t *testing.T) {
	body := "Bonjour,\n\nMerci,\nRobert Mercier"
	if text := SignatureText(body); text != "" {
		t.Fatalf("SignatureText invented a signature: %q", text)
	}
}

func TestSignatureKinds(t *testing.T) {
	for body, want := range map[string]string{
		"Bonjour,\nmerci.\n-- \nAnn\n":                                   SignatureDelimiter,
		"Hi,\nsee below.\n\nBob Smith\n+1 555 123 4567\nbob@example.org": SignatureContacts,
		"Hi,\nno signature here.":                                        SignatureNone,
		"":                                                               SignatureNone,
		"Thanks\n\n> quoted\n> -- \n> old signature":                     SignatureNone,
		"Short\n\n\n\n":                                                  SignatureNone,
	} {
		if got := Signature(body); got != want {
			t.Errorf("%q: %s, want %s", body, got, want)
		}
	}
}

// Lead decision 2026-10-15 (deadline_same_day: 36 of 38 hits were
// replies naming their own date): Outlook's plain-text Body shows a quoted
// HTML block (Gmail's, Apple Mail's <blockquote>) indented by a TAB, not
// with ">", after an attribution line, often indented with it. An
// attribution is found by its structure alone — at most intro_max_runes,
// ending in a colon (full-width too), naming an address and holding a
// digit — in any language, and cuts the text when a quoted or indented
// line follows it.
func TestCleanBodyCutsAttributionsBeforeIndentedQuotes(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"gmail reply as outlook renders it": {
			in:   "Merci, c'est noté.\r\n\r\nLe mar. 14 oct. 2026 à 10:00, Ann Lee <ann@x.org <mailto:ann@x.org> > a écrit :\r\n\r\n\tBonjour,\r\n\tLe rapport est prêt pour le 14 octobre.\r\n",
			want: "Merci, c'est noté."},
		"the attribution indented with the quote": {
			in:   "OK for me.\n\nPaul\n\n\tOn 14 Oct 2026, at 10:00, Ann <ann@x.org> wrote:\n\t\n\tHello, due today.",
			want: "OK for me.\n\nPaul"},
		"a full-width colon": {
			in:   "了解しました。\n\n2026年10月14日(火) 10:00 Ann <ann@x.org>：\n\n\t元の本文",
			want: "了解しました。"},
		"right-to-left text": {
			in:   "شكرا لك\n\nفي ١٤ أكتوبر ٢٠٢٦، كتب Ann <ann@x.org>:\n\tالنص الأصلي",
			want: "شكرا لك"},
		"a > quote to the end": {
			in:   "Done.\n\nEl 14 oct 2026, a las 10:00, Ann <ann@x.org> escribió:\n\n> ¿Listo?\n> Gracias",
			want: "Done."},
		// Lead decision 2026-10-15 (review of the crash-safety round, #8):
		// the own text is the author's non-quoted lines — the quoted block
		// and its attribution are dropped, an answer after it is kept (an
		// inline reply). This case read "Done." before.
		"a > quote with an answer after it": {
			in:   "Done.\n\nEl 14 oct 2026, a las 10:00, Ann <ann@x.org> escribió:\n\n> ¿Listo?\nMore of my own text",
			want: "Done.\n\nMore of my own text"},
	}
	for name, c := range cases {
		if got := CleanBody(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
	keep := map[string]string{
		"an own line with an address and a date, then prose": "Hello,\nWrite to ann@x.org before 10 Oct:\nthe forms and the badge.",
		"no address":                 "Hello,\nThe 2 steps:\n\tstep one\n\tstep two",
		"no digit":                   "Hello,\nAsk ann@x.org about it:\n\tthe form",
		"an indented line, no colon": "Hello,\nAnn <ann@x.org> 14 Oct 2026\n\tHello",
	}
	for name, in := range keep {
		if got := CleanBody(in); got != Strip(collapseBlank(strings.Split(in, "\n"))) {
			t.Errorf("%s: cut to %q", name, got)
		}
	}
}

// Review of the crash-safety round, #8 (failed first): the attribution
// rule looked at the next line only, so it cut real own text — an agenda
// line naming an address and a date and ending in ":" before a tab list
// left only the greeting; a quoted "> On …, x wrote:" in an inline reply
// lost every interleaved answer. The rule never applies on a quoted line,
// and cuts only when every following non-blank line, to the end or to a
// recognised boundary, is quoted or indented. An attribution is never the
// message's own text, so its date never reads as one of the message's.
func TestCleanBodyAttributionsNeverCutOwnText(t *testing.T) {
	keep := []string{
		"Hello team,\nAgenda of the call with ann@x.org on 14 Oct 2026:\n\tBudget review\n\tHiring plan\nThanks,\nBob",
		"Bonjour,\nOrdre du jour avec ann@x.org le 14 oct. 2026 :\n\tBudget\n\tEmbauches\n\nMerci,\nPaul",
		"Hi,\n\nOn 14 Oct 2026, Ann <ann@x.org> wrote:\n\tCan you send the file?\nYes, attached.\n\tAnd the budget?\nApproved.",
	}
	for _, in := range keep {
		if got := CleanBody(in); got != collapseBlank(strings.Split(in, "\n")) {
			t.Errorf("own text was cut:\n in %q\nout %q", in, got)
		}
	}
	for name, c := range map[string]struct{ in, want string }{
		"inline reply under a quoted attribution": {
			in:   "Hi Ann,\nsee my answers below.\n\n> On 14 Oct 2026, at 10:00, Ann <ann@x.org> wrote:\n> Can you send the file?\nYes, attached.\n> And the budget?\nApproved yesterday.",
			want: "Hi Ann,\nsee my answers below.\n\nYes, attached.\nApproved yesterday."},
		"an indented quote to a signature delimiter": {
			in:   "Merci.\n\nLe 14 oct. 2026 à 10:00, Ann <ann@x.org> a écrit :\n\tBonjour\n-- \nAnn",
			want: "Merci."},
		"an indented quote to a reply header": {
			in:   "OK.\n\nOn 14 Oct 2026, Ann <ann@x.org> wrote:\n\tYes\nFrom: Bob <bob@x.org>\nSent: 13 Oct 2026\nTo: Ann <ann@x.org>\nold",
			want: "OK."},
	} {
		if got := CleanBody(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
	// An attribution on the first line (nothing of the message's own
	// before it) is no own text either: its date is not the message's.
	for _, in := range []string{
		"\tOn 14 Oct 2026, Ann <ann@x.org> wrote:\n\tHello",
		"On 14 Oct 2026, Ann <ann@x.org> wrote:\n> Can you?\n> Thanks",
		"Le 14 oct. 2026 à 10:00, Ann <ann@x.org> a écrit :\r\n\r\n\tBonjour,\r\n\tVoici.",
	} {
		if got := CleanBody(in); strings.Contains(got, "2026") || got == "" {
			t.Errorf("the attribution's date leaked into %q (from %q)", got, in)
		}
	}
}
