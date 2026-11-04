package store

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// backfillPlan returns the query plan of the display backfill's page read.
func backfillPlan(t *testing.T, s *Store) string {
	t.Helper()
	ctx := t.Context()
	vis, err := s.visible(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q := `SELECT id, store_id, msg_id, k FROM (
		SELECT m.id, m.store_id, m.msg_id, ` + instantSortSQL + ` AS k
		FROM messages m JOIN folders f ON f.id = m.folder_id AND ` + vis + `
		WHERE m.source_id = ? AND ` + backfillCandidateSQL + `
	) ORDER BY k DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, "src", 200)
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
	return plan.String()
}

// TestBackfillPageIsAnIndexRange proves the display backfill's page is served
// by its own partial index in key order. Before this index it scanned
// messages_survivor and sorted the whole mailbox (324 ms per 200-row page on a
// 33,842-message snapshot), and a COALESCE sort key kept the temp B-tree even
// after the index existed.
func TestBackfillPageIsAnIndexRange(t *testing.T) {
	s, _ := open(t, nil)
	plan := backfillPlan(t, s)
	if !strings.Contains(plan, "messages_backfill") {
		t.Fatalf("the backfill page does not use its index:\n%s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("the backfill page still sorts a temp B-tree:\n%s", plan)
	}
}

// TestBackfillOrdersAbsentInstantLast pins the ordering the plain DESC sort
// must keep: an empty instant is the smallest string, so it sorts after every
// dated message, exactly as the removed COALESCE wrapper made it.
func TestBackfillOrdersAbsentInstantLast(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()

	dated := mailtest.Message("src", "dated")
	dated.ReceivedAt = "2026-03-02T10:00:00Z"
	undated := mailtest.Message("src", "undated")
	undated.ReceivedAt = ""
	if err := s.UpsertMessages(ctx, 0, []mail.Message{dated, undated}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE messages SET body_html_known = 0"); err != nil {
		t.Fatal(err)
	}

	refs, err := s.BackfillNewest(ctx, "src", BackfillCursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("backfill refs %d", len(refs))
	}
	if refs[0].Key.ID != "dated" || refs[1].Key.ID != "undated" {
		t.Fatalf("order %q, %q: a dated message must come before an absent instant",
			refs[0].Key.ID, refs[1].Key.ID)
	}
	// The cursor walks past the dated row without repeating it.
	page, err := s.BackfillNewest(ctx, "src", BackfillCursor{Instant: refs[0].Instant, RowID: refs[0].RowID}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Key.ID != "undated" {
		t.Fatalf("cursor page %+v", page)
	}
}
