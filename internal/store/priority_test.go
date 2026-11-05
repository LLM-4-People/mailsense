package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func priorityQueryPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

// priorityRoute stores one message's answers at its current revision, which
// is what a real commit writes: the read drops answers computed at an older
// revision.
func priorityRoute(t *testing.T, s *Store, id int64, fingerprint, answers, preview string) {
	t.Helper()
	var revision int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT revision FROM messages WHERE id = ?`, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO route_answers
		(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, ?, ?, ?, 0, '2026-10-26T00:00:00Z')`,
		id, fingerprint, revision, answers); err != nil {
		t.Fatal(err)
	}
	if preview != "" {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO route_preview_answers
			(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, ?, ?, ?, 0, '2026-10-26T00:00:00Z')`,
			id, fingerprint, revision, preview); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEachPriorityReplyReportsOpenStoredDue(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	openDue := mailtest.Message("src", "open-due")
	openDue.Subject, openDue.TaskDueDate, openDue.FlagStatus = "open-due", "2026-11-10T09:00:00Z", mail.Int(2)
	completed := mailtest.Message("src", "completed-due")
	completed.Subject, completed.TaskDueDate, completed.FlagStatus = "completed-due", "2026-11-11T09:00:00Z", mail.Int(1)
	noDue := mailtest.Message("src", "no-due")
	noDue.Subject, noDue.TaskDueDate = "no-due", ""
	noDue.FlagStatus = mail.OptInt{}
	msgs := []mail.Message{openDue, completed, noDue}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	verdicts := make([]VerdictRow, 0, len(msgs))
	for _, m := range msgs {
		var id int64
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE msg_id = ?", m.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag})
		priorityRoute(t, s, id, "fp", `{}`, "")
	}
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	if err := s.EachPriorityReply(ctx, "fp", false, nil, func(row PriorityRow) error {
		got[row.Subject] = row.StoredDue
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !got["open-due"] || got["completed-due"] || got["no-due"] {
		t.Fatalf("stored due = %+v, want only the open-due row", got)
	}
}

func TestEachPriorityMessageOwnerOnlyAndStrict(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	a := mailtest.Message("src", "a")
	a.Subject, a.SenderName, a.SenderAddress, a.Body = "one", "Alice", "alice@example.com", "hello world"
	b := mailtest.Message("src", "b")
	b.Subject, b.Body = "two", "second body"
	shared := mailtest.Message("src", "shared")
	unpicked := mailtest.Message("src", "unpicked")
	unpicked.StoreID = "store-3"
	for _, m := range []mail.Message{a, b, shared, unpicked} {
		if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
			t.Fatal(err)
		}
	}
	ids := map[string]int64{}
	for _, m := range []mail.Message{a, b, shared, unpicked} {
		var id int64
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM messages WHERE msg_id = ?", m.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[m.ID] = id
	}
	for _, id := range []int64{ids["a"], ids["b"], ids["shared"]} {
		answerVerdict(t, s, id, scope.Owner)
	}
	answerVerdict(t, s, ids["shared"], scope.Shared)
	answerVerdict(t, s, ids["unpicked"], scope.Owner)
	priorityRoute(t, s, ids["a"], "fp", `{"reply_needed":{"type":"noul","value":0.9}}`,
		`{"reply_needed":{"type":"noul","value":0.7}}`)
	priorityRoute(t, s, ids["b"], "fp", `{"category":{"type":"choice","choice":"work"}}`, "")
	priorityRoute(t, s, ids["shared"], "fp", `{}`, "")
	priorityRoute(t, s, ids["unpicked"], "fp", `{}`, "")

	var rows []PriorityRow
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(r PriorityRow) error {
		rows = append(rows, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want the two visible owner messages", len(rows))
	}
	if rows[0].MessageID != ids["a"] || rows[0].Subject != "one" || rows[0].SenderName != "Alice" ||
		rows[0].SenderAddress != "alice@example.com" || rows[0].Body != "hello world" ||
		rows[0].FolderRole != mail.RoleInbox || rows[0].Revision != 0 {
		t.Fatalf("first row %+v", rows[0])
	}
	if rows[0].Preview["reply_needed"].Value != 0.7 || rows[0].Route["reply_needed"].Value != 0.9 {
		t.Fatalf("answers %+v / %+v", rows[0].Route, rows[0].Preview)
	}
	if rows[1].Preview != nil {
		t.Fatalf("a missing preview row must be absent, got %+v", rows[1].Preview)
	}

	// Preview off does not decode (or fail on) preview rows.
	if err := s.EachPriorityMessage(ctx, "fp", false, nil, func(r PriorityRow) error {
		if r.Preview != nil {
			t.Fatalf("preview decoded with the preview off: %+v", r.Preview)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if none, err := s.MessageIDs(ctx, MessageFilter{}); err != nil || len(none) != 3 {
		t.Fatalf("visibility setup: %d %v", len(none), err)
	}
	if err := s.EachPriorityMessage(ctx, "other", true, nil, func(PriorityRow) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestEachPriorityMessageExcludesSharedStoresAndHiddenRoles(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"},
			[2]string{"src", "store-1"}, [2]string{"src", "store-2"}),
	})
	ctx := t.Context()
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, storeID string, role mail.FolderRole, path string) int64 {
		t.Helper()
		m := mailtest.Message("src", id)
		m.StoreID, m.FolderRole, m.FolderPath = storeID, role, path
		rowID := answerMessage(t, s, m)
		answerVerdict(t, s, rowID, scope.Owner)
		priorityRoute(t, s, rowID, "fp", `{}`, "")
		return rowID
	}
	visible := seed("visible", "store-1", mail.RoleInbox, "Inbox")
	seed("shared", "store-2", mail.RoleInbox, "Inbox")
	seed("draft", "store-1", mail.RoleDrafts, "Drafts")
	seed("deleted", "store-1", mail.RoleDeleted, "Deleted Items")
	seed("junk", "store-1", mail.RoleJunk, "Junk Email")

	var seen []int64
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(r PriorityRow) error {
		seen = append(seen, r.MessageID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != visible {
		t.Fatalf("priority rows %v, want only the visible inbox message %d", seen, visible)
	}
}

func TestEachPriorityMessageMalformedJSONFails(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	id := answerMessage(t, s, mailtest.Message("src", "a"))
	answerVerdict(t, s, id, scope.Owner)
	priorityRoute(t, s, id, "fp", `{"q":`, "")

	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(PriorityRow) error { return nil }); err == nil {
		t.Fatal("malformed route answers must fail the read")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE route_answers SET answers = '{}' WHERE message_id = ?", id); err != nil {
		t.Fatal(err)
	}
	// The preview is stored at the message's revision: an older-revision
	// answer is deliberately ignored rather than decoded.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_preview_answers
		(message_id, fingerprint, revision, answers, run_id, decided_at)
		SELECT ?, 'fp', revision, '{', 0, 'x' FROM messages WHERE id = ?`, id, id); err != nil {
		t.Fatal(err)
	}
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(PriorityRow) error { return nil }); err == nil {
		t.Fatal("malformed preview answers must fail the read")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE route_preview_answers SET answers = '' WHERE message_id = ?", id); err != nil {
		t.Fatal(err)
	}
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(PriorityRow) error { return nil }); err == nil {
		t.Fatal("an empty preview answer must fail the read")
	}
	if err := s.EachPriorityMessage(ctx, "fp", false, nil, func(PriorityRow) error { return nil }); err != nil {
		t.Fatalf("preview off must not read preview rows: %v", err)
	}
}

func TestPriorityExcludesUnpickedStore(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "u")
	m.StoreID = "store-3"
	id := answerMessage(t, s, m)
	answerVerdict(t, s, id, scope.Owner)
	priorityRoute(t, s, id, "fp", `{}`, "")
	var seen int
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(PriorityRow) error { seen++; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("an unpicked store leaked into priority: %d rows", seen)
	}
	if err := s.cfg.Set(source.SettingSelected, mailtest.Picked([]string{"src"}, [2]string{"src", "store-3"})); err != nil {
		t.Fatal(err)
	}
	seen = 0
	if err := s.EachPriorityMessage(ctx, "fp", true, nil, func(PriorityRow) error { seen++; return nil }); err != nil || seen != 1 {
		t.Fatalf("picked store read: %d %v", seen, err)
	}
}

func TestPriorityHeadsIsBoundedAndOwnerVisible(t *testing.T) {
	s, _ := open(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"},
			[2]string{"src", "store-1"}, [2]string{"src", "store-2"}),
	})
	ctx := t.Context()
	if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-2", StoreName: "Shared",
		Path: "Inbox", Role: mail.RoleInbox, StoreKind: mail.StoreShared}); err != nil {
		t.Fatal(err)
	}
	seed := func(id, storeID string, role mail.FolderRole, path, body string) int64 {
		t.Helper()
		m := mailtest.Message("src", id)
		m.StoreID, m.FolderRole, m.FolderPath, m.Body = storeID, role, path, body
		rowID := answerMessage(t, s, m)
		answerVerdict(t, s, rowID, scope.Owner)
		return rowID
	}
	shortBody := "short body"
	longBody := strings.Repeat("x", priorityBodyChars+500)
	visible := seed("visible", "store-1", mail.RoleInbox, "Inbox", shortBody)
	seed("long", "store-1", mail.RoleInbox, "Inbox", "placeholder")
	longMessage := mailtest.Message("src", "long")
	longMessage.Body = longBody
	longID := answerMessage(t, s, longMessage)
	shared := seed("shared", "store-2", mail.RoleInbox, "Inbox", "shared body")
	draft := seed("draft", "store-1", mail.RoleDrafts, "Drafts", "draft body")
	deleted := seed("deleted", "store-1", mail.RoleDeleted, "Deleted Items", "deleted body")
	junk := seed("junk", "store-1", mail.RoleJunk, "Junk Email", "junk body")
	unpicked := seed("unpicked", "store-3", mail.RoleInbox, "Inbox", "unpicked body")

	var revision int64
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM messages WHERE id = ?", longID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision == 0 {
		t.Fatal("the long message did not get a new revision")
	}
	ids := []int64{visible, longID, shared, draft, deleted, junk, unpicked, 999999}
	heads, err := s.PriorityHeads(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 {
		t.Fatalf("heads %d, want only the two visible owner messages", len(heads))
	}
	short, ok := heads[visible]
	if !ok || short.Body != shortBody || short.Truncated || short.Revision != 0 {
		t.Fatalf("short head %+v ok=%v", short, ok)
	}
	long, ok := heads[longID]
	if !ok {
		t.Fatal("the long visible message is absent from the heads")
	}
	if got := len([]rune(long.Body)); got != priorityBodyChars {
		t.Fatalf("head has %d runes, want %d", got, priorityBodyChars)
	}
	if !long.Truncated {
		t.Fatal("a body longer than the head was not reported as truncated")
	}
	if long.Revision != revision {
		t.Fatalf("head revision %d, want %d", long.Revision, revision)
	}
	if want := string([]rune(longBody)[:priorityBodyChars]); long.Body != want {
		t.Fatal("the head is not the bounded beginning of the body")
	}
	for _, absent := range []int64{shared, draft, deleted, junk, unpicked, 999999} {
		if _, ok := heads[absent]; ok {
			t.Fatalf("id %d is not owner-visible but was returned", absent)
		}
	}
}

func TestPriorityHeadsEmptyIDs(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	// Apply the selection once, then close the pool: an empty batch that
	// queried would surface sql.ErrDBClosed instead of an empty map.
	if _, err := s.visible(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	heads, err := s.PriorityHeads(ctx, nil)
	if err != nil {
		t.Fatalf("an empty id batch queried the store: %v", err)
	}
	if heads == nil || len(heads) != 0 {
		t.Fatalf("empty heads %#v", heads)
	}
}

// TestEachPriorityReplySkipsPreviewAndTempSort pins the two shapes of the
// one priority read: the normal pass (no preview) must not mention
// route_preview_answers, and neither shape may sort into a temporary
// B-tree. The read is deliberately unordered: the live planner drives from
// scope_verdicts, so any ORDER BY would sort the whole mailbox. Both shapes
// return the same row set (the order is not promised), and a malformed
// preview cannot reach the pass that does not decode it.
func TestEachPriorityReplySkipsPreviewAndTempSort(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()

	// Folders are created in the opposite order to the messages that use
	// them, and the route rows are inserted out of message order. The read
	// does not promise an order; the ranking's own total order is tested in
	// internal/priority.
	for _, path := range []string{"Zeta", "Alpha"} {
		if _, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-1", StoreName: "Amélie",
			StoreKind: mail.StoreOther, Path: path, Role: mail.RoleInbox, ItemCount: -1}); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]int64, 0, 6)
	for i := range 6 {
		path := "Alpha"
		if i%2 == 1 {
			path = "Zeta"
		}
		m := mailtest.Message("src", fmt.Sprintf("m%d", i+1))
		m.FolderPath, m.FolderRole = path, mail.RoleInbox
		id := answerMessage(t, s, m)
		answerVerdict(t, s, id, scope.Owner)
		ids = append(ids, id)
	}
	validPreview := `{"reply_needed":{"type":"noul","value":0.7}}`
	for _, i := range []int{3, 0, 5, 2, 4, 1} {
		priorityRoute(t, s, ids[i], "fp", `{"reply_needed":{"type":"noul","value":0.9}}`, validPreview)
	}
	// A malformed preview row must be invisible to the normal pass: it is
	// never selected or decoded there.
	if _, err := s.db.ExecContext(ctx, `UPDATE route_preview_answers SET answers = '{' WHERE message_id = ?`, ids[2]); err != nil {
		t.Fatal(err)
	}

	replyKey := []QuestionKey{{Name: "reply_needed", Type: "noul"}}
	var off []PriorityRow
	if err := s.EachPriorityReply(ctx, "fp", false, replyKey, func(r PriorityRow) error {
		off = append(off, r)
		return nil
	}); err != nil {
		t.Fatalf("preview off must not read the malformed preview row: %v", err)
	}
	if len(off) != len(ids) {
		t.Fatalf("preview off returned %d rows, want %d", len(off), len(ids))
	}
	seen := make(map[int64]bool, len(off))
	for _, r := range off {
		if seen[r.MessageID] {
			t.Fatalf("preview off returned message %d twice", r.MessageID)
		}
		seen[r.MessageID] = true
		if r.Preview != nil {
			t.Fatalf("preview off decoded a preview on message %d: %+v", r.MessageID, r.Preview)
		}
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("preview off omitted message %d", id)
		}
	}

	if err := s.EachPriorityReply(ctx, "fp", true, replyKey, func(PriorityRow) error { return nil }); err == nil {
		t.Fatal("the preview read must keep the strict preview decode")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE route_preview_answers SET answers = ? WHERE message_id = ?`, validPreview, ids[2]); err != nil {
		t.Fatal(err)
	}
	var on []PriorityRow
	if err := s.EachPriorityReply(ctx, "fp", true, replyKey, func(r PriorityRow) error {
		on = append(on, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(on) != len(off) {
		t.Fatalf("preview on returned %d rows, want %d", len(on), len(off))
	}
	onByID := make(map[int64]PriorityRow, len(on))
	for _, r := range on {
		if got := r.Preview["reply_needed"].Value; got != 0.7 {
			t.Fatalf("message %d preview value %v, want 0.7", r.MessageID, got)
		}
		onByID[r.MessageID] = r
	}
	for _, o := range off {
		n, ok := onByID[o.MessageID]
		if !ok {
			t.Fatalf("preview on omitted message %d", o.MessageID)
		}
		o.Preview, n.Preview = nil, nil
		if !reflect.DeepEqual(o, n) {
			t.Fatalf("message %d differs between the two shapes:\n off %+v\n  on %+v", o.MessageID, o, n)
		}
	}

	offQuery := priorityQuery(visibleSQL, false, false, true)
	onQuery := priorityQuery(visibleSQL, true, false, true)
	for _, q := range []struct {
		name  string
		query string
	}{
		{"preview off", offQuery},
		{"preview on", onQuery},
	} {
		if strings.Contains(strings.ToUpper(q.query), "ORDER BY") {
			t.Fatalf("the %s priority read must stay unordered:\n%s", q.name, q.query)
		}
		plan := priorityQueryPlan(t, s, q.query, "fp")
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("the %s priority read sorts into a temporary B-tree:\n%s", q.name, plan)
		}
		if q.name == "preview off" && strings.Contains(plan, "route_preview_answers") {
			t.Fatalf("the preview-less priority read touches route_preview_answers:\n%s", plan)
		}
		if q.name == "preview on" && !strings.Contains(plan, "route_preview_answers") {
			t.Fatalf("the preview read does not join route_preview_answers:\n%s", plan)
		}
	}
}

// TestEachPriorityReplyDecodesOnlyTheSelectedAnswers pins the store-side
// half of the ranking read: selected answers decode exactly as the full
// read, unselected answers are neither returned nor shape-checked, and a
// malformed answer the caller does read still fails. The stored payload
// itself must remain valid JSON.
func TestEachPriorityReplyDecodesOnlyTheSelectedAnswers(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	id := answerMessage(t, s, mailtest.Message("src", "selective"))
	answerVerdict(t, s, id, scope.Owner)

	routeJSON := `{"reply_needed":{"type":"noul","value":0.9},
		"category":{"type":"choice","choice":"work"},
		"has_signature":{"type":"noul","value":0.8}}`
	previewJSON := `{"reply_needed":{"type":"noul","value":0.7},
		"has_signature":{"type":"noul","value":0.6}}`
	priorityRoute(t, s, id, "fp", routeJSON, previewJSON)

	keys := []QuestionKey{{Name: "reply_needed", Type: "noul"}, {Name: "category", Type: "choice"}}
	read := func(includePreview bool, needed []QuestionKey) (PriorityRow, error) {
		var row PriorityRow
		err := s.EachPriorityReply(ctx, "fp", includePreview, needed, func(r PriorityRow) error {
			row = r
			return nil
		})
		return row, err
	}
	setAnswers := func(route, preview string) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, `UPDATE route_answers SET answers = ? WHERE message_id = ?`, route, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE route_preview_answers SET answers = ? WHERE message_id = ?`, preview, id); err != nil {
			t.Fatal(err)
		}
	}

	partial, err := read(true, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Route) != 2 {
		t.Fatalf("selected route answers %v, want only the two selected keys", partial.Route)
	}
	if len(partial.Preview) != 1 {
		t.Fatalf("selected preview answers %v, want only the selected key", partial.Preview)
	}
	if _, ok := partial.Route["has_signature"]; ok {
		t.Fatal("an unselected route answer was decoded")
	}
	if _, ok := partial.Preview["has_signature"]; ok {
		t.Fatal("an unselected preview answer was decoded")
	}
	full, err := read(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Route) != 3 || len(full.Preview) != 2 {
		t.Fatalf("full read decoded %d route / %d preview answers, want 3/2", len(full.Route), len(full.Preview))
	}
	for _, key := range keys {
		if !reflect.DeepEqual(partial.Route[key.Name], full.Route[key.Name]) {
			t.Fatalf("selected route answer %q differs:\n partial %+v\n full %+v", key.Name, partial.Route[key.Name], full.Route[key.Name])
		}
	}
	if !reflect.DeepEqual(partial.Preview["reply_needed"], full.Preview["reply_needed"]) {
		t.Fatalf("selected preview answer differs:\n partial %+v\n full %+v", partial.Preview["reply_needed"], full.Preview["reply_needed"])
	}

	// A malformed unselected answer is skipped (the payload is still valid
	// JSON), while the same malformed value on a selected key fails.
	setAnswers(`{"reply_needed":{"type":"noul","value":0.9},"has_signature":42}`, previewJSON)
	if _, err := read(false, keys); err != nil {
		t.Fatalf("a malformed unselected route answer must be skipped: %v", err)
	}
	if _, err := read(false, nil); err == nil {
		t.Fatal("the full read must refuse the malformed route answer")
	}
	setAnswers(routeJSON, `{"reply_needed":{"type":"noul","value":0.7},"has_signature":42}`)
	if _, err := read(true, keys); err != nil {
		t.Fatalf("a malformed unselected preview answer must be skipped: %v", err)
	}
	if _, err := read(true, nil); err == nil {
		t.Fatal("the full read must refuse the malformed preview answer")
	}
	setAnswers(`{"reply_needed":42}`, previewJSON)
	if _, err := read(false, keys); err == nil {
		t.Fatal("a malformed selected route answer must fail")
	}
	setAnswers(routeJSON, `{"reply_needed":42}`)
	if _, err := read(true, keys); err == nil {
		t.Fatal("a malformed selected preview answer must fail")
	}

	// The document itself must parse: an unselected value with invalid JSON
	// syntax is not a shape to skip.
	setAnswers(`{"reply_needed":{"type":"noul","value":0.9},"has_signature":}`, previewJSON)
	if _, err := read(false, keys); err == nil {
		t.Fatal("invalid JSON in an unselected answer must fail the read")
	}
}

func TestPriorityBodyProjectionAndPriorityHeadsAreBounded(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	full := strings.Repeat(" ", priorityBodyChars+1000) + strings.Repeat("visible text ", 40)
	m := mailtest.Message("src", "long")
	m.Subject, m.Body = "long", full
	id := answerMessage(t, s, m)
	answerVerdict(t, s, id, scope.Owner)
	priorityRoute(t, s, id, "fp", `{}`, "")

	var row PriorityRow
	if err := s.EachPriorityMessage(ctx, "fp", false, nil, func(r PriorityRow) error {
		row = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len([]rune(row.Body)) != priorityBodyChars {
		t.Fatalf("projected body has %d runes, want %d", len([]rune(row.Body)), priorityBodyChars)
	}
	if !row.BodyTruncated {
		t.Fatal("a body longer than the projection was not reported as truncated")
	}
	body, err := s.MessageBody(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if body != full {
		t.Fatalf("MessageBody returned %d runes, want the full %d", len([]rune(body)), len([]rune(full)))
	}
	heads, err := s.PriorityHeads(ctx, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	head, ok := heads[id]
	if !ok {
		t.Fatal("PriorityHeads omitted the owner-visible message")
	}
	if head.Body != row.Body || !head.Truncated || head.Revision != row.Revision {
		t.Fatalf("head %+v differs from the projection %+v", head, row)
	}
}
