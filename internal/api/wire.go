package api

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// wireTime is the one timestamp form of the API's wire objects: UTC, RFC 3339,
// no fractional seconds. A handler formats through it so two views cannot
// disagree about the instant they were generated, and a client parses one
// shape everywhere.
func wireTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// nonNilSlice is the one empty-array rule of the wire: a nil list is sent as
// [], never null, so a client can iterate without a guard.
func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// nonNilMap is the one empty-object rule of the wire: a nil map is sent as {},
// never null.
func nonNilMap[M ~map[K]V, K comparable, V any](m M) M {
	if m == nil {
		return M{}
	}
	return m
}

// ThreadStateViewOf is the one projection of a resolver assessment to the
// wire. The thread card and the conversation row must agree: the actor is
// owner-facing only, so a shared/delegate reply leaves it empty instead of
// naming a colleague.
func ThreadStateViewOf(state threadintel.Assessment) ThreadStateView {
	return ThreadStateView{
		State: state.State, Reason: state.Reason, Actor: state.OwnerActorName,
		LastAt: state.LastAt, LastMessageID: state.LastMessageID,
		ReplyToMessageID: state.ReplyToMessageID, Visibility: state.Visibility,
		Confidence: state.Confidence, Topic: state.Topic, Subtopic: state.Subtopic,
	}
}

// ThreadParticipantViewOf is the one projection of a stored participant.
func ThreadParticipantViewOf(p store.ThreadParticipant) ThreadParticipantView {
	return ThreadParticipantView{Name: p.Name, Address: p.Address, Messages: p.Messages, LastAt: p.LastAt}
}
