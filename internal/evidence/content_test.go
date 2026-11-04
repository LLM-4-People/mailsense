package evidence

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

func TestContentSignals(t *testing.T) {
	w := newWorld(t, everything, nil)
	fr := msg("fr", "ann@example.org", 0, "Bonjour,\n\nla réunion est fixée au jeudi 15 octobre à 14:30 ; le devis s'élève à 1 250,00 € HT, "+
		"livraison prévue le 03/11/2026. Pouvez-vous confirmer ?\n\nVoir https://example.org/devis\n\n-- \nAnn\n+33 1 23 45 67 89\n\n"+
		"> Le 10 oct. 2026, Bob a écrit :\n> ancien texte cité\n> encore")
	fr.Attachments = []mail.Attachment{{Name: "devis.pdf", ContentType: "application/pdf; name=devis.pdf", Size: 2000},
		{Name: "logo.png", ContentType: "image/png", Size: 300, Inline: true}, {Name: "x.bin", ContentType: "application/octet-stream", Size: -1}}
	en := msg("en", "bob@example.org", 1, "Hi,\nPlease send the invoice by Friday. It is USD 99.50 in total.\nThanks\n\nBob Smith\nACME Inc.\n+1 555 123 4567\nbob@example.org")
	cal := msg("cal", "carl@example.org", 2, "Invitation")
	cal.CalendarMethod = "REQUEST"
	cut := msg("cut", "dan@example.org", 3, "No date here")
	cut.BodyTruncated = true
	empty := msg("empty", "eve@example.org", 4, "")
	empty.Subject = "ok"
	w.write(fr, en, cal, cut, empty)
	w.run()

	f := w.record("fr")
	for name, v := range map[string]Value{
		"language": Str("fr"), "script": Str("Latin"), "attachment_count": Int(2), "inline_count": Int(1),
		"attachment_types": List([]string{"application/octet-stream", "application/pdf"}), "attachment_bytes": Int(2000).Cut(true),
		"signature": Str(mail.SignatureDelimiter), "link_count": Int(1), "question_marks": Int(1), "currency_count": Int(1),
		"currency_codes": List([]string{"EUR"}), "amount_magnitude": Int(3), "date_days": Ints([]int{3, 22}), "date_count": Int(2),
		"date_next_days": Int(3), "time_count": Int(1), "has_calendar": Bool(false), "has_attachments": Bool(true),
	} {
		want(t, f, name, v)
	}
	if q := f["quoted_ratio"]; q.State != OK || q.Num < 0.2 || q.Num > 0.6 {
		t.Errorf("quoted ratio %+v", q)
	}
	e := w.record("en")
	for name, v := range map[string]Value{"language": Str("en"), "signature": Str(mail.SignatureContacts), "date_days": Ints(nil),
		"weekday_days": Ints([]int{4}), "date_next_days": NotApplicable(ReasonNone), "currency_codes": List([]string{"USD"}), "amount_magnitude": Int(1)} {
		want(t, e, name, v)
	}
	want(t, w.record("cal"), "has_calendar", Bool(true))
	want(t, w.record("cal"), "message_kind", Str(string(mail.KindMeetingRequest)))
	c := w.record("cut")
	want(t, c, "date_count", Int(0).Cut(true))
	want(t, c, "date_next_days", NotApplicable(ReasonNone).Cut(true)) // a date may lie past the cut
	want(t, c, "signature", Str(mail.SignatureNone).Cut(true))
	z := w.record("empty")
	want(t, z, "quoted_ratio", NotApplicable(ReasonNone))
	want(t, z, "language", Str("und"))
	want(t, z, "amount_magnitude", NotApplicable(ReasonNone))
}

// Owner actions are reported where the source has them, and are never
// causal (never features, facts or rule inputs).
func TestOwnerActions(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("a", "ann@example.org", 0, "Hi")
	m.FlagStatus, m.TaskDueDate, m.Categories, m.LastVerbExecuted, m.IsRead = mail.Int(2), "2026-10-20T00:00:00Z", "Client; Projet X", mail.Int(103), true
	n := msg("b", "bob@example.org", 0, "Hi")
	n.LastVerbExecuted, n.IsRead, n.FlagStatus = mail.OptInt{}, false, mail.Int(7)
	w.write(m, n)
	w.run()
	a := w.record("a")
	for name, v := range map[string]Value{"flag_status": Str("marked"), "flag_due_days": Int(8), "last_verb": Str("reply_all"),
		"read_state": Bool(true), "folder_role": Str("inbox"), "folder_path": Str(opaque("Inbox"))} {
		want(t, a, name, v)
	}
	if c := a["categories"]; len(c.List) != 2 {
		t.Errorf("categories %+v", c)
	}
	b := w.record("b")
	want(t, b, "last_verb", Str("none"))
	want(t, b, "flag_status", Str("other"))
	want(t, b, "flag_due_days", NotApplicable(ReasonNone))
	for _, s := range Signals() {
		if s.Group == GroupOwner && s.Causal {
			t.Errorf("%s: the owner's later action cannot be causal", s.Name)
		}
	}
	if mailtest.Owner == "" {
		t.Fatal("unreachable")
	}
}

// Regression (review L0, bugs plausible: weekdays across zones): a weekday
// named alone is the author's — placed from the day the author wrote on
// (the Date header's offset) — and counted from the owner's day.
func TestWeekdaysAreTheAuthors(t *testing.T) {
	w := newWorld(t, everything, nil) // the owner in Europe/Paris
	m := msg("tokyo", "ann@example.org", 0, "Hello, could we please meet on Tuesday to discuss the proposal and the schedule?")
	// Tuesday 13 October 2026 23:30 in Paris is already Wednesday 14 in Tokyo.
	m.ReceivedAt, m.SentOn = "2026-10-13T21:30:00Z", "2026-10-13T21:30:00Z"
	m.Headers = "Received: from mx.example.net\nDate: Wed, 14 Oct 2026 06:30:00 +0900"
	w.write(m)
	w.run()
	// From the author's Wednesday, Tuesday is the 20th: seven days from the
	// owner's Tuesday (not the owner's own Tuesday, 0).
	want(t, w.record("tokyo"), "weekday_days", Ints([]int{7}))
}

// Lead decision 2026-10-15: deadline_same_day fired on replies whose only
// date of their own day was the attribution of the quoted message
// ("Le 14 oct. 2026 à 10:00, Ann <…> a écrit :" before a tab-indented
// quote, as Outlook renders Gmail's). The date signals read the own text
// (mail.CleanBody), which no longer holds it.
func TestTheQuotedAttributionIsNoDateOfTheMessage(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("reply", "ann@example.org", 0, "Merci, je regarde ça.\n\nLe mer. 14 oct. 2026 à 10:00, Ann Lee <ann@example.org> a écrit :\n\n\tBonjour,\n\tVoici le rapport.")
	m.ReceivedAt, m.SentOn = "2026-10-14T12:00:00Z", "2026-10-14T12:00:00Z"
	w.write(m)
	w.run()
	want(t, w.record("reply"), "date_count", Int(0))
}

// Review of the crash-safety round, #8 (failed first: date_count 1): an
// attribution on the FIRST line — nothing of the message's own before it,
// a bare quote — is no own text, so its date is not one of the message's.
func TestAnAttributionOnTheFirstLineIsNoDateOfTheMessage(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("quote", "ann@example.org", 0, "Le mer. 14 oct. 2026 à 10:00, Ann Lee <ann@example.org> a écrit :\n> Bonjour,\n> Voici le rapport.")
	m.ReceivedAt, m.SentOn = "2026-10-14T12:00:00Z", "2026-10-14T12:00:00Z"
	w.write(m)
	w.run()
	want(t, w.record("quote"), "date_count", Int(0))
}
