package store

// The priority read decodes each row's independent route and preview
// answers in a bounded worker pool and delivers rows in the scan's own
// order. These tests pin that contract against a serial reference: exact
// rows, exact order, exact metadata, exact maps and the exact strict error.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// benchQuestionKeys turns the ranking's selection map into the key slice the
// read takes. The map's own order is irrelevant: every consumer keys by name.
func benchQuestionKeys(selected map[string]QuestionKey) []QuestionKey {
	keys := make([]QuestionKey, 0, len(selected))
	for _, key := range selected {
		keys = append(keys, key)
	}
	return keys
}

// serialPriorityRead is the pre-block read: one scanned row is decoded
// immediately and handed to fn before the next row is read. It is the
// reference the block pipeline must match exactly, including the error text
// and the delivered prefix. It intentionally repeats the old loop instead of
// calling production code.
func serialPriorityRead(ctx context.Context, tb testing.TB, s *Store, fingerprint string, includePreview, body bool,
	questions []QuestionKey, fn func(PriorityRow) error) error {
	tb.Helper()
	vis, err := s.visible(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, priorityQuery(vis, includePreview, body, true), fingerprint)
	if err != nil {
		return err
	}
	defer rows.Close()
	selected := priorityQuestionSelection(questions)
	for rows.Next() {
		var raw priorityRawRow
		if err := scanPriorityRow(rows, &raw); err != nil {
			return err
		}
		raw.decodeAnswers(selected, includePreview)
		if raw.decodeErr != nil {
			return raw.decodeErr
		}
		if err := fn(raw.row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// priorityPackedRoute is a valid selected route payload that differs from
// priorityBenchAnswers, so the packed fixture mixes full, empty and partial
// answer maps.
const priorityPackedRoute = `{"category":{"type":"choice","choice":"personal","probabilities":{"personal":0.7}},
	"urgency":{"type":"score","value":4.2,"probabilities":{"0":0.01,"1":0.02,"2":0.05,"3":0.12,"4":0.8}},
	"reply_needed":{"type":"noul","value":0.1}}`

// priorityPackedPreview is a valid preview payload in the full engine shape
// (no route-only layer/rule fields).
const priorityPackedPreview = `{"category":{"type":"choice","choice":"personal","probabilities":{"personal":0.7},"confidence":0.7},
	"urgency":{"type":"score","value":4.2,"probabilities":{"0":0.01,"1":0.02,"2":0.05,"3":0.12,"4":0.8},"confidence":0.8},
	"reply_needed":{"type":"noul","value":0.1}}`

// seedPriorityPacked seeds n owner-visible messages with route answers and,
// on rows where preview is non-empty, a preview row. order permutes the
// insertion order of the messages without changing their payloads.
func seedPriorityPacked(t *testing.T, s *Store, n int, order []int) []int64 {
	t.Helper()
	ctx := t.Context()
	msgs := make([]mail.Message, 0, n)
	for _, i := range order {
		m := mailtest.Message("src", fmt.Sprintf("packed-%03d", i))
		m.Subject = fmt.Sprintf("packed %d", i)
		msgs = append(msgs, m)
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, n)
	verdicts := make([]VerdictRow, 0, n)
	for _, i := range order {
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE msg_id = ?`, fmt.Sprintf("packed-%03d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		verdicts = append(verdicts, VerdictRow{RowID: id, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag})
	}
	if err := s.WriteVerdicts(ctx, "test", verdicts); err != nil {
		t.Fatal(err)
	}
	routes := []string{priorityBenchAnswers, `{}`, priorityPackedRoute}
	previews := []string{priorityPackedPreview, "", `{}`}
	for _, i := range order {
		priorityRoute(t, s, ids[i], "fp", routes[i%len(routes)], previews[i%len(previews)])
	}
	return ids
}

// packedOrder returns the insertion order permutations the packed tests use.
func packedOrder(n int, reverse bool) []int {
	order := make([]int, n)
	for i := range n {
		if reverse {
			order[i] = n - 1 - i
		} else {
			order[i] = i
		}
	}
	return order
}

// scanPriorityIDs returns the ids the query itself yields, in scan order,
// with no decode in between. It is the order the block pipeline must keep.
func scanPriorityIDs(t *testing.T, s *Store, fingerprint string) []int64 {
	t.Helper()
	vis, err := s.visible(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(t.Context(), priorityQuery(vis, false, false, true), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	// The whole query is scanned with the production destination list, so
	// the order is exactly the block read's. Only the id is kept.
	var (
		id                                                                                 int64
		revision                                                                           int64
		conversation, subject, senderName, senderAddress, receivedAt, role, body, rawRoute string
		hasAttachments, truncated, storedDue                                               bool
		rawPreview                                                                         sql.NullString
		routeRevision                                                                      int64
		previewRevision                                                                    sql.NullInt64
	)
	for rows.Next() {
		if err := rows.Scan(&id, &revision, &conversation, &subject, &senderName, &senderAddress, &receivedAt,
			&role, &hasAttachments, &storedDue, &body, &truncated, &rawRoute, &rawPreview,
			&routeRevision, &previewRevision); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// TestEachPriorityReplyMatchesTheSerialRead is the full parity check on a
// packed fixture that crosses the block boundary: same rows in the same
// order, same metadata, same decoded route and preview maps, and the same
// strict error (message id included) for malformed selected answers, a nil
// answers object and a malformed preview. The serial reference is the old
// one-row-at-a-time read.
func TestEachPriorityReplyMatchesTheSerialRead(t *testing.T) {
	s, _ := open(t, nil)
	const n = 600 // one full block plus a partial second
	seedPriorityPacked(t, s, n, packedOrder(n, true))
	questions := benchQuestionKeys(priorityBenchSelection())

	read := func(preview, body bool) ([]PriorityRow, error) {
		var got []PriorityRow
		err := s.eachPriorityMessage(t.Context(), "fp", preview, questions, body, true, func(r PriorityRow) error {
			got = append(got, r)
			return nil
		})
		return got, err
	}
	serial := func(preview, body bool) ([]PriorityRow, error) {
		var want []PriorityRow
		err := serialPriorityRead(t.Context(), t, s, "fp", preview, body, questions, func(r PriorityRow) error {
			want = append(want, r)
			return nil
		})
		return want, err
	}
	compare := func(name string, got, want []PriorityRow, gotErr, wantErr error) {
		t.Helper()
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%s: error %v, serial %v", name, gotErr, wantErr)
		}
		if gotErr != nil && gotErr.Error() != wantErr.Error() {
			t.Fatalf("%s: error\n got %q\nwant %q", name, gotErr, wantErr)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: delivered %d rows, serial %d", name, len(got), len(want))
		}
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("%s: row %d\n got %+v\nwant %+v", name, i, got[i], want[i])
			}
		}
		if len(got) > 0 && got[0].MessageID == 0 {
			t.Fatalf("%s: the first delivered row has no message id", name)
		}
	}

	got, gotErr := read(true, false)
	want, wantErr := serial(true, false)
	compare("reply preview on", got, want, gotErr, wantErr)
	if len(got) != n {
		t.Fatalf("delivered %d rows, want %d", len(got), n)
	}
	var withPreview int
	for _, r := range got {
		if r.Route == nil {
			t.Fatalf("message %d has a nil route map", r.MessageID)
		}
		if r.Preview != nil {
			withPreview++
		}
	}
	if withPreview != n/3*2 {
		t.Fatalf("preview rows %d, want %d", withPreview, n/3*2)
	}

	got, gotErr = read(false, true)
	want, wantErr = serial(false, true)
	compare("message body", got, want, gotErr, wantErr)

	// Malformed selected route answers: the error names the message and the
	// delivered prefix is the serial prefix. The row sits in the second
	// block, so the prefix also proves block one was delivered in full.
	update := func(table, column, value string, id int64) {
		t.Helper()
		if _, err := s.db.ExecContext(t.Context(), `UPDATE `+table+` SET `+column+` = ? WHERE message_id = ?`, value, id); err != nil {
			t.Fatal(err)
		}
	}
	scanOrder := scanPriorityIDs(t, s, "fp")
	if len(scanOrder) != n {
		t.Fatalf("query scanned %d rows, want %d", len(scanOrder), n)
	}
	const badAt = 521 // in the second block
	badID := scanOrder[badAt]
	// The malformed preview cases need a preview row on the chosen message.
	var hasPreview int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM route_preview_answers WHERE message_id = ?`, badID).Scan(&hasPreview); err != nil {
		t.Fatal(err)
	}
	if hasPreview == 0 {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO route_preview_answers
			(message_id, fingerprint, revision, answers, run_id, decided_at) VALUES (?, 'fp', 1, ?, 0, '2026-10-26T00:00:00Z')`,
			badID, priorityPackedPreview); err != nil {
			t.Fatal(err)
		}
	}
	update("route_answers", "answers", `{"category":`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("malformed route answer", got, want, gotErr, wantErr)
	if gotErr == nil || !strings.Contains(gotErr.Error(), fmt.Sprintf("of message %d:", badID)) {
		t.Fatalf("malformed route error %v does not name message %d", gotErr, badID)
	}
	if len(got) != badAt {
		t.Fatalf("malformed route delivered %d rows, want the serial %d", len(got), badAt)
	}

	// A selected answer of the wrong shape is malformed too; an unselected
	// one stays skipped.
	update("route_answers", "answers", `{"category":42}`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("malformed selected answer", got, want, gotErr, wantErr)
	update("route_answers", "answers", `{"has_signature":42}`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("skipped malformed answer", got, want, gotErr, wantErr)

	// A nil route answers object is an error, not an empty map.
	update("route_answers", "answers", `null`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("nil route object", got, want, gotErr, wantErr)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "no answers object") {
		t.Fatalf("nil route object error %v", gotErr)
	}
	update("route_answers", "answers", priorityBenchAnswers, badID)

	// Preview is decoded only when requested: a malformed preview fails the
	// preview read and stays invisible to the preview-off read, exactly as
	// the serial read does.
	update("route_preview_answers", "answers", `{`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("malformed preview", got, want, gotErr, wantErr)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "priority preview answers") {
		t.Fatalf("malformed preview error %v", gotErr)
	}
	got, gotErr = read(false, false)
	want, wantErr = serial(false, false)
	compare("malformed preview ignored", got, want, gotErr, wantErr)
	if gotErr != nil {
		t.Fatalf("preview off failed on a malformed preview: %v", gotErr)
	}

	// A nil preview answers object is an error only on the preview read.
	update("route_preview_answers", "answers", `null`, badID)
	got, gotErr = read(true, false)
	want, wantErr = serial(true, false)
	compare("nil preview object", got, want, gotErr, wantErr)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "no answers object") {
		t.Fatalf("nil preview object error %v", gotErr)
	}
}

// TestEachPriorityReplyPreservesTheScanOrder pins the delivery order: rows
// leave the pipeline in exactly the order the query scans them, across block
// boundaries and for two opposite physical arrangements. The ranking is
// order-independent, so no other order is promised.
func TestEachPriorityReplyPreservesTheScanOrder(t *testing.T) {
	questions := benchQuestionKeys(priorityBenchSelection())
	for _, reverse := range []bool{false, true} {
		name := "insertion order"
		if reverse {
			name = "reverse insertion order"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := open(t, nil)
			const n = 600
			seedPriorityPacked(t, s, n, packedOrder(n, reverse))
			scanned := scanPriorityIDs(t, s, "fp")
			if len(scanned) != n {
				t.Fatalf("query scanned %d rows, want %d", len(scanned), n)
			}
			var delivered []int64
			if err := s.EachPriorityReply(t.Context(), "fp", true, questions, func(r PriorityRow) error {
				delivered = append(delivered, r.MessageID)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(delivered, scanned) {
				t.Fatalf("delivery order differs from the scan order:\n got %v\nwant %v", delivered, scanned)
			}
		})
	}
}

// TestEachPriorityReplyStopsAtTheFirstError pins the stop contract: a fn
// error ends the stream at the same row the serial read ends it, and a
// canceled context stops delivery promptly and returns the context error
// without leaking a worker.
func TestEachPriorityReplyStopsAtTheFirstError(t *testing.T) {
	s, _ := open(t, nil)
	const n = 600
	seedPriorityPacked(t, s, n, packedOrder(n, false))
	questions := benchQuestionKeys(priorityBenchSelection())

	stop := errors.New("stop")
	for _, at := range []int{0, 1, 255, 256, 299} {
		delivered := 0
		err := s.EachPriorityReply(t.Context(), "fp", false, questions, func(PriorityRow) error {
			delivered++
			if delivered == at+1 {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("fn error at %d: %v", at, err)
		}
		if delivered != at+1 {
			t.Fatalf("fn error at %d delivered %d rows", at, delivered)
		}
	}

	// fn errors at the same position through the serial reference.
	for _, at := range []int{255, 256} {
		delivered := 0
		err := serialPriorityRead(t.Context(), t, s, "fp", false, false, questions, func(PriorityRow) error {
			delivered++
			if delivered == at+1 {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) || delivered != at+1 {
			t.Fatalf("serial fn error at %d: delivered %d, %v", at, delivered, err)
		}
	}

	// Cancellation: no row is delivered after fn cancels the read, and the
	// read reports the context error.
	before := runtime.NumGoroutine()
	for range 25 {
		ctx, cancel := context.WithCancel(t.Context())
		delivered := 0
		err := s.EachPriorityReply(ctx, "fp", true, questions, func(PriorityRow) error {
			delivered++
			if delivered == 3 {
				cancel()
			}
			return nil
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled read: delivered %d, error %v", delivered, err)
		}
		if delivered != 3 {
			t.Fatalf("canceled read delivered %d rows, want 3", delivered)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("canceled reads leaked goroutines: %d before, %d after", before, after)
	}

	// An already canceled context never reaches a row.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	delivered := 0
	err := s.EachPriorityReply(ctx, "fp", false, questions, func(PriorityRow) error {
		delivered++
		return nil
	})
	if !errors.Is(err, context.Canceled) || delivered != 0 {
		t.Fatalf("pre-canceled read: delivered %d, error %v", delivered, err)
	}
}

// prioritySyntheticRows is the whole-mailbox row count the synthetic
// benchmark replays through the block pipeline.
const prioritySyntheticRows = 35000

// prioritySyntheticSource replays prebuilt answer payloads through the
// priorityRowSource the block loop consumes. Each scan clones the payload,
// as the SQL driver allocates a fresh string for each text column.
type prioritySyntheticSource struct {
	pos     int
	total   int
	routes  []string
	preview []string
	body    string
}

func (src *prioritySyntheticSource) Next() bool {
	if src.pos >= src.total {
		return false
	}
	src.pos++
	return true
}

func (src *prioritySyntheticSource) Err() error { return nil }

func (src *prioritySyntheticSource) Scan(dest ...any) error {
	i := src.pos - 1
	*dest[0].(*int64) = int64(i + 1)
	*dest[1].(*int64) = int64(i % 7)
	*dest[2].(*string) = "conversation"
	*dest[3].(*string) = "packed subject"
	*dest[4].(*string) = "Alice"
	*dest[5].(*string) = "alice@example.com"
	*dest[6].(*string) = "2026-11-02T10:00:00Z"
	*dest[7].(*string) = string(mail.RoleInbox)
	*dest[8].(*bool) = i%5 == 0
	*dest[9].(*string) = src.body
	*dest[10].(*bool) = false
	*dest[11].(*string) = strings.Clone(src.routes[i%len(src.routes)])
	preview := src.preview[i%len(src.preview)]
	*dest[12].(*sql.NullString) = sql.NullString{String: strings.Clone(preview), Valid: preview != ""}
	return nil
}

func prioritySyntheticBenchSource() *prioritySyntheticSource {
	return &prioritySyntheticSource{
		total:   prioritySyntheticRows,
		routes:  []string{priorityBenchAnswers, priorityPackedRoute, `{}`},
		preview: []string{priorityPackedPreview, "", priorityPackedPreview},
		body:    strings.Repeat("b", priorityBodyChars),
	}
}

// BenchmarkPriorityDecodeSyntheticSerial is the pre-block reference: one
// row is scanned and decoded before the next, exactly as the old read did.
func BenchmarkPriorityDecodeSyntheticSerial(b *testing.B) {
	selected := priorityBenchSelection()
	for _, preview := range []bool{false, true} {
		b.Run(fmt.Sprintf("preview=%v", preview), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				src := prioritySyntheticBenchSource()
				rows := 0
				for src.Next() {
					var raw priorityRawRow
					var role string
					if err := src.Scan(&raw.row.MessageID, &raw.row.Revision, &raw.row.ConversationID, &raw.row.Subject,
						&raw.row.SenderName, &raw.row.SenderAddress, &raw.row.ReceivedAt, &role,
						&raw.row.HasAttachments, &raw.row.Body, &raw.row.BodyTruncated, &raw.rawRoute, &raw.rawPreview); err != nil {
						b.Fatal(err)
					}
					raw.row.FolderRole = mail.FolderRole(role)
					raw.decodeAnswers(selected, preview)
					if raw.decodeErr != nil {
						b.Fatal(raw.decodeErr)
					}
					rows++
				}
				if rows != prioritySyntheticRows {
					b.Fatalf("scanned %d rows, want %d", rows, prioritySyntheticRows)
				}
			}
		})
	}
}

// BenchmarkPriorityDecodeSyntheticParallel runs the production block
// pipeline over the same 35,000 rows.
func BenchmarkPriorityDecodeSyntheticParallel(b *testing.B) {
	selected := priorityBenchSelection()
	for _, preview := range []bool{false, true} {
		b.Run(fmt.Sprintf("preview=%v", preview), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				src := prioritySyntheticBenchSource()
				rows := 0
				if err := eachPriorityBlock(b.Context(), src, selected, preview, true, func(PriorityRow) error {
					rows++
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if rows != prioritySyntheticRows {
					b.Fatalf("delivered %d rows, want %d", rows, prioritySyntheticRows)
				}
			}
		})
	}
}
