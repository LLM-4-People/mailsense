package store

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

func testContactPlan() mailquery.Plan {
	return mailquery.Plan{Kind: mailquery.KindContacts, Action: mailquery.ActionRank,
		Direction: mailquery.DirectionBoth, Metric: mailquery.MetricExchanges,
		TimeRange: mailquery.TimeAll, Limit: 15, Order: mailquery.OrderDesc}
}

func TestQAPlanCacheRoundTripPruneAndClear(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	key := strings.Repeat("a", 64)
	if _, ok, err := s.GetQAPlan(ctx, key); err != nil || ok {
		t.Fatalf("empty cache: ok=%v err=%v", ok, err)
	}
	plan := testContactPlan()
	if err := s.SaveQAPlan(ctx, key, "fr", plan); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetQAPlan(ctx, key)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Kind != plan.Kind || got.Metric != plan.Metric || got.Limit != 15 || got.TimeRange != mailquery.TimeAll {
		t.Fatalf("plan round trip %+v", got)
	}
	plan.Limit = 5
	if err := s.SaveQAPlan(ctx, key, "fr", plan); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetQAPlan(ctx, key); got.Limit != 5 {
		t.Fatalf("upsert kept %d", got.Limit)
	}
	if err := s.SaveQAPlan(ctx, "not-a-hash", "fr", plan); err == nil {
		t.Fatal("a non-hex key was accepted")
	}
	bad := plan
	bad.Limit = 500
	if err := s.SaveQAPlan(ctx, strings.Repeat("b", 64), "fr", bad); err == nil {
		t.Fatal("an invalid plan was cached")
	}
	n, err := s.ClearQAPlans(ctx)
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if _, ok, _ := s.GetQAPlan(ctx, key); ok {
		t.Fatal("a cleared plan came back")
	}
}

func TestQAHistoryCarriesTypedAnswerAndClearRemovesCache(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	plan := testContactPlan()
	result := mailquery.Result{Kind: mailquery.KindContacts, Action: mailquery.ActionRank,
		Metric: mailquery.MetricExchanges, Total: 9,
		Rows: []mailquery.Row{{Key: "ada@example.com", Label: "Ada", Address: "ada@example.com",
			Messages: 4, Sent: 2, Received: 2, Threads: 3, LastAt: "2026-10-25T09:00:00Z"}}}
	clarify := mailquery.Clarification{Question: "Which direction?", Options: []string{"Both", "Received only"}}
	saved, err := s.SaveQA(ctx, QAHistory{Question: "q", Mode: "computed", Intent: "contacts", Language: "fr",
		Counts: map[string]int{"contacts": 1}, Plan: &plan, Result: &result, Clarify: &clarify})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 {
		t.Fatal("no id")
	}
	rows, err := s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Plan == nil || rows[0].Result == nil || rows[0].Clarify == nil {
		t.Fatalf("typed fields missing: %+v", rows)
	}
	if rows[0].Plan.Limit != 15 || rows[0].Result.Rows[0].Label != "Ada" || rows[0].Clarify.Options[1] != "Received only" {
		t.Fatalf("typed round trip %+v", rows[0])
	}
	if err := s.SaveQAPlan(ctx, strings.Repeat("c", 64), "fr", plan); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearQA(ctx)
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	if _, ok, _ := s.GetQAPlan(ctx, strings.Repeat("c", 64)); ok {
		t.Fatal("clearing history must clear the plan cache")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "computed", Intent: "contacts", Language: "fr",
		Plan: &mailquery.Plan{Kind: mailquery.KindContacts, Action: mailquery.ActionRank, Limit: 500}}); err == nil {
		t.Fatal("an invalid plan entered history")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "clarify", Intent: "clarify", Language: "fr",
		Clarify: &mailquery.Clarification{Question: "q", Options: []string{"one"}}}); err == nil {
		t.Fatal("a one-option clarification entered history")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "computed", Intent: "contacts", Language: "fr",
		Clarify: &mailquery.Clarification{Question: "q", Options: []string{"one", "two"}},
		Result:  &mailquery.Result{Kind: mailquery.KindContacts, Action: mailquery.ActionRank, Rows: make([]mailquery.Row, 101)}}); err == nil {
		t.Fatal("an oversized result entered history")
	}
}

func TestQAHistoryClarificationCandidatesRoundTrip(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	options := []string{
		"Résume le dernier échange avec Nadia A <nadia.a@example.com> ?",
		"Résume le dernier échange avec Nadia B <nadia.b@example.com> ?",
	}
	clarify := mailquery.Clarification{Question: "De quelle personne parlez-vous ?", Options: options,
		Candidates: []mailquery.ClarifyCandidate{
			{Kind: "person", Label: "Nadia A", Address: "nadia.a@example.com", Messages: 10, Question: options[0]},
			{Kind: "person", Label: "Nadia B", Address: "nadia.b@example.com", Messages: 3, Question: options[1]},
		}}
	if _, err := s.SaveQA(ctx, QAHistory{Question: "Résume le dernier échange avec Nadia", Mode: "clarify",
		Intent: "clarify", Language: "fr", Clarify: &clarify}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Clarify == nil || len(rows[0].Clarify.Candidates) != 2 {
		t.Fatalf("clarify candidates missing: %+v", rows)
	}
	if got := rows[0].Clarify.Candidates[1]; got.Address != "nadia.b@example.com" || got.Messages != 3 ||
		got.Question != options[1] {
		t.Fatalf("candidate round trip %+v", got)
	}
	notAnOption := mailquery.Clarification{Question: "q", Options: options,
		Candidates: []mailquery.ClarifyCandidate{
			{Kind: "person", Label: "Nadia A", Address: "nadia.a@example.com", Question: "another question"},
			{Kind: "person", Label: "Nadia B", Address: "nadia.b@example.com", Question: options[1]},
		}}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "clarify", Intent: "clarify", Language: "fr",
		Clarify: &notAnOption}); err == nil {
		t.Fatal("a candidate question outside the options entered history")
	}
	badKind := clarify
	badKind.Candidates = append([]mailquery.ClarifyCandidate{}, clarify.Candidates...)
	badKind.Candidates[0].Kind = "alien"
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "clarify", Intent: "clarify", Language: "fr",
		Clarify: &badKind}); err == nil {
		t.Fatal("an unknown candidate kind entered history")
	}
}

func TestQAHistoryMalformedPlanFailsTheRead(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO qa_history
		(question, answer, mode, intent, counts, citations, language, preview, created_at, plan)
		VALUES ('q', 'a', 'computed', 'contacts', '{}', '[]', 'fr', 0, 'x', '{')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListQA(ctx, 10); err == nil {
		t.Fatal("malformed plan must fail the read")
	}
}
