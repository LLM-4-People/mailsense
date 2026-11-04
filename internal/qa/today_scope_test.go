package qa

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestPlannerContextScopesTodayQuestionsToTheOwnerDay reproduces the live
// defect where "tell me more about today's messages" was grounded on old
// mail. The search must carry the owner-day window, and the day's messages
// must be in the evidence even when the question text matches none of them.
func TestPlannerContextScopesTodayQuestionsToTheOwnerDay(t *testing.T) {
	w := newWorld(t, false)
	const id int64 = 701
	w.store.summaries[id] = store.Summary{
		Subject: "Message reçu aujourd'hui", SenderName: "Alice", SenderAddress: "alice@example.com",
		Instant: "2026-10-26T09:00:00Z", Role: mail.RoleInbox, Body: "Synthetic today body",
	}
	w.store.details[id] = store.ContextDetail{IsRead: true, Importance: 1}
	w.store.queryResult = mailquery.Result{Total: 1, Rows: []mailquery.Row{{
		MessageID: id, Subject: "Message reçu aujourd'hui", SenderName: "Alice",
		Instant: "2026-10-26T09:00:00Z",
	}}}

	msgs, _, _, err := w.svc.plannerContext(t.Context(),
		"dis-moi plus sur les messages que j'ai reçu aujourd'hui", nil)
	if err != nil {
		t.Fatal(err)
	}
	since, until := w.search.query.Filter.Since, w.search.query.Filter.Until
	wantSince := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	wantUntil := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	if !since.Equal(wantSince) || !until.Equal(wantUntil) {
		t.Fatalf("today filter = [%s, %s), want [%s, %s)", since, until, wantSince, wantUntil)
	}
	if len(msgs) != 1 || msgs[0].ID != id {
		t.Fatalf("today evidence = %+v, want the day's message %d", msgs, id)
	}

	w.search.query = search.Query{}
	if _, _, _, err := w.svc.plannerContext(t.Context(), "dis-moi plus sur le projet Atlas", nil); err != nil {
		t.Fatal(err)
	}
	if !w.search.query.Filter.Since.IsZero() || !w.search.query.Filter.Until.IsZero() {
		t.Fatalf("a non-today question was date-scoped: %+v", w.search.query.Filter)
	}
}
