package evidence

import (
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func TestHeaderSignals(t *testing.T) {
	w := newWorld(t, everything, nil)
	bulk := msg("bulk", "news@shop.example.net", 1, "Promo")
	bulk.Headers = "Received: from a\nReceived: from b\nReceived: from c\nList-Id: <news.shop.example.net>\n" +
		"List-Unsubscribe: <https://shop.example.net/u>\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\nPrecedence: Bulk\n" +
		"Auto-Submitted: auto-generated (newsletter)\nX-Mailer: Mailer 3.2.1\nReturn-Path: <bounce@mailer.example.org>\n" +
		"Authentication-Results: mx.example.com; spf=pass (sender IP ok) smtp.mailfrom=example.org; dkim=fail header.d=x; dmarc=bestguesspass\n" +
		"Authentication-Results: forged.example; dmarc=pass\nX-MS-Exchange-Organization-SCL: 5\nReply-To: Sales <sales@other.example.org>\n" +
		"X-Spam-Flag: YES\nDisposition-Notification-To: news@shop.example.net\nX-Priority: 1"
	bulk.SpamConfidence, bulk.Importance, bulk.ReadReceiptRequested = mail.OptInt{}, mail.OptInt{}, false
	plain := msg("plain", "ann@example.org", 2, "Hi")
	plain.SpamConfidence, plain.Importance = mail.Int(-1), mail.Int(2)
	cut := msg("cut", "bob@example.org", 3, "Hi")
	cut.Headers = "Received: from a\nReceived: from b"
	cut.Alter(mail.AltHeadersTruncated, "")
	cut.SpamConfidence, cut.Importance = mail.OptInt{}, mail.OptInt{}
	disguised := msg("disguised", "x@example.net", 4, "Hi")
	disguised.SenderName = "ceo@example.com via"
	w.write(bulk, plain, cut, disguised, sentMsg("mine", "ann@example.org", 5, "Hello"))
	w.run()

	b := w.record("bulk")
	for name, v := range map[string]Value{
		"list_id": Bool(true), "list_unsubscribe": Str("one_click"), "precedence": Str("bulk"), "auto_submitted": Str("auto-generated"),
		"auth_spf": Str("pass"), "auth_dkim": Str("fail"), "auth_dmarc": Str("other"), "spam_scl": Int(5), "hop_count": Int(3),
		"return_path": Str("other_domain"), "reply_to": Str("other_domain"), "spam_flag": Bool(true), "read_receipt": Bool(true),
		"importance": Str("high"), "mailer": Str(opaque("mailer 9.9")), "message_kind": Str("ordinary"),
	} {
		want(t, b, name, v)
	}
	p := w.record("plain")
	for name, v := range map[string]Value{"list_id": Bool(false), "list_unsubscribe": Str("none"), "precedence": Str("none"),
		"auth_dmarc": Str("absent"), "spam_scl": Int(-1), "mailer": Str("none"), "reply_to": Str("none"), "name_address": Str("none"),
		// Review L0 (rules plausible: importance): the property is the owner's (editable on a
		// received item); the sender's importance is the one the headers carried.
		"importance": NotApplicable(ReasonNone), "importance_mark": Str("high")} {
		want(t, p, name, v)
	}
	// A cut block: what is not in the part read is not known to be absent;
	// its last line, cut anywhere, is not read (a lower bound of 1 relay).
	c := w.record("cut")
	for name, v := range map[string]Value{"list_id": Bool(false).Cut(true), "hop_count": Int(1).Cut(true),
		"auth_spf": Str("absent").Cut(true), "spam_scl": NotApplicable(ReasonNone).Cut(true)} {
		want(t, c, name, v)
	}
	want(t, w.record("disguised"), "name_address", Str("other_domain"))
	// The owner's own copy carries no transport headers: never guessed.
	m := w.record("mine")
	for _, name := range []string{"list_id", "auth_dmarc", "hop_count", "mailer", "read_receipt"} {
		want(t, m, name, Missing(ReasonNoHeaders))
	}
}

// A source that cannot report a field leaves the signal unavailable for
// its mail, with the capability named.
func TestMissingCapabilitiesAreUnavailable(t *testing.T) {
	w := newWorld(t, source.Capabilities{Recipients: true}, nil)
	m := msg("a", "ann@example.org", 1, "Hello")
	m.Attachments = []mail.Attachment{{Name: "a.pdf", ContentType: "application/pdf", Size: 10}}
	w.write(m)
	w.run()
	r := w.record("a")
	for name, cap := range map[string]string{"list_id": "headers", "attachment_count": "attachments", "has_calendar": "attachments",
		"folder_role": "folder_roles", "read_state": "read_state", "flag_status": "flags", "categories": "categories",
		"last_verb": "last_verb", "message_class": "message_class",
		// Regression (review L0, rules plausible: AnyOf disclosure): every capability that would do is named.
		"message_kind": "message_class|headers"} {
		want(t, r, name, Missing(ReasonCapability+cap))
	}
	want(t, r, "has_attachments", Bool(true)) // the source's flag needs no capability
}

// Regression (review L0, bugs #6): a header block cut in the middle of
// its last line never reads that fragment as a complete value.
func TestTheCutLineOfAHeaderBlockIsNotRead(t *testing.T) {
	w := newWorld(t, everything, nil)
	auto := msg("auto", "news@shop.example.net", 1, "Promo")
	auto.Headers = "Received: from a\nAuto-Submitted: auto-gen"
	auto.Alter(mail.AltHeadersTruncated, "")
	auth := msg("auth", "ann@example.org", 2, "Hi")
	auth.Headers = "Received: from a\nAuthentication-Results: mx.example.com; spf=pass smtp.mailfrom=example.org; dmarc=fa"
	auth.Alter(mail.AltHeadersTruncated, "")
	whole := msg("whole", "ann@example.org", 3, "Hi")
	whole.Headers = "Received: from a\nAuto-Submitted: auto-gen"
	w.write(auto, auth, whole)
	w.run()
	want(t, w.record("auto"), "auto_submitted", Str("none").Cut(true))
	want(t, w.record("auth"), "auth_dmarc", Str("absent").Cut(true))
	want(t, w.record("auth"), "auth_spf", Str("absent").Cut(true))
	want(t, w.record("whole"), "auto_submitted", Str("other")) // an uncut block is read whole
}

// Regression (review L0, rules #11): a header value outside the known
// importances is "other", a value of the vocabulary (a feature cell, a
// fact); an extractor that returns a value outside its vocabulary is an
// error, never stored.
func TestEnumValuesStayInTheirVocabulary(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("urgent", "ann@example.org", 1, "Hi")
	m.Headers = "Received: from a\nImportance: urgent"
	w.write(m)
	w.run()
	r := w.record("urgent")
	want(t, r, "importance", Str("other"))
	layout := w.ev.FeatureLayout()
	f, schema := layout.Vector(r), layout.Names
	if i := slices.Index(schema, "importance=other"); i < 0 || f[i] != 1 {
		t.Fatalf("the value has its cell: %d", i)
	}
	bad := Signal{Name: "zz_enum", Version: 1, Type: TypeEnum, Values: []string{"a"}, Group: GroupContent, Doc: "d",
		Extract: func(*Input) Value { return Str("b") }}
	w.ev.signals = append(w.ev.signals, bad)
	if _, err := w.ev.Run(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "outside its vocabulary") {
		t.Fatalf("a value outside the vocabulary fails the run: %v", err)
	}
}
