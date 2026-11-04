package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestContextDetailsOmitsProductionAnswer: the judge's production answer is
// the model's own output, written for audit. It must never be presented as a
// learned fact next to a message, and a reference label must win over it when
// both exist for the same question.
func TestContextDetailsOmitsProductionAnswer(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()

	only := mailtest.Message("src", "answer-only")
	onlyID := answerMessage(t, s, only)
	answerVerdict(t, s, onlyID, scope.Owner)
	answerLabel(t, s, onlyID, "category", `"spam"`, LabelJudgeAnswer)

	both := mailtest.Message("src", "answer-and-judge")
	bothID := answerMessage(t, s, both)
	answerVerdict(t, s, bothID, scope.Owner)
	answerLabel(t, s, bothID, "category", `"spam"`, LabelJudgeAnswer)
	answerLabel(t, s, bothID, "category", `"work"`, LabelJudge)

	details, err := s.ContextDetails(ctx, []int64{onlyID, bothID})
	if err != nil {
		t.Fatal(err)
	}
	if got := details[onlyID].Labels["category"]; got != (LabelFact{}) {
		t.Fatalf("a production answer surfaced as a learned fact: %+v", got)
	}
	if got := details[bothID].Labels["category"]; got != (LabelFact{Value: `"work"`, Source: LabelJudge}) {
		t.Fatalf("the reference label did not win over the production answer: %+v", got)
	}
}
