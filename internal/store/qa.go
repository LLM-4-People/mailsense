package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
)

// QAHistoryMax is how many Q&A history rows the store keeps (newest by id).
const QAHistoryMax = 100

// QATurnsMax bounds one conversation read. A chat thread is short: the Q&A
// service asks for the newest six turns, and the store never returns more
// than this many in one call.
const QATurnsMax = 20

// QAConversationIDMax is the longest accepted conversation id. The Q&A
// service generates 16 lowercase hexadecimal characters; the bound and
// alphabet keep the id a safe opaque key, never a mailbox conversation id.
const QAConversationIDMax = 64

// NormalizeQAConversationID trims and validates one conversation id. An
// empty id is valid and means a standalone turn. Only ASCII letters, digits,
// '_' and '-' are accepted, at most QAConversationIDMax characters.
func NormalizeQAConversationID(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", true
	}
	if len(id) > QAConversationIDMax {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return "", false
	}
	return id, true
}

// QACitation is the persisted part of one answer's citation: the message
// id and the reason it was retrieved. Message content is never stored in
// the history.
type QACitation struct {
	MessageID int64  `json:"message_id"`
	Reason    string `json:"reason,omitempty"`
}

// QATopicAnswer is the persisted topic count: the catalogue path, the count
// the census reported, and the classification coverage behind it. It mirrors
// the Q&A service's TopicAnswer; internal/store cannot import internal/qa
// (qa imports store), so the two share the wire shape instead of a type.
type QATopicAnswer struct {
	Topic      string `json:"topic"`
	Subtopic   string `json:"subtopic"`
	Count      int    `json:"count"`
	Classified int    `json:"classified"`
	Scanned    int    `json:"scanned"`
}

// QAHistory is one persisted question and its answer. ConversationID groups
// the turns of one short chat thread; an empty id is a standalone turn.
// Counts, Citations, Plan, Result and Clarify are typed in Go and stored as
// strict JSON; subject, sender and date are not part of a citation, they are
// rehydrated on read. Plan and Result never carry a mail body.
// QAHistorySummary is one rail row of the stored Q&A history: the fields
// the Ask rail and the Home recent-questions list render. The answer,
// citations and plan columns are read only when the owner opens the row.
type QAHistorySummary struct {
	ID        int64
	Question  string
	Mode      string
	CreatedAt string
}

type QAHistory struct {
	ID             int64
	ConversationID string
	// Route is the explicit answer route of this turn: auto, local or
	// remote. It is per turn and never inherited; an empty value is auto.
	Route     string
	Question  string
	Answer    string
	Mode      string
	Intent    string
	Counts    map[string]int
	Citations []QACitation
	Provider  string
	Model     string
	Language  string
	Preview   bool
	CreatedAt string
	Plan      *mailquery.Plan
	Result    *mailquery.Result
	Agenda    *agenda.Result
	Clarify   *mailquery.Clarification
	Notice    string
	// Topic is the structured topic-count answer: the catalogue path, the
	// count and the classification coverage the answer disclosed.
	Topic *QATopicAnswer
}

// SaveQA writes one history row and prunes every row older than the newest
// QAHistoryMax, atomically. It returns the stored row with its id and
// creation time. Empty content is valid (a structured answer is composed
// by the client), but the mode, intent and language must be present.
func (s *Store) SaveQA(ctx context.Context, rec QAHistory) (QAHistory, error) {
	if strings.TrimSpace(rec.Mode) == "" || strings.TrimSpace(rec.Intent) == "" || strings.TrimSpace(rec.Language) == "" {
		return QAHistory{}, fmt.Errorf("store: qa history needs a mode, intent and language")
	}
	conversationID, ok := NormalizeQAConversationID(rec.ConversationID)
	if !ok {
		return QAHistory{}, fmt.Errorf("store: qa history conversation id is invalid")
	}
	rec.ConversationID = conversationID
	rec.Route = strings.TrimSpace(rec.Route)
	if rec.Route == "" {
		rec.Route = "auto"
	}
	// The route is an opaque per-turn token here. The Q&A service owns the
	// auto/local/remote enum and validates it before saving; the store only
	// enforces the column's alphabet and bound so a corrupt value cannot be
	// replayed by a history read.
	if len(rec.Route) > 16 {
		return QAHistory{}, fmt.Errorf("store: qa history route is too long")
	}
	for i := 0; i < len(rec.Route); i++ {
		c := rec.Route[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return QAHistory{}, fmt.Errorf("store: qa history route is invalid")
	}
	if rec.Counts == nil {
		rec.Counts = map[string]int{}
	}
	for key, n := range rec.Counts {
		if strings.TrimSpace(key) == "" || n < 0 {
			return QAHistory{}, fmt.Errorf("store: qa history count %q is invalid", key)
		}
	}
	if rec.Citations == nil {
		rec.Citations = []QACitation{}
	}
	for _, c := range rec.Citations {
		if c.MessageID <= 0 {
			return QAHistory{}, fmt.Errorf("store: qa citation message id must be positive, got %d", c.MessageID)
		}
	}
	if rec.Plan != nil {
		plan := rec.Plan.Normalize()
		if err := plan.Validate(); err != nil {
			return QAHistory{}, fmt.Errorf("store: qa history plan: %w", err)
		}
		rec.Plan = &plan
	}
	if rec.Result != nil {
		if rec.Result.Kind == "" || rec.Result.Action == "" || len(rec.Result.Rows) > 100 {
			return QAHistory{}, fmt.Errorf("store: qa history result is invalid")
		}
	}
	if rec.Clarify != nil {
		if strings.TrimSpace(rec.Clarify.Question) == "" || len(rec.Clarify.Options) < 2 || len(rec.Clarify.Options) > 6 {
			return QAHistory{}, fmt.Errorf("store: qa history clarification needs a question and 2..6 options")
		}
		options := make(map[string]bool, len(rec.Clarify.Options))
		for _, opt := range rec.Clarify.Options {
			if strings.TrimSpace(opt) == "" || len(opt) > 300 {
				return QAHistory{}, fmt.Errorf("store: qa history clarification option is invalid")
			}
			options[opt] = true
		}
		if len(rec.Clarify.Candidates) > 0 {
			if len(rec.Clarify.Candidates) != len(rec.Clarify.Options) || len(rec.Clarify.Candidates) > 6 {
				return QAHistory{}, fmt.Errorf("store: qa history clarification candidates do not match its options")
			}
			for _, candidate := range rec.Clarify.Candidates {
				if candidate.Kind != "person" && candidate.Kind != "company" && candidate.Kind != "all" {
					return QAHistory{}, fmt.Errorf("store: qa history clarification candidate kind %q is invalid", candidate.Kind)
				}
				if strings.TrimSpace(candidate.Question) == "" || len(candidate.Question) > 300 || !options[candidate.Question] {
					return QAHistory{}, fmt.Errorf("store: qa history clarification candidate question is invalid")
				}
				if candidate.Messages < 0 {
					return QAHistory{}, fmt.Errorf("store: qa history clarification candidate count is invalid")
				}
				if strings.TrimSpace(candidate.Label) == "" && strings.TrimSpace(candidate.Address) == "" &&
					strings.TrimSpace(candidate.Domain) == "" && candidate.Kind != "all" {
					return QAHistory{}, fmt.Errorf("store: qa history clarification candidate has no label or identifier")
				}
			}
		}
	}
	if rec.Topic != nil {
		if err := validateQATopic(rec.Topic); err != nil {
			return QAHistory{}, err
		}
	}
	counts, err := json.Marshal(rec.Counts)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa counts: %w", err)
	}
	citations, err := json.Marshal(rec.Citations)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa citations: %w", err)
	}
	planJSON, err := marshalOptional(rec.Plan)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa plan: %w", err)
	}
	resultJSON, err := marshalOptional(rec.Result)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa result: %w", err)
	}
	clarifyJSON, err := marshalOptional(rec.Clarify)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa clarification: %w", err)
	}
	agendaJSON, err := marshalOptional(rec.Agenda)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa agenda: %w", err)
	}
	topicJSON, err := marshalOptional(rec.Topic)
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: qa topic: %w", err)
	}
	ts := ""
	var id int64
	err = s.busy.WithBusyRetry(ctx, "save qa history", func() error {
		// ts and id belong to the attempt that commits: a busy retry re-runs
		// the whole insert and prune, and a rolled-back attempt's id is never
		// returned.
		ts = s.now()
		return s.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO qa_history
			(conversation_id, route, question, answer, mode, intent, counts, citations, provider, model, language, preview, created_at,
			 plan, result, clarify, notice, agenda, topic)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rec.ConversationID, rec.Route, rec.Question, rec.Answer, rec.Mode, rec.Intent, string(counts), string(citations),
				rec.Provider, rec.Model, rec.Language, boolInt(rec.Preview), ts,
				planJSON, resultJSON, clarifyJSON, rec.Notice, agendaJSON, topicJSON)
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
			_, err = pruneNewest(ctx, tx, "qa_history", "id", "id DESC", QAHistoryMax)
			return err
		})
	})
	if err != nil {
		return QAHistory{}, fmt.Errorf("store: save qa history: %w", err)
	}
	rec.ID, rec.CreatedAt = id, ts
	return rec, nil
}

// qaHistoryColumns is the one column order of every Q&A history read. The
// scanner below is its only consumer; a new persisted field is added here
// and scanned in the same position.
const qaHistoryColumns = `id, conversation_id, route, question, answer, mode, intent, counts, citations,
	provider, model, language, preview, created_at, plan, result, clarify, notice, agenda, topic`

// ListQA returns the newest history rows, newest first, at most limit.
func (s *Store) ListQA(ctx context.Context, limit int) ([]QAHistory, error) {
	if limit < 1 || limit > QAHistoryMax {
		return nil, fmt.Errorf("store: qa history limit must be between 1 and %d, got %d", QAHistoryMax, limit)
	}
	return query(ctx, s.db, `SELECT `+qaHistoryColumns+` FROM qa_history ORDER BY id DESC LIMIT ?`,
		[]any{limit}, scanQAHistory)
}

// qaHistorySummaryColumns is the rail projection of the history. It is a
// subset of qaHistoryColumns, not a second schema: the scanner below reads
// exactly these four fields and never decodes an answer or a plan.
const qaHistorySummaryColumns = `id, question, mode, created_at`

// ListQASummaries returns the newest history rail rows. A list mount must
// not pay for twenty full answer payloads it never renders.
func (s *Store) ListQASummaries(ctx context.Context, limit int) ([]QAHistorySummary, error) {
	if limit < 1 || limit > QAHistoryMax {
		return nil, fmt.Errorf("store: qa history limit must be between 1 and %d, got %d", QAHistoryMax, limit)
	}
	return query(ctx, s.db, `SELECT `+qaHistorySummaryColumns+` FROM qa_history ORDER BY id DESC LIMIT ?`,
		[]any{limit}, func(r *sql.Rows, h *QAHistorySummary) error {
			return r.Scan(&h.ID, &h.Question, &h.Mode, &h.CreatedAt)
		})
}

// GetQA reads one persisted answer by id. It is the detail read behind the
// history rail.
func (s *Store) GetQA(ctx context.Context, id int64) (QAHistory, bool, error) {
	if id <= 0 {
		return QAHistory{}, false, fmt.Errorf("store: qa history id must be positive, got %d", id)
	}
	rows, err := query(ctx, s.db, `SELECT `+qaHistoryColumns+` FROM qa_history WHERE id = ?`,
		[]any{id}, scanQAHistory)
	if err != nil {
		return QAHistory{}, false, err
	}
	if len(rows) == 0 {
		return QAHistory{}, false, nil
	}
	return rows[0], true, nil
}

// ListQATurns returns the newest limit turns of one conversation, oldest
// first. The query is scoped by conversation_id, so a turn of one thread is
// never prompt context for another. It rehydrates every persisted field the
// same way ListQA does.
func (s *Store) ListQATurns(ctx context.Context, conversationID string, limit int) ([]QAHistory, error) {
	conversationID, ok := NormalizeQAConversationID(conversationID)
	if !ok || conversationID == "" {
		return nil, fmt.Errorf("store: qa conversation id is invalid")
	}
	if limit < 1 || limit > QATurnsMax {
		return nil, fmt.Errorf("store: qa conversation limit must be between 1 and %d, got %d", QATurnsMax, limit)
	}
	return query(ctx, s.db, `SELECT `+qaHistoryColumns+` FROM (
		SELECT `+qaHistoryColumns+` FROM qa_history WHERE conversation_id = ? ORDER BY id DESC LIMIT ?
	) ORDER BY id ASC`, []any{conversationID, limit}, scanQAHistory)
}

func scanQAHistory(r *sql.Rows, h *QAHistory) error {
	var counts, citations, planJSON, resultJSON, clarifyJSON, agendaJSON, topicJSON string
	var preview int
	if err := r.Scan(&h.ID, &h.ConversationID, &h.Route, &h.Question, &h.Answer, &h.Mode, &h.Intent, &counts, &citations,
		&h.Provider, &h.Model, &h.Language, &preview, &h.CreatedAt,
		&planJSON, &resultJSON, &clarifyJSON, &h.Notice, &agendaJSON, &topicJSON); err != nil {
		return err
	}
	if err := jsonx.DecodeStrict([]byte(counts), &h.Counts); err != nil {
		return fmt.Errorf("store: qa history %d counts: %w", h.ID, err)
	}
	if h.Counts == nil {
		h.Counts = map[string]int{}
	}
	if err := jsonx.DecodeStrict([]byte(citations), &h.Citations); err != nil {
		return fmt.Errorf("store: qa history %d citations: %w", h.ID, err)
	}
	if h.Citations == nil {
		h.Citations = []QACitation{}
	}
	for key, n := range h.Counts {
		if strings.TrimSpace(key) == "" || n < 0 {
			return fmt.Errorf("store: qa history %d count %q is invalid", h.ID, key)
		}
	}
	for _, c := range h.Citations {
		if c.MessageID <= 0 {
			return fmt.Errorf("store: qa history %d citation id is invalid", h.ID)
		}
	}
	plan, err := decodeOptional[mailquery.Plan](planJSON)
	if err != nil {
		return fmt.Errorf("store: qa history %d plan: %w", h.ID, err)
	}
	if plan != nil {
		if err := plan.Validate(); err != nil {
			return fmt.Errorf("store: qa history %d plan: %w", h.ID, err)
		}
	}
	h.Plan = plan
	result, err := decodeOptional[mailquery.Result](resultJSON)
	if err != nil {
		return fmt.Errorf("store: qa history %d result: %w", h.ID, err)
	}
	h.Result = result
	clarify, err := decodeOptional[mailquery.Clarification](clarifyJSON)
	if err != nil {
		return fmt.Errorf("store: qa history %d clarification: %w", h.ID, err)
	}
	h.Clarify = clarify
	agenda, err := decodeOptional[agenda.Result](agendaJSON)
	if err != nil {
		return fmt.Errorf("store: qa history %d agenda: %w", h.ID, err)
	}
	h.Agenda = agenda
	topic, err := decodeOptional[QATopicAnswer](topicJSON)
	if err != nil {
		return fmt.Errorf("store: qa history %d topic: %w", h.ID, err)
	}
	if topic != nil {
		if err := validateQATopic(topic); err != nil {
			return fmt.Errorf("store: qa history %d topic: %w", h.ID, err)
		}
	}
	h.Topic = topic
	h.Preview = preview != 0
	return nil
}

// validateQATopic is the one validator of a persisted topic answer. It keeps
// the store a persistence layer: the catalogue owns which topic ids exist, so
// the store only refuses an empty path and a negative count.
func validateQATopic(topic *QATopicAnswer) error {
	if topic == nil {
		return nil
	}
	if strings.TrimSpace(topic.Topic) == "" || topic.Count < 0 || topic.Classified < 0 || topic.Scanned < 0 {
		return fmt.Errorf("store: qa history topic is invalid")
	}
	return nil
}

// ClearQA removes every history row and the plan cache (whose keys are
// hashes of questions), atomically, and returns how many history rows it
// removed.
func (s *Store) ClearQA(ctx context.Context) (int64, error) {
	var n int64
	err := s.busy.WithBusyRetry(ctx, "clear qa history", func() error {
		// A retry starts from zero: an attempt that removed history but
		// failed on the plan cache must not double-count the next one.
		n = 0
		return s.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "DELETE FROM qa_history")
			if err != nil {
				return err
			}
			if n, err = res.RowsAffected(); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM qa_plan_cache")
			return err
		})
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func marshalOptional[T any](v *T) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func decodeOptional[T any](raw string) (*T, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var v T
	if err := jsonx.DecodeStrict([]byte(raw), &v); err != nil {
		return nil, err
	}
	return &v, nil
}
