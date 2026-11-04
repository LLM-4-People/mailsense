package triage

import (
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func testRules(t *testing.T) *Rules {
	t.Helper()
	r, err := loadRules(rulesJSON)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// #6: facts are measured with Unicode classes, per script: sentence
// terminals and question marks of every script, all-caps words of every
// cased script, no word count for spaceless scripts, no all-caps fact
// for uncased ones.
func TestFactsAreScriptAware(t *testing.T) {
	r := testRules(t)
	norm, _ := mail.NewSubjectNormalizer(1, []string{"re"})
	cases := []struct {
		lang, text                        string
		caps, sentences, words, questions int
	}{
		{"en", "Can you SEND the REPORT? Thanks. Bye!", 2, 3, 7, 1},
		{"fr", "Le SERVEUR ÉTÉ est down ?! Voir… ok", 2, 2, 8, 1},
		{"ru", "СРОЧНО: пришлите отчёт. Спасибо!", 1, 2, 4, 0},
		{"el", "ΠΡΟΣΟΧΗ: μπορείτε να επιβεβαιώσετε\u037e", 1, 1, 4, 1}, // the Greek question mark
		{"ar", "هل يمكنك مراجعة الفاتورة؟ شكرا.", -1, 2, 5, 1},
		{"he", "האם אפשר לקבוע פגישה? תודה.", -1, 2, 5, 1},
		{"zh", "请在星期五之前发送报告。谢谢！可以吗？", -1, 3, -1, 1},
		{"ja", "会議の議事録を送ってください。お願いします！", -1, 2, -1, 0},
		{"th", "กรุณาส่งใบแจ้งหนี้ภายในวันศุกร์", -1, 1, -1, 0},
		{"hi", "क्या आप रिपोर्ट भेज सकते हैं? धन्यवाद।", -1, 2, 7, 1},
	}
	for _, c := range cases {
		f := measure(&mail.Message{}, c.text, norm, r)
		if f.CapsWords != c.caps || f.Sentences != c.sentences || f.Words != c.words || f.QuestionMarks != c.questions {
			t.Errorf("%s %q: caps %d/%d sentences %d/%d words %d/%d questions %d/%d", c.lang, c.text,
				f.CapsWords, c.caps, f.Sentences, c.sentences, f.Words, c.words, f.QuestionMarks, c.questions)
		}
	}
	// The block says characters (no words) for a spaceless text, and has
	// no all-caps line for an uncased one.
	zh := measure(&mail.Message{}, "请发送报告。", norm, r).render(r, false)
	if !strings.Contains(zh, "characters in 1 sentences") || strings.Contains(zh, "words") {
		t.Fatalf("spaceless facts:\n%s", zh)
	}
}

// #6: the arrival is rendered in the sender's own Date-header offset —
// a Tokyo morning stays a morning, whatever the machine or time.zone —
// and is omitted when the message carries no Date header.
func TestArrivalIsInTheSendersOffset(t *testing.T) {
	r := testRules(t)
	norm, _ := mail.NewSubjectNormalizer(1, []string{"re"})
	m := &mail.Message{FolderRole: mail.RoleInbox, ReceivedAt: "2026-03-02T00:05:00Z",
		Headers: "Received: x\r\nDate: Mon, 2 Mar 2026 09:04:30 +0900\r\nSubject: x"}
	f := measure(m, "本文", norm, r)
	if f.weekday != "Monday" || f.hour != "09" || f.offset != "+09:00" || f.Arrived != "2026-03-02T09:05:00+09:00" {
		t.Fatalf("%+v", f)
	}
	if !strings.Contains(f.render(r, false), "Arrived Monday at 09:00 in the sender's time (UTC+09:00).") {
		t.Fatalf("%s", f.render(r, false))
	}
	for _, h := range []string{"", "Date: garbage", "Received: only"} {
		m.Headers = h
		if f := measure(m, "x", norm, r); f.Arrived != "" || strings.Contains(f.render(r, false), "Arrived") {
			t.Errorf("no usable Date header (%q): the arrival is omitted, never guessed", h)
		}
	}
}

func TestFactsRulesAreValidated(t *testing.T) {
	for name, over := range map[string]string{
		"marks are Unicode data": `{"facts": {"question_marks": ["?"]}}`,
		"no caps minimum":        `{"facts": {"caps_min_letters": 0}}`,
		"bad share":              `{"facts": {"spaceless_share": 2}}`,
	} {
		merged, err := jsonx.Overlay(rulesJSON, []byte(over), rulesDeep...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadRules(merged); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
