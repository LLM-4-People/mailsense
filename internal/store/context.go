package store

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// LabelFact is one message's learned label: the raw JSON value of one
// question and the source that produced it (manual or judge).
type LabelFact struct {
	Value  string
	Source string
}

// ContextDetail is the stored context of one message that a reader renders
// next to its summary: the conversation it belongs to, its addressees, its
// learned labels and its stored read/importance state. Only owner-facing
// content gets an entry: a shared or delegate store can mark its own mailbox
// owner as verdict=owner, but its participants and labels never leave that
// store. Importance is the stored 0/1/2 level; an unset column reads as
// normal (1), the same value the mail source omits.
type ContextDetail struct {
	ConversationID string
	Recipients     []mail.Recipient
	Labels         map[string]LabelFact
	IsRead         bool
	Importance     int
}

// ContextDetails reads the message context Ask needs in two queries: the
// owner-visible messages with their read/importance state, recipients and
// conversation id, then their labels. The label read keeps only the sources
// that may be shown as learned facts and orders manual before judge, keeping
// the first row: a human decision owns that fact, and the judge's production
// answer is audit data that never becomes a prior. A message with no
// recipients has an empty slice and one with no labels an empty map. A
// shared, unattributed or removed message is absent, never another owner's
// content under a known id.
func (s *Store) ContextDetails(ctx context.Context, ids []int64) (map[int64]ContextDetail, error) {
	out := make(map[int64]ContextDetail, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, COALESCE(m.conversation_id, ''), m.is_read,
		COALESCE(m.importance, 1), `+recipientsSQL+`
		FROM messages m JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE m.id IN `+idSetSQL+` AND `+ownerFacingWhere(vis), idSet(ids))
	if err != nil {
		return nil, fmt.Errorf("store: message context: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var conversationID, raw string
		var isRead bool
		var importance int
		if err := rows.Scan(&id, &conversationID, &isRead, &importance, &raw); err != nil {
			return nil, fmt.Errorf("store: message context row: %w", err)
		}
		recipients, err := parseRecipients(raw)
		if err != nil {
			return nil, err
		}
		out[id] = ContextDetail{ConversationID: conversationID, Recipients: recipients,
			Labels: map[string]LabelFact{}, IsRead: isRead, Importance: importance}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: message context rows: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	labels, err := s.db.QueryContext(ctx, `SELECT message_id, question, value, source FROM labels
		WHERE message_id IN `+idSetSQL+` AND `+labelContextSQL+`
		ORDER BY message_id, question, `+labelPrecedenceSQL, idSet(ids))
	if err != nil {
		return nil, fmt.Errorf("store: message labels: %w", err)
	}
	defer labels.Close()
	for labels.Next() {
		var id int64
		var question string
		var fact LabelFact
		if err := labels.Scan(&id, &question, &fact.Value, &fact.Source); err != nil {
			return nil, fmt.Errorf("store: message label row: %w", err)
		}
		detail, ok := out[id]
		if !ok {
			// The message is not owner-visible: its labels never leave the
			// boundary, even when the row itself still exists.
			continue
		}
		if _, taken := detail.Labels[question]; taken {
			continue
		}
		detail.Labels[question] = fact
		out[id] = detail
	}
	if err := labels.Err(); err != nil {
		return nil, fmt.Errorf("store: message label rows: %w", err)
	}
	return out, nil
}
