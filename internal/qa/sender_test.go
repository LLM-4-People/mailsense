package qa

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestSenderQuestionUsesExactAddressScope(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{{
		Address: "alice@example.test", Name: "Alice Martin",
		Messages: 4, LastAt: "2026-10-01T10:00:00Z",
	}}
	w.store.queryResult = mailquery.Result{Kind: mailquery.KindMessages, Action: mailquery.ActionList,
		Rows: []mailquery.Row{{MessageID: 31, Subject: "Plan", SenderName: "Alice Martin", Instant: "2026-10-01T10:00:00Z"}}}
	view, err := w.svc.Ask(t.Context(), "Emails from Alice Martin", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeStructured || view.Intent != IntentSender || len(view.Citations) != 1 ||
		view.Citations[0].MessageID != 31 {
		t.Fatalf("sender view = %+v", view)
	}
	if w.store.queryCalls != 1 || w.store.lastPlan.Kind != mailquery.KindMessages ||
		w.store.lastPlan.Direction != mailquery.DirectionReceived ||
		len(w.store.lastPlan.ContactAddresses) != 1 ||
		w.store.lastPlan.ContactAddresses[0] != "alice@example.test" {
		t.Fatalf("sender plan = %+v calls=%d", w.store.lastPlan, w.store.queryCalls)
	}
}

func TestSenderQuestionUnknownIsHonest(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = nil
	view, err := w.svc.Ask(t.Context(), "Emails from Nobody Here", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Notice != NoticeSenderUnknown || len(view.Citations) != 0 || view.Answer != "" {
		t.Fatalf("unknown sender view = %+v", view)
	}
	if w.store.queryCalls != 0 {
		t.Fatalf("an unresolved sender still ran %d mail queries", w.store.queryCalls)
	}
}
