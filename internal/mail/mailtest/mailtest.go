// Package mailtest builds valid, fully populated messages for tests. Every
// name, address and text is synthetic (example.com / example.org and
// IANA-reserved IDN test domains): no real person's data in the repository.
package mailtest

import (
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Owner is the synthetic mailbox owner of the test messages.
const Owner = "amelie.durand@example.com"

// Message returns a valid inbox message with every field set; id varies the
// keys and subject.
func Message(sourceID, id string) mail.Message {
	return mail.Message{
		SourceID: sourceID, StoreID: "store-1", StoreName: "Amélie", FolderPath: "Inbox", FolderRole: mail.RoleInbox,
		ID: id, EntryID: "EID" + id, LongTermEntryID: "LT" + id, SearchKey: "SK" + id, Origin: "outlook",
		Subject: "Réunion budget " + id, SenderName: "Élodie Martin", SenderAddress: "elodie@example.org",
		SentRepresentingAddress: "elodie@example.org",
		Recipients: []mail.Recipient{
			{Kind: mail.To, Name: "Amélie", Address: Owner},
			{Kind: mail.Cc, Name: "", Address: "lucas@example.com"},
		},
		AddressedToMe: "to", ReceivedAt: "2026-10-12T15:48:34Z", SentOn: "2026-10-12T15:48:25Z",
		CreationTime: "2026-10-12T15:48:34Z", LastModificationTime: "2026-10-12T15:53:36Z",
		LastVerbExecutionTime: "2026-10-12T16:00:00Z", LastVerbExecuted: mail.Int(102),
		LocalCommitTime: "2026-10-12T16:00:01Z", MessageFlags: mail.Int(0x412),
		IsRead: true, HasAttachments: true, Importance: mail.Int(1), Sensitivity: mail.Int(0), Size: mail.Int(48213),
		SpamConfidence: mail.Int(-1), Categories: "Client", FlagStatus: mail.Int(2), FlagRequest: "Follow up",
		TaskDueDate: "2026-10-20T00:00:00Z", TaskStartDate: "2026-10-13T00:00:00Z", ReminderSet: true,
		ReminderTime: "2026-10-19T13:00:00Z", ReadReceiptRequested: true, VotingOptions: "Oui;Non", VotingResponse: "Oui",
		MessageClass: "IPM.Note", InternetMessageID: "<" + id + "@mail>", InReplyTo: "<parent@mail>",
		References: "<root@mail> <parent@mail>", ConversationID: "CONV1", ConversationIndex: "01D9",
		Headers: "Received: from x", Body: "Pourriez-vous peaufiner l'ébauche du plan de mise en œuvre avant lundi ?",
		BodyTruncated: false,
	}
}

// Sample is one synthetic message of the multilingual corpus.
type Sample struct {
	Lang    string // BCP 47 tag of the text
	Script  string // its dominant Unicode script (Go's unicode.Scripts name)
	Subject string
	Body    string
	Sender  string
	Term    string // a word of the body a search for that language must find
}

// Corpus is a small synthetic multilingual mailbox: Latin, Cyrillic,
// Greek, Arabic and Hebrew (right to left), Han, Kana, Hangul and Thai
// (no spaces between words), with IDN addresses.
func Corpus() []Sample {
	return []Sample{
		{"en", "Latin", "Re: Budget review", "Could you send the revised budget by Friday? Thanks.", "olivia@example.com", "revised"},
		{"fr", "Latin", "TR : Réunion de lundi", "Bonjour, la réunion est déplacée à 14 h. Merci de confirmer.", "elodie@example.org", "déplacée"},
		{"de", "Latin", "AW: Angebot", "Könnten Sie das Angebot bis Mittwoch prüfen? Grüße", "jonas@beispiel.example", "Angebot"},
		{"es", "Latin", "RV: Factura pendiente", "¿Puede revisar la factura antes del viernes? Gracias.", "lucia@example.org", "factura"},
		{"ru", "Cyrillic", "Ответ: Отчёт", "Пожалуйста, пришлите отчёт до пятницы.", "ivan@пример.испытание", "отчёт"},
		{"el", "Greek", "ΑΠ: Συνάντηση", "Μπορείτε να επιβεβαιώσετε τη συνάντηση;", "nikos@example.org", "συνάντηση"},
		{"ar", "Arabic", "رد: الفاتورة", "هل يمكنك مراجعة الفاتورة قبل يوم الجمعة؟", "omar@مثال.إختبار", "الفاتورة"},
		{"he", "Hebrew", "השב: פגישה", "האם אפשר לקבוע פגישה ביום שלישי?", "noa@example.org", "פגישה"},
		{"zh", "Han", "回复：项目进度报告", "请在星期五之前发送项目进度报告。谢谢！", "li.na@例子.测试", "进度报告"},
		{"ja", "Han", "転送：会議の議事録", "会議の議事録を金曜日までに送ってください。", "sato@例え.テスト", "議事録"},
		{"ko", "Hangul", "회신: 견적서", "견적서를 금요일까지 보내 주시겠습니까?", "minji@실례.테스트", "견적서"},
		{"th", "Thai", "ตอบกลับ: ใบแจ้งหนี้", "กรุณาส่งใบแจ้งหนี้ภายในวันศุกร์", "somchai@example.org", "ใบแจ้งหนี้"},
	}
}

// IndicSample is one matra-based Indic script of the substring-search
// fixture: four words that differ only in the dependent vowel sign after
// the same consonant. unicode61 tokenizes the sign as a separator, so the
// word index sees one token for all four; the substring index and its scan
// keep the sign.
type IndicSample struct {
	Script string    // Go's unicode.Scripts name (UCD long name)
	Terms  [4]string // the same consonant with the vowel signs i, ii, u, uu
}

// IndicMatras is the synthetic fixture of the ten matra-based Indic scripts
// the app's locale data covers. The consonants are letters; every vowel
// sign is a combining mark (Mn/Mc), never a letter or a digit.
func IndicMatras() []IndicSample {
	return []IndicSample{
		{"Devanagari", [4]string{"कि", "की", "कु", "कू"}},
		{"Bengali", [4]string{"কি", "কী", "কু", "কূ"}},
		{"Gurmukhi", [4]string{"ਕਿ", "ਕੀ", "ਕੁ", "ਕੂ"}},
		{"Gujarati", [4]string{"કિ", "કી", "કુ", "કૂ"}},
		{"Oriya", [4]string{"କି", "କୀ", "କୁ", "କୂ"}},
		{"Tamil", [4]string{"கி", "கீ", "கு", "கூ"}},
		{"Telugu", [4]string{"కి", "కీ", "కు", "కూ"}},
		{"Kannada", [4]string{"ಕಿ", "ಕೀ", "ಕು", "ಕೂ"}},
		{"Malayalam", [4]string{"കി", "കീ", "കു", "കൂ"}},
		{"Sinhala", [4]string{"කි", "කී", "කු", "කූ"}},
	}
}

// Picked is the mailboxes.selected value that picks the store of the test
// messages (Message: store "store-1") of each source id, plus any other
// (source id, store id) pairs: nothing is visible until it is picked.
func Picked(sourceIDs []string, more ...[2]string) []any {
	var out []any
	for _, id := range sourceIDs {
		out = append(out, source.StoreKey(id, "store-1"))
	}
	for _, p := range more {
		out = append(out, source.StoreKey(p[0], p[1]))
	}
	return out
}
