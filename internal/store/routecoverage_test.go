package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

const (
	coverageRouteKey = "coverage-route-key"
	coverageProfile  = "coverage-profile"
	coverageDecider  = "fixture"
)

// coverageQuestions is the fixture's question list: one of each type, in
// the order the triage summary passes them.
var coverageQuestions = []QuestionKey{
	{Name: "category", Type: "choice"},
	{Name: "urgency", Type: "score"},
	{Name: "reply_needed", Type: "noul"},
}

const (
	coverageFull    = `{"category":{"type":"choice"},"urgency":{"type":"score"},"reply_needed":{"type":"noul"}}`
	coverageMissing = `{"category":{"type":"choice"},"reply_needed":{"type":"noul"}}`
	coverageWrong   = `{"category":{"type":"choice"},"urgency":{"type":"noul"},"reply_needed":{"type":"noul"}}`
)

// coverageWorld seeds the coverage fixture: four covered inbox members (one
// fresh, one stale, one missing a question, one with a wrong type), one
// unjudged member, and three route rows outside the default filter (a Sent
// message, a shared-scope message, and a message of an unpicked store).
func coverageWorld(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	s, _ := open(t, nil)
	type seed struct {
		id, store string
		role      mail.FolderRole
		path      string
	}
	seeds := []seed{
		{"fresh", "store-1", mail.RoleInbox, "Inbox"},
		{"stale", "store-1", mail.RoleInbox, "Inbox"},
		{"missing", "store-1", mail.RoleInbox, "Inbox"},
		{"wrong", "store-1", mail.RoleInbox, "Inbox"},
		{"unjudged", "store-1", mail.RoleInbox, "Inbox"},
		{"sent", "store-1", mail.RoleSent, "Sent"},
		{"hidden", "store-1", mail.RoleInbox, "Inbox"},
		{"unpicked", "store-9", mail.RoleInbox, "Inbox"},
	}
	var msgs []mail.Message
	for _, sd := range seeds {
		m := mailtest.Message("src", sd.id)
		m.StoreID, m.StoreName, m.FolderPath, m.FolderRole = sd.store, "Fixture", sd.path, sd.role
		msgs = append(msgs, m)
	}
	if err := s.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]int64, len(seeds))
	revisions := make(map[string]int64, len(seeds))
	for _, sd := range seeds {
		var id, revision int64
		if err := s.db.QueryRow("SELECT id, revision FROM messages WHERE source_id = 'src' AND msg_id = ?", sd.id).
			Scan(&id, &revision); err != nil {
			t.Fatal(err)
		}
		ids[sd.id], revisions[sd.id] = id, revision
	}
	var verdicts []VerdictRow
	for _, sd := range seeds {
		verdict := scope.Owner
		if sd.id == "hidden" {
			verdict = scope.Shared
		}
		verdicts = append(verdicts, VerdictRow{RowID: ids[sd.id], Verdict: verdict, Reason: scope.ReasonAddressedFlag})
	}
	if err := s.WriteVerdicts(t.Context(), "test", verdicts); err != nil {
		t.Fatal(err)
	}
	run, err := s.BeginLayer1Run(t.Context(), "coverage")
	if err != nil {
		t.Fatal(err)
	}
	rows := []RouteAnswer{
		{MessageID: ids["fresh"], Revision: revisions["fresh"], Answers: coverageFull},
		{MessageID: ids["stale"], Revision: revisions["stale"] - 1, Answers: coverageFull},
		{MessageID: ids["missing"], Revision: revisions["missing"], Answers: coverageMissing},
		{MessageID: ids["wrong"], Revision: revisions["wrong"], Answers: coverageWrong},
		{MessageID: ids["sent"], Revision: revisions["sent"], Answers: coverageFull},
		{MessageID: ids["hidden"], Revision: revisions["hidden"], Answers: coverageFull},
		{MessageID: ids["unpicked"], Revision: revisions["unpicked"], Answers: coverageFull},
	}
	for i := range rows {
		rows[i].RunID = run
	}
	if err := s.CommitLayer1(t.Context(), Layer1Commit{
		RunID: run, Report: func() (string, error) { return "{}", nil },
		Key: TriageKey{Fingerprint: coverageProfile, Decider: coverageDecider}, RouteKey: coverageRouteKey,
		Routes: routeRows(rows...),
	}); err != nil {
		t.Fatal(err)
	}
	// One failed member (counted even though it is fresh) and one failure
	// row for a message outside every filter this test applies.
	failKey := TriageKey{Fingerprint: coverageProfile, Decider: coverageDecider}
	for _, id := range []string{"fresh", "unpicked"} {
		if err := s.SaveFailure(t.Context(), failKey, Failure{MessageID: ids[id], Error: "fixture failure", RunID: run}); err != nil {
			t.Fatal(err)
		}
	}
	return s, ids
}

// goRouteCoverage is the old summary implementation: it materializes the
// population with MessageRefs, streams every route row and decodes each
// answer map in Go. Failures are a separate per-member count under the
// triage key, exactly as the legacy coverage counted them. It is the oracle
// the aggregate must equal.
func goRouteCoverage(t *testing.T, s *Store, routeKey, fingerprint, decider string, f MessageFilter, questions []QuestionKey) RouteCoverage {
	t.Helper()
	refs, err := s.MessageRefs(t.Context(), f)
	if err != nil {
		t.Fatal(err)
	}
	revision := make(map[int64]int64, len(refs))
	for _, r := range refs {
		revision[r.RowID] = r.Revision
	}
	failures, err := s.LoadFailures(t.Context(), TriageKey{Fingerprint: fingerprint, Decider: decider})
	if err != nil {
		t.Fatal(err)
	}
	var out RouteCoverage
	out.Eligible = len(refs)
	for _, r := range refs {
		if _, ok := failures[r.RowID]; ok {
			out.Failed++
		}
	}
	if err := s.EachRouteAnswer(t.Context(), routeKey, func(row RouteAnswer) error {
		memberRevision, ok := revision[row.MessageID]
		if !ok {
			return nil
		}
		out.Total++
		var answers map[string]route.Answer
		if err := jsonx.DecodeStrict([]byte(row.Answers), &answers); err != nil {
			return err
		}
		if row.Revision != memberRevision {
			out.Stale++
			return nil
		}
		for _, q := range questions {
			if a, ok := answers[q.Name]; !ok || string(a.Type) != q.Type {
				out.Partial++
				return nil
			}
		}
		out.Fresh++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRouteCoverageMatchesTheGoSummary(t *testing.T) {
	s, _ := coverageWorld(t)
	inbox := []mail.FolderRole{mail.RoleInbox}
	inboxAndSent := []mail.FolderRole{mail.RoleInbox, mail.RoleSent}
	cases := []struct {
		name        string
		routeKey    string
		fingerprint string
		decider     string
		filter      MessageFilter
		questions   []QuestionKey
		want        RouteCoverage
	}{
		{"inbox owner", coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: inbox, OwnerOnly: true}, coverageQuestions,
			RouteCoverage{Eligible: 5, Total: 4, Fresh: 1, Stale: 1, Partial: 2, Failed: 1}},
		{"inbox and sent owner", coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: inboxAndSent, OwnerOnly: true}, coverageQuestions,
			RouteCoverage{Eligible: 6, Total: 5, Fresh: 2, Stale: 1, Partial: 2, Failed: 1}},
		{"inbox any scope", coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: inbox}, coverageQuestions,
			RouteCoverage{Eligible: 6, Total: 5, Fresh: 2, Stale: 1, Partial: 2, Failed: 1}},
		{"another route key", "other", coverageProfile, coverageDecider, MessageFilter{Roles: inbox, OwnerOnly: true}, coverageQuestions,
			RouteCoverage{Eligible: 5, Total: 0, Failed: 1}},
		{"another failure fingerprint", coverageRouteKey, "other", coverageDecider, MessageFilter{Roles: inbox, OwnerOnly: true}, coverageQuestions,
			RouteCoverage{Eligible: 5, Total: 4, Fresh: 1, Stale: 1, Partial: 2}},
		{"another failure decider", coverageRouteKey, coverageProfile, "other", MessageFilter{Roles: inbox, OwnerOnly: true}, coverageQuestions,
			RouteCoverage{Eligible: 5, Total: 4, Fresh: 1, Stale: 1, Partial: 2}},
		{"no questions", coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: inbox, OwnerOnly: true}, nil,
			RouteCoverage{Eligible: 5, Total: 4, Fresh: 3, Stale: 1, Failed: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.RouteCoverage(t.Context(), c.routeKey, c.fingerprint, c.decider, c.filter, c.questions)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("aggregate %+v, want %+v", got, c.want)
			}
			legacy := goRouteCoverage(t, s, c.routeKey, c.fingerprint, c.decider, c.filter, c.questions)
			if got != legacy {
				t.Fatalf("aggregate %+v, Go summary %+v", got, legacy)
			}
		})
	}
}

func TestRouteCoverageQuestionNameWithDot(t *testing.T) {
	s, ids := coverageWorld(t)
	if _, err := s.db.Exec("UPDATE route_answers SET answers = ? WHERE message_id = ? AND fingerprint = ?",
		`{"follow.up":{"type":"score"}}`, ids["fresh"], coverageRouteKey); err != nil {
		t.Fatal(err)
	}
	questions := []QuestionKey{{Name: "follow.up", Type: "score"}}
	got, err := s.RouteCoverage(t.Context(), coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}, questions)
	if err != nil {
		t.Fatal(err)
	}
	want := RouteCoverage{Eligible: 5, Total: 4, Fresh: 1, Stale: 1, Partial: 2, Failed: 1}
	if got != want {
		t.Fatalf("aggregate %+v, want %+v", got, want)
	}
	if legacy := goRouteCoverage(t, s, coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}, questions); got != legacy {
		t.Fatalf("aggregate %+v, Go summary %+v", got, legacy)
	}
}

func TestRouteCoverageFailsOnMalformedAnswers(t *testing.T) {
	for _, msg := range []string{"fresh", "stale"} {
		t.Run(msg, func(t *testing.T) {
			s, ids := coverageWorld(t)
			if _, err := s.db.Exec("UPDATE route_answers SET answers = '{' WHERE message_id = ? AND fingerprint = ?",
				ids[msg], coverageRouteKey); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RouteCoverage(t.Context(), coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}, coverageQuestions); err == nil {
				t.Fatal("a malformed answers payload must fail the read")
			}
			// A route row outside the member filter is never decoded, exactly
			// like the Go path: the same payload on a non-member is ignored.
			if _, err := s.db.Exec("UPDATE route_answers SET answers = ? WHERE message_id = ? AND fingerprint = ?",
				coverageFull, ids[msg], coverageRouteKey); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE route_answers SET answers = '{' WHERE message_id = ? AND fingerprint = ?",
				ids["hidden"], coverageRouteKey); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RouteCoverage(t.Context(), coverageRouteKey, coverageProfile, coverageDecider, MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}, coverageQuestions); err != nil {
				t.Fatalf("a non-member's malformed payload failed the read: %v", err)
			}
		})
	}
}
