package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// TestPriorityIgnoresAnswersFromAnOlderRevision: after a message is edited,
// its stored route and preview answers describe the old text. The ranking
// read must treat the message as unanswered rather than score it on a
// classification the message no longer has.
func TestPriorityIgnoresAnswersFromAnOlderRevision(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	id := answerMessage(t, s, mailtest.Message("src", "stale"))
	answerVerdict(t, s, id, scope.Owner)
	priorityRoute(t, s, id, "fp",
		`{"reply_needed":{"type":"noul","value":0.9}}`,
		`{"reply_needed":{"type":"noul","value":0.7}}`)

	keys := []QuestionKey{{Name: "reply_needed", Type: "noul"}}
	row := func() PriorityRow {
		t.Helper()
		var out PriorityRow
		found := false
		if err := s.EachPriorityReply(ctx, "fp", true, keys, func(r PriorityRow) error {
			out, found = r, true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("the message is missing from the priority read")
		}
		return out
	}

	current := row()
	if current.Route["reply_needed"].Value != 0.9 || current.Preview["reply_needed"].Value != 0.7 {
		t.Fatalf("current answers were dropped: %+v %+v", current.Route, current.Preview)
	}

	// A mail write bumps the message revision; the stored answers stay at
	// revision 1.
	edited := mailtest.Message("src", "stale")
	edited.Subject = "edited subject"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{edited}); err != nil {
		t.Fatal(err)
	}
	answerVerdict(t, s, id, scope.Owner)

	stale := row()
	if len(stale.Route) != 0 || len(stale.Preview) != 0 {
		t.Fatalf("obsolete answers still scored the message: %+v %+v", stale.Route, stale.Preview)
	}
	if stale.Revision == current.Revision {
		t.Fatalf("the test did not bump the revision: %d", stale.Revision)
	}
}
