package qa

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The live failure: a plural client question resolved several people and
// stopped at "which one?" before the planner ran. It must now execute a local
// topic ranking, group the matching mail by exact sender, and show a name and
// an address for every contact. The two resolved Nadias also prove the plural
// guard: they are present, and they must not turn the list into a clarification.
func TestTopicRankingListsEverySenderWithoutClarify(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{
		{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 10},
		{Address: "nadia.smith@beta.test", Name: "Nadia Smith", Messages: 4},
	}
	question := "donne-moi la liste de clients qui ont déjà fait mention de l'IA, n'importe laquelle dans n'importe quel contexte"
	topic := "IA intelligence artificielle"
	w.gen.content = `{"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"received","metric":"messages","text":"` + topic + `","time_range":"all","limit":50,"order":"desc"}}`
	w.search.results = map[string]search.Result{
		question: {},
		topic: {Hits: []search.Hit{
			{Summary: store.Summary{RowID: 11, SenderName: "Ada Lovelace", SenderAddress: "ada@example.com",
				Subject: "AI note", Instant: "2026-10-20T10:00:00Z", Role: mail.RoleInbox}},
			{Summary: store.Summary{RowID: 12, SenderName: "Ada Lovelace", SenderAddress: "ada@example.com",
				Subject: "More AI", Instant: "2026-10-21T10:00:00Z", Role: mail.RoleInbox}},
			{Summary: store.Summary{RowID: 13, SenderName: "Bob Stone", SenderAddress: "bob@example.com",
				Subject: "AI question", Instant: "2026-10-19T10:00:00Z", Role: mail.RoleOther}},
			{Summary: store.Summary{RowID: 14, SenderName: "Owner", SenderAddress: "owner@example.com",
				Subject: "Re: AI note", Instant: "2026-10-22T10:00:00Z", Role: mail.RoleSent}},
		}},
		"default": {},
	}

	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode == ModeClarify || view.Clarify != nil {
		t.Fatalf("a plural client question was clarified: %+v", view.Clarify)
	}
	if view.Mode != ModeComputed || view.Intent != string(mailquery.KindContacts) || view.Result == nil {
		t.Fatalf("topic ranking view %+v", view)
	}
	if len(view.Result.Rows) != 2 {
		t.Fatalf("rows %+v, want two senders", view.Result.Rows)
	}
	first := view.Result.Rows[0]
	if first.Label != "Ada Lovelace" || first.Address != "ada@example.com" || first.Messages != 2 {
		t.Fatalf("first contact %+v", first)
	}
	if view.Result.Total != 3 || view.Counts["contacts"] != 2 || view.Counts["messages"] != 3 {
		t.Fatalf("totals result=%+v counts=%+v", view.Result, view.Counts)
	}
	if len(view.Citations) != 2 || view.Citations[0].MessageID != 12 || view.Citations[1].MessageID != 13 {
		t.Fatalf("one representative citation per contact: %+v", view.Citations)
	}
	if w.store.queryCalls != 0 {
		t.Fatalf("the topic lane executed %d SQL plans", w.store.queryCalls)
	}
	if len(w.store.planSaves) != 0 {
		t.Fatalf("a retrieval hit set was cached as a replayable plan: %+v", w.store.planSaves)
	}
	if w.search.query.Text != topic || w.search.query.Limit != 200 ||
		!w.search.query.Filter.OwnerOnly || len(w.search.query.Filter.Roles) != 3 {
		t.Fatalf("topic search query %+v", w.search.query)
	}
	if !w.search.query.Wait {
		t.Fatal("topic retrieval must wait for a complete ranking")
	}
	if len(w.store.saved) != 1 || w.store.saved[0].Mode != ModeComputed || w.store.saved[0].Result == nil {
		t.Fatalf("computed topic answer not persisted: %+v", w.store.saved)
	}
}
