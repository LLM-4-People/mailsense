package action

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// TestAskActionReadsTheOneVocabulary: the owner-facing consequence of a work
// ask is decided here, but the option it names is threadintel's constant. A raw
// literal is how one option starts meaning two things.
func TestAskActionReadsTheOneVocabulary(t *testing.T) {
	data, err := os.ReadFile("action.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, option := range []string{
		"answer_question", "approval_decision", "information_document", "payment_money",
		"meeting_availability", "task_work", "nothing_fyi",
	} {
		if strings.Contains(source, `"`+option+`"`) {
			t.Errorf("action.go spells %q; use the threadintel constant", option)
		}
	}
	if !strings.Contains(source, "threadintel.ClassifyAsk(") {
		t.Error("AskAction does not read the structural classification")
	}

	for _, c := range []struct {
		asksFor string
		want    Primary
		work    bool
	}{
		{threadintel.AskPaymentMoney, Pay, true},
		{threadintel.AskMeetingAvailability, Schedule, true},
		{threadintel.AskInformationDocument, Send, true},
		{threadintel.AskTaskWork, Do, true},
		{threadintel.AskApprovalDecision, Decide, true},
		{threadintel.AskAnswerQuestion, Reply, false},
		{threadintel.AskNothingFYI, Read, false},
		{"  Payment_Money ", Pay, true},
		{"other", Read, false},
		{"", Read, false},
	} {
		got, work := AskAction(c.asksFor)
		if got != c.want || work != c.work {
			t.Errorf("AskAction(%q) = %v, %v; want %v, %v", c.asksFor, got, work, c.want, c.work)
		}
	}
}

// TestAskVocabularyIsDeclaredOnce: each option has one declaration in
// threadintel, and the classifier is the one reader of the set.
func TestAskVocabularyIsDeclaredOnce(t *testing.T) {
	data, err := os.ReadFile("../threadintel/threadintel.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, option := range []string{
		"answer_question", "approval_decision", "information_document", "payment_money",
		"meeting_availability", "task_work", "nothing_fyi",
	} {
		if got := strings.Count(source, `"`+option+`"`); got != 1 {
			t.Errorf("threadintel spells %q %d times, want one constant", option, got)
		}
	}
}
