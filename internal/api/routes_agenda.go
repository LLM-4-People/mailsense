package api

import (
	"net/http"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
)

// handleAgenda is the gated "next X" view: calendar occurrences, threads
// waiting on the owner, and due commitments in one ordered list. The
// service owns the join and the horizon bounds; the handler validates the
// query.
func (s *Server) handleAgenda(w http.ResponseWriter, r *http.Request) {
	hours, err := boundedQueryFloat(r, "hours", s.services.Agenda.DefaultHorizon().Hours(),
		agenda.MinHorizon.Hours(), agenda.MaxHorizon.Hours())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.services.Agenda.Build(r.Context(), time.Duration(hours*float64(time.Hour)))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, AgendaViewOf(res))
}
