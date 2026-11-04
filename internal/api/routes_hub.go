package api

import (
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	view, err := s.services.Hub.Hub(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleAnswers(w http.ResponseWriter, r *http.Request) {
	query, err := answerQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.services.Hub.Answers(r.Context(), query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, page)
}

func (s *Server) handleLabelSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.services.Hub.LabelSummary(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, summary)
}

// answerQuery validates the current-label page: question optional, source
// manual or judge (empty: both), message and cursor positive, limit 1..100
// (default 50). Unknown query parameters are ignored like every read.
func answerQuery(r *http.Request) (hub.AnswerQuery, error) {
	out := hub.AnswerQuery{}
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	switch source {
	case "", store.LabelManual, store.LabelJudge:
		out.Source = source
	default:
		return out, &APIError{Status: http.StatusBadRequest, Code: "invalid_source",
			Message: "source must be manual or judge"}
	}
	out.Question = r.URL.Query().Get("question")
	if message, ok, err := queryInt(r, "message"); err != nil {
		return out, err
	} else if ok {
		if message <= 0 {
			return out, &APIError{Status: http.StatusBadRequest, Code: "invalid_message", Message: "message must be a positive row id"}
		}
		out.MessageID = int64(message)
	}
	if cursor, ok, err := queryInt(r, "cursor"); err != nil {
		return out, invalidCursor()
	} else if ok {
		if cursor <= 0 {
			return out, invalidCursor()
		}
		out.Cursor = int64(cursor)
	}
	limit, err := boundedQueryInt(r, "limit", store.AnswerDefaultLimit, 1, store.AnswerMaxLimit)
	if err != nil {
		return out, err
	}
	out.Limit = limit
	return out, nil
}
