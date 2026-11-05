package evidence

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func ruleWithOption(id, question, option string) *Rule {
	return &Rule{ID: id, Question: question, answer: answer{option: option}}
}

func ruleWithTruth(id, question string, truth bool) *Rule {
	return &Rule{ID: id, Question: question, answer: answer{truth: &truth}}
}

func ruleWithRange(id, question string, min, max int) *Rule {
	return &Rule{ID: id, Question: question, answer: answer{min: float64(min), max: float64(max), isRange: true}}
}

func choiceQuestion() *engine.Question {
	return &engine.Question{Name: "category", Type: engine.Choice,
		Options: []engine.Option{{Name: "work"}, {Name: "finance"}}}
}

func scoreQuestion() *engine.Question {
	return &engine.Question{Name: "deadline", Type: engine.Score,
		Levels: []string{"none", "months", "month", "week", "24h", "overdue"}}
}

// Agreeing rules answer once, with the first rule's id as provenance.
func TestRuleAnswersAgreement(t *testing.T) {
	a, rule, ok := agreedRuleAnswer(choiceQuestion(), []*Rule{
		ruleWithOption("cat_work", "category", "work"),
		ruleWithOption("cat_work_again", "category", "work"),
	})
	if !ok {
		t.Fatal("agreeing rules answered nothing")
	}
	if a.Choice != "work" || a.AutoRule != "cat_work" {
		t.Fatalf("answer = %+v, rule = %q", a, rule)
	}
	if len(a.Probabilities) != 2 || a.Probabilities["work"] != 1 || a.Probabilities["finance"] != 0 {
		t.Fatalf("distribution = %v", a.Probabilities)
	}
}

// Disagreeing rules are ambiguous: the route's abstention stays.
func TestRuleAnswersConflictAbstains(t *testing.T) {
	if _, _, ok := agreedRuleAnswer(choiceQuestion(), []*Rule{
		ruleWithOption("cat_work", "category", "work"),
		ruleWithOption("cat_finance", "category", "finance"),
	}); ok {
		t.Fatal("conflicting rules produced an answer")
	}
}

// A score rule answers only an exact level; the narrowest firing range wins.
func TestRuleAnswersScoreNarrowestExact(t *testing.T) {
	a, rule, ok := agreedRuleAnswer(scoreQuestion(), []*Rule{
		ruleWithRange("deadline_any", "deadline", 1, 5),
		ruleWithRange("deadline_week", "deadline", 3, 3),
	})
	if !ok {
		t.Fatal("the exact deadline rule answered nothing")
	}
	if a.Value != 3 || rule != "deadline_week" {
		t.Fatalf("answer = %+v, rule = %q", a, rule)
	}
	if len(a.Probabilities) != 6 || a.Probabilities[engine.LevelKey(3)] != 1 {
		t.Fatalf("distribution = %v", a.Probabilities)
	}
}

// Two different exact levels are ambiguous; a range alone names no level.
func TestRuleAnswersScoreAmbiguity(t *testing.T) {
	if _, _, ok := agreedRuleAnswer(scoreQuestion(), []*Rule{
		ruleWithRange("deadline_week", "deadline", 3, 3),
		ruleWithRange("deadline_24h", "deadline", 4, 4),
	}); ok {
		t.Fatal("two exact levels produced an answer")
	}
	if _, _, ok := agreedRuleAnswer(scoreQuestion(), []*Rule{
		ruleWithRange("deadline_any", "deadline", 1, 5),
	}); ok {
		t.Fatal("a range produced an exact answer")
	}
}

// The shipped rules answer a real stored record: one question mark names one
// request and a question.
func TestRuleAnswersReadsStoredEvidence(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(msg("m1", "a@client.com", 0, "Can you send the report by Friday?"))
	w.run()
	var row int64
	_ = w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == "m1" {
			row = r.RowID
		}
		return nil
	})
	got, err := w.ev.RuleAnswers(t.Context(), []int64{row})
	if err != nil {
		t.Fatal(err)
	}
	answers := got[row]
	if a := answers["request_count"]; a.Value != 1 || a.AutoRule != "request_one" {
		t.Fatalf("request_count = %+v, want the one-request rule", a)
	}
	if a := answers["is_question"]; a.Value != 1 || a.AutoRule != "question_mark" {
		t.Fatalf("is_question = %+v, want the question-mark rule", a)
	}
}

// A yes/no rule keeps a single value and no model distribution.
func TestRuleAnswersNoul(t *testing.T) {
	q := &engine.Question{Name: "is_question", Type: engine.Noul}
	a, rule, ok := agreedRuleAnswer(q, []*Rule{
		ruleWithTruth("question_mark", "is_question", true),
	})
	if !ok {
		t.Fatal("the yes/no rule answered nothing")
	}
	if a.Value != 1 || a.AutoRule != "question_mark" || len(a.Probabilities) != 0 {
		t.Fatalf("answer = %+v, rule = %q", a, rule)
	}
	if _, _, ok := agreedRuleAnswer(q, []*Rule{
		ruleWithTruth("yes", "is_question", true),
		ruleWithTruth("no", "is_question", false),
	}); ok {
		t.Fatal("conflicting yes/no rules produced an answer")
	}
}
