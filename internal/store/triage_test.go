package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func seed(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	var msgs []mail.Message
	for i := range n {
		m := mailtest.Message("src", string(rune('a'+i)))
		msgs = append(msgs, m)
	}
	if err := s.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, m := range msgs {
		var id int64
		if err := s.db.QueryRow("SELECT id FROM messages WHERE msg_id = ?", m.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestTriageCacheFailuresAndRuns(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	k := TriageKey{Fingerprint: "9c9956169b0c", Decider: "subq/subq-snap-1.1"}
	if err := s.SaveDecision(ctx, k, Decision{MessageID: ids[0], StateHash: "h1", Answers: `{"a":1}`, RunID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFailure(ctx, k, Failure{MessageID: ids[0], Error: "402", RunID: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LoadDecisions(ctx, k)
	fails, _ := s.LoadFailures(ctx, k)
	if got[ids[0]].StateHash != "h1" || fails[ids[0]].Error != "402" {
		t.Fatalf("a failure never removes the earlier decision: %v %v", got, fails)
	}
	if err := s.SaveDecision(ctx, k, Decision{MessageID: ids[0], StateHash: "h2", Answers: `{}`, RunID: 3}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.LoadDecisions(ctx, k)
	fails, _ = s.LoadFailures(ctx, k)
	if got[ids[0]].StateHash != "h2" || len(fails) != 0 {
		t.Fatalf("a success replaces and clears the failure: %v %v", got, fails)
	}
	other, _ := s.LoadDecisions(ctx, TriageKey{Fingerprint: "other", Decider: k.Decider})
	if len(other) != 0 {
		t.Fatal("another fingerprint never sees these results")
	}
	id, err := s.BeginTriageRun(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTriageRun(ctx, id, runstate.Succeeded, `{"decided":1}`, ""); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.ListTriageRuns(ctx, 5)
	if len(runs) != 1 || runs[0].State != runstate.Succeeded || runs[0].Report != `{"decided":1}` {
		t.Fatalf("runs %+v", runs)
	}
	// A message deleted between the id snapshot and the save is skipped, not
	// a foreign-key failure that would roll back the whole batch.
	if err := s.SaveDecision(ctx, k, Decision{MessageID: 999999, StateHash: "x", Answers: "{}"}); err != nil {
		t.Fatalf("a decision for a deleted message failed: %v", err)
	}
	if _, ok := mustDecisions(t, s, k)[999999]; ok {
		t.Fatal("a decision for a deleted message was stored")
	}
}

func TestLabelsAndUsage(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	if err := s.PutLabel(ctx, ids[0], "category", `"spam"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLabel(ctx, ids[0], "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	labels, _ := s.ListLabels(ctx)
	if len(labels) != 1 || labels[0].Value != `"work"` || labels[0].Role != mail.RoleInbox || labels[0].ReceivedAt == "" || labels[0].SenderAddress != "elodie@example.org" || !strings.HasPrefix(labels[0].Subject, "Réunion") {
		t.Fatalf("labels %+v", labels)
	}
	if err := s.PutLabel(ctx, ids[0], "", `1`, LabelManual); err == nil {
		t.Fatal("empty question refused")
	}
	if err := s.DeleteLabel(ctx, ids[0], "category", LabelManual); err != nil {
		t.Fatal(err)
	}
	if labels, _ = s.ListLabels(ctx); len(labels) != 0 {
		t.Fatal("deleted")
	}
	for range 2 {
		if err := s.AddUsage(ctx, engine.UsageRecord{Provider: "subq", Model: "m", Usage: engine.Usage{Calls: 1, InputTokens: 100, EstimatedUSD: 0.5}}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.AddUsage(ctx, engine.UsageRecord{Provider: "subq", Model: "m", Usage: engine.Usage{Calls: 1}, Failed: true})
	usage, _ := s.ListUsage(ctx, 365)
	if len(usage) != 1 || usage[0].Calls != 3 || usage[0].Failures != 1 || usage[0].InputTokens != 200 || usage[0].CostUSD != 1 {
		t.Fatalf("usage %+v", usage)
	}
}

func TestEachMessageStreamsWithRecipientsAndVerdicts(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: ids[0], Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag},
		{RowID: ids[1], Verdict: scope.Shared, Reason: scope.ReasonNotAddressed}}); err != nil {
		t.Fatal(err)
	}
	var seen []*MessageRow
	err := s.EachMessage(ctx, MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}}, func(r *MessageRow) error {
		c := *r
		seen = append(seen, &c)
		return nil
	})
	if err != nil || len(seen) != 3 || len(seen[0].Message.Recipients) != 2 || seen[0].Verdict != scope.Owner || seen[2].Verdict != "" ||
		seen[0].Message.Body == "" || seen[0].RowID != ids[0] {
		t.Fatalf("stream: %v %d", err, len(seen))
	}
	var owner int
	_ = s.EachMessage(ctx, MessageFilter{OwnerOnly: true}, func(*MessageRow) error { owner++; return nil })
	var sent int
	_ = s.EachMessage(ctx, MessageFilter{Roles: []mail.FolderRole{mail.RoleSent}}, func(*MessageRow) error { sent++; return nil })
	stop := errors.New("stop")
	if owner != 1 || sent != 0 || s.EachMessage(ctx, MessageFilter{}, func(*MessageRow) error { return stop }) != stop {
		t.Fatalf("filters: owner %d sent %d", owner, sent)
	}
	m, err := s.MessageByRow(ctx, ids[1])
	if err != nil || m.ID != "b" {
		t.Fatalf("by row: %v", err)
	}
	if _, err := s.MessageByRow(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.EachMessage(cctx, MessageFilter{}, func(*MessageRow) error { return nil }); err == nil {
		t.Fatal("cancelled stream must fail")
	}
}

// The key reads ask by (fingerprint, decider), a prefix the primary key
// cannot serve. The covering index makes the ordered read, the count/max and
// the key delete one range.
func TestTriageKeyIndexesServeTheKeyReads(t *testing.T) {
	s, _ := open(t, nil)
	for _, name := range []string{"triage_decisions_by_key", "triage_failures_by_key"} {
		var got string
		if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&got); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
	}
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN
		SELECT message_id FROM triage_decisions WHERE fingerprint = ? AND decider = ? ORDER BY message_id`, "fp", "d")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}
	if !strings.Contains(plan.String(), "triage_decisions_by_key") || strings.Contains(plan.String(), "TEMP B-TREE") {
		t.Fatalf("the decision key read does not use its index:\n%s", plan.String())
	}
}

func mustDecisions(t *testing.T, s *Store, k TriageKey) map[int64]Decision {
	t.Helper()
	out, err := s.LoadDecisions(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
