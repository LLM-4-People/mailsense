package qa

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/locale"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The answer-language corpus is fixed synthetic data. Each answer is a
// realistic generated answer to one question, and the quoted entries carry
// the short mail sentence a reply quotes. The corpus is deliberately kept
// separate from every other test: the counts logged by the corpus test are
// the round's measured baseline and after numbers.
//
// Host answers: six clearly English, six clearly French, six French answers
// quoting a short English mail sentence, six English answers quoting a short
// French mail sentence.
// answerLanguageNotice is the wire value the corpus pins independently of
// the production constant, so the corpus test compiles and fails on a
// missing notice instead of failing to build.
const answerLanguageNotice = "answer_language"

type answerLanguageCase struct {
	name     string
	language string // the language the owner requested
	answer   string
	quoted   bool // part of the 12-case quoted-mail false-reject metric
}

var answerLanguageHosts = []answerLanguageCase{
	{
		name: "en-clearly-1", language: "en",
		answer: "The invoice was approved by the finance team. The payment will be sent before the end of the week.",
	},
	{
		name: "en-clearly-2", language: "en",
		answer: "The meeting is confirmed for Thursday at ten. We will meet in room 302.",
	},
	{
		name: "en-clearly-3", language: "en",
		answer: "The contract is signed by both parties. A copy was sent to the legal department.",
	},
	{
		name: "en-clearly-4", language: "en",
		answer: "Yes, I confirm the delivery address. The delivery will take place next Monday before noon.",
	},
	{
		name: "en-clearly-5", language: "en",
		answer: "We received your request. The support team will review it this week.",
	},
	{
		name: "en-clearly-6", language: "en",
		answer: "The project budget is approved. Expenses will be reviewed every month by management.",
	},
	{
		name: "fr-clearly-1", language: "fr",
		answer: "La facture a été approuvée par l'équipe financière. Le paiement sera envoyé avant la fin de la semaine.",
	},
	{
		name: "fr-clearly-2", language: "fr",
		answer: "La réunion est confirmée pour jeudi à dix heures. Nous nous retrouverons dans la salle 302.",
	},
	{
		name: "fr-clearly-3", language: "fr",
		answer: "Le contrat est signé par les deux parties. Une copie a été transmise au service juridique.",
	},
	{
		name: "fr-clearly-4", language: "fr",
		answer: "Oui, je confirme l'adresse de livraison. La livraison aura lieu lundi prochain avant midi.",
	},
	{
		name: "fr-clearly-5", language: "fr",
		answer: "Nous avons bien reçu votre demande. Elle sera traitée par l'équipe de support cette semaine.",
	},
	{
		name: "fr-clearly-6", language: "fr",
		answer: "Le budget du projet est approuvé. Les dépenses seront suivies chaque mois par la direction.",
	},
	{
		name: "fr-quotes-en-1", language: "fr", quoted: true,
		answer: "Merci beaucoup, c'est bien noté : je m'occupe de la facture corrigée dès aujourd'hui.\n\n" +
			"> Could you send the revised invoice before Friday?",
	},
	{
		name: "fr-quotes-en-2", language: "fr", quoted: true,
		answer: "La réunion est bien confirmée pour jeudi à dix heures dans la salle 302.\n\n" +
			"> The meeting is confirmed for Thursday at ten, room 302.",
	},
	{
		name: "fr-quotes-en-3", language: "fr", quoted: true,
		answer: "> The invoice was approved by finance and will be paid this week.\n\n" +
			"La facture est donc approuvée et le paiement suivra cette semaine.",
	},
	{
		name: "fr-quotes-en-4", language: "fr", quoted: true,
		answer: "Oui, je confirme l'adresse de livraison et la date de lundi prochain.\n\n" +
			"> Please confirm the delivery address and the delivery date.",
	},
	{
		name: "fr-quotes-en-5", language: "fr", quoted: true,
		answer: "Le contrat est signé par les deux parties et une copie part au service juridique.\n\n" +
			"> The contract was signed by both parties and sent to legal.",
	},
	{
		name: "fr-quotes-en-6", language: "fr", quoted: true,
		answer: "Nous avons bien reçu votre demande et nous la transmettons à l'équipe de support.\n\n" +
			"> We received your request and forwarded it to support.",
	},
	{
		name: "en-quotes-fr-1", language: "en", quoted: true,
		answer: "Thanks, noted: I will send the revised invoice today.\n\n" +
			"> Merci d'envoyer la facture corrigée avant vendredi.",
	},
	{
		name: "en-quotes-fr-2", language: "en", quoted: true,
		answer: "The meeting is confirmed for Thursday at ten in room 302.\n\n" +
			"> La réunion est confirmée pour jeudi à dix heures, salle 302.",
	},
	{
		name: "en-quotes-fr-3", language: "en", quoted: true,
		answer: "> La facture a été approuvée et sera payée cette semaine.\n\n" +
			"The invoice is therefore approved and the payment will follow this week.",
	},
	{
		name: "en-quotes-fr-4", language: "en", quoted: true,
		answer: "Yes, I confirm the delivery address and the date for next Monday.\n\n" +
			"> Veuillez confirmer l'adresse et la date de livraison.",
	},
	{
		name: "en-quotes-fr-5", language: "en", quoted: true,
		answer: "The contract is signed by both parties and a copy goes to legal.\n\n" +
			"> Le contrat a été signé et envoyé au service juridique.",
	},
	{
		name: "en-quotes-fr-6", language: "en", quoted: true,
		answer: "We received your request and forwarded it to the support team.\n\n" +
			"> Nous avons reçu votre demande et l'avons transmise au support.",
	},
}

// Wrong-language answers: no French answer for a French request and no
// English answer for an English request. One German and one Spanish answer
// prove that the detector is not limited to the en/fr pair.
var answerLanguageWrong = []answerLanguageCase{
	{
		name: "request-fr-english-1", language: "fr",
		answer: "The invoice was approved by the finance team. The payment will be sent before the end of the week.",
	},
	{
		name: "request-fr-english-2", language: "fr",
		answer: "The meeting is confirmed for Thursday at ten. We will meet in room 302.",
	},
	{
		name: "request-fr-english-3", language: "fr",
		answer: "The contract is signed by both parties. A copy was sent to the legal department.",
	},
	{
		name: "request-fr-english-4", language: "fr",
		answer: "Yes, I confirm the delivery address. The delivery will take place next Monday before noon.",
	},
	{
		name: "request-fr-english-5", language: "fr",
		answer: "We received your request. The support team will review it this week.",
	},
	{
		name: "request-fr-english-6", language: "fr",
		answer: "The project budget is approved. Expenses will be reviewed every month by management.",
	},
	{
		name: "request-fr-german", language: "fr",
		answer: "Die Rechnung wurde am Montag von der Finanzabteilung genehmigt und die Zahlung erfolgt vor Ende der Woche.",
	},
	{
		name: "request-en-french-1", language: "en",
		answer: "La facture a été approuvée par l'équipe financière. Le paiement sera envoyé avant la fin de la semaine.",
	},
	{
		name: "request-en-french-2", language: "en",
		answer: "La réunion est confirmée pour jeudi à dix heures. Nous nous retrouverons dans la salle 302.",
	},
	{
		name: "request-en-french-3", language: "en",
		answer: "Le contrat est signé par les deux parties. Une copie a été transmise au service juridique.",
	},
	{
		name: "request-en-french-4", language: "en",
		answer: "Oui, je confirme l'adresse de livraison. La livraison aura lieu lundi prochain avant midi.",
	},
	{
		name: "request-en-french-5", language: "en",
		answer: "Nous avons bien reçu votre demande. Elle sera traitée par l'équipe de support cette semaine.",
	},
	{
		name: "request-en-french-6", language: "en",
		answer: "Le budget du projet est approuvé. Les dépenses seront suivies chaque mois par la direction.",
	},
	{
		name: "request-en-spanish", language: "en",
		answer: "La factura fue aprobada por el equipo financiero el lunes y el pago se enviará antes del final de la semana.",
	},
}

// Short or ambiguous answers carry too few letters to identify a language.
// They stay unjudged whatever the requested language is.
var answerLanguageShort = []answerLanguageCase{
	{name: "short-ok", language: "en", answer: "OK [1]."},
	{name: "short-subject", language: "fr", answer: "Re: invoice [1]."},
	{name: "short-person", language: "en", answer: "Nadia Tremblay [1]."},
}

// The independent corpus answers the audit finding that 12 of the original
// 14 wrong-language cases were verbatim copies of the accepted clear-host
// sentences. Every sentence below is new and shares no wording with the
// clear-host corpus. It is an additional corpus: the original 24/12/14/3
// counts are unchanged.
var answerLanguageIndependentWrong = []answerLanguageCase{
	{
		name: "independent-fr-english-1", language: "fr",
		answer: "The supplier confirmed that the shipment left the warehouse on Tuesday morning.",
	},
	{
		name: "independent-fr-english-2", language: "fr",
		answer: "Please review the attached estimate before the meeting on Friday.",
	},
	{
		name: "independent-fr-english-3", language: "fr",
		answer: "The finance director asked for a revised forecast by the end of the month.",
	},
	{
		name: "independent-fr-english-4", language: "fr",
		answer: "Our team will prepare the purchase order once the price is approved.",
	},
	{
		name: "independent-fr-english-5", language: "fr",
		answer: "The client accepted the proposal and asked us to start next week.",
	},
	{
		name: "independent-fr-english-6", language: "fr",
		answer: "All the documents were archived after the audit closed last month.",
	},
	{
		name: "independent-en-french-1", language: "en",
		answer: "Le fournisseur a confirmé que l'expédition a quitté l'entrepôt mardi matin.",
	},
	{
		name: "independent-en-french-2", language: "en",
		answer: "Merci de relire le devis ci-joint avant la réunion de vendredi.",
	},
	{
		name: "independent-en-french-3", language: "en",
		answer: "La directrice financière demande une prévision révisée avant la fin du mois.",
	},
	{
		name: "independent-en-french-4", language: "en",
		answer: "Notre équipe préparera le bon de commande dès que le prix sera validé.",
	},
	{
		name: "independent-en-french-5", language: "en",
		answer: "Le client a accepté la proposition et nous a demandé de commencer la semaine prochaine.",
	},
	{
		name: "independent-en-french-6", language: "en",
		answer: "Tous les documents ont été archivés après la clôture de l'audit le mois dernier.",
	},
}

// Independent quoted cases carry a multi-line quoted mail and an attribution
// line in the host's language, so CleanBody must drop both the attribution
// and every quoted line before the host language is judged.
var answerLanguageIndependentQuoted = []answerLanguageCase{
	{
		name: "independent-fr-quotes-en-1", language: "fr", quoted: true,
		answer: "Merci, je confirme que le paiement partira vendredi au plus tard.\n\n" +
			"Le 24 octobre, Alice a écrit :\n" +
			"> The invoice is approved and ready for payment.\n" +
			"> Please confirm the bank details before Friday.\n" +
			"> We will send the receipt once the transfer is complete.",
	},
	{
		name: "independent-fr-quotes-en-2", language: "fr", quoted: true,
		answer: "C'est noté, je prépare la réponse pour la réunion de lundi.\n\n" +
			"Le 25 octobre, Bob a écrit :\n" +
			"> The client asked for a revised schedule.\n" +
			"> Can you send the new dates before the weekend?\n" +
			"> We need them to confirm the kickoff.",
	},
	{
		name: "independent-en-quotes-fr-1", language: "en", quoted: true,
		answer: "Thanks, I confirm the payment will leave on Friday at the latest.\n\n" +
			"On 24 October, Alice wrote:\n" +
			"> La facture est approuvée et prête pour le paiement.\n" +
			"> Merci de confirmer les coordonnées bancaires avant vendredi.\n" +
			"> Nous enverrons le reçu dès que le virement sera terminé.",
	},
	{
		name: "independent-en-quotes-fr-2", language: "en", quoted: true,
		answer: "Noted, I will prepare the reply for Monday's meeting.\n\n" +
			"On 25 October, Bob wrote:\n" +
			"> Le client demande un calendrier révisé.\n" +
			"> Peux-tu envoyer les nouvelles dates avant le week-end ?\n" +
			"> Nous en avons besoin pour confirmer le lancement.",
	},
}

// Independent short answers stay below the 30-letter threshold.
var answerLanguageIndependentShort = []answerLanguageCase{
	{name: "independent-short-ok", language: "fr", answer: "D'accord [1]."},
	{name: "independent-short-no", language: "en", answer: "Not now [1]."},
}

// logLanguageDetections reports the raw detector over the whole answer and
// the quote-stripped detector at min_letters 30, so the ledger's before and
// after counts are reproducible from the test log.
func logLanguageDetections(t *testing.T, cases []answerLanguageCase) (rawAgree, rawFalseRejects, strippedAccepted int) {
	t.Helper()
	for _, c := range cases {
		raw := locale.Language(c.answer, 20)
		cleaned := mail.CleanBody(c.answer)
		stripped := locale.Language(cleaned, 30)
		if raw.Tag == c.language {
			rawAgree++
		}
		if c.quoted && raw.Tag != c.language {
			rawFalseRejects++
		}
		if stripped.Tag == c.language || stripped.Tag == locale.Undetermined {
			strippedAccepted++
		}
		t.Logf("%s: requested=%s raw=%s stripped=%s quoted=%v", c.name, c.language,
			raw.Tag, stripped.Tag, c.quoted)
	}
	return rawAgree, rawFalseRejects, strippedAccepted
}

// setGeneratedAnswerContent makes the fake generator return one grounded
// JSON answer through the ordinary planner route.
func setGeneratedAnswerContent(t *testing.T, w *testWorld, answer string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"route": "answer", "answer": answer, "used": []int{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.gen.content = string(payload)
}

// askCorpusAnswer asks one corpus question through the real Ask path, with
// the fake generator returning the corpus answer as one grounded JSON
// answer. The question carries the case index so no two cases share a cache
// key.
func askCorpusAnswer(t *testing.T, w *testWorld, index int, language, answer string) AskView {
	t.Helper()
	question := fmt.Sprintf("Que s'est-il passé avec le budget, cas %d ?", index)
	if language == "en" {
		question = fmt.Sprintf("What happened with the budget in case %d?", index)
	}
	setGeneratedAnswerContent(t, w, answer)
	view, err := w.svc.Ask(t.Context(), question, language)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// TestAnswerLanguageCorpus measures the fixed corpus end to end. Before the
// language notice existed every ask returned no notice, so the three
// wrong-language assertions failed and the host, quoted and short
// assertions passed without exercising a check. After the fix the same
// assertions prove the notice without touching an accepted answer.
func TestAnswerLanguageCorpus(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Budget", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z", Body: "body 1"}

	rawAgree, rawFalseRejects, strippedAccepted := logLanguageDetections(t, answerLanguageHosts)
	t.Logf("corpus raw strict agreement %d/24, raw quoted-mail false rejects %d/12, quote-stripped host acceptance %d/24",
		rawAgree, rawFalseRejects, strippedAccepted)
	if strippedAccepted != len(answerLanguageHosts) {
		t.Errorf("quote-stripped host acceptance %d/%d, want 24/24",
			strippedAccepted, len(answerLanguageHosts))
	}

	accepted, quotedFalseRejects := 0, 0
	for i, c := range answerLanguageHosts {
		view := askCorpusAnswer(t, w, i, c.language, c.answer)
		if view.Notice == answerLanguageNotice {
			if c.quoted {
				quotedFalseRejects++
			}
			t.Errorf("%s: a %s answer was rejected as another language", c.name, c.language)
			continue
		}
		accepted++
	}
	t.Logf("host accepted %d/%d, quoted-mail false rejects %d/12",
		accepted, len(answerLanguageHosts), quotedFalseRejects)

	noticed := 0
	for i, c := range answerLanguageWrong {
		view := askCorpusAnswer(t, w, i+len(answerLanguageHosts), c.language, c.answer)
		t.Logf("%s: notice=%q", c.name, view.Notice)
		if view.Notice != answerLanguageNotice {
			t.Errorf("%s: a wrong-language answer carried notice %q, want %q",
				c.name, view.Notice, answerLanguageNotice)
			continue
		}
		noticed++
	}
	t.Logf("wrong-language answers noticed %d/%d", noticed, len(answerLanguageWrong))

	unjudged := 0
	for i, c := range answerLanguageShort {
		view := askCorpusAnswer(t, w, i+len(answerLanguageHosts)+len(answerLanguageWrong), c.language, c.answer)
		t.Logf("%s: notice=%q", c.name, view.Notice)
		if view.Notice == answerLanguageNotice {
			t.Errorf("%s: a short answer was judged", c.name)
			continue
		}
		unjudged++
	}
	t.Logf("short/ambiguous answers unjudged %d/%d", unjudged, len(answerLanguageShort))

	if accepted != len(answerLanguageHosts) {
		t.Errorf("host accepted %d/%d, want 24/24", accepted, len(answerLanguageHosts))
	}
	if quotedFalseRejects != 0 {
		t.Errorf("quoted-mail false rejects %d/12, want 0/12", quotedFalseRejects)
	}
	if noticed != len(answerLanguageWrong) {
		t.Errorf("wrong-language notices %d/%d, want 14/14", noticed, len(answerLanguageWrong))
	}
	if unjudged != len(answerLanguageShort) {
		t.Errorf("short/ambiguous unjudged %d/%d, want 3/3", unjudged, len(answerLanguageShort))
	}
}

// TestAnswerLanguageCorpusIndependent measures the independent corpus end to
// end through the same Ask path. It is an additional corpus, not a
// replacement: a failure here is a real detector or finalizer defect, not a
// weaker copy of the original corpus.
func TestAnswerLanguageCorpusIndependent(t *testing.T) {
	w := newWorld(t, false)
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "Budget", SenderName: "Alice",
		SenderAddress: "alice@example.com", Instant: "2026-10-25T10:00:00Z", Body: "body 1"}

	// The raw detector sees the independent wrong answers too: every one
	// must disagree with the requested language, raw and quote-stripped.
	rawRequested, strippedRequested := 0, 0
	for _, c := range answerLanguageIndependentWrong {
		raw := locale.Language(c.answer, 20)
		stripped := locale.Language(mail.CleanBody(c.answer), 30)
		if raw.Tag == c.language {
			rawRequested++
		}
		if stripped.Tag == c.language {
			strippedRequested++
		}
		t.Logf("%s: requested=%s raw=%s stripped=%s", c.name, c.language, raw.Tag, stripped.Tag)
	}
	t.Logf("independent wrong answers read as the requested language: raw %d/%d, quote-stripped %d/%d",
		rawRequested, len(answerLanguageIndependentWrong), strippedRequested, len(answerLanguageIndependentWrong))

	noticed := 0
	const wrongBase = 100
	for i, c := range answerLanguageIndependentWrong {
		view := askCorpusAnswer(t, w, wrongBase+i, c.language, c.answer)
		t.Logf("%s: notice=%q", c.name, view.Notice)
		if view.Notice != answerLanguageNotice {
			t.Errorf("%s: a wrong-language answer carried notice %q, want %q",
				c.name, view.Notice, answerLanguageNotice)
			continue
		}
		noticed++
	}
	t.Logf("independent wrong-language notices %d/%d", noticed, len(answerLanguageIndependentWrong))

	rawAgree, rawFalseRejects, strippedAccepted := logLanguageDetections(t, answerLanguageIndependentQuoted)
	t.Logf("independent quoted hosts: raw strict agreement %d/%d, raw false rejects %d/%d, quote-stripped acceptance %d/%d",
		rawAgree, len(answerLanguageIndependentQuoted), rawFalseRejects, len(answerLanguageIndependentQuoted),
		strippedAccepted, len(answerLanguageIndependentQuoted))
	accepted := 0
	for i, c := range answerLanguageIndependentQuoted {
		view := askCorpusAnswer(t, w, wrongBase+len(answerLanguageIndependentWrong)+i, c.language, c.answer)
		t.Logf("%s: notice=%q", c.name, view.Notice)
		if view.Notice == answerLanguageNotice {
			t.Errorf("%s: a %s host answer quoting other-language mail was rejected",
				c.name, c.language)
			continue
		}
		accepted++
	}
	t.Logf("independent quoted host acceptance %d/%d", accepted, len(answerLanguageIndependentQuoted))

	unjudged := 0
	for i, c := range answerLanguageIndependentShort {
		view := askCorpusAnswer(t, w, wrongBase+len(answerLanguageIndependentWrong)+len(answerLanguageIndependentQuoted)+i,
			c.language, c.answer)
		t.Logf("%s: notice=%q", c.name, view.Notice)
		if view.Notice == answerLanguageNotice {
			t.Errorf("%s: a short answer was judged", c.name)
			continue
		}
		unjudged++
	}
	t.Logf("independent short unjudged %d/%d", unjudged, len(answerLanguageIndependentShort))

	if noticed != len(answerLanguageIndependentWrong) {
		t.Errorf("independent wrong-language notices %d/%d, want 12/12",
			noticed, len(answerLanguageIndependentWrong))
	}
	if accepted != len(answerLanguageIndependentQuoted) {
		t.Errorf("independent quoted host acceptance %d/%d, want 4/4",
			accepted, len(answerLanguageIndependentQuoted))
	}
	if unjudged != len(answerLanguageIndependentShort) {
		t.Errorf("independent short unjudged %d/%d, want 2/2",
			unjudged, len(answerLanguageIndependentShort))
	}
}

// TestFinalizeGeneratedAnswerLanguage pins the shared post-grounding check:
// only a confident mismatch on an otherwise clean generated view adds the
// notice, and it never changes the answer, citations, mode or provider.
func TestFinalizeGeneratedAnswerLanguage(t *testing.T) {
	const englishLong = "The invoice was approved by the finance team. The payment will be sent before the end of the week."
	const frenchLong = "La facture a été approuvée par l'équipe financière. Le paiement sera envoyé avant la fin de la semaine."
	const frenchQuotingEnglish = "Merci beaucoup, c'est bien noté : je m'occupe de la facture corrigée dès aujourd'hui.\n\n" +
		"> Could you send the revised invoice before Friday?"
	const frenchShort = "Oui [1]."

	w := newWorld(t, false)
	cases := []struct {
		name          string
		language      string
		answer        string
		existing      string
		wantNotice    string
		wantUnchanged bool
	}{
		{name: "english-for-english", language: "en", answer: englishLong, wantNotice: ""},
		{name: "english-for-french", language: "fr", answer: englishLong, wantNotice: NoticeAnswerLanguage},
		{name: "french-for-english", language: "en", answer: frenchLong, wantNotice: NoticeAnswerLanguage},
		{name: "quote-stripped-matches", language: "fr", answer: frenchQuotingEnglish, wantNotice: ""},
		{name: "short-stays-unjudged", language: "fr", answer: frenchShort, wantNotice: ""},
		{name: "unsupported-language-stays-unjudged", language: "und", answer: frenchLong, wantNotice: ""},
		{name: "existing-notice-is-preserved", language: "en", answer: frenchLong,
			existing: NoticeNoGroundedAnswer, wantNotice: NoticeNoGroundedAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := AskView{Answer: tc.answer, Mode: ModeGenerated, Intent: IntentFreeform,
				Citations: []Citation{{MessageID: 7, Subject: "Facture"}},
				Counts:    map[string]int{"total": 1}, Provider: "fake", Model: "fake-model",
				Notice: tc.existing}
			got := w.svc.finalizeGeneratedAnswer(view, tc.language)
			if got.Notice != tc.wantNotice {
				t.Fatalf("notice = %q, want %q", got.Notice, tc.wantNotice)
			}
			if got.Answer != view.Answer || got.Mode != view.Mode || got.Intent != view.Intent ||
				got.Provider != view.Provider || got.Model != view.Model {
				t.Fatalf("the language check changed the view: %+v", got)
			}
			if !reflect.DeepEqual(got.Citations, view.Citations) || !reflect.DeepEqual(got.Counts, view.Counts) {
				t.Fatalf("the language check changed citations or counts: %+v", got)
			}
		})
	}
}

// TestFinalizeGeneratedAnswerUsesConfiguredMinLetters proves the threshold is
// read on every call, so a settings change applies without rebuilding the
// service.
func TestFinalizeGeneratedAnswerUsesConfiguredMinLetters(t *testing.T) {
	w := newWorld(t, false)
	answer := "The invoice was approved by our finance team today." // about 45 letters
	if letters := mail.Scripts(answer).Letters; letters < 30 || letters >= 100 {
		t.Fatalf("the fixture must sit between the two thresholds, got %d letters", letters)
	}
	view := AskView{Answer: answer, Mode: ModeGenerated, Provider: "fake", Model: "fake-model"}
	if err := w.cfg.Set("qa.answer_language.min_letters", 100); err != nil {
		t.Fatal(err)
	}
	if got := w.svc.finalizeGeneratedAnswer(view, "fr"); got.Notice != "" {
		t.Fatalf("a %d-letter answer was judged at min_letters 100: %q", mail.Scripts(answer).Letters, got.Notice)
	}
	if err := w.cfg.Set("qa.answer_language.min_letters", 30); err != nil {
		t.Fatal(err)
	}
	if got := w.svc.finalizeGeneratedAnswer(view, "fr"); got.Notice != NoticeAnswerLanguage {
		t.Fatalf("a %d-letter answer was not judged at min_letters 30: %q", mail.Scripts(answer).Letters, got.Notice)
	}
}
