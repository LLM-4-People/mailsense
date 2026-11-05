package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
)

// PriorityRow is one owner message with its route answers and, when the
// explicit test preview is on, its threshold-free preview answers. Route
// and Preview are typed maps decoded strictly at this trust boundary and
// hold only the question keys the caller selected (or every key when the
// selection is empty). A selected route answer materializes only the fields
// the ranking reads: type, refused, value, choice, abstain and a score's
// distribution; a selected preview answer keeps the full engine shape
// because the preview validation reads the confidence and the complete
// distribution. A nil selection (the full-read fallback) keeps the full
// route shape. A selected answer with malformed JSON or an unknown field
// fails the read; a field the ranking never reads is syntax-checked and
// consumed, never shape-validated. An unselected answer is skipped without
// shape validation, although the stored payload must still be valid JSON. A
// message with no preview row has a nil Preview (never zero-filled).
type PriorityRow struct {
	MessageID      int64
	Revision       int64
	ConversationID string
	Subject        string
	SenderName     string
	SenderAddress  string
	ReceivedAt     string // mail.Instant: sent_on for a sent item, received_at otherwise
	FolderRole     mail.FolderRole
	HasAttachments bool
	// StoredDue reports an open follow-up date the mailbox stores for this
	// message. A completed flag is not open work.
	StoredDue bool
	Body      string
	// BodyTruncated reports that Body is only the bounded head of a longer
	// stored body, so a consumer may need the full text for a snippet.
	BodyTruncated bool
	Route         map[string]route.Answer
	Preview       map[string]engine.Answer
}

// priorityBodyChars is the bounded body head the ranking reads for the
// cached top 200 items. Scoring reads no body text at all, and the item
// snippet is 180 runes, so this head is enough for almost every snippet;
// a body that starts with more whitespace than the head falls back to
// MessageBody for that one item. Streaming a multi-kilobyte body for every
// owner message was most of the scan's I/O on a large mailbox.
const priorityBodyChars = 2000

// PriorityBody is the bounded head of one owner-visible message body. The
// revision is the message revision the head was projected from, so a caller
// can discard a head that no longer matches the row it scored. Truncated is
// true when the stored body is longer than the head.
type PriorityBody struct {
	Body      string
	Truncated bool
	Revision  int64
}

// EachPriorityMessage streams the owner-facing mail that has route answers
// under fingerprint (visible folders of picked stores only), with the
// matching preview row when includePreview is true. It uses the same
// content boundary as the conversation digest: shared/delegate/public
// stores and drafts, deleted mail and junk never reach a subject, a body or
// a snippet. It never holds the mailbox in memory; fn returning an error
// stops. questions names the profile questions the caller reads: only those
// answer keys are decoded per row, a route answer materializes only the
// fields the ranking reads (see PriorityRow), and an empty selection decodes
// every key in full (the full-read fallback). A selected route row with
// invalid JSON, an unknown field or a malformed retained field fails the
// whole read, and so does a malformed selected preview row when
// includePreview is true (the preview table is not read at all otherwise): a
// stored answer the caller reads is display data, never silently an absent
// one. An unselected answer is skipped without shape validation, but the
// stored payload itself must still be valid JSON. Body is the bounded head
// (priorityBodyChars) the caller's snippet needs, never the full body. Rows
// are streamed in the planner's order; no order is promised.
func (s *Store) EachPriorityMessage(ctx context.Context, fingerprint string, includePreview bool, questions []QuestionKey, fn func(PriorityRow) error) error {
	return s.eachPriorityMessage(ctx, fingerprint, includePreview, questions, true, true, fn)
}

// EachPriorityReply is EachPriorityMessage without the body: the pass that
// only resolves reply obligations and conversation ids reads no message
// text at all. questions has the same meaning as in EachPriorityMessage.
func (s *Store) EachPriorityReply(ctx context.Context, fingerprint string, includePreview bool, questions []QuestionKey, fn func(PriorityRow) error) error {
	return s.eachPriorityMessage(ctx, fingerprint, includePreview, questions, false, true, fn)
}

// priorityQuery builds the one priority read. The preview join and column
// exist only when the caller decodes the preview; the normal priority pass
// asks for no preview, so it never reads route_preview_answers at all (a
// NULL preview scans as the absent nil the caller already handles).
//
// The query deliberately has no ORDER BY. The live planner drives from
// scope_verdicts (the owner population), and any order over the joined rows
// forces a temporary B-tree over the whole mailbox; sorting by the driving
// index does not help once the planner chooses another table. The read is
// therefore streamed unordered. Its one consumer is order-independent: the
// ranking heap has a total order (score, received, message id) and the topic
// evidence sorts itself. Consumers that need another order sort their own
// result.
func priorityQuery(vis string, includePreview, body, decode bool) string {
	bodySQL, truncatedSQL := "''", "0"
	if body {
		bodySQL = fmt.Sprintf("substr(m.body, 1, %d)", priorityBodyChars)
		truncatedSQL = fmt.Sprintf("length(m.body) > %d", priorityBodyChars)
	}
	routeSQL, previewSQL, previewJoin := "ra.answers", "NULL", ""
	if !decode {
		routeSQL = "''"
	}
	if includePreview && decode {
		previewSQL = "rp.answers"
		previewJoin = `
		LEFT JOIN route_preview_answers rp ON rp.message_id = m.id AND rp.fingerprint = ra.fingerprint`
	}
	routeRevSQL, previewRevSQL := "ra.revision", "NULL"
	if includePreview && decode {
		previewRevSQL = "rp.revision"
	}
	return `SELECT m.id, m.revision, m.conversation_id, m.subject, m.sender_name, m.sender_address,
		` + instantSQL + `, f.role, m.has_attachments,
		CASE WHEN COALESCE(m.flag_status, 0) <> 1 AND m.task_due_date <> '' THEN 1 ELSE 0 END,
		` + bodySQL + `, ` + truncatedSQL + `, ` + routeSQL + `, ` + previewSQL + `,
		` + routeRevSQL + `, ` + previewRevSQL + `
		FROM route_answers ra
		JOIN messages m ON m.id = ra.message_id
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id` + previewJoin + `
		WHERE ` + ownerFacingWhere(vis) + ` AND ra.fingerprint = ?`
}

// priorityQuestionSelection turns the caller's question keys into the
// answers to decode: each selected name maps to its key, so the route
// decode can tell a score answer from the rest without importing the
// taxonomy. A nil result selects every answer: the full-read fallback for
// a caller that names no questions.
func priorityQuestionSelection(questions []QuestionKey) map[string]QuestionKey {
	if len(questions) == 0 {
		return nil
	}
	selected := make(map[string]QuestionKey, len(questions))
	for _, q := range questions {
		selected[q.Name] = q
	}
	return selected
}

// discardJSON accepts and validates any JSON value without retaining it.
// The decoder's scanner already checks the value's syntax, so an unselected
// answer costs no retained map, pointer or string.
type discardJSON struct{}

func (*discardJSON) UnmarshalJSON([]byte) error { return nil }

// The skipped fields keep the JSON kind route.Answer declares for them, so
// priority and triage accept exactly the same stored rows: only the value
// is discarded, never the shape. Each decoder runs after the scanner has
// validated the syntax; it checks the kind the old full decode enforced.
func discardKind(b []byte, want byte) error {
	if len(b) == 4 && string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == want {
		return nil
	}
	if want == '"' {
		return fmt.Errorf("expected a JSON string")
	}
	return fmt.Errorf("expected a JSON object")
}

type discardString struct{}

func (*discardString) UnmarshalJSON(b []byte) error { return discardKind(b, '"') }

type discardNumber struct{}

func (*discardNumber) UnmarshalJSON(b []byte) error {
	if len(b) == 4 && string(b) == "null" {
		return nil
	}
	if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		return fmt.Errorf("expected a JSON number")
	}
	return nil
}

type discardObject struct{}

func (*discardObject) UnmarshalJSON(b []byte) error { return discardKind(b, '{') }

// priorityRouteFields is the part of one stored route answer the ranking
// reads. Every field route.Answer can carry stays declared, so
// DisallowUnknownFields still refuses a genuinely unknown field; a field
// the ranking never reads keeps its JSON kind (number, string or object)
// but is not materialized. It is shared by the two answer shapes below so
// the field set has one owner.
type priorityRouteFields struct {
	Type       engine.QuestionType `json:"type"`
	Refused    bool                `json:"refused,omitempty"`
	Value      float64             `json:"value,omitempty"`
	Choice     string              `json:"choice,omitempty"`
	Abstain    string              `json:"abstain,omitempty"`
	Confidence discardNumber       `json:"confidence,omitempty"`
	Layer      discardString       `json:"layer,omitempty"`
	Rule       discardString       `json:"rule,omitempty"`
}

// answer is the engine answer the lean route shape materializes. A score
// answer adds its distribution, the only one the ranking reads.
func (a priorityRouteFields) answer() engine.Answer {
	return engine.Answer{Type: a.Type, Refused: a.Refused, Value: a.Value, Choice: a.Choice, Abstain: a.Abstain}
}

// priorityScoreAnswer keeps a score's level distribution: the ranking's
// level is the class selective.Decide chooses from it, which can differ
// from round(Value).
type priorityScoreAnswer struct {
	priorityRouteFields
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

func (a priorityScoreAnswer) routeAnswer() route.Answer {
	out := route.Answer{Answer: a.answer()}
	out.Probabilities = a.Probabilities
	return out
}

// priorityFlatAnswer is every non-score route answer: its distribution is
// never read, so it is consumed without being retained.
type priorityFlatAnswer struct {
	priorityRouteFields
	Probabilities discardObject `json:"probabilities,omitempty"`
}

func (a priorityFlatAnswer) routeAnswer() route.Answer {
	return route.Answer{Answer: a.answer()}
}

// decodePriorityRouteAnswer decodes one selected route answer into the lean
// shape the ranking reads. A score answer keeps its level distribution;
// every other answer discards the distribution, confidence, layer and rule
// it never reads. The zero QuestionKey means the caller made no selection
// (the full-read fallback), so the whole answer is decoded exactly as
// before.
func decodePriorityRouteAnswer(dec *json.Decoder, key QuestionKey) (route.Answer, error) {
	switch key.Type {
	case "":
		var a route.Answer
		return a, dec.Decode(&a)
	case string(engine.Score):
		var a priorityScoreAnswer
		if err := dec.Decode(&a); err != nil {
			return route.Answer{}, err
		}
		return a.routeAnswer(), nil
	default:
		var a priorityFlatAnswer
		if err := dec.Decode(&a); err != nil {
			return route.Answer{}, err
		}
		return a.routeAnswer(), nil
	}
}

// decodePriorityPreviewAnswer keeps the full engine.Answer decode: the
// preview path feeds checkPreviewAnswer, which validates the confidence and
// the complete distribution.
func decodePriorityPreviewAnswer(dec *json.Decoder, _ QuestionKey) (engine.Answer, error) {
	var a engine.Answer
	return a, dec.Decode(&a)
}

// decodePriorityAnswers decodes one stored answers object, the selected
// names through decode and every other name skipped. One streaming pass
// walks the object and materializes only the selected typed answers, so the
// ranking's fifteen questions do not pay for the profile's extra questions.
// A selected value is decoded directly by the same decoder with unknown
// fields refused: the old strict shape without a raw copy or a second
// parse. An unselected value must be syntactically valid JSON, but its
// shape is not validated and it is not retained: a malformed answer the
// caller never reads cannot fail the read. Route and preview answers share
// this one policy but not the decoded shape.
func decodePriorityAnswers[T any](raw string, selected map[string]QuestionKey,
	decode func(*json.Decoder, QuestionKey) (T, error)) (map[string]T, error) {
	out := make(map[string]T, len(selected))
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		// JSON null: the caller reports the absent answers object exactly as
		// the old full-map decode did.
		return nil, nil
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected an answers object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("expected an answers object key")
		}
		var key QuestionKey
		if selected != nil {
			key, ok = selected[name]
			if !ok {
				// Consume the unselected value to its end: the decoder
				// validates its syntax but never materializes or retains its
				// typed answer.
				var discard discardJSON
				if err := dec.Decode(&discard); err != nil {
					return nil, err
				}
				continue
			}
		}
		answer, err := decode(dec, key)
		if err != nil {
			return nil, err
		}
		out[name] = answer
	}
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if delim, ok := tok.(json.Delim); !ok || delim != '}' {
		return nil, fmt.Errorf("expected the answers object to close")
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing data after the answers object")
		}
		return nil, err
	}
	return out, nil
}

const (
	// priorityBlockRows is the raw scan block the priority read decodes
	// together. A row retains its bounded body head and its two stored
	// answer payloads, so one block stays a few megabytes: peak retention
	// is bounded by the block, not by the mailbox. 512 rows had the best
	// median serial/block ratio in the block-size benchmark; 128 lost more
	// to the per-block barrier and 256 was consistently a little behind.
	priorityBlockRows = 512
	// priorityDecodeWorkers bounds the decode pool. The per-row work is
	// strict JSON decoding (allocation- and parser-bound), which stops
	// scaling past a few Ps, and the priority build shares the machine
	// with the rest of the startup warm. Four is also the GOMAXPROCS the
	// startup evidence runs with, so the pool never oversubscribes there.
	priorityDecodeWorkers = 4
)

// priorityRowSource is the read side of the priority query: the block loop
// needs Next, Scan and Err and nothing else. The production source is the
// one *sql.Rows of the read; a test can drive the same loop from raw rows.
type priorityRowSource interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// priorityRawRow is one scanned row whose independent JSON payloads are not
// decoded yet. The scalar fields are final; route and preview are decoded
// by a block worker, and decodeErr holds the exact error the serial read
// would have reported for this row.
type priorityRawRow struct {
	row        PriorityRow
	rawRoute   string
	rawPreview sql.NullString
	// routeRevision and previewRevision are the revisions the stored
	// answers were computed at. A message edited after the answer was
	// written has a newer revision, and that answer is obsolete: scoring it
	// would rank a message on a classification the message no longer has.
	routeRevision   int64
	previewRevision sql.NullInt64
	decodeErr       error
}

// scanPriorityRow scans one row and materializes its scalar fields. The two
// answer payloads stay raw until the block decode.
func scanPriorityRow(rows priorityRowSource, raw *priorityRawRow) error {
	var role string
	if err := rows.Scan(&raw.row.MessageID, &raw.row.Revision, &raw.row.ConversationID, &raw.row.Subject,
		&raw.row.SenderName, &raw.row.SenderAddress, &raw.row.ReceivedAt, &role, &raw.row.HasAttachments,
		&raw.row.StoredDue, &raw.row.Body, &raw.row.BodyTruncated, &raw.rawRoute, &raw.rawPreview,
		&raw.routeRevision, &raw.previewRevision); err != nil {
		return err
	}
	raw.row.FolderRole = mail.FolderRole(role)
	return nil
}

// decodeAnswers decodes this row's independent answer objects: the route
// answers always, the preview answers only when the caller asked for them
// and the joined row exists. The first route failure ends the row exactly
// as the serial read did (the preview is not decoded after it), and the
// error text is unchanged. Decoding is stateless, so a worker needs no
// scratch state of its own.
func (raw *priorityRawRow) decodeAnswers(selected map[string]QuestionKey, includePreview bool) {
	if raw.routeRevision != raw.row.Revision {
		// The message was edited after its route answer was written. The
		// stored classification is obsolete; the ranking must treat the
		// message as unanswered rather than score the old answer.
		raw.rawRoute = ""
		raw.rawPreview = sql.NullString{}
		return
	}
	routeAnswers, err := decodePriorityAnswers(raw.rawRoute, selected, decodePriorityRouteAnswer)
	if err != nil {
		raw.decodeErr = fmt.Errorf("store: priority route answers of message %d: %w", raw.row.MessageID, err)
		return
	}
	raw.row.Route = routeAnswers
	raw.rawRoute = ""
	if raw.row.Route == nil {
		raw.decodeErr = fmt.Errorf("store: priority route answers of message %d: no answers object", raw.row.MessageID)
		return
	}
	if includePreview && raw.rawPreview.Valid && raw.previewRevision.Int64 == raw.row.Revision {
		previewAnswers, err := decodePriorityAnswers(raw.rawPreview.String, selected, decodePriorityPreviewAnswer)
		if err != nil {
			raw.decodeErr = fmt.Errorf("store: priority preview answers of message %d: %w", raw.row.MessageID, err)
			return
		}
		raw.row.Preview = previewAnswers
		raw.rawPreview = sql.NullString{}
		if raw.row.Preview == nil {
			raw.decodeErr = fmt.Errorf("store: priority preview answers of message %d: no answers object", raw.row.MessageID)
		}
	}
}

// decodePriorityBlock decodes one block's rows in parallel. Rows are
// independent and the decoder is stateless, so each worker owns only its
// current row index. A canceled scan leaves the remaining rows undecoded;
// the caller reports the context error and never delivers a partial block.
func decodePriorityBlock(ctx context.Context, block []priorityRawRow, selected map[string]QuestionKey, includePreview bool) {
	workers := min(runtime.GOMAXPROCS(0), priorityDecodeWorkers, len(block))
	if workers < 2 {
		for i := range block {
			if ctx.Err() != nil {
				return
			}
			block[i].decodeAnswers(selected, includePreview)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(block) || ctx.Err() != nil {
					return
				}
				block[i].decodeAnswers(selected, includePreview)
			}
		}()
	}
	wg.Wait()
}

// eachPriorityBlock streams the query in bounded blocks: the single Rows
// reader fills one block, the block's independent rows decode in parallel,
// and fn then sees the rows in the scan's original order. A row that fails
// to decode is reported at its own position, so the delivered prefix and
// the error are exactly the serial read's. fn errors stop the stream, and a
// canceled context stops both the reader and the workers.
func eachPriorityBlock(ctx context.Context, rows priorityRowSource, selected map[string]QuestionKey,
	includePreview, decode bool, fn func(PriorityRow) error) error {
	return eachPriorityBlockOf(ctx, rows, selected, includePreview, decode, priorityBlockRows, fn)
}

// eachPriorityBlockOf is eachPriorityBlock with an explicit block bound.
// The read itself always uses priorityBlockRows; the bound is a parameter
// so the block-size benchmark can compare bounds under one load.
func eachPriorityBlockOf(ctx context.Context, rows priorityRowSource, selected map[string]QuestionKey,
	includePreview, decode bool, blockRows int, fn func(PriorityRow) error) error {
	block := make([]priorityRawRow, 0, blockRows)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		block = block[:0]
		for len(block) < blockRows && rows.Next() {
			block = append(block, priorityRawRow{})
			if err := scanPriorityRow(rows, &block[len(block)-1]); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(block) == 0 {
			return nil
		}
		if decode {
			decodePriorityBlock(ctx, block, selected, includePreview)
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for i := range block {
			if decode && block[i].decodeErr != nil {
				return block[i].decodeErr
			}
			if err := fn(block[i].row); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// The decoded maps and the bounded body head are dead once fn
			// returns; clearing them keeps the block's live set flat.
			block[i] = priorityRawRow{}
		}
	}
}

func (s *Store) eachPriorityMessage(ctx context.Context, fingerprint string, includePreview bool, questions []QuestionKey, body, decode bool, fn func(PriorityRow) error) error {
	vis, err := s.visible(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, priorityQuery(vis, includePreview, body, decode), fingerprint)
	if err != nil {
		return err
	}
	defer rows.Close()
	selected := priorityQuestionSelection(questions)
	return eachPriorityBlock(ctx, rows, selected, includePreview, decode, fn)
}

// PriorityHeads reads the bounded body head, truncation flag and revision of
// the given owner-visible messages in one query. It uses the same visibility
// and content boundary as EachPriorityMessage, so absent, unpicked, shared,
// draft, deleted and junk ids are absent from the result rather than
// zero-filled. The caller bounds the id list; there is deliberately no LIMIT
// here that could disagree with the caller's cached set.
func (s *Store) PriorityHeads(ctx context.Context, ids []int64) (map[int64]PriorityBody, error) {
	out := make(map[int64]PriorityBody, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT m.id, substr(m.body, 1, %d),
		length(m.body) > %d, m.revision
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE m.id IN `+idSetSQL+` AND `+ownerFacingWhere(vis)+`
		ORDER BY m.id`, priorityBodyChars, priorityBodyChars), idSet(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var head PriorityBody
		if err := rows.Scan(&id, &head.Body, &head.Truncated, &head.Revision); err != nil {
			return nil, err
		}
		out[id] = head
	}
	return out, rows.Err()
}

// MessageBody returns the full canonical body of one owner-visible message.
// It enforces the same content boundary as EachPriorityMessage; it exists
// only for the rare snippet the projected body head cannot fill (a body that
// begins with more whitespace than the projection).
func (s *Store) MessageBody(ctx context.Context, messageID int64) (string, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", err
	}
	var body string
	err = s.db.QueryRowContext(ctx, `SELECT m.body
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE m.id = ? AND `+ownerFacingWhere(vis), messageID).Scan(&body)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("store: message %d is not owner-visible", messageID)
		}
		return "", err
	}
	return body, nil
}
