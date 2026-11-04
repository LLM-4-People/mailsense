package qa

// The numbered message of a planner prompt, its one constructor, and the one
// enrichment loop of the legs that read stored summaries: the company profile,
// the person profile and the thread summary. A new stored fact is added once.

import (
	"context"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// newContextMessage builds one numbered message from its stored summary and
// ranking signals. Every planner-context leg calls it, so a new stored field
// is added here once instead of once per leg.
func newContextMessage(sum store.Summary, signals *priority.Signals, reason string) contextMessage {
	return contextMessage{
		ID: sum.RowID, Subject: sum.Subject, SenderName: sum.SenderName,
		SenderAddress: sum.SenderAddress, ReceivedAt: sum.Instant, Sent: sum.Role == mail.RoleSent,
		HasAttachments: sum.HasAttachments, Body: sum.Body, Signals: signals, Reason: reason,
	}
}

// contextMessages is the one enrichment loop of the three planner-context legs
// that read stored summaries: the company profile, the person profile and the
// thread summary. It reads the summaries and the details for ids, logs and
// degrades to summary-only when the detail read fails, attaches the ranking
// signals, and builds one numbered message per stored summary. keep drops a
// summary the leg does not want (the person leg checks the sender); decorate
// adds a leg-only field (the person name). Both are optional.
//
// Freeform retrieval keeps its own loop on purpose: it merges three pre-built
// message sources (today, person, retrieved), applies detail fields only when
// non-empty, and derives each reason from the ranking item, so folding it in
// would need a variant flag per difference. The summary-only route projection
// (route.go) reads no details at all and stays lean.
//
// A failed detail read costs only the thread/recipient/label/read/importance
// dimensions; the message facts stay usable and nothing is invented in their
// place.
func (s *Service) contextMessages(ctx context.Context, ids []int64, reason, logLabel string,
	keep func(int64, store.Summary) bool, decorate func(*contextMessage)) ([]contextMessage, error) {
	sums, err := s.store.SummariesOwner(ctx, ids)
	if err != nil {
		return nil, err
	}
	details, err := s.store.ContextDetails(ctx, ids)
	if err != nil {
		s.log.Warn(logLabel, "reason", errorClass(err))
		details = nil
	}
	signals := s.signalMap(ctx)
	msgs := make([]contextMessage, 0, len(ids))
	for _, id := range ids {
		sum, ok := sums[id]
		if !ok || (keep != nil && !keep(id, sum)) {
			continue
		}
		msg := newContextMessage(sum, signals[sum.RowID], reason)
		detail, hasDetail := details[sum.RowID]
		withContextDetail(&msg, detail, hasDetail)
		if decorate != nil {
			decorate(&msg)
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// withContextDetail attaches the stored thread, recipient, label, read and
// importance facts to msg. A missing detail leaves ContextKnown false: those
// facts are unknown, never "unread" or "low importance".
func withContextDetail(msg *contextMessage, detail store.ContextDetail, ok bool) {
	if !ok {
		return
	}
	msg.Recipients = detail.Recipients
	msg.Labels = detail.Labels
	msg.ConversationID = detail.ConversationID
	msg.IsRead = detail.IsRead
	msg.Importance = detail.Importance
	msg.ContextKnown = true
}

// contextMessage is one numbered message of a planner prompt. Sent carries
// the owner direction, and Recipients carries the To/Cc/Bcc lines, so a
// reply-state question ("did X answer?") and a recipient question ("who did
// I send this to?") read stored facts instead of inferring them from who
// replied later. Person names the shared display name when the message came
// from the person-evidence leg; it is empty for every other message.
// ConversationID and Labels are the stored thread and learned-label facts of
// the same message, HasAttachments is its stored attachment flag, and
// IsRead/Importance are its stored read state and importance level.
// ContextKnown reports that those stored facts were actually read: a missing
// detail is unknown, never "unread" or "low importance".
type contextMessage struct {
	ID            int64
	Subject       string
	SenderName    string
	SenderAddress string
	ReceivedAt    string
	Sent          bool
	Recipients    []mail.Recipient
	Body          string
	// Excerpt is the search hit's query-centered window. A long body is
	// rendered from its head; the excerpt keeps the passage the question
	// matched visible to the model even when it sits beyond that head.
	Excerpt        string
	Signals        *priority.Signals
	Reason         string
	Person         string
	ConversationID string
	Labels         map[string]store.LabelFact
	HasAttachments bool
	IsRead         bool
	Importance     int
	ContextKnown   bool
}
