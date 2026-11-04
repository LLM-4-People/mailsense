package api

import (
	"net/http"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// threadLimitDefault is the page size when the query names none. The store's
// MaxThreadMessages is the bound, so the two contracts cannot drift.
const threadLimitDefault = 50

// handleThread is the gated owner-only conversation view. The handler owns
// query validation and the 404/400 contract; the adapter owns the store
// read and the shared thread state.
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	conversationID := r.PathValue("conversation_id")
	if err := store.ValidateConversationID(conversationID); err != nil {
		s.writeError(w, r, invalidThread())
		return
	}
	limit, err := boundedQueryInt(r, "limit", threadLimitDefault, 1, store.MaxThreadMessages)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	before, err := threadBefore(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	at, err := threadAt(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if before > 0 && at > 0 {
		s.writeError(w, r, invalidCursor())
		return
	}
	view, err := s.services.Threads.Get(r.Context(), conversationID, limit, before, at)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Messages = nonNilSlice(view.Messages)
	view.Participants = nonNilSlice(view.Participants)
	s.writeJSON(w, r, http.StatusOK, view)
}

func invalidThread() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_thread", Message: "invalid conversation id"}
}

// threadCursor reads one non-negative message row id a thread page is anchored
// on. An absent parameter means "the latest message" (zero); an empty,
// malformed or negative value is the parameter's own invalid_<name> error. The
// before and at parameters differ only in their name and error code, so one
// parser keeps them from drifting.
func threadCursor(r *http.Request, name string) (int64, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return 0, nil
	}
	invalid := invalidParam(name)
	if len(values) == 0 {
		return 0, invalid
	}
	v, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || v < 0 {
		return 0, invalid
	}
	return v, nil
}

// threadBefore reads the exclusive older-page cursor. Zero (or an absent
// parameter) starts at the latest message.
func threadBefore(r *http.Request) (int64, error) { return threadCursor(r, "before") }

// threadAt reads the inclusive anchor: the page ends at this message, so an
// older opened mail still sees its own conversation context.
func threadAt(r *http.Request) (int64, error) { return threadCursor(r, "at") }

func invalidBefore() *APIError { return invalidParam("before") }

// invalidParam is the one invalid-parameter answer: the code and the message
// are both derived from the parameter's own name, so a new cursor cannot
// spell a third pair.
func invalidParam(name string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_" + name, Message: "invalid " + name}
}
