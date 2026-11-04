package api

import (
	"net/http"
)

// handleQuestions serves the profile's typed questions to the labelling
// screen. It is deliberately pre-gate and read-only: the screen must know
// what it can ask before learning opens the gate, and no message content
// (or label value) is part of the answer.
func (s *Server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	view, err := s.services.Triage.Questions(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Questions = nonNilSlice(view.Questions)
	s.writeJSON(w, r, http.StatusOK, view)
}
