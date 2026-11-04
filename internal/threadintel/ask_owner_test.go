package threadintel

import "testing"

// TestAsksOwnerReadsTheOneVocabulary: the owner obligation is reply_needed or
// the dominant ask being a reply. The ask is classified by ClassifyAsk, so a
// differently-cased profile value reads the same way.
func TestAsksOwnerReadsTheOneVocabulary(t *testing.T) {
	for _, c := range []struct {
		name    string
		message Message
		want    bool
	}{
		{"reply_needed", Message{ReplyNeeded: true}, true},
		{"answer question", Message{AsksFor: "answer_question"}, true},
		{"answer question, mixed case and spaces", Message{AsksFor: "  Answer_Question "}, true},
		{"fyi", Message{AsksFor: "nothing_fyi"}, false},
		{"fyi, mixed case", Message{AsksFor: "Nothing_FYI"}, false},
		{"work", Message{AsksFor: "payment_money"}, false},
		{"unknown", Message{AsksFor: "other"}, false},
		{"empty", Message{}, false},
		{"automated", Message{ReplyNeeded: true, Automated: true}, false},
	} {
		if got := c.message.asksOwner(); got != c.want {
			t.Errorf("%s: asksOwner = %v, want %v", c.name, got, c.want)
		}
	}
}
