package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/store"
)

type askRequest struct {
	Question       string `json:"question"`
	Language       string `json:"language"`
	ConversationID string `json:"conversation_id"`
	Route          string `json:"route"`
}

// handleAsk answers one question, optionally in a conversation thread. It is
// a long route: a written answer may use the whole qa.call_timeout.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body askRequest
	if err := s.decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Ask.AskConversation(r.Context(), qa.AskRequest{
		Question: body.Question, Language: body.Language, ConversationID: body.ConversationID,
		Route: qa.Route(body.Route),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, AskViewWireOf(view))
}

// handleAskHistory returns the newest persisted answers.
func (s *Server) handleAskHistory(w http.ResponseWriter, r *http.Request) {
	// One bounded parse, like every other limited route: absent gets the
	// default, and malformed or out-of-range gets the same ranged
	// invalid_limit the client composes its localized message from. The
	// hand-rolled pair emitted a bare invalid_limit for a malformed value and
	// a ranged one for an out-of-range value for the same parameter.
	limit, err := boundedQueryInt(r, "limit", qa.DefaultHistorySize, 1, qa.MaxHistorySize)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.services.Ask.History(r.Context(), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items = nonNilSlice(items)
	s.writeJSON(w, r, http.StatusOK, AskHistoryView{Items: items})
}

// handleAskHistoryItem returns one persisted answer. The history list is a
// projection; this is the detail read when the owner opens a row.
func (s *Server) handleAskHistoryItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, ok, err := s.services.Ask.HistoryItem(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !ok {
		s.writeError(w, r, store.ErrNotFound)
		return
	}
	s.writeJSON(w, r, http.StatusOK, AskViewWireOf(view))
}

// handleAskClear removes every persisted answer.
func (s *Server) handleAskClear(w http.ResponseWriter, r *http.Request) {
	if err := s.services.Ask.ClearHistory(r.Context()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusNoContent, nil)
}
