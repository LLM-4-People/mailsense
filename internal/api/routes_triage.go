package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/triage"
)

func (s *Server) handleTriage(w http.ResponseWriter, r *http.Request) {
	query, err := triagePageQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Triage.Page(r.Context(), query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleTriageSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.services.Triage.Summary(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, summary)
}

// triagePageQuery reads the page bounds and refuses misuse: limit 1..200
// (default 50), offset 0 or more.
func triagePageQuery(r *http.Request) (triage.PageQuery, error) {
	out := triage.PageQuery{}
	limit, err := boundedQueryInt(r, "limit", triage.TriagePageDefault, 1, triage.TriagePageMax)
	if err != nil {
		return out, err
	}
	out.Limit = limit
	offset, err := boundedQueryOffset(r, "offset", 0, -1)
	if err != nil {
		return out, err
	}
	out.Offset = offset
	return out, nil
}

func (s *Server) handleTriageResult(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.services.Triage.Result(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, result)
}
