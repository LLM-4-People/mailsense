package api

import "net/http"

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.services.Shutdown.Request(r.Context()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, okView{OK: true})
}
