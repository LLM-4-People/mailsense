package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// routeRows is a Routes stream of the given rows.
func routeRows(rows ...RouteAnswer) func(func(RouteAnswer) error) error {
	return func(fn func(RouteAnswer) error) error {
		for _, r := range rows {
			if err := fn(r); err != nil {
				return err
			}
		}
		return nil
	}
}

// decisionRows is a Decisions stream of the given rows.
func decisionRows(rows ...Decision) func(func(Decision) error) error {
	return func(fn func(Decision) error) error {
		for _, d := range rows {
			if err := fn(d); err != nil {
				return err
			}
		}
		return nil
	}
}

const routeJSON = `{"q":{"type":"noul","value":0.75}}`

func TestRouteAnswersRoundTripAndStrictRead(t *testing.T) {
	s, cfg := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 3)
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":{"type":"noul","value":0.5}}`}),
		Routes: routeRows(
			RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: routeJSON},
			RouteAnswer{MessageID: ids[1], Revision: 2, RunID: run, Answers: routeJSON},
		)}); err != nil {
		t.Fatal(err)
	}
	var streamed []RouteAnswer
	if err := s.EachRouteAnswer(ctx, "fp1", func(a RouteAnswer) error { streamed = append(streamed, a); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(streamed) != 2 || streamed[0].MessageID != ids[0] || streamed[1].MessageID != ids[1] ||
		streamed[0].Revision != 1 || streamed[1].Revision != 2 || streamed[0].RunID != run || streamed[0].DecidedAt == "" {
		t.Fatalf("streamed %+v", streamed)
	}
	got, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0], ids[1], 999999})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[ids[0]]["q"].Value != 0.75 || got[ids[1]]["q"].Value != 0.75 {
		t.Fatalf("loaded %+v", got)
	}
	if _, ok := got[999999]; ok {
		t.Fatal("an unknown id must be absent, never zero-filled")
	}
	records, err := s.LoadRouteRecords(ctx, "fp1", []int64{ids[0], ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	if r := records[ids[0]]; r.Revision != 1 || r.RunID != run || r.DecidedAt == "" || r.Answers["q"].Value != 0.75 {
		t.Fatalf("the record's metadata: %+v", r)
	}
	if r := records[ids[1]]; r.Revision != 2 {
		t.Fatalf("the record's revision: %+v", r)
	}
	page, total, err := s.RoutePage(ctx, "fp1", MessageFilter{}, 1, 0)
	if err != nil || total != 2 || len(page) != 1 || page[0].MessageID != ids[1] || page[0].Revision != 2 {
		t.Fatalf("first route page %+v total %d: %v", page, total, err)
	}
	page, total, err = s.RoutePage(ctx, "fp1", MessageFilter{}, 1, 1)
	if err != nil || total != 2 || len(page) != 1 || page[0].MessageID != ids[0] {
		t.Fatalf("second route page %+v total %d: %v", page, total, err)
	}
	if page, total, err = s.RoutePage(ctx, "fp1", MessageFilter{}, 1, 2); err != nil || total != 2 || len(page) != 0 {
		t.Fatalf("past the end %+v total %d: %v", page, total, err)
	}
	if other, err := s.LoadRouteAnswers(ctx, "other", []int64{ids[0]}); err != nil || len(other) != 0 {
		t.Fatalf("another fingerprint sees nothing: %v %v", other, err)
	}
	if none, err := s.LoadRouteAnswers(ctx, "fp1", nil); err != nil || len(none) != 0 {
		t.Fatalf("no ids: %v %v", none, err)
	}
	// A malformed stored row fails the read: a route answer is never
	// silently an abstention.
	if _, err := s.db.ExecContext(ctx, "UPDATE route_answers SET answers = '{' WHERE message_id = ? AND fingerprint = 'fp1'", ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0], ids[1]}); err == nil {
		t.Fatal("malformed route answers must fail")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE route_answers SET answers = 'null' WHERE message_id = ? AND fingerprint = 'fp1'", ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[1]}); err == nil {
		t.Fatal("a null answers value must fail, never become an empty map")
	}
	// The migration is durable: reopening keeps the rows.
	_ = s.Close()
	again, err := Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if row, err := again.LoadRouteAnswers(ctx, "fp1", []int64{ids[0]}); err != nil || row[ids[0]]["q"].Value != 0.75 {
		t.Fatalf("after reopening: %v %v", row, err)
	}
}

// pruneRouteAnswers delegates to the shared answer-table owner, including its
// empty-key guard: an empty key must never mean "delete every profile".
func TestPruneRouteAnswersRefusesAnEmptyKey(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	for i, fingerprint := range []string{"fp1", "old-profile"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO route_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
			VALUES (?, ?, 0, ?, 0, '')`, ids[i], fingerprint, routeJSON); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.PruneRouteAnswers(ctx, ""); err == nil || n != 0 {
		t.Fatalf("empty-key prune: %d %v", n, err)
	}
	if n, err := pruneRouteAnswers(ctx, s.db, ""); err == nil || n != 0 {
		t.Fatalf("transactional empty-key prune: %d %v", n, err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_answers").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("an empty-key prune deleted rows: %d %v", rows, err)
	}
	// The guard sits at the shared owner, not in this caller: the normal
	// prune still removes every other profile.
	if n, err := pruneRouteAnswers(ctx, s.db, "fp1"); err != nil || n != 1 {
		t.Fatalf("guarded prune: %d %v", n, err)
	}
}

// route_answers is the persistence format of the route: trimming the triage
// wire must leave a choice distribution intact in the stored row and in
// every read of it.
func TestRouteAnswersKeepChoiceProbabilities(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	run, err := s.BeginLayer1Run(ctx, "inputs")
	if err != nil {
		t.Fatal(err)
	}
	confidence := 0.82
	choice := route.Answer{Answer: engine.Answer{Type: engine.Choice, Choice: "work", Confidence: &confidence,
		Probabilities: map[string]float64{"work": 0.82, "finance": 0.18}}, Layer: route.Layer1}
	raw, err := json.Marshal(map[string]route.Answer{"category": choice})
	if err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp-choice", Decider: "layer1"}
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"category":{"type":"choice"}}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: string(raw)})}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadRouteAnswers(ctx, key.Fingerprint, []int64{ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[ids[0]]["category"], choice) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got[ids[0]]["category"], choice)
	}
	records, err := s.LoadRouteRecords(ctx, key.Fingerprint, []int64{ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(records[ids[0]].Answers["category"].Probabilities, choice.Probabilities) {
		t.Fatalf("the record dropped the distribution: %+v", records[ids[0]].Answers["category"])
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, "SELECT answers FROM route_answers WHERE message_id = ? AND fingerprint = ?",
		ids[0], key.Fingerprint).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `"probabilities"`) {
		t.Fatalf("the stored row dropped its distribution: %s", stored)
	}
}

func TestCommitLayer1PrunesAndRollsBackRouteAnswers(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
		VALUES (?, 'old-profile', 0, ?, 0, '')`, ids[1], routeJSON); err != nil {
		t.Fatal(err)
	}
	key := TriageKey{Fingerprint: "fp1", Decider: "layer1"}
	run, _ := s.BeginLayer1Run(ctx, "inputs")
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 1, Answers: `{"q":{"type":"noul","value":0.5}}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[0], Revision: 1, RunID: run, Answers: routeJSON})}); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM route_answers WHERE fingerprint = 'old-profile'").Scan(&old); err != nil || old != 0 {
		t.Fatalf("the commit prunes every other profile: %d %v", old, err)
	}
	before, err := s.LoadDecisions(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	beforeRoutes, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	// A failure after the route stream (the report) rolls everything back:
	// the previous answers and routes stay, and no other profile is pruned.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_answers (message_id, fingerprint, revision, answers, run_id, decided_at)
		VALUES (?, 'old-profile', 0, ?, 0, '')`, ids[1], routeJSON); err != nil {
		t.Fatal(err)
	}
	run, _ = s.BeginLayer1Run(ctx, "inputs")
	err = s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "", errors.New("boom") }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 2, Answers: `{"q":{"type":"noul","value":0.25}}`}),
		Routes:    routeRows(RouteAnswer{MessageID: ids[0], Revision: 2, RunID: run, Answers: `{"q":{"type":"noul","value":0.25}}`})})
	if err == nil {
		t.Fatal("a failing commit must fail")
	}
	after, _ := s.LoadDecisions(ctx, key)
	afterRoutes, _ := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0]})
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRoutes, afterRoutes) {
		t.Fatalf("a failed commit changed the stored answers:\nbefore %+v %v\nafter  %+v %v",
			before, beforeRoutes, after, afterRoutes)
	}
	if n, err := s.PruneRouteAnswers(ctx, "fp1"); err != nil || n != 1 {
		t.Fatalf("the rollback kept the other profile: %d %v", n, err)
	}
	// A commit without a route stream (a re-score until the route reaches
	// it) leaves the stored routes as they are.
	run, _ = s.BeginLayer1Run(ctx, "inputs")
	if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil }, Key: key, RouteKey: key.Fingerprint,
		Decisions: decisionRows(Decision{MessageID: ids[0], Revision: 3, Answers: `{"q":{"type":"noul","value":0.4}}`})}); err != nil {
		t.Fatal(err)
	}
	if kept, err := s.LoadRouteAnswers(ctx, "fp1", []int64{ids[0]}); err != nil || kept[ids[0]]["q"].Value != 0.75 {
		t.Fatalf("a commit without routes changed them: %v %v", kept, err)
	}
}

// Decisions stay under the paid key while route rows live under the route
// key: a route configuration change must not invalidate (or re-bill) paid
// decisions, and a paid decider change must not make old routes addressable.
func TestDecisionsAndRoutesHaveSeparateKeys(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 1)
	key := TriageKey{Fingerprint: "spec", Decider: "layer1"}
	const first, second = "rk1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "rk1-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	commit := func(routeKey string) {
		t.Helper()
		run, err := s.BeginLayer1Run(ctx, "inputs")
		if err != nil {
			t.Fatal(err)
		}
		each := func(fn func(Layer1Answer) error) error {
			return fn(Layer1Answer{MessageID: ids[0], Revision: 1, RunID: run, StateHash: "h",
				Decision: `{"q":1}`, Route: routeJSON})
		}
		if err := s.CommitLayer1(ctx, Layer1Commit{RunID: run, Report: func() (string, error) { return "{}", nil },
			Key: key, RouteKey: routeKey, Answers: &Layer1Answers{Each: each, Routes: true}}); err != nil {
			t.Fatal(err)
		}
	}
	commit(first)
	if n, _, err := s.DecisionSummary(ctx, key); err != nil || n != 1 {
		t.Fatalf("decisions under the paid key: %d %v", n, err)
	}
	if rows, err := s.LoadRouteAnswers(ctx, first, ids); err != nil || len(rows) != 1 {
		t.Fatalf("route rows under the route key: %d %v", len(rows), err)
	}
	if rows, err := s.LoadRouteAnswers(ctx, "spec", ids); err != nil || len(rows) != 0 {
		t.Fatalf("route rows are addressable under the paid key: %d %v", len(rows), err)
	}
	commit(second)
	if n, _, err := s.DecisionSummary(ctx, key); err != nil || n != 1 {
		t.Fatalf("a route key change moved paid decisions: %d %v", n, err)
	}
	if rows, _ := s.LoadRouteAnswers(ctx, first, ids); len(rows) != 0 {
		t.Fatal("the previous route key still has rows")
	}
	if rows, _ := s.LoadRouteAnswers(ctx, second, ids); len(rows) != 1 {
		t.Fatal("the new route key has no rows")
	}
}

// routePageSeed writes messages in two roles and a route row for each under
// fingerprint, returning the row ids by message id.
func routePageSeed(t *testing.T, s *Store, fingerprint string) map[string]int64 {
	t.Helper()
	msgs := []mail.Message{
		msgAt("in-old", mail.RoleInbox, "2026-01-01T00:00:00Z"),
		msgAt("sent", mail.RoleSent, "2026-01-02T00:00:00Z"),
		msgAt("in-new", mail.RoleInbox, "2026-01-03T00:00:00Z"),
		msgAt("in-same", mail.RoleInbox, "2026-01-03T00:00:00Z"),
	}
	ids := seedMsgs(t, s, msgs...)
	for _, id := range ids {
		if err := s.WriteVerdicts(t.Context(), "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO route_answers
			(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, ?, 1, ?, 0, '2026-11-03T00:00:00Z')`,
			id, fingerprint, routeJSON); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// routeTotalEntries reports the cached RoutePage totals, so the test can
// prove a filter change is a distinct entry rather than a stale hit.
func (s *Store) routeTotalEntries() int {
	s.routeTotalsMu.Lock()
	defer s.routeTotalsMu.Unlock()
	return len(s.routeTotals)
}

// TestRoutePageTotalMatchesThePageAndFollowsTheFilter proves the total and
// the page are one population, and that the cached total is keyed by the
// filter: a role change must produce its own count, not the previous
// filter's. A route write must invalidate it as well.
func TestRoutePageTotalMatchesThePageAndFollowsTheFilter(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := routePageSeed(t, s, "fp-page")
	all := MessageFilter{OwnerOnly: true}
	inbox := MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}

	page, total, err := s.RoutePage(ctx, "fp-page", all, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(page) != 2 || page[0].MessageID != ids["in-same"] || page[1].MessageID != ids["in-new"] {
		t.Fatalf("all page %+v total %d", page, total)
	}
	// A page wide enough to hold the population proves count == page total.
	if page, total, err = s.RoutePage(ctx, "fp-page", all, 10, 0); err != nil || total != 4 || len(page) != 4 {
		t.Fatalf("all page has %d rows, total %d: %v", len(page), total, err)
	}
	page, total, err = s.RoutePage(ctx, "fp-page", inbox, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(page) != 3 {
		t.Fatalf("inbox page has %d rows, total %d, want 3/3", len(page), total)
	}
	// The previous total must not answer this filter, and the first filter
	// must still answer its own count from the cache.
	if _, again, err := s.RoutePage(ctx, "fp-page", all, 1, 3); err != nil || again != 4 {
		t.Fatalf("all total after the inbox read: %d %v", again, err)
	}
	if n := s.routeTotalEntries(); n != 2 {
		t.Fatalf("route total cache has %d entries, want one per filter", n)
	}
	// A route write (the layer-1 commit bumps the route generation) makes
	// the cached total stale.
	extra := seedMsgs(t, s, msgAt("in-extra", mail.RoleInbox, "2026-01-04T00:00:00Z"))["in-extra"]
	if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: extra,
		Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_answers
		(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, 'fp-page', 1, ?, 0, '2026-11-03T00:00:00Z')`,
		extra, routeJSON); err != nil {
		t.Fatal(err)
	}
	s.touchRoutes()
	if _, total, err := s.RoutePage(ctx, "fp-page", all, 1, 0); err != nil || total != 5 {
		t.Fatalf("route generation did not invalidate the total: %d %v", total, err)
	}
}

// TestRoutePageOrdersByInstantNotRowID is the backfill regression: a source
// that syncs newest-first (or backfills older mail) gives the older message
// the higher row id. The page is newest-first by the stored mail instant, not
// by insertion order, and equal instants keep the row-id tie-break.
func TestRoutePageOrdersByInstantNotRowID(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s,
		msgAt("newer", mail.RoleInbox, "2026-01-05T00:00:00Z"),
		msgAt("older", mail.RoleInbox, "2026-01-01T00:00:00Z"),
	)
	if ids["newer"] >= ids["older"] {
		t.Fatalf("fixture row ids: newer %d, older %d (want the older message inserted last)", ids["newer"], ids["older"])
	}
	for _, id := range []int64{ids["newer"], ids["older"]} {
		if err := s.WriteVerdicts(ctx, "test", []VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO route_answers
			(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, 'fp-order', 1, ?, 0, '2026-11-03T00:00:00Z')`,
			id, routeJSON); err != nil {
			t.Fatal(err)
		}
	}
	filter := MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}
	page, total, err := s.RoutePage(ctx, "fp-order", filter, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(page) != 2 || page[0].MessageID != ids["newer"] || page[1].MessageID != ids["older"] {
		t.Fatalf("page %+v total %d, want newest %d then older %d", page, total, ids["newer"], ids["older"])
	}
	first, _, err := s.RoutePage(ctx, "fp-order", filter, 1, 0)
	if err != nil || len(first) != 1 || first[0].MessageID != ids["newer"] {
		t.Fatalf("first page %+v: %v", first, err)
	}
	second, _, err := s.RoutePage(ctx, "fp-order", filter, 1, 1)
	if err != nil || len(second) != 1 || second[0].MessageID != ids["older"] {
		t.Fatalf("second page %+v: %v", second, err)
	}
}

// TestRoutePagePlanUsesInstantOrderAndTheRouteTotalIndex uses the production
// MessageFilter.sql predicate and a stored fingerprint. The page must stream
// messages_instant and probe route_answers by (message_id, fingerprint), with
// no temporary sort; the total keeps the bounded route index. Sharing the
// predicate is what keeps both counts and rows on one membership.
func TestRoutePagePlanUsesInstantOrderAndTheRouteTotalIndex(t *testing.T) {
	s, _ := open(t, nil)
	routePageSeed(t, s, "fp-plan")
	f := MessageFilter{Roles: []mail.FolderRole{mail.RoleInbox}, OwnerOnly: true}
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	where, args := f.sql(vis)
	pageArgs := append(append([]any{"fp-plan"}, args...), 100, 0)
	pagePlan := priorityQueryPlan(t, s, routePageQuery(where), pageArgs...)
	assertIndexOrderedPlan(t, "route page", pagePlan, messageListOrderIndex, "route_answers")
	totalPlan := priorityQueryPlan(t, s, routePageTotalQuery(where), append([]any{"fp-plan"}, args...)...)
	assertIndexOrderedPlan(t, "route page total", totalPlan, routePageIndex)
}
