package layer1

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// A rule that fired on no evaluation label does not teach (the shipped
// unmeasured_precision is null). The measured reason: on the gold dev split
// a model trained from the rules alone reached 0.292 mean accuracy against
// 0.669 for the majority baseline (ENGINEERING_LOG, 2026-11-05). Learning
// from unmeasured rules looked like self-teaching and produced a head that
// was worse than a constant, so the shipped default keeps the rule layer as
// a deterministic answer source and lets the model wait for measured
// targets.
func TestUnmeasuredRulesDoNotTeach(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, nil)

	// Split the two question-mark classes so a head could be fitted if the
	// rule were allowed to teach unmeasured.
	var msgs []mail.Message
	if err := w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		m := r.Message
		if r.RowID%2 == 0 && !strings.HasSuffix(m.Body, "?") {
			m.Body += "?"
		}
		msgs = append(msgs, m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ev.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}

	rep, err := w.r.Run(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var question *QuestionReport
	for i := range rep.Questions {
		if rep.Questions[i].Question == "is_question" {
			question = &rep.Questions[i]
		}
	}
	if question == nil {
		t.Fatal("the run reported no is_question question")
	}
	if question.Train != 0 || question.Code != BlockNoTeacher {
		t.Fatalf("is_question trained on unmeasured rules: train=%d code=%q", question.Train, question.Code)
	}

	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	answers, err := LoadAnswers(t.Context(), &StoreInputs{Config: w.cfg, Store: w.st, Evidence: w.ev, Embedder: fakeSource{}},
		store.TriageKey{Fingerprint: plan.Profile.Fingerprint, Decider: Decider})
	if err != nil {
		t.Fatal(err)
	}
	for id, byQuestion := range answers {
		if _, ok := byQuestion["is_question"]; ok {
			t.Fatalf("message %d has an is_question decision from unmeasured rules", id)
		}
	}
}
