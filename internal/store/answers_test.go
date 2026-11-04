package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// answerMessage seeds one message and returns its row id.
func answerMessage(t *testing.T, s *Store, m mail.Message) int64 {
	t.Helper()
	if err := s.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT id FROM messages WHERE source_id = ? AND store_id = ? AND msg_id = ?",
		m.SourceID, m.StoreID, m.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func answerVerdict(t *testing.T, s *Store, id int64, verdict scope.Verdict) {
	t.Helper()
	if err := s.WriteVerdicts(t.Context(), "test", []VerdictRow{{RowID: id, Verdict: verdict, Reason: scope.ReasonAddressedFlag}}); err != nil {
		t.Fatal(err)
	}
}

func answerLabel(t *testing.T, s *Store, id int64, question, value, source string) int64 {
	t.Helper()
	if err := s.PutLabel(t.Context(), id, question, value, source); err != nil {
		t.Fatal(err)
	}
	var rowid int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT rowid FROM labels WHERE message_id = ? AND question = ? AND source = ?",
		id, question, source).Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	return rowid
}

// oneAnswerWorld seeds two visible owner messages, one message of another
// picked store and one shared message. Every count read must ignore the
// last two, and unpicking the second store must hide its label at once.
type answerWorld struct {
	a, b, c, d        int64
	labelIDs          []int64 // visible owner manual/judge labels in insertion order
	categoryOnB       int64
	urgencyOnB        int64
	categoryJudgeOnA  int64
	categoryManualOnA int64
}

func seedAnswerWorld(t *testing.T) (*Store, *answerWorld) {
	t.Helper()
	s, _ := open(t, nil)
	a := answerMessage(t, s, mailtest.Message("src", "a"))
	b := answerMessage(t, s, mailtest.Message("src", "b"))
	cMessage := mailtest.Message("src", "c")
	cMessage.StoreID = "store-2"
	c := answerMessage(t, s, cMessage)
	d := answerMessage(t, s, mailtest.Message("src", "d"))
	for _, id := range []int64{a, b, c} {
		answerVerdict(t, s, id, scope.Owner)
	}
	answerVerdict(t, s, d, scope.Shared)

	categoryManualOnA := answerLabel(t, s, a, "category", `"work"`, LabelManual)
	categoryJudgeOnA := answerLabel(t, s, a, "category", `"spam"`, LabelJudge)
	urgencyOnA := answerLabel(t, s, a, "urgency", `2`, LabelJudge)
	categoryOnB := answerLabel(t, s, b, "category", `"finance"`, LabelManual)
	urgencyOnB := answerLabel(t, s, b, "urgency", `1`, LabelJudge)
	answerLabel(t, s, c, "category", `"work"`, LabelManual)
	answerLabel(t, s, c, "category", `"spam"`, LabelJudge)
	answerLabel(t, s, d, "category", `"work"`, LabelManual)
	answerLabel(t, s, a, "category", `"finance"`, "route")
	// store-2 stays discovered but unpicked: its label rows are kept yet
	// every read below must leave them out.
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	return s, &answerWorld{
		a: a, b: b, c: c, d: d,
		labelIDs:          []int64{categoryManualOnA, categoryJudgeOnA, urgencyOnA, categoryOnB, urgencyOnB},
		categoryOnB:       categoryOnB,
		urgencyOnB:        urgencyOnB,
		categoryJudgeOnA:  categoryJudgeOnA,
		categoryManualOnA: categoryManualOnA,
	}
}

func TestLabelCountsFollowVisibilityAndOwnerScope(t *testing.T) {
	s, _ := seedAnswerWorld(t)
	ctx := t.Context()
	counts, err := s.LabelCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ question, source string }
	got := map[key]int{}
	for _, c := range counts {
		got[key{c.Question, c.Source}] = c.Count
	}
	want := map[key]int{
		{"category", LabelManual}: 2,
		{"category", LabelJudge}:  1,
		{"category", "route"}:     1,
		{"urgency", LabelJudge}:   2,
	}
	if len(got) != len(want) {
		t.Fatalf("counts %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("%s/%s = %d, want %d (all: %v)", k.question, k.source, got[k], n, got)
		}
	}
	// Re-picking store-2 restores its label at the next read, without any
	// write.
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})); err != nil {
		t.Fatal(err)
	}
	counts, _ = s.LabelCounts(ctx)
	got = map[key]int{}
	for _, c := range counts {
		got[key{c.Question, c.Source}] = c.Count
	}
	if got[key{"category", LabelManual}] != 3 || got[key{"category", LabelJudge}] != 2 {
		t.Fatalf("after re-picking store-2: %v", got)
	}
}

func TestReviewCountsNeedJudgeWithoutManual(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	// b's category and a's category both have a manual label now, so only
	// the judge labels without one remain: none for category, two for
	// urgency would be one per message; a and b both have judge urgency and
	// no manual urgency.
	if err := s.PutLabel(ctx, w.b, "category", `"work"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	review, err := s.ReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review["category"] != 0 || review["urgency"] != 2 {
		t.Fatalf("review %v, want category 0 urgency 2", review)
	}
	// A manual label on one message leaves the other message's judge label
	// reviewable.
	if err := s.PutLabel(ctx, w.a, "urgency", `1`, LabelManual); err != nil {
		t.Fatal(err)
	}
	review, _ = s.ReviewCounts(ctx)
	if review["urgency"] != 1 {
		t.Fatalf("after one manual urgency: %v", review)
	}
	// The unpicked store and the shared message never count.
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	review, _ = s.ReviewCounts(ctx)
	if review["category"] != 0 || review["urgency"] != 1 {
		t.Fatalf("after unpicking store-2: %v", review)
	}
}

func TestAnswerRowsOrderPaginationAndFilters(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	all, more, err := s.AnswerRows(ctx, AnswerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// a category manual, a category judge, a urgency judge, b category
	// manual, b urgency judge. Newest is the last inserted.
	wantIDs := []int64{w.urgencyOnB, w.categoryOnB, w.labelIDs[2], w.categoryJudgeOnA, w.categoryManualOnA}
	if more || len(all) != len(wantIDs) {
		t.Fatalf("rows %d more %v, want %d", len(all), more, len(wantIDs))
	}
	for i, id := range wantIDs {
		if all[i].ID != id {
			t.Fatalf("row %d id %d, want %d (all %+v)", i, all[i].ID, id, all)
		}
	}
	first, more, err := s.AnswerRows(ctx, AnswerFilter{Limit: 2})
	if err != nil || !more || len(first) != 2 || first[0].ID != wantIDs[0] || first[1].ID != wantIDs[1] {
		t.Fatalf("first page %+v more %v: %v", first, more, err)
	}
	second, more, err := s.AnswerRows(ctx, AnswerFilter{Limit: 2, Cursor: first[1].ID})
	if err != nil || !more || len(second) != 2 || second[0].ID != wantIDs[2] {
		t.Fatalf("second page %+v more %v: %v", second, more, err)
	}
	if last, more, _ := s.AnswerRows(ctx, AnswerFilter{Limit: 2, Cursor: second[1].ID}); len(last) != 1 || more || last[0].ID != wantIDs[4] {
		t.Fatalf("last page %+v more %v", last, more)
	}

	judge, _, err := s.AnswerRows(ctx, AnswerFilter{Source: LabelJudge})
	if err != nil || len(judge) != 3 || judge[0].ID != w.urgencyOnB || judge[1].ID != w.labelIDs[2] || judge[2].ID != w.categoryJudgeOnA {
		t.Fatalf("judge rows %+v: %v", judge, err)
	}
	category, _, _ := s.AnswerRows(ctx, AnswerFilter{Question: "category"})
	if len(category) != 3 || category[0].ID != w.categoryOnB {
		t.Fatalf("category rows %+v", category)
	}
	onA, _, _ := s.AnswerRows(ctx, AnswerFilter{MessageID: w.a})
	if len(onA) != 3 || onA[0].ID != w.labelIDs[2] || onA[1].ID != w.categoryJudgeOnA || onA[2].ID != w.categoryManualOnA {
		t.Fatalf("message a rows %+v", onA)
	}
	combined, _, _ := s.AnswerRows(ctx, AnswerFilter{MessageID: w.b, Question: "urgency"})
	if len(combined) != 1 || combined[0].ID != w.urgencyOnB {
		t.Fatalf("combined filter %+v", combined)
	}
	if exact, _, _ := s.AnswerRows(ctx, AnswerFilter{Cursor: w.categoryJudgeOnA}); len(exact) != 1 || exact[0].ID != w.categoryManualOnA {
		t.Fatalf("cursor boundary %+v", exact)
	}
	if rows, _, err := s.AnswerRows(ctx, AnswerFilter{Source: "route"}); err != nil || len(rows) != 0 {
		t.Fatalf("unsupported source filter: %+v: %v", rows, err)
	}
}

func TestAnswerRowsIgnoreHiddenMailAndUnsupportedSources(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	if err := s.PutLabel(ctx, w.a, "category", `"finance"`, "route"); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.AnswerRows(ctx, AnswerFilter{Limit: AnswerMaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Source != LabelManual && row.Source != LabelJudge {
			t.Fatalf("unsupported source in answers: %+v", row)
		}
		if row.MessageID == w.c || row.MessageID == w.d {
			t.Fatalf("hidden or shared message in answers: %+v", row)
		}
	}
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = s.AnswerRows(ctx, AnswerFilter{Limit: AnswerMaxLimit})
	for _, row := range rows {
		if row.MessageID == w.c {
			t.Fatalf("unpicked store's label survived: %+v", row)
		}
	}
}

func TestAnswerRowsLimitAndCursor(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	if rows, more, err := s.AnswerRows(ctx, AnswerFilter{Limit: 0}); err != nil || len(rows) != 5 || more {
		t.Fatalf("default limit: %d rows, more %v: %v", len(rows), more, err)
	}
	for _, limit := range []int{-1, AnswerMaxLimit + 1} {
		if _, _, err := s.AnswerRows(ctx, AnswerFilter{Limit: limit}); err == nil {
			t.Fatalf("limit %d was accepted", limit)
		}
	}
	if rows, more, err := s.AnswerRows(ctx, AnswerFilter{Limit: 1, Cursor: w.categoryManualOnA}); err != nil || len(rows) != 0 || more {
		t.Fatalf("oldest cursor: %+v more %v: %v", rows, more, err)
	}
}

// labelCountsLegacy is the old scope-driven statement, kept as the parity
// oracle: the owner population drove the read and every label row was
// probed by message id.
func labelCountsLegacy(t *testing.T, s *Store) []LabelCount {
	t.Helper()
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	counts, err := query(t.Context(), s.db, `SELECT l.question, l.source, COUNT(*) FROM labels l
		JOIN messages m ON m.id = l.message_id
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+vis+` AND `+ownerWhere+`
		GROUP BY l.question, l.source ORDER BY l.question, l.source`, nil,
		func(r *sql.Rows, c *LabelCount) error { return r.Scan(&c.Question, &c.Source, &c.Count) })
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func reviewCountsLegacy(t *testing.T, s *Store) map[string]int {
	t.Helper()
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	rows, err := s.db.QueryContext(t.Context(), `SELECT l.question, COUNT(*) FROM labels l
		JOIN messages m ON m.id = l.message_id
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+vis+` AND `+ownerWhere+` AND l.source = ?
		AND NOT EXISTS (SELECT 1 FROM labels manual WHERE manual.message_id = l.message_id
			AND manual.question = l.question AND manual.source = ?)
		GROUP BY l.question ORDER BY l.question`, LabelJudge, LabelManual)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var question string
		var n int
		if err := rows.Scan(&question, &n); err != nil {
			t.Fatal(err)
		}
		out[question] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLabelCountsMatchTheScopeDrivenAggregate compares both label counts
// with the old scope-driven statements on a fixture with every source, a
// manual/judge pair on one message, a shared message, an unpicked store and
// the visibility switch. The labels-driven read must return the same wire
// shape and the same numbers.
func TestLabelCountsMatchTheScopeDrivenAggregate(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	if err := s.PutLabel(ctx, w.a, "priority", `"high"`, LabelJudgeAnswer); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLabel(ctx, w.b, "priority", `"low"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLabel(ctx, w.c, "urgency", `3`, LabelJudge); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLabel(ctx, w.d, "urgency", `4`, LabelJudge); err != nil {
		t.Fatal(err)
	}
	check := func(label string) {
		t.Helper()
		counts, err := s.LabelCounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := labelCountsLegacy(t, s)
		if !reflect.DeepEqual(counts, want) {
			t.Fatalf("%s: counts %+v, scope-driven %+v", label, counts, want)
		}
		review, err := s.ReviewCounts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		wantReview := reviewCountsLegacy(t, s)
		if !reflect.DeepEqual(review, wantReview) {
			t.Fatalf("%s: review %v, scope-driven %v", label, review, wantReview)
		}
	}
	check("second store unpicked")
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})); err != nil {
		t.Fatal(err)
	}
	check("second store re-picked")
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"})); err != nil {
		t.Fatal(err)
	}
	check("second store unpicked again")
}

// TestLabelCountsStartFromLabels pins the failure class: the labels table is
// the outer loop, so an empty labels table ends the read after one empty
// covering-index scan. The joins to messages, folders and scope come after it
// and are entered only for a label row. This test seeds owner mail with no
// labels, which used to cost a whole-mailbox probe per count.
func TestLabelCountsStartFromLabels(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, mailtest.Message("src", "a"), mailtest.Message("src", "b"), mailtest.Message("src", "c"))
	verdicts := make([]VerdictRow, 0, len(ids))
	for _, id := range ids {
		verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag})
	}
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	counts, err := s.LabelCounts(ctx)
	if err != nil || len(counts) != 0 {
		t.Fatalf("counts %+v: %v", counts, err)
	}
	review, err := s.ReviewCounts(ctx)
	if err != nil || len(review) != 0 {
		t.Fatalf("review %+v: %v", review, err)
	}
	rq, rargs := reviewCountsQuery(visibleSQL, nil)
	for _, tc := range []struct {
		name string
		q    string
		args []any
	}{
		{"counts", labelCountsQuery(visibleSQL), nil},
		{"reviews", rq, rargs},
	} {
		plan := priorityQueryPlan(t, s, tc.q, tc.args...)
		if !strings.HasPrefix(plan, "SCAN l ") {
			t.Fatalf("%s does not start from labels:\n%s", tc.name, plan)
		}
		if strings.Contains(plan, "SCAN m") || strings.Contains(plan, "SCAN f") || strings.Contains(plan, "SCAN v") {
			t.Fatalf("%s scans the mail population:\n%s", tc.name, plan)
		}
		if at := strings.Index(plan, "SEARCH m"); at >= 0 && strings.Index(plan, "SCAN l") > at {
			t.Fatalf("%s visits messages before labels:\n%s", tc.name, plan)
		}
	}
}

func TestMailboxCountsVisibleOwnerAndPickedStores(t *testing.T) {
	s, _ := seedAnswerWorld(t)
	ctx := t.Context()
	messages, stores, err := s.MailboxCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messages != 2 || stores != 1 {
		t.Fatalf("mailbox %d messages %d stores, want 2/1", messages, stores)
	}
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})); err != nil {
		t.Fatal(err)
	}
	messages, stores, _ = s.MailboxCounts(ctx)
	if messages != 3 || stores != 2 {
		t.Fatalf("after re-picking: %d messages %d stores, want 3/2", messages, stores)
	}
}

func TestAnswerRowsUpdateKeepsItsCursor(t *testing.T) {
	s, w := seedAnswerWorld(t)
	ctx := t.Context()
	if err := s.PutLabel(ctx, w.a, "category", `"finance"`, LabelManual); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.AnswerRows(ctx, AnswerFilter{MessageID: w.a, Question: "category", Source: LabelManual})
	if err != nil || len(rows) != 1 || rows[0].ID != w.categoryManualOnA {
		t.Fatalf("updated label moved its rowid: %+v: %v", rows, err)
	}
}

func TestAnswerRowsCarryNoMessageContent(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "secret")
	m.Subject = "STORE-SECRET-SUBJECT"
	m.SenderAddress = "store-secret-sender@example.org"
	m.Body = "STORE-SECRET-BODY"
	id := answerMessage(t, s, m)
	answerVerdict(t, s, id, scope.Owner)
	answerLabel(t, s, id, "category", `"work"`, LabelManual)
	rows, _, err := s.AnswerRows(ctx, AnswerFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v: %v", rows, err)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"STORE-SECRET-SUBJECT", "store-secret-sender", "STORE-SECRET-BODY"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("answer rows leaked %q: %s", secret, raw)
		}
	}
}

// The review count and the review queue read the same triage.folder_roles
// population: a judge label in a role outside the selection must not inflate
// the badge.
func TestReviewCountsFollowsTriageFolderRoles(t *testing.T) {
	s, _ := open(t, map[string]any{"triage.folder_roles": []any{"inbox"}})
	ctx := t.Context()
	inbox := answerMessage(t, s, mailtest.Message("src", "inbox-role"))
	sentMessage := mailtest.Message("src", "sent-role")
	sentMessage.FolderPath, sentMessage.FolderRole = "Sent Items", mail.RoleSent
	sent := answerMessage(t, s, sentMessage)
	for _, id := range []int64{inbox, sent} {
		answerVerdict(t, s, id, scope.Owner)
		answerLabel(t, s, id, "urgency", `1`, LabelJudge)
	}
	review, err := s.ReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review["urgency"] != 1 {
		t.Fatalf("with inbox only: %v, want urgency 1", review)
	}
	if err := s.cfg.Set("triage.folder_roles", []any{"inbox", "sent"}); err != nil {
		t.Fatal(err)
	}
	review, err = s.ReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if review["urgency"] != 2 {
		t.Fatalf("with inbox and sent: %v, want urgency 2", review)
	}
}

// TestAnswerRowsPlanStartsFromLabels pins the answer history's access path:
// labels drive the join, so the rowid order is free and an empty labels table
// never visits a message. The old message-driven plan probed labels through
// its autoindex and then sorted with a temporary B-tree.
func TestAnswerRowsPlanStartsFromLabels(t *testing.T) {
	s, _ := open(t, nil)
	base := visibleSQL + " AND " + ownerWhere + " AND l.source IN ('" + LabelManual + "', '" + LabelJudge + "')"
	for name, tc := range map[string]struct {
		where string
		args  []any
	}{
		"all":      {base, []any{AnswerDefaultLimit + 1}},
		"source":   {base + ` AND l.source = ?`, []any{LabelJudge, AnswerDefaultLimit + 1}},
		"question": {base + ` AND l.question = ?`, []any{"category", AnswerDefaultLimit + 1}},
		"cursor":   {base + ` AND l.rowid < ?`, []any{int64(1 << 62), AnswerDefaultLimit + 1}},
	} {
		plan := priorityQueryPlan(t, s, answerRowsQuery(tc.where), tc.args...)
		first, _, _ := strings.Cut(plan, "\n")
		if first != "SCAN l" && !strings.HasPrefix(first, "SEARCH l USING INTEGER PRIMARY KEY") {
			t.Fatalf("%s does not start from labels:\n%s", name, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("%s sorts with a temporary B-tree:\n%s", name, plan)
		}
		if strings.Contains(plan, "SCAN m") || strings.Contains(plan, "SCAN f") || strings.Contains(plan, "SCAN v") {
			t.Fatalf("%s scans the mail population:\n%s", name, plan)
		}
	}
}

// TestAnswerRowsOverLargeLabelPopulation covers the shape and the filters on
// an empty, a one-row and a 2,000-row labels table: the rowid cursor walks
// every label once in descending order, and the source, question and message
// filters keep their exact rows and hasMore boundary.
func TestAnswerRowsOverLargeLabelPopulation(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seedMsgs(t, s, mailtest.Message("src", "a"), mailtest.Message("src", "b"), mailtest.Message("src", "c"))
	verdicts := make([]VerdictRow, 0, len(ids))
	for _, id := range ids {
		verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag})
	}
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}

	if rows, more, err := s.AnswerRows(ctx, AnswerFilter{}); err != nil || len(rows) != 0 || more {
		t.Fatalf("zero labels: %+v more=%v: %v", rows, more, err)
	}

	only := answerLabel(t, s, ids["a"], "q0000", `"v"`, LabelManual)
	if rows, more, err := s.AnswerRows(ctx, AnswerFilter{}); err != nil || len(rows) != 1 || more || rows[0].ID != only {
		t.Fatalf("one label: %+v more=%v: %v", rows, more, err)
	}

	const total = 2000
	messages := []int64{ids["a"], ids["b"], ids["c"]}
	writes := make([]LabelWrite, 0, total)
	for i := 0; i < total; i++ {
		source := LabelManual
		if i%2 == 1 {
			source = LabelJudge
		}
		writes = append(writes, LabelWrite{
			MessageID: messages[i%len(messages)],
			Question:  fmt.Sprintf("q%04d", i),
			Value:     `"v"`,
			Source:    source,
		})
	}
	if err := s.PutLabels(ctx, writes); err != nil {
		t.Fatal(err)
	}

	// The cursor walks the whole population once, newest rowid first, with
	// the last page reporting no successor.
	var walked []int64
	before := int64(0)
	for page := 0; ; page++ {
		if page > total {
			t.Fatalf("pagination did not terminate after %d rows", len(walked))
		}
		rows, more, err := s.AnswerRows(ctx, AnswerFilter{Cursor: before, Limit: AnswerMaxLimit})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > AnswerMaxLimit {
			t.Fatalf("page %d returned %d rows over the limit", page, len(rows))
		}
		for i, row := range rows {
			if i > 0 && rows[i-1].ID <= row.ID {
				t.Fatalf("page %d is not descending at row %d: %+v", page, i, rows)
			}
			walked = append(walked, row.ID)
		}
		if !more {
			break
		}
		if len(rows) == 0 {
			t.Fatalf("page %d reported more with no rows", page)
		}
		before = rows[len(rows)-1].ID
	}
	if len(walked) != total {
		t.Fatalf("cursor walked %d labels, want %d", len(walked), total)
	}
	for i := 1; i < len(walked); i++ {
		if walked[i-1] <= walked[i] {
			t.Fatalf("cursor repeated or reordered labels at %d: %d then %d", i, walked[i-1], walked[i])
		}
	}

	judge, more, err := s.AnswerRows(ctx, AnswerFilter{Source: LabelJudge, Limit: AnswerMaxLimit})
	if err != nil || !more || len(judge) != AnswerMaxLimit {
		t.Fatalf("judge page: %d rows more=%v: %v", len(judge), more, err)
	}
	for _, row := range judge {
		if row.Source != LabelJudge {
			t.Fatalf("judge filter returned %q", row.Source)
		}
	}
	// i=1 is odd: judge, message b, question q0001.
	question, more, err := s.AnswerRows(ctx, AnswerFilter{Question: "q0001"})
	if err != nil || more || len(question) != 1 || question[0].MessageID != ids["b"] ||
		question[0].Source != LabelJudge {
		t.Fatalf("question filter: %+v more=%v: %v", question, more, err)
	}
	// Message b owns every third label from i=1 through i=1999.
	onB, more, err := s.AnswerRows(ctx, AnswerFilter{MessageID: ids["b"], Limit: AnswerMaxLimit})
	if err != nil || !more || len(onB) != AnswerMaxLimit {
		t.Fatalf("message page: %d rows more=%v: %v", len(onB), more, err)
	}
	for _, row := range onB {
		if row.MessageID != ids["b"] {
			t.Fatalf("message filter returned message %d", row.MessageID)
		}
	}
	combined, more, err := s.AnswerRows(ctx, AnswerFilter{
		MessageID: ids["c"], Question: "q0002", Source: LabelManual})
	if err != nil || more || len(combined) != 1 || combined[0].MessageID != ids["c"] {
		t.Fatalf("combined filter: %+v more=%v: %v", combined, more, err)
	}
}
