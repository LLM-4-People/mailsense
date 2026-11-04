package qa

// The deterministic thread-state evidence block of a planner prompt: the
// resolver's verdict on one recent conversation, copied once from its
// assessment so every leg shows the same facts.

import (
	"context"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// threadEvidence is the resolver's deterministic verdict on one recent
// conversation that contributed numbered messages. State, actor, reason,
// visibility, message count and last message time come from
// threadintel.Resolve over every visible message (including shared mail);
// they are copied here, never inferred from the order of the numbered
// messages. FirstAt and Participants come from the owner-visible thread page
// when the company path already loaded it, so they stay empty for the
// general and person paths that load no page.
type threadEvidence struct {
	Subject      string
	State        threadintel.State
	Reason       threadintel.Reason
	Visibility   threadintel.Visibility
	Messages     int
	Participants []string
	FirstAt      string
	ActorName    string
	ActorAddr    string
	LastAt       string
	MessageID    int64
	// The owner-facing actor projection. Empty when the last move came
	// from shared/delegate mail; the state and reason stay available.
	OwnerActorName string
	OwnerActorAddr string
	OwnerLastAt    string
	OwnerMessageID int64
}

// threadEvidenceFrom copies the assessment facts of one conversation into the
// evidence block, with the subject and last time the caller already holds.
// Company and retrieval both read the same assessment, so the projection is
// written once: a field added to threadintel.Assessment is either mapped here
// or explicitly left out, never silently dropped by one leg and kept by the
// other.
func threadEvidenceFrom(a threadintel.Assessment, subject, lastAt string) threadEvidence {
	return threadEvidence{
		Subject: subject, State: a.State, Reason: a.Reason, Visibility: a.Visibility,
		Messages: a.Messages, ActorName: a.ActorName, ActorAddr: a.ActorAddress,
		LastAt: lastAt, MessageID: a.LastMessageID,
		OwnerActorName: a.OwnerActorName, OwnerActorAddr: a.OwnerActorAddress,
		OwnerLastAt: a.OwnerLastAt, OwnerMessageID: a.OwnerLastMessageID,
	}
}

// threadEvidenceFor resolves the state of every conversation that contributed
// one of the numbered messages. The conversation id is the one stored on the
// message and the subject is that message's own subject, so the block never
// reads a second body. The states come from the resolver over every visible
// message (including shared mail); a failed read costs only this block and
// leaves the numbered messages as the evidence.
func (s *Service) threadEvidenceFor(ctx context.Context, msgs []contextMessage) []threadEvidence {
	ids := make([]string, 0, len(msgs))
	subjects := make(map[string]string, len(msgs))
	for _, m := range msgs {
		if m.ConversationID == "" {
			continue
		}
		if _, ok := subjects[m.ConversationID]; ok {
			continue
		}
		subjects[m.ConversationID] = m.Subject
		ids = append(ids, m.ConversationID)
	}
	if len(ids) == 0 {
		return nil
	}
	states, err := s.threadStates(ctx, ids)
	if err != nil {
		s.log.Warn("qa thread states unavailable", "reason", errorClass(err))
		return nil
	}
	out := make([]threadEvidence, 0, len(ids))
	for _, id := range ids {
		assessment, ok := states[id]
		if !ok {
			continue
		}
		out = append(out, threadEvidenceFrom(assessment, subjects[id], assessment.LastAt))
	}
	slices.SortStableFunc(out, func(a, b threadEvidence) int {
		return strings.Compare(b.LastAt, a.LastAt)
	})
	return out
}
