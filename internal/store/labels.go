package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Label sources.
const (
	LabelManual      = "manual"
	LabelJudge       = "judge"
	LabelJudgeAnswer = "judge_answer"
)

// labelPrecedenceSQL orders one message's label rows by trust: the owner's
// manual label, then the judge's independent reference label, then the
// judge's production answer. The source vocabulary is spelled here from its
// Go owner, never as a second literal; the third rank keeps the order
// deterministic when more than one source carries the same question.
const labelPrecedenceSQL = "CASE source WHEN '" + LabelManual + "' THEN 0 WHEN '" + LabelJudge + "' THEN 1 WHEN '" + LabelJudgeAnswer + "' THEN 2 ELSE 3 END"

// labelContextSQL keeps only the labels a reader may present as learned
// facts. The judge's production answer (LabelJudgeAnswer) is audit data
// written for the owner to inspect; it is the model's own output, so it must
// never become a prior or a fact shown next to a message. Store cannot import
// the feedback policy (feedback imports store), so the read spells the same
// vocabulary from these constants at its one query site.
const labelContextSQL = "source IN ('" + LabelManual + "', '" + LabelJudge + "')"

// LabelRow is one stored label with the message facts few-shot selection
// needs (read from the message, never copied into the label).
type LabelRow struct {
	MessageID     int64  `json:"message_id"`
	Question      string `json:"question"`
	Value         string `json:"value"` // JSON: string (choice) or number
	Source        string `json:"source"`
	Prompt        string `json:"prompt,omitempty"` // judge: the prompt/taxonomy digest that produced it
	CreatedAt     string `json:"created_at"`
	SenderAddress string `json:"sender_address"`
	Subject       string `json:"subject"`
	// The labelled message's role and times (mail.Instant gives its instant).
	Role       mail.FolderRole `json:"role"`
	ReceivedAt string          `json:"received_at"`
	SentOn     string          `json:"sent_on"`
}

// LabelWrite is one label to store: a message's answer of a question from a
// source (feedback validates the value against the profile first). Prompt
// is the judge's prompt/taxonomy digest ("" for manual labels).
type LabelWrite struct {
	MessageID int64
	Question  string
	Value     string // the label's JSON form (taxonomy.Label.JSON)
	Source    string
	Prompt    string
}

// PutLabels sets (or replaces) every label in one transaction: all or none.
// The values must already be valid for their questions
// (taxonomy.ParseLabel).
func (s *Store) PutLabels(ctx context.Context, rows []LabelWrite) error {
	if len(rows) == 0 {
		return fmt.Errorf("store: put labels: no labels")
	}
	if err := s.busy.WithBusyRetry(ctx, "put labels", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error { return s.putLabels(ctx, tx, rows) })
	}); err != nil {
		return err
	}
	s.touchLabels()
	return nil
}

// PutLabel sets (or replaces) one label; the value must already be valid for
// its question (taxonomy.ParseLabel).
func (s *Store) PutLabel(ctx context.Context, messageID int64, question, valueJSON, source string) error {
	if err := s.busy.WithBusyRetry(ctx, "put label", func() error {
		return s.putLabels(ctx, s.db, []LabelWrite{{MessageID: messageID, Question: question, Value: valueJSON, Source: source}})
	}); err != nil {
		return err
	}
	s.touchLabels()
	return nil
}

// putLabels validates every row before writing any, then upserts them
// through ex: the one label write path of PutLabel and PutLabels (a batch
// runs inside the caller's transaction).
func (s *Store) putLabels(ctx context.Context, ex execer, rows []LabelWrite) error {
	for i, row := range rows {
		if row.MessageID <= 0 || row.Question == "" || row.Value == "" || row.Source == "" {
			return fmt.Errorf("store: label %d needs a positive message, question, value and source", i+1)
		}
	}
	now := s.now()
	for _, row := range rows {
		if _, err := ex.ExecContext(ctx, `INSERT INTO labels (message_id, question, value, source, prompt, created_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(message_id, question, source) DO UPDATE SET value = excluded.value, prompt = excluded.prompt, created_at = excluded.created_at`,
			row.MessageID, row.Question, row.Value, row.Source, row.Prompt, now); err != nil {
			return fmt.Errorf("store: put label for message %d: %w", row.MessageID, err)
		}
	}
	return nil
}

// DeleteLabel removes a label.
func (s *Store) DeleteLabel(ctx context.Context, messageID int64, question, source string) error {
	if err := s.busy.WithBusyRetry(ctx, "delete label", func() error {
		_, err := s.db.ExecContext(ctx, "DELETE FROM labels WHERE message_id = ? AND question = ? AND source = ?", messageID, question, source)
		return err
	}); err != nil {
		return err
	}
	s.touchLabels()
	return nil
}

// ListLabels returns every label of visible mail, oldest first. Labels of an
// unpicked store are not learning input either: the same visibleSQL
// predicate (through visible/applySelection) owns every read of the user's
// mail, so unpicking a store hides its labels at once and re-picking brings
// them back (the rows are kept).
func (s *Store) ListLabels(ctx context.Context) ([]LabelRow, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	return query(ctx, s.db, `SELECT l.message_id, l.question, l.value, l.source, l.prompt, l.created_at, m.sender_address, m.subject,
		f.role, m.received_at, m.sent_on
		FROM labels l JOIN messages m ON m.id = l.message_id JOIN folders f ON f.id = m.folder_id
		WHERE `+vis+`
		ORDER BY l.created_at, l.message_id, l.question`, nil,
		func(r *sql.Rows, v *LabelRow) error {
			var role string
			err := r.Scan(&v.MessageID, &v.Question, &v.Value, &v.Source, &v.Prompt, &v.CreatedAt, &v.SenderAddress, &v.Subject, &role, &v.ReceivedAt, &v.SentOn)
			v.Role = mail.FolderRole(role)
			return err
		})
}
